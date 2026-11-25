package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const savedKey = "zk-TestSaved123456"

func TestChatRequestFromOllama(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR....."))
	body := `{
	  "model": "qwen3.5-9b:latest",
	  "messages": [
	    {"role": "system", "content": "کوتاه جواب بده"},
	    {"role": "user", "content": "این چیست؟", "images": ["` + png + `"]},
	    {"role": "assistant", "content": "", "tool_calls": [{"function": {"name": "weather", "arguments": {"city": "تهران"}}}]},
	    {"role": "tool", "content": "۱۸ درجه", "tool_name": "weather"}
	  ],
	  "options": {"temperature": 0.2, "top_p": 0.9, "top_k": 40, "num_predict": 128, "stop": ["\n\n", "END"], "seed": 7, "num_ctx": 8192},
	  "format": "json",
	  "tools": [{"type": "function", "function": {"name": "weather", "parameters": {"type": "object"}}}],
	  "think": "high"
	}`
	var r ollamaChatRequest
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	req, show := chatRequestFromOllama(&r)
	if !show || req.ReasoningEffort != "high" {
		t.Errorf("think: show %v effort %q", show, req.ReasoningEffort)
	}
	if req.Model != "qwen3.5-9b" {
		t.Errorf("model %q: :latest should go", req.Model)
	}
	if *req.Temperature != 0.2 || *req.TopP != 0.9 || *req.TopK != 40 || *req.MaxTokens != 128 || *req.Seed != 7 {
		t.Errorf("options: %+v", req)
	}
	if strings.Join(req.Stop, "|") != "\n\n|END" {
		t.Errorf("stop: %q", req.Stop)
	}
	if rf, _ := json.Marshal(req.ResponseFormat); string(rf) != `{"type":"json_object"}` {
		t.Errorf("format: %s", rf)
	}
	if len(req.Tools) == 0 {
		t.Error("tools dropped")
	}
	parts, ok := req.Messages[1].Content.([]oaContentPart)
	if !ok || len(parts) != 2 || parts[0].Text != "این چیست؟" || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image message: %#v", req.Messages[1].Content)
	}
	call := req.Messages[2].ToolCalls
	if len(call) != 1 || call[0].ID != "call_1" || call[0].Function.Arguments != `{"city":"تهران"}` {
		t.Errorf("tool call: %+v", call)
	}
	if req.Messages[3].ToolCallID != "call_1" || req.Messages[3].Role != "tool" {
		t.Errorf("tool answer: %+v", req.Messages[3])
	}
	// num_predict -1 means no limit; a JSON schema becomes structured output.
	r2 := ollamaChatRequest{Model: "m", Options: map[string]any{"num_predict": float64(-1)}, Format: json.RawMessage(`{"type":"object"}`)}
	req2, _ := chatRequestFromOllama(&r2)
	if req2.MaxTokens != nil {
		t.Errorf("num_predict -1 → %v", *req2.MaxTokens)
	}
	if rf, _ := json.Marshal(req2.ResponseFormat); !strings.Contains(string(rf), `"json_schema"`) {
		t.Errorf("schema format: %s", rf)
	}
	if _, show := thinkSetting(json.RawMessage("false")); show {
		t.Error("think false should hide reasoning")
	}
}

