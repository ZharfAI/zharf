package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const launchUsage = `Usage: zharf launch <app> --model <id> [-- app arguments]

Starts a coding agent on platform.zharf.ai with your saved key and the model you choose,
like 'ollama launch'. Arguments after -- go to the app.

  opencode   OpenCode 2    a "zharf" provider and model in OPENCODE_CONFIG_CONTENT for this
                           run; opencode.json is not changed
  codex      Codex CLI     -c overrides only; ~/.codex/config.toml is not changed
  qwen       Qwen Code     OPENAI_API_KEY, OPENAI_BASE_URL and OPENAI_MODEL for this run
  hermes     Hermes Agent  --provider custom with CUSTOM_BASE_URL; config.yaml not changed

  zharf launch                 lists the apps and whether each is installed
  zharf launch codex --model gpt-5-mini
  zharf launch hermes -m deepseek-v4-flash -- --yolo

Claude Code is not offered: Claude models are not available on this platform. OpenClaw runs
its models in its own Gateway service; point it at 'zharf serve' instead (docs/launch.md shows how).

Flags:
  -m, --model ID   The model (required; see 'zharf models --type text')`

var launchFlags = []flagDef{{long: "model", short: "m", value: true}}

// launchPlan is what a launch runs: the program, its arguments and the environment it gets.
type launchPlan struct {
	path  string
	args  []string
	env   map[string]string
	notes []string
}

type launchApp struct {
	name    string
	title   string
	binary  string
	install string
	plan    func(a *app, p *launchPlan, model, apiBase, key string) error
}

func launchApps() []launchApp {
	return []launchApp{
		{name: "opencode", title: "OpenCode", binary: "opencode", install: "curl -fsSL https://opencode.ai/v2/install | bash   (or: npm install -g @opencode/cli)", plan: planOpenCode},
		{name: "codex", title: "Codex CLI", binary: "codex", install: "npm install -g @openai/codex", plan: planCodex},
		{name: "qwen", title: "Qwen Code", binary: "qwen", install: "npm install -g @qwen-code/qwen-code@latest", plan: planQwen},
		{name: "hermes", title: "Hermes Agent", binary: "hermes", install: "curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash", plan: planHermes},
	}
}

// tomlString quotes a value for a Codex -c override (parsed as TOML).
func tomlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Codex: a "zharf" model provider given entirely as -c overrides, speaking the Responses API,
// with the key read from ZHARF_API_KEY.
func planCodex(_ *app, p *launchPlan, model, apiBase, key string) error {
	p.args = []string{
		"-c", "model_provider=" + tomlString("zharf"),
		"-c", "model=" + tomlString(model),
		"-c", "model_providers.zharf.name=" + tomlString("Zharf"),
		"-c", "model_providers.zharf.base_url=" + tomlString(apiBase),
		"-c", "model_providers.zharf.env_key=" + tomlString("ZHARF_API_KEY"),
		"-c", "model_providers.zharf.wire_api=" + tomlString("responses"),
	}
	p.env["ZHARF_API_KEY"] = key
	return nil
}

// Qwen Code reads an OpenAI-compatible endpoint from these three variables.
func planQwen(_ *app, p *launchPlan, model, apiBase, key string) error {
	p.env["OPENAI_API_KEY"] = key
	p.env["OPENAI_BASE_URL"] = apiBase
	p.env["OPENAI_MODEL"] = model
	return nil
}

// hermesKeyVar mirrors Hermes's _host_derived_api_key: for `--provider custom` it sends the
// key found in <VENDOR>_API_KEY, the vendor being the host's second-to-last label after any
// "api."/"www." (platform.zharf.ai → ZHARF_API_KEY). IP addresses and localhost get none.
func hermesKeyVar(apiBase string) string {
	host := hostOf(apiBase)
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	labels := strings.Split(host, ".")
	if host == "" || host == "localhost" || strings.ContainsAny(labels[len(labels)-1], "0123456789") {
		return ""
	}
	for len(labels) > 0 && (labels[0] == "api" || labels[0] == "www") {
		labels = labels[1:]
	}
	if len(labels) < 2 {
		return ""
	}
	vendor := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(labels[len(labels)-2], "_")
	return strings.ToUpper(vendor) + "_API_KEY"
}

// Hermes: its built-in "custom" provider, with the endpoint in CUSTOM_BASE_URL and the model
// as -m. Nothing in ~/.hermes/config.yaml changes.
func planHermes(_ *app, p *launchPlan, model, apiBase, key string) error {
	keyVar := hermesKeyVar(apiBase)
	if keyVar == "" {
		return fmt.Errorf("Hermes only sends a key to a named host, not to %s; use the default base URL", hostOf(apiBase))
	}
	p.args = []string{"--provider", "custom", "-m", model}
	p.env["CUSTOM_BASE_URL"] = apiBase
	p.env[keyVar] = key
	p.notes = append(p.notes, "Hermes reads ~/.hermes/.env after the environment, so a CUSTOM_BASE_URL or "+keyVar+" set there wins.")
	return nil
}

