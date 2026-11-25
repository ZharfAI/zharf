package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The Ollama API's shapes (https://github.com/ollama/ollama/blob/main/docs/api.md) and their
// mapping to the platform's OpenAI-compatible chat API.

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Index     *int            `json:"index,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaChatRequest struct {
	Model     string          `json:"model"`
	Messages  []ollamaMessage `json:"messages"`
	Stream    *bool           `json:"stream"`
	Format    json.RawMessage `json:"format"`
	Options   map[string]any  `json:"options"`
	Tools     json.RawMessage `json:"tools"`
	Think     json.RawMessage `json:"think"`
	KeepAlive json.RawMessage `json:"keep_alive"`
}

type ollamaGenerateRequest struct {
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt"`
	Suffix    string          `json:"suffix"`
	System    string          `json:"system"`
	Images    []string        `json:"images"`
	Stream    *bool           `json:"stream"`
	Format    json.RawMessage `json:"format"`
	Options   map[string]any  `json:"options"`
	Think     json.RawMessage `json:"think"`
	KeepAlive json.RawMessage `json:"keep_alive"`
	Raw       bool            `json:"raw"`
}

// wantsStream: Ollama streams unless "stream": false.
func wantsStream(stream *bool) bool { return stream == nil || *stream }

// unloadRequested: keep_alive 0 means "unload", which Ollama answers with done_reason "unload".
func unloadRequested(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return s == "0" || s == "0s" || s == "0m"
}

// ollamaModelName: Ollama tools add ":latest" to names without a tag.
func ollamaModelName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), ":latest")
}

// ── Options ─────────────────────────────────────────────────────────────────────────────────

func optFloat(opts map[string]any, key string) *float64 {
	switch v := opts[key].(type) {
	case float64:
		return &v
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func optInt(opts map[string]any, key string) *int {
	if f := optFloat(opts, key); f != nil {
		n := int(*f)
		return &n
	}
	return nil
}

// applyOptions maps Ollama's options onto the OpenAI request: temperature, top_p, top_k,
// num_predict → max_tokens (Ollama's -1 and -2 mean "no limit"), stop, seed and the penalties.
// num_ctx, repeat_penalty and the other runner settings have no meaning on the platform.
func applyOptions(req *oaChatRequest, opts map[string]any) {
	if opts == nil {
		return
	}
	req.Temperature = optFloat(opts, "temperature")
	req.TopP = optFloat(opts, "top_p")
	req.TopK = optInt(opts, "top_k")
	if n := optInt(opts, "num_predict"); n != nil && *n > 0 {
		req.MaxTokens = n
	}
	if seed := optFloat(opts, "seed"); seed != nil {
		s := int64(*seed)
		req.Seed = &s
	}
	req.PresencePenalty = optFloat(opts, "presence_penalty")
	req.FrequencyPenalty = optFloat(opts, "frequency_penalty")
	switch stop := opts["stop"].(type) {
	case string:
		if stop != "" {
			req.Stop = []string{stop}
		}
	case []any:
		for _, s := range stop {
			if text, ok := s.(string); ok && text != "" {
				req.Stop = append(req.Stop, text)
			}
		}
	}
}

// responseFormat maps Ollama's "format": "json" to JSON mode, and a JSON schema to structured
// output.
func responseFormat(raw json.RawMessage) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == `""` {
		return nil
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		if text == "json" {
			return map[string]any{"type": "json_object"}
		}
		return nil
	}
	if trimmed[0] == '{' {
		return map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "response", "schema": json.RawMessage(trimmed)},
		}
	}
	return nil
}

// thinkSetting reads Ollama's "think": true/false or "low"/"medium"/"high". It returns the
// reasoning effort to ask for, and whether reasoning should be shown at all.
func thinkSetting(raw json.RawMessage) (effort string, show bool) {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "", "null", "true":
		return "", true
	case "false":
		return "", false
	}
	var level string
	if json.Unmarshal(raw, &level) == nil {
		switch level {
		case "low", "medium", "high":
			return level, true
		}
	}
	return "", true
}

// ── Messages ────────────────────────────────────────────────────────────────────────────────

// imageDataURL turns Ollama's base64 image into a data URL, with the type read from the
// image's first bytes. Data and http(s) URLs pass through.
func imageDataURL(image string) string {
	image = strings.TrimSpace(image)
	if strings.HasPrefix(image, "data:") || strings.HasPrefix(image, "http://") || strings.HasPrefix(image, "https://") {
		return image
	}
	head := image
	if len(head) > 64 {
		head = head[:64]
	}
	head = head[:len(head)/4*4]
	raw, _ := base64.StdEncoding.DecodeString(head)
	if len(raw) == 0 {
		raw, _ = base64.RawStdEncoding.DecodeString(head)
	}
	return "data:" + sniffImage(raw) + ";base64," + image
}

