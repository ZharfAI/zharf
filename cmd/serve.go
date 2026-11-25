package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const serveUsage = `Usage: zharf serve [--port 11434] [--host 127.0.0.1] [--models id,id,…] [--origins …]

Serves the platform's models on this computer over two APIs, with your saved key, so tools
need no key of their own:

  Ollama API   http://127.0.0.1:11434        /api/tags, /api/chat, /api/generate, /api/show,
                                             /api/embed, /api/version, /api/ps
  OpenAI API   http://127.0.0.1:11434/v1     passed straight to the platform

Open WebUI, Continue, Zed, Obsidian plugins and anything else that speaks Ollama or OpenAI can
use every ژرف model this way. Each call is charged to your account as usual.

Flags:
  --port N          Port (default 11434, Ollama's). Use another if Ollama itself is running.
  --host ADDR       Address to listen on (default 127.0.0.1; env ZHARF_HOST). Anything else,
                    such as 0.0.0.0 in Docker, lets other computers on the network spend
                    your credit.
  --models LIST     Only list these models in /api/tags (comma-separated ids; a trailing *
                    matches a prefix, e.g. "gpt-5*,qwen3.5-9b"). Calls to others still work.
  --origins LIST    Extra browser origins allowed to call the server (also ZHARF_ORIGINS).
                    Local pages (localhost, 127.0.0.1, app://, vscode-webview://) are allowed.`

var serveFlags = []flagDef{
	{long: "port", short: "p", value: true},
	{long: "host", value: true},
	{long: "models", value: true},
	{long: "origins", value: true},
}

// ollamaAPIVersion is the Ollama release whose API this server follows. Tools such as Open
// WebUI check it before they use newer fields.
const ollamaAPIVersion = "0.12.6"

type server struct {
	client   *Client
	baseURL  string
	catalog  *catalogCache
	only     []string
	origins  []string
	logger   *log.Logger
	proxy    *httputil.ReverseProxy
	hostname string
	// anyHost turns off the Host check. It is set when the server listens beyond loopback
	// (--host 0.0.0.0, Docker), where callers use names this computer cannot know; the check
	// guards a loopback server against DNS rebinding, as in Ollama.
	anyHost bool
}

// catalogCache keeps /v1/models for ten minutes; a stale copy is used if a refresh fails.
type catalogCache struct {
	mu      sync.Mutex
	client  *Client
	models  []Model
	fetched time.Time
	ttl     time.Duration
}

func (c *catalogCache) get(ctx context.Context) ([]Model, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models != nil && time.Since(c.fetched) < c.ttl {
		return c.models, nil
	}
	list, err := c.client.listModels(ctx)
	if err != nil {
		if c.models != nil {
			return c.models, nil
		}
		return nil, err
	}
	sortModels(list.Data)
	c.models = list.Data
	c.fetched = time.Now()
	return c.models, nil
}

// find looks a model up; ok is false when it is not in the catalogue, and known is false when
// the catalogue could not be read (then the call goes ahead and the platform decides).
func (c *catalogCache) find(ctx context.Context, id string) (m *Model, ok bool, known bool) {
	models, err := c.get(ctx)
	if err != nil {
		return nil, false, false
	}
	for i := range models {
		if models[i].ID == id {
			return &models[i], true, true
		}
	}
	return nil, false, true
}

