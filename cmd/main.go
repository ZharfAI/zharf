// Package cmd is the zharf command line: sign in, list models, chat with any of them, launch
// coding agents on them, and serve them locally over the Ollama and OpenAI APIs.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	buildversion "github.com/ZharfAI/zharf/version"
)

// version is this build's version, set with
// -ldflags "-X github.com/ZharfAI/zharf/version.Version=0.1.0".
var version = buildversion.Version

type app struct {
	baseURL string
	host    string

	stdin          io.Reader
	stdinFile      *os.File
	stdout, stderr io.Writer
	inTTY          bool
	outTTY, errTTY bool
	out, errp      palette // colours for stdout and stderr

	httpc       *http.Client
	stores      func() ([]credStore, error)
	openBrowser func(string) error
	sleep       func(context.Context, time.Duration) error
	lookPath    func(string) (string, error)
	execApp     func(path string, args, env []string) error
	runOutput   func(name string, args ...string) (string, error)
}

// Main runs the command line with args (without the program name) and returns the exit status.
func Main(args []string) int {
	removeOldBinary()
	return mainWith(args)
}

func newApp(noColor bool) *app {
	return &app{
		stdin:       os.Stdin,
		stdinFile:   os.Stdin,
		stdout:      os.Stdout,
		stderr:      os.Stderr,
		inTTY:       isTerminal(os.Stdin),
		outTTY:      isTerminal(os.Stdout),
		errTTY:      isTerminal(os.Stderr),
		out:         palette{on: colourAllowed(os.Stdout, noColor)},
		errp:        palette{on: colourAllowed(os.Stderr, noColor)},
		httpc:       newHTTPClient(),
		stores:      keyStores,
		openBrowser: openURL,
		sleep:       sleepCtx,
		lookPath:    exec.LookPath,
		execApp:     execProgram,
		runOutput:   commandOutput,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type command struct {
	name    string
	aliases []string
	summary string
	usage   string
	flags   []flagDef
	run     func(a *app, p parsed) error
}

func commands() []command {
	return []command{
		{name: "login", summary: "Sign in in the browser and save an API key", usage: loginUsage, flags: loginFlags, run: cmdLogin},
		{name: "logout", summary: "Forget the saved key", usage: logoutUsage, run: cmdLogout},
		{name: "whoami", summary: "Show which key and account are in use", usage: whoamiUsage, run: cmdWhoami},
		{name: "models", aliases: []string{"list", "ls"}, summary: "List the models and their prices in ژرف", usage: modelsUsage, flags: modelsFlags, run: cmdModels},
		{name: "show", summary: "Show one model's details and prices", usage: showUsage, flags: showFlags, run: cmdShow},
		{name: "run", summary: "Chat with a model, or ask it once", usage: runUsage, flags: runFlags, run: cmdRun},
		{name: "launch", summary: "Start OpenCode, Codex, Qwen Code or Hermes on a ژرف model", usage: launchUsage, flags: launchFlags, run: cmdLaunch},
		{name: "serve", summary: "Serve the models locally over the Ollama and OpenAI APIs", usage: serveUsage, flags: serveFlags, run: cmdServe},
		{name: "usage", summary: "Where to see spending and balance", usage: usageUsage, flags: usageFlags, run: cmdUsage},
		{name: "update", summary: "Install the latest zharf", usage: updateUsage, flags: updateFlags, run: cmdUpdate},
		{name: "version", summary: "Print the version", usage: "zharf version", run: cmdVersion},
		{name: "help", summary: "Help for a command", usage: "zharf help [command]", run: nil},
	}
}

func findCommand(name string) *command {
	for _, c := range commands() {
		if c.name == name {
			return &c
		}
		for _, alias := range c.aliases {
			if alias == name {
				return &c
			}
		}
	}
	return nil
}

func mainWith(argv []string) int {
	name, rest := splitCommand(argv)
	noColor := false
	for _, arg := range argv {
		if arg == "--no-color" || arg == "--no-colour" {
			noColor = true
		}
	}
	a := newApp(noColor)

	if name == "" {
		top, err := parseArgs(rest, append(globalFlags, flagDef{long: "version", short: "v"}))
		if err != nil {
			fmt.Fprintln(a.stderr, "zharf:", err)
			return 2
		}
		if top.has("version") {
			return exitCode(a, cmdVersion(a, top))
		}
		fmt.Fprint(a.stdout, mainHelp(a))
		if top.has("help") {
			return 0
		}
		return 2
	}

	if name == "help" {
		p, _ := parseArgs(rest, globalFlags)
		if len(p.args) > 0 {
			if c := findCommand(p.args[0]); c != nil {
				fmt.Fprintln(a.stdout, c.usage)
				return 0
			}
			fmt.Fprintf(a.stderr, "zharf: no command %q\n\n", p.args[0])
			fmt.Fprint(a.stderr, mainHelp(a))
			return 2
		}
		fmt.Fprint(a.stdout, mainHelp(a))
		return 0
	}

	cmd := findCommand(name)
	if cmd == nil {
		fmt.Fprintf(a.stderr, "zharf: no command %q. Run `zharf help` for the list.\n", name)
		return 2
	}
	p, err := parseArgs(rest, append(append([]flagDef{}, globalFlags...), cmd.flags...))
	if err != nil {
		fmt.Fprintf(a.stderr, "zharf %s: %v\n\n%s\n", cmd.name, err, cmd.usage)
		return 2
	}
	if p.has("help") {
		fmt.Fprintln(a.stdout, cmd.usage)
		return 0
	}
	base := p.str("base-url", os.Getenv("ZHARF_BASE_URL"))
	a.baseURL, err = normalizeBaseURL(base)
	if err != nil {
		fmt.Fprintln(a.stderr, "zharf:", err)
		return 2
	}
	a.host = hostOf(a.baseURL)
	return exitCode(a, cmd.run(a, p))
}

// usageError is a mistake in how a command was typed (exit status 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// errCancelled: the person pressed Ctrl+C; the message is already printed.
var errCancelled = errors.New("cancelled")

// errQuiet: the failure was already explained on screen.
var errQuiet = errors.New("")

func exitCode(a *app, err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errCancelled) || errors.Is(err, context.Canceled) {
		return 130
	}
	if errors.Is(err, errQuiet) {
		return 1
	}
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(a.stderr, "zharf:", ue.msg)
		return 2
	}
	a.printError(err)
	return 1
}