func TestImageDataURLSniffing(t *testing.T) {
	jpeg := base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F'})
	if got := imageDataURL(jpeg); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
		t.Errorf("jpeg: %s", got)
	}
	webp := base64.StdEncoding.EncodeToString([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "))
	if got := imageDataURL(webp); !strings.HasPrefix(got, "data:image/webp;base64,") {
		t.Errorf("webp: %s", got)
	}
	if got := imageDataURL("data:image/gif;base64,R0lG"); got != "data:image/gif;base64,R0lG" {
		t.Errorf("data URL changed: %s", got)
	}
}

// fakePlatform stands in for platform.zharf.ai: the catalogue, a streaming chat that checks
// the key, embeddings, and a 402 for one model.
type fakePlatform struct {
	*httptest.Server
	chatCalls atomic.Int32
	lastChat  atomic.Value // map[string]any
	lastAuth  atomic.Value // string
}

var fakeCatalogue = `{"object":"list","data":[
 {"id":"qwen3.5-9b","owned_by":"Qwen","name":"Qwen3.5 9B","modality":"text->text","input_modalities":["text"],"output_modalities":["text"],"features":["streaming","tools","reasoning"],"context_length":262144,"created":1760000000,"pricing":{"currency":"ZHARF","prompt_per_1m_tokens":12.5,"completion_per_1m_tokens":50}},
 {"id":"gpt-5-mini","owned_by":"OpenAI","modality":"text+image->text","input_modalities":["text","image"],"output_modalities":["text"],"features":["tools"],"context_length":400000,"pricing":{"prompt_per_1m_tokens":63,"completion_per_1m_tokens":509}},
 {"id":"poor-model","owned_by":"Test","modality":"text->text","output_modalities":["text"],"pricing":{}},
 {"id":"venice-embed-1","owned_by":"Venice AI","modality":"embedding","output_modalities":["embedding"],"operations":["embedding"],"pricing":{"per_unit":41.3,"media_unit":"1M tokens"}},
 {"id":"flux-image","owned_by":"BFL","modality":"text->image","output_modalities":["image"],"pricing":{"per_unit":10,"media_unit":"image"}},
 {"id":"scribe","owned_by":"ElevenLabs","modality":"audio->text","output_modalities":["text"],"operations":["stt"],"pricing":{"per_unit":2.5,"media_unit":"minute"}}
],"prices_valid_until":"2026-11-25T20:30:00.000Z"}`

func newFakePlatform(t *testing.T) *fakePlatform {
	f := &fakePlatform{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, fakeCatalogue)
	})
	mux.HandleFunc("GET /v1/models/{id}", func(w http.ResponseWriter, r *http.Request) {
		var list modelList
		json.Unmarshal([]byte(fakeCatalogue), &list)
		for _, m := range list.Data {
			if m.ID == r.PathValue("id") {
				json.NewEncoder(w).Encode(m)
				return
			}
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"message":"No model.","message_fa":"چنین مدلی نیست.","type":"invalid_request_error","code":"model_not_found"}}`)
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.chatCalls.Add(1)
		f.lastAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+savedKey {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"Invalid or revoked API key.","message_fa":"این کلید API پیدا نشد.","type":"authentication_error","code":"bad_key"}}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.lastChat.Store(body)
		if body["model"] == "poor-model" {
			w.WriteHeader(402)
			io.WriteString(w, `{"error":{"message":"Your ZHARF balance is too low.","message_fa":"اعتبار ژرف شما کافی نیست.","type":"billing_error","code":"insufficient_balance"}}`)
			return
		}
		if body["stream"] != true {
			t.Errorf("the platform should always be asked to stream, got %v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Zharf-Request-Id", "req-1")
		flusher := w.(http.Flusher)
		// Write the stream in 5-byte pieces, so Persian characters cross writes.
		data := []byte(persianStream)
		for len(data) > 0 {
			n := min(5, len(data))
			w.Write(data[:n])
			flusher.Flush()
			data = data[n:]
		}
	})
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":4,"cost_zharf":0.0002}}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func newTestServe(t *testing.T, f *fakePlatform, only ...string) *httptest.Server {
	c := &Client{BaseURL: f.URL, Key: savedKey, HTTP: newHTTPClient()}
	s := newServer(c, only, nil, nil)
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string, header ...string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readNDJSON(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not NDJSON: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func TestServeChatStreamsNDJSON(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/api/chat", `{"model":"qwen3.5-9b:latest","messages":[{"role":"user","content":"سلام"}],"options":{"num_predict":64}}`)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := readNDJSON(t, resp.Body)
	var content, thinking strings.Builder
	for _, l := range lines[:len(lines)-1] {
		msg := l["message"].(map[string]any)
		content.WriteString(msg["content"].(string))
		if th, ok := msg["thinking"].(string); ok {
			thinking.WriteString(th)
		}
		if l["done"] != false || l["model"] != "qwen3.5-9b:latest" {
			t.Fatalf("chunk %v", l)
		}
	}
	if content.String() != persianAnswer || thinking.String() != "کاربر سلام کرد" {
		t.Fatalf("content %q thinking %q", content.String(), thinking.String())
	}
	last := lines[len(lines)-1]
	if last["done"] != true || last["done_reason"] != "stop" || last["eval_count"] != float64(9) || last["prompt_eval_count"] != float64(12) || last["cost_zharf"] != 1e-06 {
		t.Fatalf("final %v", last)
	}
	sent := f.lastChat.Load().(map[string]any)
	if sent["model"] != "qwen3.5-9b" || sent["max_tokens"] != float64(64) {
		t.Fatalf("platform got %v", sent)
	}
	if opts, _ := sent["stream_options"].(map[string]any); opts["include_usage"] != true {
		t.Fatalf("usage not asked for: %v", sent)
	}
}

func TestServeChatWithoutStream(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/api/chat", `{"model":"qwen3.5-9b","stream":false,"think":false,"messages":[{"role":"user","content":"سلام"}]}`)
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	msg := got["message"].(map[string]any)
	if got["done"] != true || msg["content"] != persianAnswer || msg["thinking"] != nil {
		t.Fatalf("got %v", got)
	}
}

func TestServeGenerate(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/api/generate", `{"model":"qwen3.5-9b","system":"کوتاه","prompt":"سلام","stream":false}`)
	defer resp.Body.Close()
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	if got["response"] != persianAnswer || got["done"] != true || got["eval_count"] != float64(9) {
		t.Fatalf("got %v", got)
	}
	sent := f.lastChat.Load().(map[string]any)
	msgs := sent["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("messages %v", msgs)
	}

	// Streaming generate uses "response" pieces.
	resp2 := post(t, srv.URL+"/api/generate", `{"model":"qwen3.5-9b","prompt":"سلام"}`)
	defer resp2.Body.Close()
	var text strings.Builder
	for _, l := range readNDJSON(t, resp2.Body) {
		text.WriteString(l["response"].(string))
	}
	if text.String() != persianAnswer {
		t.Fatalf("streamed %q", text.String())
	}
}

func TestServePreloadCallsNothing(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	for _, tc := range []struct{ path, body, reason string }{
		{"/api/chat", `{"model":"qwen3.5-9b","messages":[]}`, "load"},
		{"/api/generate", `{"model":"qwen3.5-9b"}`, "load"},
		{"/api/generate", `{"model":"qwen3.5-9b","keep_alive":0}`, "unload"},
	} {
		resp := post(t, srv.URL+tc.path, tc.body)
		var got map[string]any
		json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if got["done"] != true || got["done_reason"] != tc.reason {
			t.Errorf("%s %s: %v", tc.path, tc.body, got)
		}
	}
	if n := f.chatCalls.Load(); n != 0 {
		t.Fatalf("a preload must not call the platform; it was called %d times", n)
	}
}

func TestServeErrors(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/api/chat", `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != 404 || f.chatCalls.Load() != 0 {
		t.Fatalf("unknown model: status %d, calls %d", resp.StatusCode, f.chatCalls.Load())
	}
	resp = post(t, srv.URL+"/api/chat", `{"model":"poor-model","messages":[{"role":"user","content":"hi"}]}`)
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != 402 || !strings.Contains(got["error"], "اعتبار ژرف شما کافی نیست") {
		t.Fatalf("balance error: %d %v", resp.StatusCode, got)
	}
}

