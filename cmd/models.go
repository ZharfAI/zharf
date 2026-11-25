package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Model is one entry of GET /v1/models.
type Model struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	OwnedBy             string   `json:"owned_by"`
	Description         string   `json:"description"`
	Created             int64    `json:"created"`
	ContextLength       *float64 `json:"context_length"`
	MaxCompletionTokens *float64 `json:"max_completion_tokens"`
	Modality            string   `json:"modality"`
	InputModalities     []string `json:"input_modalities"`
	OutputModalities    []string `json:"output_modalities"`
	Features            []string `json:"features"`
	Operations          []string `json:"operations"`
	Pricing             Pricing  `json:"pricing"`
}

type Pricing struct {
	Currency    string      `json:"currency"`
	Prompt      zharfAmount `json:"prompt_per_1m_tokens"`
	Completion  zharfAmount `json:"completion_per_1m_tokens"`
	PerUnit     zharfAmount `json:"per_unit"`
	MediaUnit   string      `json:"media_unit"`
	PricedOnUse bool        `json:"priced_on_use"`
}

type modelList struct {
	Data             []Model `json:"data"`
	PricesValidUntil string  `json:"prices_valid_until"`
}

func (c *Client) listModels(ctx context.Context) (*modelList, error) {
	var list modelList
	if err := c.getJSON(ctx, "/v1/models", false, &list, 60*time.Second); err != nil {
		return nil, err
	}
	return &list, nil
}

func (c *Client) getModel(ctx context.Context, id string) (*Model, error) {
	var m Model
	if err := c.getJSON(ctx, "/v1/models/"+url.PathEscape(id), false, &m, 30*time.Second); err != nil {
		return nil, err
	}
	return &m, nil
}

