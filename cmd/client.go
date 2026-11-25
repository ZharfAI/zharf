package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://platform.zharf.ai"

// normalizeBaseURL accepts "platform.zharf.ai", "https://platform.zharf.ai/", or the API base
// "https://platform.zharf.ai/v1", and returns the origin form without a trailing slash or /v1.
func normalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = defaultBaseURL
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("base URL %q is not an http(s) address", raw)
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/v1")
	u.Path = strings.TrimRight(path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

// hostOf is the name a key is saved under: the base URL's host (with port), so a key for a
// test server never replaces the one for platform.zharf.ai.
func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return baseURL
	}
	return u.Host
}

// newHTTPClient: proxies from HTTPS_PROXY/HTTP_PROXY/NO_PROXY, 15 s to connect and to finish the
// TLS handshake, and no overall timeout, because a streamed answer may run for minutes and a
// non-streamed one sends its headers only when it is done.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: transport}
}

func userAgent() string {
	return fmt.Sprintf("zharf-cli/%s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
}

// APIError is the platform's error body, { "error": { message, message_fa, type, code } }.
type APIError struct {
	Status     int
	Message    string
	MessageFa  string
	Type       string
	Code       string
	RetryAfter int
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

type errorBody struct {
	Message   string `json:"message"`
	MessageFa string `json:"message_fa"`
	Type      string `json:"type"`
	Code      any    `json:"code"`
}

func (b *errorBody) toAPIError(status int) *APIError {
	e := &APIError{Status: status, Message: b.Message, MessageFa: b.MessageFa, Type: b.Type}
	switch c := b.Code.(type) {
	case string:
		e.Code = c
	case float64:
		e.Code = strconv.Itoa(int(c))
	}
	return e
}

// parseAPIError reads an error answer. It understands the platform's shape, Ollama's
// {"error":"…"}, and falls back to the status line and the start of the body.
func parseAPIError(status int, header http.Header, body []byte) *APIError {
	var wrapped struct {
		Error        json.RawMessage `json:"error"`
		RetryAfterS  int             `json:"retry_after_s"`
		Message      string          `json:"message"`
		MessageFa    string          `json:"message_fa"`
		Code         string          `json:"code"`
		ErrorMessage string          `json:"error_message"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(body, &wrapped) == nil {
		if len(wrapped.Error) > 0 {
			var inner errorBody
			var text string
			if json.Unmarshal(wrapped.Error, &inner) == nil && (inner.Message != "" || inner.Code != nil) {
				e = inner.toAPIError(status)
			} else if json.Unmarshal(wrapped.Error, &text) == nil {
				e.Message = text
			}
		} else if wrapped.Message != "" {
			e.Message, e.MessageFa, e.Code = wrapped.Message, wrapped.MessageFa, wrapped.Code
		}
		e.RetryAfter = wrapped.RetryAfterS
	}
	if e.RetryAfter == 0 && header != nil {
		if n, err := strconv.Atoi(header.Get("Retry-After")); err == nil {
			e.RetryAfter = n
		}
	}
	if e.Message == "" {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 || strings.HasPrefix(snippet, "<") {
			snippet = ""
		}
		e.Message = strings.TrimSpace(fmt.Sprintf("HTTP %d %s. %s", status, http.StatusText(status), snippet))
	}
	return e
}

// NetworkError is a failure to reach the platform at all.
type NetworkError struct {
	Host string
	Err  error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("could not reach %s: %v", e.Host, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// Client talks to one platform (its base URL) with one key, or none for public routes.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// do sends a request and returns the answer when its status is below 400. Anything else comes
// back as *APIError (body read and closed), and transport failures as *NetworkError.
func (c *Client) do(ctx context.Context, method, path string, body any, auth bool, extra http.Header) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			reader = bytes.NewReader(b)
		case io.Reader:
			reader = b
		default:
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(encoded)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, values := range extra {
		req.Header[name] = values
	}
	if auth {
		if c.Key == "" {
			return nil, errNotSignedIn
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &NetworkError{Host: hostOf(c.BaseURL), Err: unwrapURLError(err)}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, parseAPIError(resp.StatusCode, resp.Header, data)
	}
	return resp, nil
}

func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// getJSON fetches path and decodes the JSON answer into out, within timeout.
func (c *Client) getJSON(ctx context.Context, path string, auth bool, out any, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil, auth, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("unexpected answer from %s%s: %w", c.BaseURL, path, err)
	}
	return nil
}

var errNotSignedIn = errors.New("not signed in")

// zharfAmount is a ژرف amount as the platform sends it: a JSON number (usage.cost_zharf), or a
// decimal string (the dashboard API). It keeps the text, so no float rounding sneaks in.
type zharfAmount string

func (z *zharfAmount) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "null" {
		*z = ""
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*z = zharfAmount(text)
		return nil
	}
	*z = zharfAmount(s)
	return nil
}

func (z zharfAmount) MarshalJSON() ([]byte, error) {
	if z == "" {
		return []byte("null"), nil
	}
	if _, err := strconv.ParseFloat(string(z), 64); err == nil {
		return []byte(z), nil
	}
	return json.Marshal(string(z))
}

func (z zharfAmount) Float() float64 {
	f, _ := strconv.ParseFloat(string(z), 64)
	return f
}

// String: plain decimal notation (never 1e-06), trailing zeros dropped.
func (z zharfAmount) String() string {
	s := string(z)
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, "eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}