// opencodeConfig is the "zharf" provider and the model, in OpenCode 2's config format.
func opencodeConfig(model, apiBase string) map[string]any {
	return map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"providers": map[string]any{
			"zharf": map[string]any{
				"name":     "Zharf",
				"env":      []string{"ZHARF_API_KEY"},
				"package":  "@opencode/ai/providers/openai-compatible",
				"settings": map[string]any{"baseURL": apiBase},
				"models":   map[string]any{model: map[string]any{"name": model}},
			},
		},
		"model": "zharf/" + model,
	}
}

// OpenCode 2 reads one more config source from OPENCODE_CONFIG_CONTENT, after the global and
// project files. The provider and the model go there for this run only: no file is written, and
// OpenCode reads the key from ZHARF_API_KEY, so the key is not in the config either.
func planOpenCode(a *app, p *launchPlan, model, apiBase, key string) error {
	if out, err := a.runOutput(p.path, "--version"); err == nil {
		if m := regexp.MustCompile(`(\d+)\.\d+`).FindStringSubmatch(out); m != nil && (m[1] == "0" || m[1] == "1") {
			return fmt.Errorf("zharf launch works with OpenCode 2 (this is %s). Upgrade: curl -fsSL https://opencode.ai/v2/install | bash", strings.TrimSpace(out))
		}
	}
	content, err := json.Marshal(opencodeConfig(model, apiBase))
	if err != nil {
		return err
	}
	p.env["OPENCODE_CONFIG_CONTENT"] = string(content)
	p.env["ZHARF_API_KEY"] = key
	return nil
}

// ── The command ─────────────────────────────────────────────────────────────────────────────

func cmdLaunch(a *app, p parsed) error {
	apps := launchApps()
	if len(p.args) == 0 {
		fmt.Fprintln(a.stdout, "Apps zharf can launch:")
		for _, app := range apps {
			state := a.out.dim("not installed · " + app.install)
			if path, err := a.lookPath(app.binary); err == nil {
				state = a.out.green("installed") + a.out.dim(" · "+path)
			}
			fmt.Fprintf(a.stdout, "  %s %s %s\n", padRight(app.name, 9), padRight(app.title, 13), state)
		}
		fmt.Fprintln(a.stdout, a.out.dim("\nzharf launch <app> --model <id>   e.g. zharf launch codex --model gpt-5-mini"))
		return nil
	}
	name := strings.ToLower(p.args[0])
	extra := p.args[1:]
	var chosen *launchApp
	for i := range apps {
		if apps[i].name == name {
			chosen = &apps[i]
		}
	}
	switch {
	case chosen != nil:
	case name == "claude" || name == "claude-code":
		return errors.New("Claude Code is not offered: Claude models are not available on this platform")
	case name == "openclaw":
		return fmt.Errorf("OpenClaw runs its models in its Gateway service, so zharf launch does not reconfigure it. Run `zharf serve` and add it to OpenClaw as an OpenAI-compatible provider at http://127.0.0.1:11434/v1 (docs/launch.md in github.com/ZharfAI/zharf shows how)")
	default:
		names := make([]string, len(apps))
		for i, app := range apps {
			names[i] = app.name
		}
		sort.Strings(names)
		return usageErr("no app %q; zharf launch knows %s", name, strings.Join(names, ", "))
	}

	model := strings.TrimSuffix(p.str("model", ""), ":latest")
	if model == "" {
		return usageErr("which model? zharf launch %s --model <id> (see `zharf models --type text`)", chosen.name)
	}
	path, err := a.lookPath(chosen.binary)
	if err != nil {
		fmt.Fprintf(a.stderr, "%s is not installed. Install it with:\n\n  %s\n\nthen run this again.\n", chosen.title, chosen.install)
		return errQuiet
	}
	c, src, err := a.client(true)
	if err != nil {
		return err
	}
	m, err := c.getModel(context.Background(), model)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			s := &chatSession{a: a, c: c, model: model}
			a.printError(err)
			s.suggest()
			return errQuiet
		}
		return err
	}
	if kind := modelType(m); kind != "text" {
		return fmt.Errorf("%s is a model of type %s; coding agents need a text model (see `zharf models --type text`)", model, kind)
	}

	plan := &launchPlan{path: path, env: map[string]string{}}
	apiBase := a.baseURL + "/v1"
	if err := chosen.plan(a, plan, model, apiBase, c.Key); err != nil {
		return err
	}
	plan.args = append(plan.args, extra...)
	for _, note := range plan.notes {
		fmt.Fprintln(a.stderr, a.errp.dim(note))
	}
	fmt.Fprintf(a.stderr, "Launching %s with %s on %s (key %s from %s)…\n", chosen.title, a.errp.bold(model), a.host, keyPrefix(c.Key), src.Where)
	return a.execApp(plan.path, plan.args, mergeEnv(os.Environ(), plan.env))
}

// mergeEnv returns base with the given variables set (replacing any earlier value).
func mergeEnv(base []string, set map[string]string) []string {
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[name]; replaced {
			continue
		}
		out = append(out, kv)
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out = append(out, name+"="+set[name])
	}
	return out
}

// execProgram replaces zharf with the app, so it owns the terminal, its signals and its exit
// status. Windows has no exec: there the app runs as a child and its exit status is passed on.
func execProgram(path string, args, env []string) error {
	if runtime.GOOS != "windows" {
		return syscall.Exec(path, append([]string{filepath.Base(path)}, args...), env)
	}
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		os.Exit(exitErr.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// commandOutput runs a program briefly and returns what it printed.
func commandOutput(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