func has(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// modelTypes are the values --type takes.
var modelTypes = []string{"text", "image", "audio", "video", "embedding", "other"}

// modelType files a model under one --type. Speech-to-text answers in text but takes audio,
// so it is "audio" with text-to-speech and music; the decisions models are "other".
func modelType(m *Model) string {
	switch {
	case has(m.OutputModalities, "embedding") || m.Modality == "embedding" || has(m.Operations, "embedding"):
		return "embedding"
	case has(m.Operations, "stt") || has(m.Operations, "tts") || has(m.Operations, "music"):
		return "audio"
	case has(m.OutputModalities, "video"):
		return "video"
	case has(m.OutputModalities, "image"):
		return "image"
	case has(m.OutputModalities, "audio") || has(m.OutputModalities, "music"):
		return "audio"
	case has(m.OutputModalities, "text"):
		return "text"
	}
	switch {
	case strings.HasSuffix(m.Modality, "->text"):
		return "text"
	case strings.HasSuffix(m.Modality, "->image"):
		return "image"
	case m.Modality == "video":
		return "video"
	case m.Modality == "music" || strings.HasSuffix(m.Modality, "->audio"):
		return "audio"
	}
	return "other"
}

// isChatModel: a model /v1/chat/completions can talk to (text out, not speech-to-text).
func isChatModel(m *Model) bool {
	return modelType(m) == "text"
}

// ── Order ───────────────────────────────────────────────────────────────────────────────────
// The dashboard's recommended («پیشنهادی») order: the families people come
// for first, in this order; inside a family text models before image ones and the newest version
// first; then every other model by maker, then by id.

var families = []*regexp.Regexp{
	regexp.MustCompile(`^gpt-5`),       // OpenAI
	regexp.MustCompile(`^gemini-3`),    // Google
	regexp.MustCompile(`^deepseek-v4`), // DeepSeek
	regexp.MustCompile(`^qwen3[.-]5`),  // Qwen 3.5
	regexp.MustCompile(`^glm-5`),       // Z.ai
	regexp.MustCompile(`^kimi-k2`),     // Moonshot
	regexp.MustCompile(`^grok-4`),      // xAI
	regexp.MustCompile(`^minimax-m2`),  // MiniMax
}

var notTextModality = regexp.MustCompile(`->image|^video$|^music$|->audio|^embedding$`)

func family(m *Model) int {
	id := strings.ToLower(m.ID)
	for i, f := range families {
		if f.MatchString(id) {
			return i
		}
	}
	return len(families)
}

func isTextForOrder(m *Model) bool { return !notTextModality.MatchString(m.Modality) }

// sortModels puts models in the dashboard's recommended order (stable).
func sortModels(models []Model) {
	sort.SliceStable(models, func(i, j int) bool {
		a, b := &models[i], &models[j]
		fa, fb := family(a), family(b)
		if fa != fb {
			return fa < fb
		}
		if fa < len(families) {
			ta, tb := 1, 1
			if isTextForOrder(a) {
				ta = 0
			}
			if isTextForOrder(b) {
				tb = 0
			}
			if ta != tb {
				return ta < tb
			}
			return naturalCompare(b.ID, a.ID) < 0 // newest first
		}
		if c := naturalCompare(a.OwnedBy, b.OwnedBy); c != 0 {
			return c < 0
		}
		return naturalCompare(a.ID, b.ID) < 0
	})
}

// naturalCompare approximates JavaScript's a.localeCompare(b, 'en', { numeric: true }): runs of
// digits compare as numbers, letters compare without case first, and spaces and punctuation
// sort before digits and letters in ICU's order ("_" < "-" < "," < ";" < ":" < "!" < "?" < ".").
func naturalCompare(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		ca, cb := ra[i], rb[j]
		if unicode.IsDigit(ca) && unicode.IsDigit(cb) {
			si := i
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			sj := j
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			na := strings.TrimLeft(string(ra[si:i]), "0")
			nb := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(na) != len(nb) {
				return cmpInt(len(na), len(nb))
			}
			if na != nb {
				return strings.Compare(na, nb)
			}
			continue
		}
		ka, kb := collationKey(ca), collationKey(cb)
		if ka != kb {
			return cmpInt(ka, kb)
		}
		i++
		j++
	}
	switch {
	case i < len(ra):
		return 1
	case j < len(rb):
		return -1
	}
	// Equal without case: ICU puts lowercase first.
	for k := 0; k < len(ra) && k < len(rb); k++ {
		if ra[k] != rb[k] {
			if unicode.IsLower(ra[k]) && unicode.IsUpper(rb[k]) {
				return -1
			}
			if unicode.IsUpper(ra[k]) && unicode.IsLower(rb[k]) {
				return 1
			}
		}
	}
	return 0
}

const icuPunctuation = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$"