func newServer(c *Client, only, origins []string, logger *log.Logger) *server {
	s := &server{
		client:  c,
		baseURL: c.BaseURL,
		catalog: &catalogCache{client: &Client{BaseURL: c.BaseURL, HTTP: c.HTTP}, ttl: 10 * time.Minute},
		only:    only,
		origins: origins,
		logger:  logger,
	}
	s.hostname, _ = os.Hostname()
	target, _ := url.Parse(c.BaseURL)
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			// Whatever key the tool sent ("ollama", "sk-…", nothing) is replaced by the saved one.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Origin")
			pr.Out.Header.Set("Authorization", "Bearer "+c.Key)
			pr.Out.Header.Set("User-Agent", userAgent()+" serve")
		},
		Transport:     c.HTTP.Transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
				"message":    fmt.Sprintf("Could not reach %s: %v", hostOf(c.BaseURL), err),
				"message_fa": "فعلاً به سرور ژرف دسترسی نداریم. اتصال اینترنت یا پراکسی را بررسی کنید.",
				"type":       "api_error",
				"code":       "platform_unreachable",
			}})
		},
	}
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /api/version", s.version)
	mux.HandleFunc("GET /api/tags", s.tags)
	mux.HandleFunc("GET /api/ps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"models": []any{}})
	})
	mux.HandleFunc("POST /api/show", s.show)
	mux.HandleFunc("POST /api/chat", s.chat)
	mux.HandleFunc("POST /api/generate", s.generate)
	mux.HandleFunc("POST /api/embed", s.embed)
	mux.HandleFunc("POST /api/embeddings", s.embed)
	mux.HandleFunc("POST /api/pull", s.pull)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		ollamaError(w, http.StatusNotImplemented, fmt.Sprintf("%s %s is not available in zharf serve: the models live on %s, nothing is stored here", r.Method, r.URL.Path, hostOf(s.baseURL)))
	})
	mux.Handle("/v1/", s.proxy)
	return s.guard(s.logRequests(mux))
}

// ── Guards: only this computer's programs and local pages may spend the credit ─────────────

// allowedHost rejects requests whose Host header names another site, which is how a web page
// reaches a local server through DNS rebinding. Local names, private and loopback addresses
// (and Docker's host.docker.internal) pass, as in Ollama.
func (s *server) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" || host == "localhost" || (s.hostname != "" && strings.EqualFold(host, s.hostname)) {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
	}
	for _, suffix := range []string{".localhost", ".local", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// allowedOrigin: a browser page may call only from a local origin, an app origin, or one the
// person listed. Without this, any web site could make the browser spend the credit.
func (s *server) allowedOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	for _, allowed := range s.origins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "app", "tauri", "vscode-webview", "vscode-file":
		return true
	case "http", "https":
		switch strings.ToLower(u.Hostname()) {
		case "localhost", "127.0.0.1", "::1", "0.0.0.0":
			return true
		}
	}
	return false
}

func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.anyHost && !s.allowedHost(r.Host) {
			ollamaError(w, http.StatusForbidden, "zharf serve only answers requests addressed to this computer")
			return
		}
		origin := r.Header.Get("Origin")
		if !s.allowedOrigin(origin) {
			ollamaError(w, http.StatusForbidden, "origin "+origin+" is not allowed; start zharf serve with --origins "+origin+" to allow it")
			return
		}
		if origin != "" {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, User-Agent, X-Requested-With, OpenAI-Organization, OpenAI-Beta, X-Stainless-Lang, X-Stainless-Package-Version, X-Stainless-OS, X-Stainless-Arch, X-Stainless-Runtime, X-Stainless-Runtime-Version, X-Stainless-Retry-Count, X-Stainless-Timeout")
			h.Set("Access-Control-Expose-Headers", "X-Zharf-Request-Id, X-Zharf-Cost")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Logging ─────────────────────────────────────────────────────────────────────────────────

type logNote struct {
	model string
	cost  zharfAmount
}

type logKey struct{}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		note := &logNote{}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), logKey{}, note)))
		if s.logger == nil {
			return
		}
		line := fmt.Sprintf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(started).Round(time.Millisecond))
		if note.model != "" {
			line += " " + note.model
		}
		if note.cost != "" {
			line += " " + note.cost.String() + " ژرف"
		}
		s.logger.Println(line)
	})
}

func noteFrom(r *http.Request) *logNote {
	if n, ok := r.Context().Value(logKey{}).(*logNote); ok {
		return n
	}
	return &logNote{}
}

// ── Helpers ─────────────────────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// ollamaError answers in Ollama's error shape, {"error": "…"}.
func ollamaError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// platformError passes a platform error on in Ollama's shape, English then Persian.
func platformError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	var netErr *NetworkError
	switch {
	case errors.As(err, &apiErr):
		msg := apiErr.Message
		if apiErr.MessageFa != "" {
			msg += " — " + apiErr.MessageFa
		}
		status := apiErr.Status
		if status < 400 {
			status = http.StatusBadGateway
		}
		ollamaError(w, status, msg)
	case errors.As(err, &netErr):
		ollamaError(w, http.StatusBadGateway, netErr.Error())
	default:
		ollamaError(w, http.StatusInternalServerError, err.Error())
	}
}