// printError shows an error in English with the platform's Persian message under it, and a
// hint on what to do when there is one.
func (a *app) printError(err error) {
	pal := a.errp
	var apiErr *APIError
	var netErr *NetworkError
	switch {
	case errors.Is(err, errNotSignedIn):
		fmt.Fprintf(a.stderr, "%s Not signed in to %s.\n", pal.red("Error:"), a.host)
		fmt.Fprintln(a.stderr, "Run `zharf login`, or set ZHARF_API_KEY to an API key from "+a.baseURL+"/keys.")
	case errors.As(err, &apiErr):
		fmt.Fprintf(a.stderr, "%s %s\n", pal.red("Error:"), apiErr.Message)
		if apiErr.MessageFa != "" {
			fmt.Fprintf(a.stderr, "       %s\n", apiErr.MessageFa)
		}
		if hint := a.hintFor(apiErr); hint != "" {
			fmt.Fprintln(a.stderr, pal.dim(hint))
		}
	case errors.As(err, &netErr):
		fmt.Fprintf(a.stderr, "%s %v\n", pal.red("Error:"), netErr)
		fmt.Fprintln(a.stderr, pal.dim("Check your connection. Behind a proxy? Set HTTPS_PROXY, e.g. HTTPS_PROXY=http://127.0.0.1:8080."))
	default:
		fmt.Fprintf(a.stderr, "%s %v\n", pal.red("Error:"), err)
	}
}