func TestServeTagsAndShow(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp, _ := http.Get(srv.URL + "/api/tags")
	var tags struct{ Models []ollamaTag }
	json.NewDecoder(resp.Body).Decode(&tags)
	resp.Body.Close()
	var names []string
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	// Chat and embedding models only, popular first: no image model, no speech-to-text.
	if strings.Join(names, ",") != "gpt-5-mini,qwen3.5-9b,poor-model,venice-embed-1" {
		t.Fatalf("tags: %v", names)
	}
	resp = post(t, srv.URL+"/api/show", `{"model":"gpt-5-mini:latest"}`)
	var show map[string]any
	json.NewDecoder(resp.Body).Decode(&show)
	resp.Body.Close()
	caps := fmt.Sprint(show["capabilities"])
	if caps != "[completion tools vision]" {
		t.Fatalf("capabilities %s", caps)
	}
	if show["model_info"].(map[string]any)["openai.context_length"] != float64(400000) {
		t.Fatalf("model_info %v", show["model_info"])
	}
	only := newTestServe(t, f, "qwen*")
	resp, _ = http.Get(only.URL + "/api/tags")
	tags.Models = nil
	json.NewDecoder(resp.Body).Decode(&tags)
	resp.Body.Close()
	if len(tags.Models) != 1 || tags.Models[0].Name != "qwen3.5-9b" {
		t.Fatalf("--models filter: %v", tags.Models)
	}
}

