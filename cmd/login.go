package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"time"
)

const loginUsage = `Usage: zharf login [--no-browser] [--with-key]

Signs in through the browser, like 'gh auth login': zharf shows a one-time code, opens
the platform's page, and you approve the sign-in there. The platform then makes an API key
named "zharf CLI · <this computer>", and zharf saves it in the macOS keychain, the Linux
secret service (secret-tool), or ~/.config/zharf/credentials (mode 0600).

Flags:
  --no-browser   Print the link without opening a browser
  --with-key     Read an existing API key from stdin instead:
                   echo "$KEY" | zharf login --with-key

ZHARF_API_KEY, when set, is used instead of the saved key.`

var loginFlags = []flagDef{{long: "no-browser"}, {long: "with-key"}}

// ── The device flow: POST /api/cli/device, then poll POST /api/cli/token ──────────────────

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

type deviceAccount struct {
	Phone       string `json:"phone"`
	DisplayName string `json:"display_name"`
}

type deviceToken struct {
	APIKey  string        `json:"api_key"`
	Account deviceAccount `json:"account"`
}

var (
	errDeviceDenied  = errors.New("the sign-in was denied in the browser")
	errDeviceExpired = errors.New("the code expired before the sign-in was approved; run `zharf login` again")
)

func clientName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "computer"
	}
	host = strings.TrimSuffix(host, ".local")
	return fmt.Sprintf("%s (%s/%s)", host, runtime.GOOS, runtime.GOARCH)
}

func (c *Client) startDevice(ctx context.Context, name string) (*deviceStart, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, "/api/cli/device", map[string]string{"client_name": name}, false, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var start deviceStart
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		return nil, fmt.Errorf("unexpected answer from %s/api/cli/device: %w", c.BaseURL, err)
	}
	if start.DeviceCode == "" || start.UserCode == "" {
		return nil, fmt.Errorf("unexpected answer from %s/api/cli/device: no code", c.BaseURL)
	}
	return &start, nil
}

// pollDevice asks /api/cli/token every interval until the person approves, denies, or the
// code expires. Network failures and 5xx answers are retried until then: a phone hotspot or
// a filtered link should not end a sign-in that is still valid.
func pollDevice(ctx context.Context, c *Client, start *deviceStart, sleep func(context.Context, time.Duration) error, now func() time.Time) (*deviceToken, error) {
	interval := time.Duration(start.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	expiresIn := time.Duration(start.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 10 * time.Minute
	}
	deadline := now().Add(expiresIn)
	for {
		wait := interval
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
		if now().After(deadline) {
			return nil, errDeviceExpired
		}
		token, err := askToken(ctx, c, start.DeviceCode)
		if err == nil {
			return token, nil
		}
		var apiErr *APIError
		var netErr *NetworkError
		switch {
		case errors.As(err, &apiErr):
			switch {
			case apiErr.Code == "authorization_pending" || apiErr.Status == http.StatusPreconditionRequired:
				continue
			case apiErr.Code == "slow_down":
				interval += 5 * time.Second
				continue
			case apiErr.Code == "access_denied":
				return nil, errDeviceDenied
			case apiErr.Code == "expired_token" || apiErr.Status == http.StatusGone:
				return nil, errDeviceExpired
			case apiErr.Status == http.StatusForbidden:
				return nil, errDeviceDenied
			case apiErr.Status == http.StatusTooManyRequests:
				if apiErr.RetryAfter > 0 {
					if err := sleep(ctx, time.Duration(apiErr.RetryAfter)*time.Second); err != nil {
						return nil, err
					}
				}
				continue
			case apiErr.Status >= 500:
				continue
			}
			return nil, apiErr
		case errors.As(err, &netErr):
			continue
		case ctx.Err() != nil:
			return nil, ctx.Err()
		}
		return nil, err
	}
}

func askToken(ctx context.Context, c *Client, deviceCode string) (*deviceToken, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodPost, "/api/cli/token", map[string]string{"device_code": deviceCode}, false, nil)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &NetworkError{Host: hostOf(c.BaseURL), Err: err}
		}
		return nil, err
	}
	defer resp.Body.Close()
	var token deviceToken
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil || token.APIKey == "" {
		return nil, fmt.Errorf("unexpected answer from %s/api/cli/token", c.BaseURL)
	}
	return &token, nil
}

// ── Commands ────────────────────────────────────────────────────────────────────────────────

