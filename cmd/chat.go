package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// OpenAI-compatible chat types, as /v1/chat/completions takes and returns them.

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"` // a string, or []oaContentPart
	Name       string       `json:"name,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaContentPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaToolCall struct {
	Index    *int           `json:"index,omitempty"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function oaFunctionCall `json:"function"`
}

type oaFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaChatRequest struct {
	Model            string           `json:"model"`
	Messages         []oaMessage      `json:"messages"`
	Stream           bool             `json:"stream"`
	StreamOptions    *oaStreamOptions `json:"stream_options,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	TopK             *int             `json:"top_k,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	Stop             []string         `json:"stop,omitempty"`
	Seed             *int64           `json:"seed,omitempty"`
	PresencePenalty  *float64         `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64         `json:"frequency_penalty,omitempty"`
	ResponseFormat   any              `json:"response_format,omitempty"`
	Tools            json.RawMessage  `json:"tools,omitempty"`
	ReasoningEffort  string           `json:"reasoning_effort,omitempty"`
}

type oaUsage struct {
	PromptTokens     int         `json:"prompt_tokens"`
	CompletionTokens int         `json:"completion_tokens"`
	TotalTokens      int         `json:"total_tokens"`
	CostZharf        zharfAmount `json:"cost_zharf"`
}

type oaDelta struct {
	Role             string       `json:"role"`
	Content          string       `json:"content"`
	Reasoning        string       `json:"reasoning"`
	ReasoningContent string       `json:"reasoning_content"`
	ToolCalls        []oaToolCall `json:"tool_calls"`
}

type oaChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		Index        int      `json:"index"`
		Delta        *oaDelta `json:"delta"`
		Message      *oaDelta `json:"message"`
		FinishReason string   `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaUsage        `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// chatResult is a whole answer, gathered while it streamed.
type chatResult struct {
	Model        string
	Content      string
	Reasoning    string
	ToolCalls    []oaToolCall
	FinishReason string
	Usage        *oaUsage
	RequestID    string
}

// deltaFunc receives each piece of the answer as it arrives. Either string may be empty.
type deltaFunc func(content, reasoning string) error

// readChatStream reads an answer from /v1/chat/completions: server-sent events, or a single
// JSON object when the server did not stream. Tool-call fragments are joined by index.
func readChatStream(body io.Reader, contentType string, onDelta deltaFunc) (*chatResult, error) {
	res := &chatResult{}
	calls := map[int]*oaToolCall{}
	var content, reasoning strings.Builder

	apply := func(chunk *oaChunk) error {
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return chunkError(chunk.Error)
		}
		if chunk.Model != "" {
			res.Model = chunk.Model
		}
		if chunk.Usage != nil {
			res.Usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			d := choice.Delta
			if d == nil {
				d = choice.Message
			}
			if choice.FinishReason != "" {
				res.FinishReason = choice.FinishReason
			}
			if d == nil {
				continue
			}
			think := d.Reasoning
			if think == "" {
				think = d.ReasoningContent
			}
			if d.Content != "" || think != "" {
				content.WriteString(d.Content)
				reasoning.WriteString(think)
				if onDelta != nil {
					if err := onDelta(d.Content, think); err != nil {
						return err
					}
				}
			}
			for i, tc := range d.ToolCalls {
				idx := i
				if tc.Index != nil {
					idx = *tc.Index
				}
				call, ok := calls[idx]
				if !ok {
					call = &oaToolCall{Type: "function"}
					calls[idx] = call
				}
				if tc.ID != "" {
					call.ID = tc.ID
				}
				if tc.Type != "" {
					call.Type = tc.Type
				}
				if tc.Function.Name != "" && call.Function.Name == "" {
					call.Function.Name = tc.Function.Name
				}
				call.Function.Arguments += tc.Function.Arguments
			}
		}
		return nil
	}

	finish := func() *chatResult {
		res.Content = content.String()
		res.Reasoning = reasoning.String()
		indexes := make([]int, 0, len(calls))
		for i := range calls {
			indexes = append(indexes, i)
		}
		sort.Ints(indexes)
		for _, i := range indexes {
			res.ToolCalls = append(res.ToolCalls, *calls[i])
		}
		return res
	}

	if !strings.Contains(contentType, "text/event-stream") {
		var chunk oaChunk
		data, err := io.ReadAll(body)
		if err != nil {
			return finish(), fmt.Errorf("the answer was cut off: %w", err)
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return finish(), fmt.Errorf("unexpected answer from the platform: %w", err)
		}
		if err := apply(&chunk); err != nil {
			return finish(), err
		}
		return finish(), nil
	}

	events := newSSEReader(body)
	for {
		ev, err := events.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return finish(), nil
			}
			return finish(), fmt.Errorf("the answer was cut off: %w", err)
		}
		data := strings.TrimSpace(ev.Data)
		if data == "[DONE]" {
			return finish(), nil
		}
		if data == "" || data[0] != '{' {
			continue
		}
		var chunk oaChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := apply(&chunk); err != nil {
			return finish(), err
		}
	}
}

func chunkError(raw json.RawMessage) error {
	var body errorBody
	if json.Unmarshal(raw, &body) == nil && body.Message != "" {
		e := body.toAPIError(http.StatusBadGateway)
		return e
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && text != "" {
		return &APIError{Status: http.StatusBadGateway, Message: text}
	}
	return &APIError{Status: http.StatusBadGateway, Message: "the model stopped with an error: " + string(raw)}
}

// streamChat sends a chat request as a stream (asking for the usage chunk, which carries
// cost_zharf) and calls onDelta as text arrives.
func (c *Client) streamChat(ctx context.Context, req *oaChatRequest, onDelta deltaFunc) (*chatResult, error) {
	req.Stream = true
	req.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	resp, err := c.do(ctx, http.MethodPost, "/v1/chat/completions", req, true, http.Header{"Accept": {"text/event-stream"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	res, err := readChatStream(resp.Body, resp.Header.Get("Content-Type"), onDelta)
	if res != nil {
		res.RequestID = resp.Header.Get("X-Zharf-Request-Id")
		if res.Usage != nil && res.Usage.CostZharf == "" {
			if h := resp.Header.Get("X-Zharf-Cost"); h != "" {
				res.Usage.CostZharf = zharfAmount(h)
			}
		}
	}
	if err != nil && ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, err
}
