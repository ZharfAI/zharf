package cmd

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseArgsInterspersed(t *testing.T) {
	defs := append(append([]flagDef{}, globalFlags...), runFlags...)
	p, err := parseArgs([]string{"qwen3.5-9b", "-5 degrees is cold", "--system", "be brief", "--temperature=0.3", "-q", "--", "--not-a-flag"}, defs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.args, []string{"qwen3.5-9b", "-5 degrees is cold", "--not-a-flag"}) {
		t.Errorf("args %q", p.args)
	}
	if p.str("system", "") != "be brief" || p.str("temperature", "") != "0.3" || !p.has("quiet") {
		t.Errorf("flags %v", p.flags)
	}
	if _, err := parseArgs([]string{"--nope"}, defs); err == nil {
		t.Error("unknown flag accepted")
	}
	if _, err := parseArgs([]string{"--system"}, defs); err == nil {
		t.Error("missing value accepted")
	}
	cmd, rest := splitCommand([]string{"--base-url", "http://x", "run", "m"})
	if cmd != "run" || !reflect.DeepEqual(rest, []string{"--base-url", "http://x", "m"}) {
		t.Errorf("splitCommand: %q %q", cmd, rest)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                              "https://platform.zharf.ai",
		"platform.zharf.ai":             "https://platform.zharf.ai",
		"https://platform.zharf.ai/v1/": "https://platform.zharf.ai",
		"http://127.0.0.1:18795":        "http://127.0.0.1:18795",
		"http://127.0.0.1:18795/v1?x=1": "http://127.0.0.1:18795",
		"https://example.com/zharf/v1":  "https://example.com/zharf",
	}
	for in, want := range cases {
		got, err := normalizeBaseURL(in)
		if err != nil || got != want {
			t.Errorf("%q: got %q (%v), want %q", in, got, err, want)
		}
	}
	if _, err := normalizeBaseURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}

func TestFileStoreIsPrivate(t *testing.T) {
	dir := t.TempDir()
	f := fileStore{path: filepath.Join(dir, "zharf", "credentials")}
	if err := f.Set("platform.zharf.ai", "zk-one"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("127.0.0.1:18795", "zk-two"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Get("platform.zharf.ai"); got != "zk-one" {
		t.Fatalf("got %q", got)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(f.path)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", info.Mode().Perm())
		}
	}
	if err := f.Delete("platform.zharf.ai"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("platform.zharf.ai"); err == nil {
		t.Fatal("deleted key still there")
	}
	if got, _ := f.Get("127.0.0.1:18795"); got != "zk-two" {
		t.Fatal("other host's key lost")
	}
}

func TestSaveKeyFallsBackAndFindKeyPrefersEnv(t *testing.T) {
	t.Setenv("ZHARF_API_KEY", "")
	keyring := newMemStore("keyring")
	keyring.failSet = true
	file := newMemStore("file")
	where, err := saveKey([]credStore{keyring, file}, "h", "zk-k")
	if err != nil || where != file {
		t.Fatalf("fallback: %v %v", where, err)
	}
	src, err := findKey([]credStore{keyring, file}, "h")
	if err != nil || src.Key != "zk-k" || src.Where != "file" {
		t.Fatalf("findKey: %+v %v", src, err)
	}
	t.Setenv("ZHARF_API_KEY", "zk-env")
	src, _ = findKey([]credStore{keyring, file}, "h")
	if src.Key != "zk-env" {
		t.Fatalf("env should win: %+v", src)
	}
}

func TestCompareVersionsAndProgramCheck(t *testing.T) {
	if compareVersions("0.10.0", "0.9.9") <= 0 || compareVersions("v0.1.0", "0.1.0") != 0 || compareVersions("0.1.0", "0.1.1-rc1") >= 0 {
		t.Error("compareVersions")
	}
	if !looksLikeProgram([]byte{0xcf, 0xfa, 0xed, 0xfe}, "darwin") || !looksLikeProgram([]byte("\x7fELF"), "linux") || !looksLikeProgram([]byte("MZ\x90\x00"), "windows") {
		t.Error("magic")
	}
	if looksLikeProgram([]byte("<htm"), "linux") {
		t.Error("an HTML error page passed as a program")
	}
}

func TestReadMessageMultiline(t *testing.T) {
	in := bufio.NewReader(strings.NewReader("one line\n\"\"\"first\nسطر دوم\nlast\"\"\"\n\"\"\"inline\"\"\"\n"))
	var prompt strings.Builder
	want := []string{"one line", "first\nسطر دوم\nlast", "inline"}
	for _, w := range want {
		got, err := readMessage(in, &prompt, palette{})
		if err != nil || got != w {
			t.Fatalf("got %q (%v), want %q", got, err, w)
		}
	}
	if _, err := readMessage(in, &prompt, palette{}); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

// `cat file | zharf run model "summarise"`: the piped text comes first, then the prompt; the
// answer alone goes to stdout and the cost to stderr.
func TestRunOneShotWithPipedInput(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer zk-run" {
			w.WriteHeader(401)
			return
		}
		json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, persianStream)
	}))
	defer srv.Close()
	a, tio := testApp(t, srv.URL, "متن فایل\n")
	tio.store.Set(hostOf(srv.URL), "zk-run")
	err := cmdRun(a, parsed{flags: map[string]string{"system": "خلاصه کن"}, args: []string{"qwen3.5-9b", "summarise"}})
	if err != nil {
		t.Fatalf("run: %v\n%s", err, tio.stderr)
	}
	if tio.stdout.String() != persianAnswer+"\n" {
		t.Fatalf("stdout %q", tio.stdout.String())
	}
	if !strings.Contains(tio.stderr.String(), "0.000001 ژرف") {
		t.Fatalf("cost line missing:\n%s", tio.stderr)
	}
	msgs := sent["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["content"] != "خلاصه کن" || msgs[1].(map[string]any)["content"] != "متن فایل\n\nsummarise" {
		t.Fatalf("messages %v", msgs)
	}
}