func sniffImage(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && string(b[8:12]) == "WEBP":
		return "image/webp"
	case len(b) >= 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "heic" || string(b[8:12]) == "heix" || string(b[8:12]) == "mif1"):
		return "image/heic"
	case bytes.HasPrefix(b, []byte("BM")):
		return "image/bmp"
	}
	ct := http.DetectContentType(b)
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	return "image/jpeg"
}

// toOpenAIMessages converts Ollama messages. Images become image_url parts. Ollama's tool calls
// carry no ids, so each gets one ("call_1", …) and the next "tool" message answering that
// function (by tool_name, else in order) gets the matching tool_call_id.
func toOpenAIMessages(msgs []ollamaMessage) []oaMessage {
	out := make([]oaMessage, 0, len(msgs))
	type pendingCall struct{ id, name string }
	var pending []pendingCall
	next := 0
	for _, m := range msgs {
		om := oaMessage{Role: m.Role}
		if len(m.Images) > 0 {
			parts := []oaContentPart{}
			if m.Content != "" {
				parts = append(parts, oaContentPart{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, oaContentPart{Type: "image_url", ImageURL: &oaImageURL{URL: imageDataURL(img)}})
			}
			om.Content = parts
		} else {
			om.Content = m.Content
		}
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				next++
				id := fmt.Sprintf("call_%d", next)
				om.ToolCalls = append(om.ToolCalls, oaToolCall{ID: id, Type: "function", Function: oaFunctionCall{Name: tc.Function.Name, Arguments: argumentsString(tc.Function.Arguments)}})
				pending = append(pending, pendingCall{id: id, name: tc.Function.Name})
			}
		}
		if m.Role == "tool" {
			at := -1
			for i, p := range pending {
				if m.ToolName == "" || p.name == m.ToolName {
					at = i
					break
				}
			}
			if at >= 0 {
				om.ToolCallID = pending[at].id
				pending = append(pending[:at], pending[at+1:]...)
			} else {
				next++
				om.ToolCallID = fmt.Sprintf("call_%d", next)
			}
			if m.ToolName != "" {
				om.Name = m.ToolName
			}
		}
		out = append(out, om)
	}
	return out
}

// argumentsString: Ollama sends arguments as an object; OpenAI wants them as a JSON string.
func argumentsString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "{}"
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	var buf bytes.Buffer
	if json.Compact(&buf, trimmed) == nil {
		return buf.String()
	}
	return string(trimmed)
}

// argumentsObject: the reverse, for tool calls going back to an Ollama client.
func argumentsObject(args string) json.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed)
	}
	quoted, _ := json.Marshal(map[string]string{"input": args})
	return quoted
}

// chatRequestFromOllama builds the platform request for /api/chat.
func chatRequestFromOllama(r *ollamaChatRequest) (*oaChatRequest, bool) {
	req := &oaChatRequest{Model: ollamaModelName(r.Model), Messages: toOpenAIMessages(r.Messages)}
	applyOptions(req, r.Options)
	req.ResponseFormat = responseFormat(r.Format)
	if t := bytes.TrimSpace(r.Tools); len(t) > 0 && string(t) != "null" && string(t) != "[]" {
		req.Tools = r.Tools
	}
	effort, show := thinkSetting(r.Think)
	req.ReasoningEffort = effort
	return req, show
}

// chatRequestFromGenerate builds the platform request for /api/generate: the system text and
// the prompt (with its images) as a two-message chat.
func chatRequestFromGenerate(r *ollamaGenerateRequest) (*oaChatRequest, bool) {
	var msgs []ollamaMessage
	if r.System != "" {
		msgs = append(msgs, ollamaMessage{Role: "system", Content: r.System})
	}
	msgs = append(msgs, ollamaMessage{Role: "user", Content: r.Prompt, Images: r.Images})
	req := &oaChatRequest{Model: ollamaModelName(r.Model), Messages: toOpenAIMessages(msgs)}
	applyOptions(req, r.Options)
	req.ResponseFormat = responseFormat(r.Format)
	effort, show := thinkSetting(r.Think)
	req.ReasoningEffort = effort
	return req, show
}

// ── Answers ─────────────────────────────────────────────────────────────────────────────────

func ollamaTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// doneReason maps OpenAI's finish_reason to Ollama's done_reason.
func doneReason(finish string) string {
	switch finish {
	case "length":
		return "length"
	case "", "stop", "tool_calls", "function_call", "end_turn":
		return "stop"
	}
	return finish
}

// ollamaFinal holds the counters Ollama sends with "done": true. Ollama reports durations
// in nanoseconds; the time to the first token stands in for prompt evaluation.
type ollamaFinal struct {
	DoneReason         string      `json:"done_reason"`
	TotalDuration      int64       `json:"total_duration"`
	LoadDuration       int64       `json:"load_duration"`
	PromptEvalCount    int         `json:"prompt_eval_count"`
	PromptEvalDuration int64       `json:"prompt_eval_duration"`
	EvalCount          int         `json:"eval_count"`
	EvalDuration       int64       `json:"eval_duration"`
	CostZharf          zharfAmount `json:"cost_zharf,omitempty"`
}

