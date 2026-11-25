package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

const runUsage = `Usage: zharf run <model> [prompt] [flags]

With a prompt, streams the answer to stdout and exits. Without one, starts a chat that keeps
the conversation. Piped input is added to the prompt:

  zharf run qwen3.5-9b "سلام، یک جمله درباره‌ی پاییز بنویس"
  cat report.txt | zharf run gpt-5-mini "summarise"
  zharf run deepseek-v4-flash

The cost of each answer, in ژرف, is printed dimly on stderr, so stdout holds only the answer.

In a chat:
  """            Start and end a multi-line message
  /model <id>    Switch model (the conversation stays)
  /system <text> Set the system message (/system alone shows it)
  /clear         Forget the conversation
  /cost          What this chat has cost so far
  /bye           Leave (or Ctrl+D). Ctrl+C stops an answer.

Flags:
  --system TEXT        System message
  --temperature N      Sampling temperature
  --max-tokens N       Longest answer, in tokens
  --effort LEVEL       Reasoning effort for models that think: low, medium or high
  --hide-thinking      Do not show the model's reasoning
  -q, --quiet          Do not print the cost line`

var runFlags = []flagDef{
	{long: "system", value: true},
	{long: "temperature", value: true},
	{long: "max-tokens", value: true},
	{long: "effort", value: true},
	{long: "hide-thinking"},
	{long: "quiet", short: "q"},
}

type runOptions struct {
	temperature  *float64
	maxTokens    *int
	effort       string
	hideThinking bool
	quiet        bool
}

// chatSession is one conversation with one model at a time.
type chatSession struct {
	a        *app
	c        *Client
	model    string
	system   string
	opts     runOptions
	messages []oaMessage
	total    *big.Rat // ژرف spent in this session
	tokens   int
	unknown  int // answers whose cost was not reported
}

func cmdRun(a *app, p parsed) error {
	if len(p.args) == 0 {
		return usageErr("which model? Usage: zharf run <model> [prompt]. See `zharf models`.")
	}
	model := strings.TrimSuffix(p.args[0], ":latest")
	prompt := strings.TrimSpace(strings.Join(p.args[1:], " "))

	opts := runOptions{effort: p.str("effort", ""), hideThinking: p.has("hide-thinking"), quiet: p.has("quiet")}
	var err error
	if opts.temperature, err = p.num("temperature"); err != nil {
		return usageErr("%v", err)
	}
	if opts.maxTokens, err = p.integer("max-tokens"); err != nil {
		return usageErr("%v", err)
	}

	c, _, err := a.client(true)
	if err != nil {
		return err
	}
	s := &chatSession{a: a, c: c, model: model, system: p.str("system", ""), opts: opts, total: new(big.Rat)}

	if !a.inTTY {
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		piped := strings.TrimRight(string(data), "\n\r\t ")
		switch {
		case piped != "" && prompt != "":
			prompt = piped + "\n\n" + prompt
		case piped != "":
			prompt = piped
		}
		if prompt == "" {
			return usageErr("nothing to send: give a prompt or pipe some text in")
		}
	}

	if prompt != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		err := s.ask(ctx, prompt)
		if ctx.Err() != nil {
			return errCancelled
		}
		return err
	}
	return s.repl()
}