func cmdLogin(a *app, p parsed) error {
	if p.has("with-key") {
		return loginWithKey(a)
	}
	c, _, _ := a.client(false)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	start, err := c.startDevice(ctx, clientName())
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			fmt.Fprintf(a.stderr, "%s %s does not offer browser sign-in for the CLI yet.\n", a.errp.red("Error:"), a.host)
			fmt.Fprintf(a.stderr, "Make a key at %s/keys and run:  zharf login --with-key\n", a.baseURL)
			return errQuiet
		}
		return err
	}
	link := start.VerificationURIComplete
	if link == "" {
		link = start.VerificationURI
	}

	pal := a.errp
	w := a.stderr
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Your one-time code:")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "      %s\n", pal.bold(pal.cyan(start.UserCode)))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  این صفحه را در مرورگر باز کنید و اگر همین کد را نشان داد، ورود را تأیید کنید.")
	fmt.Fprintln(w, "  Open this page and approve the sign-in if it shows the same code:")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "      %s\n", pal.bold(link))
	fmt.Fprintln(w)
	if !p.has("no-browser") && os.Getenv("ZHARF_NO_BROWSER") == "" {
		if err := a.openBrowser(link); err == nil {
			fmt.Fprintln(w, pal.dim("  Opened your browser."))
		}
	}
	fmt.Fprintln(w, pal.dim("  Waiting for approval… (Ctrl+C to stop)"))

	token, err := pollDevice(ctx, c, start, a.sleep, time.Now)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(w, "\nSign-in stopped.")
			return errCancelled
		}
		return err
	}
	return a.storeKey(token.APIKey, &profile{
		Phone:       token.Account.Phone,
		DisplayName: token.Account.DisplayName,
		SignedInAt:  time.Now().UTC().Format(time.RFC3339),
		Method:      "device",
	})
}

func (a *app) storeKey(key string, prof *profile) error {
	stores, err := a.stores()
	if err != nil {
		return err
	}
	where, err := saveKey(stores, a.host, key)
	if err != nil {
		return fmt.Errorf("could not save the key: %w", err)
	}
	if err := saveProfile(a.host, prof); err != nil {
		fmt.Fprintln(a.stderr, a.errp.dim("(Could not save the account name: "+err.Error()+")"))
	}
	who := prof.DisplayName
	if prof.Phone != "" {
		if who != "" {
			who += " (" + prof.Phone + ")"
		} else {
			who = prof.Phone
		}
	}
	if who != "" {
		fmt.Fprintf(a.stderr, "\nSigned in to %s as %s.\n", a.host, a.errp.bold(who))
	} else {
		fmt.Fprintf(a.stderr, "\nSigned in to %s.\n", a.host)
	}
	fmt.Fprintf(a.stderr, "Key %s saved in %s.\n", keyPrefix(key), where.Name())
	if os.Getenv("ZHARF_API_KEY") != "" {
		fmt.Fprintln(a.stderr, a.errp.yellow("Note: ZHARF_API_KEY is set, and it is used instead of the saved key until you unset it."))
	}
	fmt.Fprintln(a.stderr, a.errp.dim("Try:  zharf run qwen3.5-9b \"سلام\""))
	return nil
}

func loginWithKey(a *app) error {
	var key string
	if a.inTTY {
		fmt.Fprint(a.stderr, "Paste your API key (zk-…), then press Enter: ")
		restore := hideInput(a.stdinFile)
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		restore()
		fmt.Fprintln(a.stderr)
		if err != nil && line == "" {
			return usageErr("no key given")
		}
		key = line
	} else {
		data, err := io.ReadAll(io.LimitReader(a.stdin, 4096))
		if err != nil {
			return err
		}
		key = string(data)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return usageErr("no key given on stdin")
	}
	if !strings.HasPrefix(key, "zk-") {
		fmt.Fprintln(a.stderr, a.errp.yellow("Note: platform keys start with zk-; saving this one anyway."))
	}
	c := &Client{BaseURL: a.baseURL, HTTP: a.httpc, Key: key}
	switch status, detail := checkKey(context.Background(), c); status {
	case keyRejected:
		return &APIError{Status: 401, Code: "bad_key", Message: "The platform rejected this key: " + detail}
	case keyUnchecked:
		fmt.Fprintln(a.stderr, a.errp.dim("Could not check the key ("+detail+"); saving it anyway."))
	}
	return a.storeKey(key, &profile{SignedInAt: time.Now().UTC().Format(time.RFC3339), Method: "key"})
}