func TestServeEmbed(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/api/embed", `{"model":"venice-embed-1","input":["a","b"]}`)
	var got struct {
		Embeddings [][]float64 `json:"embeddings"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if len(got.Embeddings) != 2 || got.Embeddings[0][0] != 0.1 || got.Embeddings[1][0] != 0.3 {
		t.Fatalf("embeddings out of order: %v", got.Embeddings)
	}
}

func TestServeGuardsHostAndOrigin(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	check := func(name string, mutate func(*http.Request), want int) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat", strings.NewReader(`{"model":"qwen3.5-9b","messages":[]}`))
		mutate(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, want)
		}
	}
	check("another site's page", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403)
	check("DNS rebinding", func(r *http.Request) { r.Host = "evil.example:11434" }, 403)
	check("local page", func(r *http.Request) { r.Header.Set("Origin", "http://localhost:3000") }, 200)
	check("docker", func(r *http.Request) { r.Host = "host.docker.internal:11434" }, 200)
	check("VS Code", func(r *http.Request) { r.Header.Set("Origin", "vscode-webview://abc") }, 200)

	// Listening beyond loopback (Docker): any Host name, but still no foreign web pages.
	c := &Client{BaseURL: f.URL, Key: savedKey, HTTP: newHTTPClient()}
	open := newServer(c, nil, nil, nil)
	open.anyHost = true
	wide := httptest.NewServer(open.handler())
	defer wide.Close()
	for name, tc := range map[string]struct {
		host, origin string
		want         int
	}{
		"compose service name": {"zharf:11434", "", 200},
		"foreign page":         {"zharf:11434", "https://evil.example", 403},
	} {
		req, _ := http.NewRequest(http.MethodPost, wide.URL+"/api/chat", strings.NewReader(`{"model":"qwen3.5-9b","messages":[]}`))
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: status %d, want %d", name, resp.StatusCode, tc.want)
		}
	}
}

func TestServeOpenAIPassThroughUsesSavedKey(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	resp := post(t, srv.URL+"/v1/chat/completions", `{"model":"qwen3.5-9b","stream":true,"messages":[{"role":"user","content":"سلام"}]}`, "Authorization", "Bearer ollama")
	defer resp.Body.Close()
	if got := f.lastAuth.Load(); got != "Bearer "+savedKey {
		t.Fatalf("the platform saw %v", got)
	}
	// Byte for byte: comments, CRLF line ends, the usage chunk and [DONE] all arrive unchanged.
	raw, err := io.ReadAll(resp.Body)
	if err != nil || string(raw) != persianStream {
		t.Fatalf("passed-through stream differs (%v):\n%q", err, raw)
	}
	res, err := readChatStream(strings.NewReader(string(raw)), resp.Header.Get("Content-Type"), nil)
	if err != nil || res.Content != persianAnswer {
		t.Fatalf("passed-through stream: %v %q", err, res.Content)
	}
	m, _ := http.Get(srv.URL + "/v1/models")
	data, _ := io.ReadAll(m.Body)
	m.Body.Close()
	if !bytes.Contains(data, []byte(`"qwen3.5-9b"`)) {
		t.Fatalf("/v1/models: %s", data)
	}
}

func TestServeVersionAndPs(t *testing.T) {
	f := newFakePlatform(t)
	srv := newTestServe(t, f)
	for path, want := range map[string]string{"/api/version": `"version":"` + ollamaAPIVersion + `"`, "/api/ps": `{"models":[]}`, "/": "Ollama is running"} {
		resp, _ := http.Get(srv.URL + path)
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(data), want) {
			t.Errorf("%s: %s", path, data)
		}
	}
}
