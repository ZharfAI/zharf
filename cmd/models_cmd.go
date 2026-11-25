package cmd

import (
	"context"
	"fmt"
	"strings"
)

const modelsUsage = `Usage: zharf models [--search WORDS] [--type TYPE] [--limit N] [--json]
       (also: zharf list, zharf ls)

Lists the platform's models, the families people use most first (GPT-5, Gemini 3, DeepSeek V4,
Qwen 3.5, GLM-5, Kimi K2, Grok 4, MiniMax M2), then the rest by maker. Prices are in ژرف per
1M input and output tokens; media models show their unit price (per image, second, job…).
No key is needed.

Flags:
  -s, --search WORDS   Only models whose id, name or maker contain every word
  -t, --type TYPE      text, image, audio, video, embedding or other
                       (audio covers speech, transcription and music; other is the
                       decisions models)
  -n, --limit N        Show at most N models
  --json               The models as JSON, in the same order

Examples:
  zharf models --search gpt
  zharf models --type image
  zharf models --search "qwen 3.5" --json`

var modelsFlags = []flagDef{
	{long: "search", short: "s", value: true},
	{long: "type", short: "t", value: true},
	{long: "limit", short: "n", value: true},
	{long: "json"},
}

func cmdModels(a *app, p parsed) error {
	kind := strings.ToLower(p.str("type", ""))
	if kind != "" && !has(modelTypes, kind) {
		return usageErr("--type takes one of: %s", strings.Join(modelTypes, ", "))
	}
	search := p.str("search", "")
	if len(p.args) > 0 {
		search = strings.TrimSpace(search + " " + strings.Join(p.args, " "))
	}
	limit, err := p.integer("limit")
	if err != nil {
		return usageErr("%v", err)
	}
	c, _, _ := a.client(false)
	list, err := c.listModels(context.Background())
	if err != nil {
		return err
	}
	models := filterModels(list.Data, search, kind)
	sortModels(models)
	if limit != nil && *limit >= 0 && *limit < len(models) {
		models = models[:*limit]
	}
	if p.has("json") {
		if models == nil {
			models = []Model{}
		}
		return encodeJSON(a.stdout, models)
	}
	if len(models) == 0 {
		fmt.Fprintln(a.stderr, "No models match.")
		return errQuiet
	}
	writeModelTable(a.stdout, models, a.out)
	fmt.Fprintln(a.stderr, a.errp.dim(fmt.Sprintf("%d of %d models. %s", len(models), len(list.Data), pricesFootnote(list.PricesValidUntil))))
	return nil
}

const showUsage = `Usage: zharf show <model> [--json]

Shows one model: maker, type, what it takes and returns, context, features and prices in ژرف.`

var showFlags = []flagDef{{long: "json"}}

func cmdShow(a *app, p parsed) error {
	if len(p.args) == 0 {
		return usageErr("which model? Usage: zharf show <model>")
	}
	id := strings.TrimSuffix(p.args[0], ":latest")
	c, _, _ := a.client(false)
	m, err := c.getModel(context.Background(), id)
	if err != nil {
		var list *modelList
		if apiErr, ok := err.(*APIError); ok && apiErr.Status == 404 {
			if list, _ = c.listModels(context.Background()); list != nil {
				if hits := suggestModels(list.Data, id, 5); len(hits) > 0 {
					a.printError(err)
					fmt.Fprintln(a.stderr, a.errp.dim("Did you mean: "+strings.Join(hits, ", ")+"?"))
					return errQuiet
				}
			}
		}
		return err
	}
	if p.has("json") {
		return encodeJSON(a.stdout, m)
	}
	pal := a.out
	row := func(label, value string) {
		if value != "" {
			fmt.Fprintf(a.stdout, "  %s %s\n", pal.dim(padRight(label, 12)), value)
		}
	}
	fmt.Fprintln(a.stdout, pal.bold(m.ID))
	if m.Name != "" && m.Name != m.ID {
		row("Name", m.Name)
	}
	row("Maker", m.OwnedBy)
	row("Type", modelType(m))
	row("Modality", m.Modality)
	row("Context", formatContext(m.ContextLength))
	if m.MaxCompletionTokens != nil && *m.MaxCompletionTokens > 0 {
		row("Max output", formatContext(m.MaxCompletionTokens))
	}
	if len(m.Features) > 0 {
		row("Features", strings.Join(m.Features, ", "))
	}
	in, out := priceColumns(m)
	switch {
	case in == "on use":
		row("Price", "priced when it runs (the cost comes back with each answer)")
	case out == "-":
		row("Price", in+" ژرف")
	default:
		row("Price", fmt.Sprintf("%s ژرف per 1M input tokens, %s per 1M output tokens", in, out))
	}
	if m.Description != "" {
		fmt.Fprintln(a.stdout)
		fmt.Fprintln(a.stdout, wrapText(m.Description, 88, "  "))
	}
	return nil
}

// wrapText breaks text at spaces so no line passes width runes.
func wrapText(text string, width int, indent string) string {
	var b strings.Builder
	for i, para := range strings.Split(strings.TrimSpace(text), "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		line := 0
		b.WriteString(indent)
		for j, word := range strings.Fields(para) {
			n := len([]rune(word))
			if j > 0 && line+1+n > width {
				b.WriteString("\n" + indent)
				line = 0
			} else if j > 0 {
				b.WriteByte(' ')
				line++
			}
			b.WriteString(word)
			line += n
		}
	}
	return b.String()
}