func errorText(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.MessageFa != "" {
		return apiErr.Message + " — " + apiErr.MessageFa
	}
	return err.Error()
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the request body is not valid JSON: %v", err)
	}
	return nil
}

// ── Ollama routes ───────────────────────────────────────────────────────────────────────────

func (s *server) root(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Ollama is running")
}

func (s *server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": ollamaAPIVersion, "zharf": version})
}

func (s *server) listed(id string) bool {
	if len(s.only) == 0 {
		return true
	}
	for _, pattern := range s.only {
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(id, strings.TrimSuffix(pattern, "*")) {
				return true
			}
		} else if pattern == id {
			return true
		}
	}
	return false
}

func (s *server) tags(w http.ResponseWriter, r *http.Request) {
	models, err := s.catalog.get(r.Context())
	if err != nil {
		platformError(w, err)
		return
	}
	tags := []ollamaTag{}
	for i := range models {
		m := &models[i]
		if servedModel(m) && s.listed(m.ID) {
			tags = append(tags, tagFor(m))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": tags})
}

func (s *server) show(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
		Name  string `json:"name"`
	}
	if err := decodeBody(r, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := ollamaModelName(req.Model)
	if id == "" {
		id = ollamaModelName(req.Name)
	}
	m, ok, known := s.catalog.find(r.Context(), id)
	if !known {
		got, err := s.catalog.client.getModel(r.Context(), id)
		if err != nil {
			platformError(w, err)
			return
		}
		m, ok = got, true
	}
	if !ok {
		ollamaError(w, http.StatusNotFound, fmt.Sprintf("model '%s' not found", id))
		return
	}
	writeJSON(w, http.StatusOK, showResponse(m, s.baseURL))
}

func (s *server) pull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model  string `json:"model"`
		Name   string `json:"name"`
		Stream *bool  `json:"stream"`
	}
	if err := decodeBody(r, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := ollamaModelName(req.Model)
	if id == "" {
		id = ollamaModelName(req.Name)
	}
	if _, ok, known := s.catalog.find(r.Context(), id); known && !ok {
		ollamaError(w, http.StatusNotFound, fmt.Sprintf("model '%s' is not on %s; `zharf models` lists them", id, hostOf(s.baseURL)))
		return
	}
	// Nothing to download: the model runs on the platform.
	if wantsStream(req.Stream) {
		w.Header().Set("Content-Type", "application/x-ndjson")
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success"})
}

// checkChatModel answers 404 for a model the catalogue does not have, before any call is made.
func (s *server) checkChatModel(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == "" {
		ollamaError(w, http.StatusBadRequest, "model is required")
		return false
	}
	if _, ok, known := s.catalog.find(r.Context(), id); known && !ok {
		ollamaError(w, http.StatusNotFound, fmt.Sprintf("model '%s' not found on %s; `zharf models` lists them", id, hostOf(s.baseURL)))
		return false
	}
	return true
}

// ndjson writes one JSON object per line and flushes it at once.
type ndjson struct {
	w       http.ResponseWriter
	flusher http.Flusher
	started bool
}

func newNDJSON(w http.ResponseWriter) *ndjson {
	f, _ := w.(http.Flusher)
	return &ndjson{w: w, flusher: f}
}

func (n *ndjson) send(v any) error {
	if !n.started {
		n.w.Header().Set("Content-Type", "application/x-ndjson")
		n.w.WriteHeader(http.StatusOK)
		n.started = true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := n.w.Write(data); err != nil {
		return err
	}
	if n.flusher != nil {
		n.flusher.Flush()
	}
	return nil
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	var req ollamaChatRequest
	if err := decodeBody(r, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := ollamaModelName(req.Model)
	noteFrom(r).model = model
	if !s.checkChatModel(w, r, model) {
		return
	}
	// No messages: Ollama loads (or unloads) the model and answers at once. Nothing to call.
	if len(req.Messages) == 0 {
		reason := "load"
		if unloadRequested(req.KeepAlive) {
			reason = "unload"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model": req.Model, "created_at": ollamaTime(time.Now()),
			"message": map[string]string{"role": "assistant", "content": ""}, "done_reason": reason, "done": true,
		})
		return
	}
	oa, showThinking := chatRequestFromOllama(&req)
	s.answer(w, r, oa, req.Model, wantsStream(req.Stream), showThinking, false)
}

func (s *server) generate(w http.ResponseWriter, r *http.Request) {
	var req ollamaGenerateRequest
	if err := decodeBody(r, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := ollamaModelName(req.Model)
	noteFrom(r).model = model
	if !s.checkChatModel(w, r, model) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" && len(req.Images) == 0 {
		reason := "load"
		if unloadRequested(req.KeepAlive) {
			reason = "unload"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model": req.Model, "created_at": ollamaTime(time.Now()), "response": "", "done_reason": reason, "done": true,
		})
		return
	}
	oa, showThinking := chatRequestFromGenerate(&req)
	s.answer(w, r, oa, req.Model, wantsStream(req.Stream), showThinking, true)
}

// answer calls the platform (always streaming, which carries the cost) and replies in
// Ollama's shape: NDJSON chunks, or one object when the client asked for no stream. The
// platform call is tied to the client's connection, so a client that hangs up stops it.
func (s *server) answer(w http.ResponseWriter, r *http.Request, oa *oaChatRequest, name string, stream, showThinking, generate bool) {
	started := time.Now()
	var firstToken time.Time
	out := newNDJSON(w)

	piece := func(content, thinking string) map[string]any {
		m := map[string]any{"model": name, "created_at": ollamaTime(time.Now()), "done": false}
		if generate {
			m["response"] = content
			if thinking != "" {
				m["thinking"] = thinking
			}
		} else {
			msg := map[string]any{"role": "assistant", "content": content}
			if thinking != "" {
				msg["thinking"] = thinking
			}
			m["message"] = msg
		}
		return m
	}

	onDelta := func(content, reasoning string) error {
		if firstToken.IsZero() {
			firstToken = time.Now()
		}
		if !showThinking {
			reasoning = ""
		}
		if !stream || (content == "" && reasoning == "") {
			return nil
		}
		return out.send(piece(content, reasoning))
	}

	res, err := s.client.streamChat(r.Context(), oa, onDelta)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		if out.started {
			_ = out.send(map[string]string{"error": errorText(err)})
			return
		}
		platformError(w, err)
		return
	}
	noteFrom(r).cost = zharfAmountOrEmpty(res)

	final := finalCounters(res, started, firstToken)
	done := map[string]any{"model": name, "created_at": ollamaTime(time.Now()), "done": true}
	addFinal(done, final)

	if stream {
		if len(res.ToolCalls) > 0 && !generate {
			_ = out.send(map[string]any{"model": name, "created_at": ollamaTime(time.Now()), "done": false,
				"message": map[string]any{"role": "assistant", "content": "", "tool_calls": ollamaToolCalls(res.ToolCalls)}})
		}
		if generate {
			done["response"] = ""
		} else {
			done["message"] = map[string]any{"role": "assistant", "content": ""}
		}
		_ = out.send(done)
		return
	}

	if generate {
		done["response"] = res.Content
		if showThinking && res.Reasoning != "" {
			done["thinking"] = res.Reasoning
		}
	} else {
		msg := map[string]any{"role": "assistant", "content": res.Content}
		if showThinking && res.Reasoning != "" {
			msg["thinking"] = res.Reasoning
		}
		if len(res.ToolCalls) > 0 {
			msg["tool_calls"] = ollamaToolCalls(res.ToolCalls)
		}
		done["message"] = msg
	}
	writeJSON(w, http.StatusOK, done)
}

func zharfAmountOrEmpty(res *chatResult) zharfAmount {
	if res == nil || res.Usage == nil {
		return ""
	}
	return res.Usage.CostZharf
}

func addFinal(m map[string]any, f ollamaFinal) {
	m["done_reason"] = f.DoneReason
	m["total_duration"] = f.TotalDuration
	m["load_duration"] = f.LoadDuration
	m["prompt_eval_count"] = f.PromptEvalCount
	m["prompt_eval_duration"] = f.PromptEvalDuration
	m["eval_count"] = f.EvalCount
	m["eval_duration"] = f.EvalDuration
	if f.CostZharf != "" {
		m["cost_zharf"] = f.CostZharf
	}
}

// embed serves /api/embed ({input}: one text or many → "embeddings") and the older
// /api/embeddings ({prompt} → "embedding") through /v1/embeddings.
func (s *server) embed(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model      string          `json:"model"`
		Input      json.RawMessage `json:"input"`
		Prompt     string          `json:"prompt"`
		Dimensions int             `json:"dimensions"`
	}
	if err := decodeBody(r, &req); err != nil {
		ollamaError(w, http.StatusBadRequest, err.Error())
		return
	}
	legacy := strings.HasSuffix(r.URL.Path, "/embeddings")
	model := ollamaModelName(req.Model)
	noteFrom(r).model = model
	if !s.checkChatModel(w, r, model) {
		return
	}
	body := map[string]any{"model": model}
	if legacy {
		body["input"] = req.Prompt
	} else {
		if len(req.Input) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"model": req.Model, "embeddings": [][]float64{}})
			return
		}
		body["input"] = req.Input
	}
	if req.Dimensions > 0 {
		body["dimensions"] = req.Dimensions
	}
	started := time.Now()
	resp, err := s.client.do(r.Context(), http.MethodPost, "/v1/embeddings", body, true, nil)
	if err != nil {
		platformError(w, err)
		return
	}
	defer resp.Body.Close()
	var answer struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage oaUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		ollamaError(w, http.StatusBadGateway, "unexpected embeddings answer: "+err.Error())
		return
	}
	cost := answer.Usage.CostZharf
	if cost == "" {
		cost = zharfAmount(resp.Header.Get("X-Zharf-Cost"))
	}
	noteFrom(r).cost = cost
	vectors := make([][]float64, len(answer.Data))
	for i, d := range answer.Data {
		at := d.Index
		if at < 0 || at >= len(vectors) {
			at = i
		}
		vectors[at] = d.Embedding
	}
	if legacy {
		var first []float64
		if len(vectors) > 0 {
			first = vectors[0]
		}
		writeJSON(w, http.StatusOK, map[string]any{"embedding": first})
		return
	}
	out := map[string]any{
		"model": req.Model, "embeddings": vectors,
		"total_duration": time.Since(started).Nanoseconds(), "load_duration": 0,
		"prompt_eval_count": answer.Usage.PromptTokens,
	}
	if cost != "" {
		out["cost_zharf"] = cost
	}
	writeJSON(w, http.StatusOK, out)
}