// ask sends one user message, streams the answer, and keeps both in the conversation when the
// answer completes.
func (s *chatSession) ask(ctx context.Context, prompt string) error {
	a := s.a
	var msgs []oaMessage
	if s.system != "" {
		msgs = append(msgs, oaMessage{Role: "system", Content: s.system})
	}
	msgs = append(msgs, s.messages...)
	msgs = append(msgs, oaMessage{Role: "user", Content: prompt})

	req := &oaChatRequest{Model: s.model, Messages: msgs, Temperature: s.opts.temperature, MaxTokens: s.opts.maxTokens, ReasoningEffort: s.opts.effort}

	spin := startSpinner(a)
	thinking := false
	wroteContent := false
	lastByte := byte('\n')
	onDelta := func(content, reasoning string) error {
		spin.stop()
		if reasoning != "" && !s.opts.hideThinking && a.errTTY {
			if !thinking {
				thinking = true
				fmt.Fprint(a.stderr, a.errp.dimStart()+"Thinking…\n")
			}
			fmt.Fprint(a.stderr, reasoning)
		}
		if content != "" {
			if thinking {
				thinking = false
				fmt.Fprint(a.stderr, a.errp.reset()+"\n\n")
			}
			wroteContent = true
			if _, err := io.WriteString(a.stdout, content); err != nil {
				return err
			}
			lastByte = content[len(content)-1]
		}
		return nil
	}
	res, err := s.c.streamChat(ctx, req, onDelta)
	spin.stop()
	if thinking {
		fmt.Fprint(a.stderr, a.errp.reset())
	}
	if wroteContent && lastByte != '\n' {
		fmt.Fprintln(a.stdout)
	} else if thinking {
		fmt.Fprintln(a.stderr)
	}

	if ctx.Err() != nil {
		fmt.Fprintln(a.stderr, a.errp.dim("(stopped; this answer's cost will show on "+a.baseURL+"/usage)"))
		s.unknown++
		return errCancelled
	}
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "model_not_found" {
			s.suggest()
		}
		return err
	}

	s.messages = append(s.messages, oaMessage{Role: "user", Content: prompt}, oaMessage{Role: "assistant", Content: res.Content})
	if !wroteContent {
		switch {
		case res.FinishReason == "length" && res.Reasoning != "":
			fmt.Fprintln(a.stderr, a.errp.yellow("The model used every token thinking and gave no answer. Raise --max-tokens, or try --effort low."))
		case res.FinishReason == "length":
			fmt.Fprintln(a.stderr, a.errp.yellow("The answer hit the token limit before any text. Raise --max-tokens."))
		case res.Reasoning != "":
			fmt.Fprintln(a.stderr, a.errp.yellow("The model only thought and gave no answer."))
		default:
			fmt.Fprintln(a.stderr, a.errp.dim("(the model sent an empty answer)"))
		}
	}
	s.printCost(res)
	return nil
}

func (s *chatSession) printCost(res *chatResult) {
	if res.Usage != nil {
		s.tokens += res.Usage.PromptTokens + res.Usage.CompletionTokens
	}
	if res.Usage == nil || res.Usage.CostZharf == "" {
		s.unknown++
		if !s.opts.quiet {
			fmt.Fprintln(s.a.stderr, s.a.errp.dim("cost not reported; see "+s.a.baseURL+"/usage"))
		}
		return
	}
	if r, ok := new(big.Rat).SetString(res.Usage.CostZharf.String()); ok {
		s.total.Add(s.total, r)
	}
	if s.opts.quiet {
		return
	}
	line := fmt.Sprintf("%s ژرف · %d in, %d out tokens", res.Usage.CostZharf.String(), res.Usage.PromptTokens, res.Usage.CompletionTokens)
	fmt.Fprintln(s.a.stderr, s.a.errp.dim(line))
}

func (s *chatSession) suggest() {
	list, err := s.c.listModels(context.Background())
	if err != nil {
		return
	}
	if hits := suggestModels(list.Data, s.model, 5); len(hits) > 0 {
		fmt.Fprintln(s.a.stderr, s.a.errp.dim("Did you mean: "+strings.Join(hits, ", ")+"?"))
	} else {
		fmt.Fprintln(s.a.stderr, s.a.errp.dim("`zharf models` lists every model."))
	}
}

// checkModel makes sure the model exists and can chat before a REPL starts.
func (s *chatSession) checkModel(id string) error {
	m, err := s.c.getModel(context.Background(), id)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Code == "model_not_found") {
			old := s.model
			s.model = id
			s.suggest()
			s.model = old
		}
		return err
	}
	if kind := modelType(m); kind != "text" {
		return fmt.Errorf("%s is a model of type %s; zharf run talks to text models (see `zharf models --type text`)", id, kind)
	}
	return nil
}

func formatRat(r *big.Rat) string {
	return trimZeros(r.FloatString(6))
}

// ── The chat loop ───────────────────────────────────────────────────────────────────────────

