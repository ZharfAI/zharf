package cmd

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// launchTest signs in to a fake platform and launches an app; nothing is executed.
func launchTest(t *testing.T, args ...string) (*testIO, string, error) {
	t.Helper()
	f := newFakePlatform(t)
	a, tio := testApp(t, f.URL, "")
	tio.store.Set(hostOf(f.URL), savedKey)
	p, err := parseArgs(args, append(append([]flagDef{}, globalFlags...), launchFlags...))
	if err != nil {
		t.Fatal(err)
	}
	return tio, f.URL, cmdLaunch(a, p)
}

func TestLaunchCodex(t *testing.T) {
	tio, base, err := launchTest(t, "codex", "--model", "qwen3.5-9b", "--", "exec", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, tio.stderr)
	}
	want := []string{
		"-c", `model_provider="zharf"`,
		"-c", `model="qwen3.5-9b"`,
		"-c", `model_providers.zharf.name="Zharf"`,
		"-c", `model_providers.zharf.base_url="` + base + `/v1"`,
		"-c", `model_providers.zharf.env_key="ZHARF_API_KEY"`,
		"-c", `model_providers.zharf.wire_api="responses"`,
		"exec", "hi",
	}
	if tio.execPath != "/opt/test/bin/codex" || !reflect.DeepEqual(tio.execArgs, want) {
		t.Fatalf("exec %s %q", tio.execPath, tio.execArgs)
	}
	if tio.execEnv["ZHARF_API_KEY"] != savedKey {
		t.Fatal("ZHARF_API_KEY not passed")
	}
	if len(tio.execEnv) <= 1 || len(tio.execEnv) < len(os.Environ())/2 {
		t.Fatal("the rest of the environment should be inherited")
	}
}

func TestLaunchQwen(t *testing.T) {
	tio, base, err := launchTest(t, "qwen", "-m", "gpt-5-mini:latest")
	if err != nil {
		t.Fatal(err)
	}
	if len(tio.execArgs) != 0 || tio.execEnv["OPENAI_API_KEY"] != savedKey || tio.execEnv["OPENAI_BASE_URL"] != base+"/v1" || tio.execEnv["OPENAI_MODEL"] != "gpt-5-mini" {
		t.Fatalf("args %q env %v", tio.execArgs, tio.execEnv)
	}
}

func TestLaunchHermes(t *testing.T) {
	// The fake platform is on 127.0.0.1, and Hermes sends no key to an IP address.
	if _, _, err := launchTest(t, "hermes", "-m", "qwen3.5-9b"); err == nil || !strings.Contains(err.Error(), "named host") {
		t.Fatalf("want a refusal for an IP base URL, got %v", err)
	}
	for base, want := range map[string]string{
		"https://platform.zharf.ai/v1": "ZHARF_API_KEY",
		"https://api.deepseek.com/v1":  "DEEPSEEK_API_KEY",
		"http://localhost:8080/v1":     "",
	} {
		if got := hermesKeyVar(base); got != want {
			t.Errorf("%s: %q, want %q", base, got, want)
		}
	}
	p := &launchPlan{env: map[string]string{}}
	if err := planHermes(nil, p, "deepseek-v4-flash", "https://platform.zharf.ai/v1", "zk-k"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.args, []string{"--provider", "custom", "-m", "deepseek-v4-flash"}) || p.env["CUSTOM_BASE_URL"] != "https://platform.zharf.ai/v1" || p.env["ZHARF_API_KEY"] != "zk-k" {
		t.Fatalf("hermes plan %+v", p)
	}
}

func TestLaunchOpenCode(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	tio, base, err := launchTest(t, "opencode", "--model", "qwen3.5-9b", "--", "run", "hi")
	if err != nil {
		t.Fatalf("%v\n%s", err, tio.stderr)
	}
	if tio.execPath != "/opt/test/bin/opencode" || !reflect.DeepEqual(tio.execArgs, []string{"run", "hi"}) {
		t.Fatalf("exec %s %q", tio.execPath, tio.execArgs)
	}
	if tio.execEnv["ZHARF_API_KEY"] != savedKey {
		t.Fatal("ZHARF_API_KEY not passed")
	}
	want := `{"$schema":"https://opencode.ai/config.json","model":"zharf/qwen3.5-9b","providers":{"zharf":{"env":["ZHARF_API_KEY"],"models":{"qwen3.5-9b":{"name":"qwen3.5-9b"}},"name":"Zharf","package":"@opencode/ai/providers/openai-compatible","settings":{"baseURL":"` + base + `/v1"}}}}`
	if got := tio.execEnv["OPENCODE_CONFIG_CONTENT"]; got != want {
		t.Fatalf("OPENCODE_CONFIG_CONTENT:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(tio.execEnv["OPENCODE_CONFIG_CONTENT"], savedKey) {
		t.Fatal("the key must not be in the config")
	}
	// Nothing is written to OpenCode's own config.
	entries, _ := os.ReadDir(xdg)
	if len(entries) != 0 {
		t.Fatalf("wrote into the config folder: %v", entries)
	}
}

func TestLaunchOpenCodeOneIsRefused(t *testing.T) {
	f := newFakePlatform(t)
	a, tio := testApp(t, f.URL, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tio.store.Set(hostOf(f.URL), savedKey)
	tio.version = "1.19.6"
	err := cmdLaunch(a, parsed{flags: map[string]string{"model": "qwen3.5-9b"}, args: []string{"opencode"}})
	if err == nil || !strings.Contains(err.Error(), "OpenCode 2") || tio.execPath != "" {
		t.Fatalf("OpenCode 1: %v", err)
	}
}

func TestLaunchRefusals(t *testing.T) {
	var ue *usageError
	if _, _, err := launchTest(t, "codex"); !errors.As(err, &ue) {
		t.Fatalf("no model: %v", err)
	}
	if _, _, err := launchTest(t, "claude", "-m", "qwen3.5-9b"); err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("claude: %v", err)
	}
	if _, _, err := launchTest(t, "openclaw", "-m", "qwen3.5-9b"); err == nil || !strings.Contains(err.Error(), "zharf serve") {
		t.Fatalf("openclaw: %v", err)
	}
	if _, _, err := launchTest(t, "codex", "-m", "flux-image"); err == nil || !strings.Contains(err.Error(), "type image") {
		t.Fatalf("image model: %v", err)
	}
	if tio, _, err := launchTest(t, "codex", "-m", "no-such"); !errors.Is(err, errQuiet) || tio.execPath != "" {
		t.Fatalf("unknown model: %v", err)
	}

	// Not installed: print the install command, exit 1, run nothing.
	f := newFakePlatform(t)
	a, tio := testApp(t, f.URL, "")
	tio.store.Set(hostOf(f.URL), savedKey)
	tio.installed["qwen"] = false
	err := cmdLaunch(a, parsed{flags: map[string]string{"model": "qwen3.5-9b"}, args: []string{"qwen"}})
	if !errors.Is(err, errQuiet) || tio.execPath != "" || !strings.Contains(tio.stderr.String(), "npm install -g @qwen-code/qwen-code@latest") {
		t.Fatalf("not installed: %v\n%s", err, tio.stderr)
	}

	// No app: the list, with what is installed.
	if err := cmdLaunch(a, parsed{flags: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	out := tio.stdout.String()
	if !strings.Contains(out, "codex") || !strings.Contains(out, "/opt/test/bin/codex") || !strings.Contains(out, "not installed") {
		t.Fatalf("list:\n%s", out)
	}
}
