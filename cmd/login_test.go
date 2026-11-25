package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// deviceStub is the sign-in half of the platform, scripted: each poll of /api/cli/token gets
// the next answer.
type deviceStub struct {
	*httptest.Server
	mu        sync.Mutex
	answers   []func(w http.ResponseWriter)
	polls     int
	started   map[string]string
	badOrigin bool
}

func pending(w http.ResponseWriter) {
	w.WriteHeader(428)
	io.WriteString(w, `{"error":{"code":"authorization_pending","message":"Waiting.","message_fa":"در انتظار تأیید."}}`)
}

func slowDown(w http.ResponseWriter) {
	w.WriteHeader(400)
	io.WriteString(w, `{"error":{"code":"slow_down","message":"Slow down."}}`)
}

func approved(w http.ResponseWriter) {
	io.WriteString(w, `{"api_key":"zk-DeviceKey987654321","account":{"phone":"09121234567","display_name":"سارا"}}`)
}

func newDeviceStub(t *testing.T, answers ...func(w http.ResponseWriter)) *deviceStub {
	d := &deviceStub{answers: answers}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/cli/device", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			d.badOrigin = true
		}
		json.NewDecoder(r.Body).Decode(&d.started)
		io.WriteString(w, `{"device_code":"dev-123","user_code":"WDJB-MJHT","verification_uri":"`+"http://"+r.Host+`/cli","verification_uri_complete":"`+"http://"+r.Host+`/cli?code=WDJB-MJHT","interval":3,"expires_in":600}`)
	})
	mux.HandleFunc("POST /api/cli/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["device_code"] != "dev-123" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"code":"bad_device_code"}}`)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.polls >= len(d.answers) {
			w.WriteHeader(410)
			io.WriteString(w, `{"error":{"code":"expired_token"}}`)
			return
		}
		answer := d.answers[d.polls]
		d.polls++
		answer(w)
	})
	d.Server = httptest.NewServer(mux)
	t.Cleanup(d.Close)
	return d
}