func (s *chatSession) repl() error {
	a := s.a
	if err := s.checkModel(s.model); err != nil {
		return err
	}
	pal := a.errp
	fmt.Fprintln(a.stderr, pal.dim(fmt.Sprintf("Chatting with %s on %s. /? for help, /bye or Ctrl+D to leave.", s.model, a.host)))

	// Ctrl+C stops an answer; at the prompt it only reminds how to leave.
	var mu sync.Mutex
	var cancelAnswer context.CancelFunc
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	go func() {
		for range interrupts {
			mu.Lock()
			cancel := cancelAnswer
			mu.Unlock()
			if cancel != nil {
				cancel()
			} else {
				fmt.Fprint(a.stderr, "\n"+pal.dim("Use Ctrl+D or /bye to leave.")+"\n"+pal.bold(">>> "))
			}
		}
	}()

	in := bufio.NewReader(a.stdin)
	for {
		text, err := readMessage(in, a.stderr, pal)
		if err != nil {
			fmt.Fprintln(a.stderr)
			return nil // Ctrl+D
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "/") {
			if done := s.command(text); done {
				return nil
			}
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		mu.Lock()
		cancelAnswer = cancel
		mu.Unlock()
		err = s.ask(ctx, text)
		mu.Lock()
		cancelAnswer = nil
		mu.Unlock()
		cancel()
		if err != nil && !errors.Is(err, errCancelled) {
			a.printError(err)
		}
		fmt.Fprintln(a.stderr)
	}
}

// readMessage reads one message: a line, or the lines between """ and """.
func readMessage(in *bufio.Reader, prompt io.Writer, pal palette) (string, error) {
	fmt.Fprint(prompt, pal.bold(">>> "))
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, `"""`) {
		return line, nil
	}
	body := strings.TrimPrefix(trimmed, `"""`)
	if strings.HasSuffix(body, `"""`) && len(body) >= 3 {
		return strings.TrimSuffix(body, `"""`), nil
	}
	var b strings.Builder
	b.WriteString(body)
	for {
		fmt.Fprint(prompt, pal.dim("... "))
		next, err := in.ReadString('\n')
		next = strings.TrimRight(next, "\r\n")
		if strings.HasSuffix(strings.TrimRight(next, " "), `"""`) {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(strings.TrimSuffix(strings.TrimRight(next, " "), `"""`))
			return b.String(), nil
		}
		if b.Len() > 0 || body != "" {
			b.WriteByte('\n')
		}
		b.WriteString(next)
		if err != nil {
			return b.String(), nil
		}
	}
}

// command runs a /command and reports whether the chat should end.
func (s *chatSession) command(text string) bool {
	a := s.a
	pal := a.errp
	name, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "/bye", "/exit", "/quit":
		return true
	case "/clear":
		s.messages = nil
		fmt.Fprintln(a.stderr, pal.dim("Conversation cleared."))
	case "/model":
		if arg == "" {
			fmt.Fprintln(a.stderr, "Model: "+s.model)
			break
		}
		id := strings.TrimSuffix(arg, ":latest")
		if err := s.checkModel(id); err != nil {
			a.printError(err)
			break
		}
		s.model = id
		fmt.Fprintln(a.stderr, pal.dim("Now talking to "+id+"; the conversation is kept."))
	case "/system", "/set":
		if name == "/set" {
			sub, rest, _ := strings.Cut(arg, " ")
			if sub != "system" {
				fmt.Fprintln(a.stderr, "Only /set system is supported; see /?.")
				break
			}
			arg = strings.TrimSpace(rest)
		}
		if arg == "" {
			if s.system == "" {
				fmt.Fprintln(a.stderr, pal.dim("No system message."))
			} else {
				fmt.Fprintln(a.stderr, "System: "+s.system)
			}
			break
		}
		if arg == "clear" || arg == `""` {
			s.system = ""
			fmt.Fprintln(a.stderr, pal.dim("System message removed."))
			break
		}
		s.system = strings.Trim(arg, `"`)
		fmt.Fprintln(a.stderr, pal.dim("System message set."))
	case "/cost":
		line := fmt.Sprintf("This chat: %s ژرف, %d tokens.", formatRat(s.total), s.tokens)
		if s.unknown > 0 {
			line += fmt.Sprintf(" %d answer(s) had no cost reported; the dashboard has them: %s/usage", s.unknown, a.baseURL)
		}
		fmt.Fprintln(a.stderr, line)
	case "/?", "/help":
		fmt.Fprintln(a.stderr, `  """            Start and end a multi-line message
  /model <id>    Switch model (the conversation stays)
  /system <text> Set the system message; /system alone shows it, /system clear removes it
  /clear         Forget the conversation
  /cost          What this chat has cost so far
  /bye           Leave (or Ctrl+D). Ctrl+C stops an answer.`)
	default:
		fmt.Fprintf(a.stderr, "Unknown command %s. Type /? for help.\n", name)
	}
	return false
}

// ── A small spinner while the first token is on its way ────────────────────────────────────

type spinner struct {
	once sync.Once
	done chan struct{}
	wg   sync.WaitGroup
}

func startSpinner(a *app) *spinner {
	s := &spinner{done: make(chan struct{})}
	if !a.errTTY || !a.outTTY {
		close(s.done)
		return s
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		shown := false
		for i := 0; ; i++ {
			select {
			case <-s.done:
				if shown {
					fmt.Fprint(a.stderr, "\r \r")
				}
				return
			case <-ticker.C:
				fmt.Fprint(a.stderr, "\r"+a.errp.dim(frames[i%len(frames)]))
				shown = true
			}
		}
	}()
	return s
}

func (s *spinner) stop() {
	s.once.Do(func() {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	})
	s.wg.Wait()
}
