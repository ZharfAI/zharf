package cmd

import (
	"strings"
	"testing"
)

// A whole chat, typed: two turns (the second a """ block) with a model switch between them,
// then /cost and /bye. The second request must carry the first turn.
func TestREPLKeepsConversationAndSwitchesModel(t *testing.T) {
	f := newFakePlatform(t)
	script := strings.Join([]string{
		"سلام",
		"/model gpt-5-mini",
		`"""خط اول`,
		`خط دوم"""`,
		"/cost",
		"/model no-such-model",
		"/bye",
	}, "\n") + "\n"
	a, tio := testApp(t, f.URL, script)
	a.inTTY = true
	tio.store.Set(hostOf(f.URL), savedKey)

	if err := cmdRun(a, parsed{flags: map[string]string{"system": "کوتاه"}, args: []string{"qwen3.5-9b"}}); err != nil {
		t.Fatalf("run: %v\n%s", err, tio.stderr)
	}
	if n := f.chatCalls.Load(); n != 2 {
		t.Fatalf("chat calls: %d, want 2", n)
	}
	if got := tio.stdout.String(); got != persianAnswer+"\n"+persianAnswer+"\n" {
		t.Fatalf("stdout %q", got)
	}

	last := f.lastChat.Load().(map[string]any)
	if last["model"] != "gpt-5-mini" {
		t.Fatalf("second turn went to %v", last["model"])
	}
	var roles, contents []string
	for _, m := range last["messages"].([]any) {
		msg := m.(map[string]any)
		roles = append(roles, msg["role"].(string))
		contents = append(contents, msg["content"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("roles %v", roles)
	}
	if contents[0] != "کوتاه" || contents[1] != "سلام" || contents[2] != persianAnswer || contents[3] != "خط اول\nخط دوم" {
		t.Fatalf("contents %q", contents)
	}

	errOut := tio.stderr.String()
	for _, want := range []string{
		"Now talking to gpt-5-mini",
		"This chat: 0.000002 ژرف, 42 tokens.",
		"Did you mean",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
}