func TestLoginDeviceFlow(t *testing.T) {
	stub := newDeviceStub(t, pending, pending, slowDown, pending, approved)
	a, tio := testApp(t, stub.URL, "")
	if err := cmdLogin(a, parsed{flags: map[string]string{}}); err != nil {
		t.Fatalf("login: %v\n%s", err, tio.stderr)
	}
	if got, _ := tio.store.Get(hostOf(stub.URL)); got != "zk-DeviceKey987654321" {
		t.Fatalf("saved key %q", got)
	}
	if stub.badOrigin {
		t.Error("the CLI must not send an Origin header")
	}
	if !strings.Contains(stub.started["client_name"], "(") {
		t.Errorf("client_name %q", stub.started["client_name"])
	}
	out := tio.stderr.String()
	for _, want := range []string{"WDJB-MJHT", "/cli?code=WDJB-MJHT", "ورود را تأیید کنید", "Signed in", "سارا"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(tio.opened) != 1 || !strings.HasSuffix(tio.opened[0], "/cli?code=WDJB-MJHT") {
		t.Errorf("browser opened %v", tio.opened)
	}
	// Every 3 s, and 5 s slower after slow_down.
	want := []time.Duration{3 * time.Second, 3 * time.Second, 3 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(tio.slept) != len(want) {
		t.Fatalf("slept %v", tio.slept)
	}
	for i := range want {
		if tio.slept[i] != want[i] {
			t.Fatalf("slept %v, want %v", tio.slept, want)
		}
	}
	if prof := loadProfiles()[hostOf(stub.URL)]; prof.Phone != "09121234567" || prof.DisplayName != "سارا" {
		t.Errorf("profile %+v", prof)
	}
	// The key is single-use on the server; a second login starts over.
	if stub.polls != 5 {
		t.Errorf("polls %d", stub.polls)
	}
}

func TestLoginDeviceDeniedAndExpired(t *testing.T) {
	denied := func(w http.ResponseWriter) {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"access_denied"}}`)
	}
	stub := newDeviceStub(t, pending, denied)
	a, tio := testApp(t, stub.URL, "")
	err := cmdLogin(a, parsed{flags: map[string]string{"no-browser": "true"}})
	if !errors.Is(err, errDeviceDenied) {
		t.Fatalf("want denied, got %v", err)
	}
	if len(tio.opened) != 0 {
		t.Error("--no-browser opened a browser")
	}
	if _, err := tio.store.Get(hostOf(stub.URL)); err == nil {
		t.Error("a denied sign-in saved a key")
	}

	expired := newDeviceStub(t, pending) // then 410
	a2, _ := testApp(t, expired.URL, "")
	if err := cmdLogin(a2, parsed{flags: map[string]string{"no-browser": "true"}}); !errors.Is(err, errDeviceExpired) {
		t.Fatalf("want expired, got %v", err)
	}
}

func TestPollDeviceRetriesTransientFailures(t *testing.T) {
	flaky := func(w http.ResponseWriter) {
		w.WriteHeader(503)
		io.WriteString(w, `<html>bad gateway</html>`)
	}
	stub := newDeviceStub(t, flaky, pending, flaky, approved)
	c := &Client{BaseURL: stub.URL, HTTP: newHTTPClient()}
	start, err := c.startDevice(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	noSleep := func(context.Context, time.Duration) error { return nil }
	token, err := pollDevice(context.Background(), c, start, noSleep, time.Now)
	if err != nil || token.APIKey != "zk-DeviceKey987654321" {
		t.Fatalf("token %+v, err %v", token, err)
	}
}

func TestPollDeviceStopsAtExpiry(t *testing.T) {
	stub := newDeviceStub(t, pending, pending, pending, pending, pending, pending)
	c := &Client{BaseURL: stub.URL, HTTP: newHTTPClient()}
	start := &deviceStart{DeviceCode: "dev-123", Interval: 3, ExpiresIn: 10}
	clock := time.Now()
	now := func() time.Time { return clock }
	sleep := func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil }
	if _, err := pollDevice(context.Background(), c, start, sleep, now); !errors.Is(err, errDeviceExpired) {
		t.Fatalf("want expired, got %v", err)
	}
	if stub.polls != 3 {
		t.Fatalf("polled %d times in 10 s at 3 s intervals, want 3", stub.polls)
	}
}

func TestLoginWithKeyAndWhoami(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/video/generations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer zk-Pasted0000000001" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"Invalid or revoked API key.","message_fa":"این کلید API پیدا نشد.","code":"bad_key"}}`)
			return
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"No such job on this account.","code":"job_not_found"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a, tio := testApp(t, srv.URL, "zk-Pasted0000000001\n")
	if err := cmdLogin(a, parsed{flags: map[string]string{"with-key": "true"}}); err != nil {
		t.Fatalf("login --with-key: %v", err)
	}
	if got, _ := tio.store.Get(hostOf(srv.URL)); got != "zk-Pasted0000000001" {
		t.Fatalf("saved %q", got)
	}
	if err := cmdWhoami(a, parsed{}); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if out := tio.stdout.String(); !strings.Contains(out, "zk-Pasted00…") || !strings.Contains(out, "accepts this key") {
		t.Fatalf("whoami output:\n%s", out)
	}

	bad, tio2 := testApp(t, srv.URL, "zk-Wrong\n")
	err := cmdLogin(bad, parsed{flags: map[string]string{"with-key": "true"}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 {
		t.Fatalf("a rejected key should not be saved: %v", err)
	}
	if _, err := tio2.store.Get(hostOf(srv.URL)); err == nil {
		t.Fatal("rejected key saved")
	}

	// ZHARF_API_KEY wins over the saved key.
	t.Setenv("ZHARF_API_KEY", "zk-Wrong")
	tio.stdout.Reset()
	if err := cmdWhoami(a, parsed{}); !errors.Is(err, errQuiet) {
		t.Fatalf("whoami with a bad env key: %v", err)
	}
	if out := tio.stdout.String(); !strings.Contains(out, "ZHARF_API_KEY") || !strings.Contains(out, "rejects") {
		t.Fatalf("whoami output:\n%s", out)
	}
}

func TestLogoutRemovesKeyAndProfile(t *testing.T) {
	a, tio := testApp(t, "https://platform.zharf.ai", "")
	tio.store.Set("platform.zharf.ai", "zk-x")
	saveProfile("platform.zharf.ai", &profile{Phone: "0912"})
	if err := cmdLogout(a, parsed{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tio.store.Get("platform.zharf.ai"); err == nil {
		t.Fatal("key still saved")
	}
	if _, ok := loadProfiles()["platform.zharf.ai"]; ok {
		t.Fatal("profile still saved")
	}
	if !strings.Contains(tio.stderr.String(), "still works until you revoke it") {
		t.Fatalf("logout should say the key is not revoked:\n%s", tio.stderr)
	}
}