func collationKey(r rune) int {
	if i := strings.IndexRune(icuPunctuation, r); i >= 0 {
		return i
	}
	if unicode.IsDigit(r) {
		return 100 + int(r-'0')
	}
	if r < 128 {
		return 200 + int(unicode.ToLower(r))
	}
	return 1000 + int(unicode.ToLower(r))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// ── Filtering and display ───────────────────────────────────────────────────────────────────

func filterModels(models []Model, search, kind string) []Model {
	search = strings.ToLower(strings.TrimSpace(search))
	var out []Model
	for _, m := range models {
		if kind != "" && modelType(&m) != kind {
			continue
		}
		if search != "" {
			hay := strings.ToLower(m.ID + " " + m.Name + " " + m.OwnedBy)
			match := true
			for _, word := range strings.Fields(search) {
				if !strings.Contains(hay, word) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// formatZharf shows a price with sensible precision: 7,639 · 318.3 · 50.93 · 0.0764.
func formatZharf(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v >= 1000:
		return groupThousands(strconv.FormatFloat(math.Round(v), 'f', 0, 64))
	case v >= 100:
		return trimZeros(strconv.FormatFloat(v, 'f', 1, 64))
	case v >= 1:
		return trimZeros(strconv.FormatFloat(v, 'f', 2, 64))
	case v >= 0.0001:
		return trimZeros(strconv.FormatFloat(v, 'f', 4, 64))
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func formatContext(v *float64) string {
	if v == nil || *v <= 0 {
		return "-"
	}
	n := *v
	switch {
	case n >= 1_000_000:
		return trimZeros(strconv.FormatFloat(n/1_000_000, 'f', 2, 64)) + "M"
	case n >= 1000:
		return trimZeros(strconv.FormatFloat(n/1000, 'f', 1, 64)) + "K"
	}
	return strconv.FormatFloat(n, 'f', 0, 64)
}

var unitNames = map[string]string{
	"1K characters": "1K chars",
	"1M tokens":     "1M tokens",
}

// priceColumns gives the input and output price cells. Token-priced models show ژرف per
// million tokens; media shows its unit price ("91.67/image"); a model priced only when it runs
// shows "on use". Never a bare 0 for something that costs money.
func priceColumns(m *Model) (string, string) {
	p := m.Pricing
	in, out := p.Prompt.Float(), p.Completion.Float()
	if in > 0 || out > 0 {
		inCell, outCell := formatZharf(in), formatZharf(out)
		if out == 0 && modelType(m) != "text" {
			outCell = "-"
		}
		return inCell, outCell
	}
	if unit := p.PerUnit.Float(); unit > 0 && p.MediaUnit != "" {
		name := p.MediaUnit
		if short, ok := unitNames[name]; ok {
			name = short
		}
		if p.MediaUnit == "1M tokens" {
			return formatZharf(unit), "-"
		}
		return formatZharf(unit) + "/" + name, "-"
	}
	if p.PricedOnUse || (in == 0 && out == 0) {
		return "on use", "on use"
	}
	return formatZharf(in), formatZharf(out)
}

func writeModelTable(w io.Writer, models []Model, pal palette) {
	headers := []string{"MODEL", "MAKER", "TYPE", "CONTEXT", "IN/1M", "OUT/1M"}
	rows := make([][]string, 0, len(models))
	for i := range models {
		m := &models[i]
		in, out := priceColumns(m)
		rows = append(rows, []string{m.ID, truncate(m.OwnedBy, 18), modelType(m), formatContext(m.ContextLength), in, out})
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if n := len([]rune(cell)); n > widths[i] {
				widths[i] = n
			}
		}
	}
	rightAligned := map[int]bool{3: true, 4: true, 5: true}
	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for i, cell := range cells {
			if rightAligned[i] {
				parts[i] = padLeft(cell, widths[i])
			} else {
				parts[i] = padRight(cell, widths[i])
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	fmt.Fprintln(w, pal.dim(line(headers)))
	for _, row := range rows {
		fmt.Fprintln(w, line(row))
	}
}

// tehran is Iran Standard Time; Iran has kept UTC+3:30 all year since 2022.
var tehran = time.FixedZone("Tehran", 3*3600+1800)

func pricesFootnote(validUntil string) string {
	note := "Prices in ژرف: per 1M input/output tokens, or per unit for media."
	if t, err := time.Parse(time.RFC3339, validUntil); err == nil {
		note += " Fixed until " + t.In(tehran).Format("2006-01-02 15:04") + " Tehran time."
	}
	return note
}

// suggestModels lists ids close to a mistyped one: same start, or containing its words.
func suggestModels(models []Model, wanted string, limit int) []string {
	wanted = strings.ToLower(wanted)
	type scored struct {
		id    string
		score int
	}
	var hits []scored
	for _, m := range models {
		id := strings.ToLower(m.ID)
		score := 0
		switch {
		case strings.HasPrefix(id, wanted) || strings.HasPrefix(wanted, id):
			score = 3
		case strings.Contains(id, wanted):
			score = 2
		default:
			for _, part := range strings.FieldsFunc(wanted, func(r rune) bool { return r == '-' || r == '.' || r == ' ' || r == ':' }) {
				if len(part) >= 3 && strings.Contains(id, part) {
					score = 1
					break
				}
			}
		}
		if score > 0 {
			hits = append(hits, scored{m.ID, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	var out []string
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.id)
	}
	return out
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