func (a *app) hintFor(e *APIError) string {
	switch {
	case e.Code == "bad_key" || e.Code == "no_key" || e.Status == http.StatusUnauthorized:
		if os.Getenv("ZHARF_API_KEY") != "" {
			return "The key comes from ZHARF_API_KEY; check it, or unset it to use the saved one."
		}
		return "Sign in again with `zharf login`."
	case e.Code == "insufficient_balance" || e.Code == "key_spend_limit" || e.Status == http.StatusPaymentRequired:
		return "Top up at " + a.baseURL + "/wallet, or raise the key's limit at " + a.baseURL + "/keys."
	case e.Status == http.StatusTooManyRequests && e.RetryAfter > 0:
		return fmt.Sprintf("Try again in %d s.", e.RetryAfter)
	}
	return ""
}

// client returns a platform client. With auth, it needs a key and says where it came from.
func (a *app) client(auth bool) (*Client, *keySource, error) {
	c := &Client{BaseURL: a.baseURL, HTTP: a.httpc}
	if !auth {
		return c, nil, nil
	}
	// The environment first: a container may have no home folder for the key stores.
	if src, err := findKey(nil, a.host); err == nil {
		c.Key = src.Key
		return c, src, nil
	}
	stores, err := a.stores()
	if err != nil {
		return nil, nil, err
	}
	src, err := findKey(stores, a.host)
	if err != nil {
		return nil, nil, err
	}
	c.Key = src.Key
	return c, src, nil
}

// openURL opens a page in the default browser.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		// `start` breaks URLs at "&"; the URL handler takes them whole.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", u)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func cmdVersion(a *app, _ parsed) error {
	fmt.Fprintf(a.stdout, "zharf %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}

func mainHelp(a *app) string {
	var b strings.Builder
	b.WriteString("zharf: every model on platform.zharf.ai from your terminal, paid in ژرف.\n\n")
	b.WriteString("Usage:\n  zharf <command> [flags]\n\nCommands:\n")
	for _, c := range commands() {
		name := c.name
		if len(c.aliases) > 0 {
			name += ", " + strings.Join(c.aliases, ", ")
		}
		fmt.Fprintf(&b, "  %s  %s\n", padRight(name, 18), c.summary)
	}
	b.WriteString(`
Examples:
  zharf login
  zharf models --search qwen
  zharf run qwen3.5-9b "سلام! یک جمله درباره‌ی تهران بنویس."
  cat notes.txt | zharf run gpt-5-mini "Summarise this in three bullets"
  zharf serve          # Ollama API on http://127.0.0.1:11434
  zharf launch codex --model gpt-5-mini

Global flags and environment:
  --base-url URL     The platform (default https://platform.zharf.ai; env ZHARF_BASE_URL)
  --no-color         No colours (also NO_COLOR=1)
  ZHARF_API_KEY      Use this key instead of the saved one
  HTTPS_PROXY        Send everything through this proxy

Run 'zharf help <command>' for a command's flags.
`)
	return b.String()
}

const usageUsage = `Usage: zharf usage [--open]

Spending, balance and each request's cost are on the dashboard. API keys cannot read them
(the usage API takes a dashboard sign-in, not a key), so this prints the links.

Flags:
  --open    Open the usage page in the browser`

var usageFlags = []flagDef{{long: "open"}}

func cmdUsage(a *app, p parsed) error {
	fmt.Fprintln(a.stdout, "Usage and spending:   "+a.baseURL+"/usage")
	fmt.Fprintln(a.stdout, "Balance and top-ups:  "+a.baseURL+"/wallet")
	fmt.Fprintln(a.stdout, "API keys and limits:  "+a.baseURL+"/keys")
	fmt.Fprintln(a.stdout, a.out.dim("API keys cannot read usage yet, so the CLI cannot show it here. Each `zharf run` answer prints its own cost."))
	if p.has("open") {
		if err := a.openBrowser(a.baseURL + "/usage"); err != nil {
			fmt.Fprintln(a.stderr, "Could not open a browser:", err)
		}
	}
	return nil
}