func finalCounters(res *chatResult, started, firstToken time.Time) ollamaFinal {
	end := time.Now()
	if firstToken.IsZero() {
		firstToken = end
	}
	f := ollamaFinal{
		DoneReason:         doneReason(res.FinishReason),
		TotalDuration:      end.Sub(started).Nanoseconds(),
		PromptEvalDuration: firstToken.Sub(started).Nanoseconds(),
		EvalDuration:       end.Sub(firstToken).Nanoseconds(),
	}
	if res.Usage != nil {
		f.PromptEvalCount = res.Usage.PromptTokens
		f.EvalCount = res.Usage.CompletionTokens
		f.CostZharf = res.Usage.CostZharf
	}
	return f
}

func ollamaToolCalls(calls []oaToolCall) []ollamaToolCall {
	out := make([]ollamaToolCall, 0, len(calls))
	for i, c := range calls {
		idx := i
		out = append(out, ollamaToolCall{Function: ollamaToolFunction{Index: &idx, Name: c.Function.Name, Arguments: argumentsObject(c.Function.Arguments)}})
	}
	return out
}

// ── /api/tags and /api/show ─────────────────────────────────────────────────────────────────

type ollamaDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type ollamaTag struct {
	Name       string        `json:"name"`
	Model      string        `json:"model"`
	ModifiedAt string        `json:"modified_at"`
	Size       int64         `json:"size"`
	Digest     string        `json:"digest"`
	Details    ollamaDetails `json:"details"`
}

func familyName(m *Model) string {
	name := strings.ToLower(strings.TrimSpace(m.OwnedBy))
	name = strings.NewReplacer(" ", "-", ".", "", "/", "-").Replace(name)
	if name == "" {
		name = "zharf"
	}
	return name
}

func modelDigest(id string) string {
	sum := sha256.Sum256([]byte("platform.zharf.ai/" + id))
	return hex.EncodeToString(sum[:])
}

func modelTime(m *Model) string {
	if m.Created > 0 {
		return ollamaTime(time.Unix(m.Created, 0))
	}
	return ollamaTime(time.Unix(0, 0))
}

func ollamaDetailsFor(m *Model) ollamaDetails {
	fam := familyName(m)
	return ollamaDetails{Format: "api", Family: fam, Families: []string{fam}}
}

func tagFor(m *Model) ollamaTag {
	return ollamaTag{Name: m.ID, Model: m.ID, ModifiedAt: modelTime(m), Digest: modelDigest(m.ID), Details: ollamaDetailsFor(m)}
}

// servedModel: what /api/tags lists: the chat models and the embedding models.
func servedModel(m *Model) bool {
	kind := modelType(m)
	return kind == "text" || kind == "embedding"
}

// capabilities, as Ollama's /api/show reports them; tools use these to decide what to offer.
func capabilities(m *Model) []string {
	var caps []string
	switch modelType(m) {
	case "embedding":
		return []string{"embedding"}
	case "text":
		caps = append(caps, "completion")
	}
	if has(m.Features, "tools") {
		caps = append(caps, "tools")
	}
	if has(m.InputModalities, "image") || has(m.Features, "vision") || has(m.Features, "image_input") {
		caps = append(caps, "vision")
	}
	if has(m.Features, "reasoning") {
		caps = append(caps, "thinking")
	}
	return caps
}

func showResponse(m *Model, baseURL string) map[string]any {
	fam := familyName(m)
	info := map[string]any{
		"general.architecture": fam,
		"general.basename":     m.ID,
		"general.name":         m.Name,
	}
	if m.ContextLength != nil && *m.ContextLength > 0 {
		info[fam+".context_length"] = int64(*m.ContextLength)
	}
	in, out := priceColumns(m)
	params := ""
	if m.MaxCompletionTokens != nil && *m.MaxCompletionTokens > 0 {
		params = fmt.Sprintf("num_predict %d", int64(*m.MaxCompletionTokens))
	}
	return map[string]any{
		"license":      "",
		"modelfile":    fmt.Sprintf("# Served by zharf from %s\nFROM %s\n", baseURL, m.ID),
		"parameters":   params,
		"template":     "{{ .Prompt }}",
		"details":      ollamaDetailsFor(m),
		"model_info":   info,
		"capabilities": capabilities(m),
		"modified_at":  modelTime(m),
		"zharf": map[string]any{
			"name":        m.Name,
			"maker":       m.OwnedBy,
			"description": m.Description,
			"price_in":    in,
			"price_out":   out,
			"currency":    "ZHARF",
		},
	}
}