// hideInput turns off terminal echo while a key is typed (Unix; a no-op elsewhere).
func hideInput(f *os.File) func() {
	if runtime.GOOS == "windows" || f == nil {
		return func() {}
	}
	off := exec.Command("stty", "-echo")
	off.Stdin = f
	if off.Run() != nil {
		return func() {}
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = f
		_ = on.Run()
	}
}

const logoutUsage = `Usage: zharf logout

Removes the saved key for this platform from the keychain or credentials file. The key itself
keeps working until you revoke it on the dashboard's keys page.`

func cmdLogout(a *app, _ parsed) error {
	stores, err := a.stores()
	if err != nil {
		return err
	}
	removed := []string{}
	for _, store := range stores {
		if err := store.Delete(a.host); err == nil {
			removed = append(removed, store.Name())
		} else if !errors.Is(err, errNoKey) {
			fmt.Fprintf(a.stderr, "Could not remove the key from %s: %v\n", store.Name(), err)
		}
	}
	_ = saveProfile(a.host, nil)
	if len(removed) == 0 {
		fmt.Fprintf(a.stderr, "No saved key for %s.\n", a.host)
	} else {
		fmt.Fprintf(a.stderr, "Signed out of %s: removed the key from %s.\n", a.host, strings.Join(removed, " and "))
		fmt.Fprintf(a.stderr, "%s\n", a.errp.dim("The key still works until you revoke it at "+a.baseURL+"/keys."))
	}
	if os.Getenv("ZHARF_API_KEY") != "" {
		fmt.Fprintln(a.stderr, a.errp.yellow("ZHARF_API_KEY is still set in this shell."))
	}
	return nil
}

// ── whoami ──────────────────────────────────────────────────────────────────────────────────

const whoamiUsage = `Usage: zharf whoami

Shows the platform, where the key comes from, its prefix, and the account it was issued to
at sign-in, then checks the key with a request that costs nothing. Balance and spending are
on the dashboard (see 'zharf usage'): API keys cannot read them.`

type keyStatus int

const (
	keyAccepted keyStatus = iota
	keyRejected
	keySuspended
	keyUnchecked
)

// checkKey asks for a video job that cannot exist. The platform checks the key first and does
// not charge for this read, so 404 job_not_found means the key is valid, 401 that it is not.
// It says nothing about the balance.
func checkKey(ctx context.Context, c *Client) (keyStatus, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/v1/video/generations/zharf-cli-key-check", nil, true, nil)
	if err == nil {
		resp.Body.Close()
		return keyUnchecked, "unexpected answer"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == "job_not_found":
			return keyAccepted, ""
		case apiErr.Status == http.StatusUnauthorized:
			return keyRejected, apiErr.Message
		case apiErr.Code == "suspended":
			return keySuspended, apiErr.Message
		}
		return keyUnchecked, apiErr.Message
	}
	return keyUnchecked, err.Error()
}

func cmdWhoami(a *app, _ parsed) error {
	c, src, err := a.client(true)
	if err != nil {
		return err
	}
	pal := a.out
	row := func(label, value string) { fmt.Fprintf(a.stdout, "  %s %s\n", pal.dim(padRight(label, 9)), value) }
	fmt.Fprintf(a.stdout, "%s\n", pal.bold(a.host))
	row("API", a.baseURL+"/v1")
	row("Key", keyPrefix(src.Key)+" from "+src.Where)
	if prof, ok := loadProfiles()[a.host]; ok && os.Getenv("ZHARF_API_KEY") == "" {
		who := prof.DisplayName
		if prof.Phone != "" {
			if who != "" {
				who += " · "
			}
			who += prof.Phone
		}
		if who != "" {
			row("Account", who+pal.dim(" (as recorded at sign-in)"))
		}
	}
	status, detail := checkKey(context.Background(), c)
	switch status {
	case keyAccepted:
		row("Check", pal.green("the platform accepts this key"))
	case keyRejected:
		row("Check", pal.red("the platform rejects this key: "+detail))
	case keySuspended:
		row("Check", pal.red("this account is suspended"))
	default:
		row("Check", pal.yellow("could not check the key: "+detail))
	}
	row("Balance", "see "+a.baseURL+"/wallet")
	if status == keyRejected || status == keySuspended {
		return errQuiet
	}
	return nil
}