// ── The command ─────────────────────────────────────────────────────────────────────────────

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdServe(a *app, p parsed) error {
	port := 11434
	if v := p.str("port", os.Getenv("ZHARF_PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return usageErr("--port needs a port number, got %q", v)
		}
		port = n
	}
	host := p.str("host", os.Getenv("ZHARF_HOST"))
	if host == "" {
		host = "127.0.0.1"
	}
	c, src, err := a.client(true)
	if err != nil {
		return err
	}
	origins := append(splitList(os.Getenv("ZHARF_ORIGINS")), splitList(p.str("origins", ""))...)
	logger := log.New(a.stderr, "", log.Ltime)
	s := newServer(c, splitList(p.str("models", "")), origins, logger)
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		s.anyHost = true
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") || strings.Contains(err.Error(), "Only one usage") {
			return fmt.Errorf("port %d is already in use (is Ollama running?). Try: zharf serve --port %d", port, port+1)
		}
		return err
	}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute}

	shown := "http://" + addr
	pal := a.errp
	fmt.Fprintf(a.stderr, "%s on %s\n", pal.bold("zharf serve"), pal.bold(shown))
	fmt.Fprintf(a.stderr, "  Ollama clients  %s\n", shown)
	fmt.Fprintf(a.stderr, "  OpenAI clients  %s/v1  (any API key; zharf sends yours)\n", shown)
	fmt.Fprintf(a.stderr, "  %s\n", pal.dim(fmt.Sprintf("Models from %s, key %s from %s. Ctrl+C to stop.", a.host, keyPrefix(src.Key), src.Where)))
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		fmt.Fprintln(a.stderr, pal.yellow("  Listening beyond this computer: anyone who can reach "+addr+" can spend your credit."))
	}
	go func() {
		if _, err := s.catalog.get(context.Background()); err != nil {
			logger.Printf("could not load the model list yet: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(listener) }()
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		fmt.Fprintln(a.stderr, "\nStopping…")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
		return nil
	}
}
