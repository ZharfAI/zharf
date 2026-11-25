package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

const persianStream = "" +
	": keep-alive\n\n" +
	"data: {\"model\":\"qwen3.5-9b\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"کاربر سلام کرد\"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"سلام! \"}}]}\r\n\r\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"تهران در پاییز \"}}]}\n\n" +
	"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"زیباست 🍂\"},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":9,\"total_tokens\":21,\"cost_zharf\":1e-06}}\n\n" +
	"data: [DONE]\n\n"

const persianAnswer = "سلام! تهران در پاییز زیباست 🍂"

func TestSSEReaderEventsAndFields(t *testing.T) {
	stream := ": comment\n" +
		"event: message\nid: 7\ndata: first line\ndata: second line\n\n" +
		"data:no-space\r\n\r\n" +
		"data: last without blank line"
	r := newSSEReader(strings.NewReader(stream))
	want := []sseEvent{
		{Event: "message", ID: "7", Data: "first line\nsecond line"},
		{Data: "no-space"},
		{Data: "last without blank line"},
	}
	for i, w := range want {
		got, err := r.next()
		if err != nil {
			t.Fatalf("event %d: %v", i, err)
		}
		if got != w {
			t.Fatalf("event %d: got %+v, want %+v", i, got, w)
		}
	}
	if _, err := r.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

// The same Persian stream, cut into pieces of every size from 1 to 13 bytes (and one byte at
// a time), must give the same text, and every piece handed out must be whole UTF-8.
func TestReadChatStreamSplitMultibyteRunes(t *testing.T) {
	readers := map[string]func() io.Reader{
		"one byte at a time": func() io.Reader { return iotest.OneByteReader(strings.NewReader(persianStream)) },
		"half reads":         func() io.Reader { return iotest.HalfReader(strings.NewReader(persianStream)) },
	}
	for size := 1; size <= 13; size++ {
		sizes := []int{size, size + 2, 1}
		readers["chunks "+string(rune('a'+size))] = func() io.Reader {
			return &chunkReader{data: []byte(persianStream), sizes: sizes}
		}
	}
	// Make sure some of those cuts really land inside a character.
	idx := strings.Index(persianStream, "سلام")
	if idx < 0 || utf8.RuneStart(persianStream[idx+1]) {
		t.Fatal("test data: expected a two-byte character")
	}
	for name, mk := range readers {
		var content, reasoning strings.Builder
		res, err := readChatStream(mk(), "text/event-stream", func(c, r string) error {
			if !utf8.ValidString(c) || !utf8.ValidString(r) {
				t.Fatalf("%s: a piece is not valid UTF-8: %q %q", name, c, r)
			}
			content.WriteString(c)
			reasoning.WriteString(r)
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if content.String() != persianAnswer || res.Content != persianAnswer {
			t.Fatalf("%s: content %q", name, content.String())
		}
		if reasoning.String() != "کاربر سلام کرد" {
			t.Fatalf("%s: reasoning %q", name, reasoning.String())
		}
		if res.FinishReason != "stop" || res.Model != "qwen3.5-9b" {
			t.Fatalf("%s: finish %q model %q", name, res.FinishReason, res.Model)
		}
		if res.Usage == nil || res.Usage.PromptTokens != 12 || res.Usage.CompletionTokens != 9 {
			t.Fatalf("%s: usage %+v", name, res.Usage)
		}
		if got := res.Usage.CostZharf.String(); got != "0.000001" {
			t.Fatalf("%s: cost %q, want 0.000001 (never 1e-06)", name, got)
		}
	}
}

func TestReadChatStreamErrorMidStream(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"نیمه\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"Your ZHARF balance is too low.\",\"message_fa\":\"اعتبار ژرف شما کافی نیست.\",\"type\":\"billing_error\",\"code\":\"insufficient_balance\"}}\n\n"
	res, err := readChatStream(strings.NewReader(stream), "text/event-stream", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want an APIError, got %v", err)
	}
	if apiErr.Code != "insufficient_balance" || apiErr.MessageFa != "اعتبار ژرف شما کافی نیست." {
		t.Fatalf("got %+v", apiErr)
	}
	if res.Content != "نیمه" {
		t.Fatalf("the text before the error should be kept, got %q", res.Content)
	}
}

func TestReadChatStreamToolCallFragments(t *testing.T) {
	stream := "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"type\":\"function\",\"function\":{\"name\":\"weather\",\"arguments\":\"{\\\"ci\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ty\\\":\\\"تهران\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"call_b\",\"function\":{\"name\":\"time\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	res, err := readChatStream(strings.NewReader(stream), "text/event-stream; charset=utf-8", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("want 2 tool calls, got %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].ID != "call_a" || res.ToolCalls[0].Function.Name != "weather" || res.ToolCalls[0].Function.Arguments != `{"city":"تهران"}` {
		t.Fatalf("first call: %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].Function.Name != "time" || res.FinishReason != "tool_calls" {
		t.Fatalf("second call: %+v, finish %q", res.ToolCalls[1], res.FinishReason)
	}
}

func TestReadChatStreamPlainJSON(t *testing.T) {
	body := `{"model":"gpt-5-mini","choices":[{"index":0,"message":{"role":"assistant","content":"درود"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"cost_zharf":0.0123}}`
	var got string
	res, err := readChatStream(strings.NewReader(body), "application/json", func(c, _ string) error { got += c; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got != "درود" || res.Usage.CostZharf.String() != "0.0123" {
		t.Fatalf("got %q, cost %q", got, res.Usage.CostZharf)
	}
}

func TestZharfAmountFormatting(t *testing.T) {
	cases := map[string]string{"1e-06": "0.000001", "0.0123000": "0.0123", "12": "12", "2.50": "2.5", "1E-7": "0.0000001"}
	for in, want := range cases {
		if got := zharfAmount(in).String(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
