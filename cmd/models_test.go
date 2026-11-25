package cmd

import (
	"reflect"
	"strings"
	"testing"
)

func mkModel(id, maker, modality string, out ...string) Model {
	return Model{ID: id, OwnedBy: maker, Modality: modality, OutputModalities: out}
}

func ids(models []Model) []string {
	out := make([]string, len(models))
	for i, m := range models {
		out[i] = m.ID
	}
	return out
}

func TestSortModelsPopularFirst(t *testing.T) {
	models := []Model{
		mkModel("abliteration-large", "Abliteration AI", "text->text", "text"),
		mkModel("minimax-m2.5", "MiniMax", "text->text", "text"),
		mkModel("zeta-chat", "Zeta", "text->text", "text"),
		mkModel("qwen3.5-9b", "Qwen", "text->text", "text"),
		mkModel("gpt-5-image", "OpenAI", "text->image", "image"),
		mkModel("gpt-5-mini", "OpenAI", "text->text", "text"),
		mkModel("gpt-5.4", "OpenAI", "text+image->text", "text"),
		mkModel("gpt-5.10", "OpenAI", "text->text", "text"),
		mkModel("deepseek-v4-flash", "DeepSeek", "text->text", "text"),
		mkModel("gemini-3.8-flash", "Google", "text->text", "text"),
		mkModel("kimi-k2.6", "Moonshot", "text->text", "text"),
		mkModel("glm-5.1", "Z.ai", "text->text", "text"),
		mkModel("grok-4.7", "xAI", "text->text", "text"),
		mkModel("qwen3-5-flash", "Qwen", "text->text", "text"),
		mkModel("alpha-video", "Alpha", "video", "video"),
		mkModel("beta-model", "abliteration AI", "text->text", "text"),
	}
	sortModels(models)
	want := []string{
		"gpt-5.10", "gpt-5.4", "gpt-5-mini", // newest first, numbers compared as numbers
		"gpt-5-image", // image after text inside the family
		"gemini-3.8-flash",
		"deepseek-v4-flash",
		"qwen3.5-9b", "qwen3-5-flash", // qwen3[.-]5
		"glm-5.1",
		"kimi-k2.6",
		"grok-4.7",
		"minimax-m2.5",
		"beta-model", "abliteration-large", // then by maker: letters first, and on a tie lowercase first, as ICU does
		"alpha-video",
		"zeta-chat",
	}
	if got := ids(models); !reflect.DeepEqual(got, want) {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}
}

func TestNaturalCompare(t *testing.T) {
	less := [][2]string{
		{"gpt-5.2", "gpt-5.10"},
		{"glm-5", "glm-5.1"},
		{"a-b", "a.b"},
		{"a_b", "a-b"},
		{"Venice AI", "venice-ai"},
		{"xAI", "Xiaohongshu"},
		{"deepseek-v4-flash-0731", "deepseek-v4-flash-1017"},
	}
	for _, pair := range less {
		if naturalCompare(pair[0], pair[1]) >= 0 || naturalCompare(pair[1], pair[0]) <= 0 {
			t.Errorf("want %q < %q", pair[0], pair[1])
		}
	}
	if naturalCompare("abc", "abc") != 0 {
		t.Error("equal strings")
	}
}

func TestModelType(t *testing.T) {
	cases := []struct {
		m    Model
		want string
	}{
		{Model{Modality: "text+image->text", OutputModalities: []string{"text"}}, "text"},
		{Model{Modality: "audio->text", OutputModalities: []string{"text"}, Operations: []string{"stt"}}, "audio"},
		{Model{Modality: "music", OutputModalities: []string{"music"}, Operations: []string{"music"}}, "audio"},
		{Model{Modality: "music", OutputModalities: []string{"music"}, Operations: []string{"tts"}}, "audio"},
		{Model{Modality: "text->audio", OutputModalities: []string{"audio"}}, "audio"},
		{Model{Modality: "text->image", OutputModalities: []string{"image"}}, "image"},
		{Model{Modality: "video", OutputModalities: []string{"video"}}, "video"},
		{Model{Modality: "embedding", OutputModalities: []string{"embedding"}}, "embedding"},
		{Model{Modality: "decisions", OutputModalities: []string{"decisions"}}, "other"},
	}
	for _, c := range cases {
		if got := modelType(&c.m); got != c.want {
			t.Errorf("%s: got %s, want %s", c.m.Modality, got, c.want)
		}
	}
}

func TestPriceColumns(t *testing.T) {
	text := Model{OutputModalities: []string{"text"}, Pricing: Pricing{Prompt: "318.2905", Completion: "636.581"}}
	if in, out := priceColumns(&text); in != "318.3" || out != "636.6" {
		t.Errorf("text: %s %s", in, out)
	}
	image := Model{OutputModalities: []string{"image"}, Pricing: Pricing{Prompt: "0", Completion: "0", PerUnit: "91.6677", MediaUnit: "image"}}
	if in, out := priceColumns(&image); in != "91.67/image" || out != "-" {
		t.Errorf("image: %s %s", in, out)
	}
	tts := Model{OutputModalities: []string{"audio"}, Pricing: Pricing{PerUnit: "23.4262", MediaUnit: "1K characters"}}
	if in, _ := priceColumns(&tts); in != "23.43/1K chars" {
		t.Errorf("tts: %s", in)
	}
	onUse := Model{OutputModalities: []string{"text"}, Pricing: Pricing{Prompt: "0", Completion: "0", PricedOnUse: true}}
	if in, out := priceColumns(&onUse); in != "on use" || out != "on use" {
		t.Errorf("on use: %s %s", in, out)
	}
	big := Model{OutputModalities: []string{"image"}, Pricing: Pricing{Prompt: "76.3898", Completion: "7638.972"}}
	if in, out := priceColumns(&big); in != "76.39" || out != "7,639" {
		t.Errorf("big: %s %s", in, out)
	}
}

func TestFilterModels(t *testing.T) {
	models := []Model{
		{ID: "gpt-5-mini", OwnedBy: "OpenAI", OutputModalities: []string{"text"}},
		{ID: "gpt-5-image", OwnedBy: "OpenAI", OutputModalities: []string{"image"}},
		{ID: "qwen3.5-9b", Name: "Qwen 3.5 9B", OwnedBy: "Qwen", OutputModalities: []string{"text"}},
	}
	if got := ids(filterModels(models, "GPT", "")); !reflect.DeepEqual(got, []string{"gpt-5-mini", "gpt-5-image"}) {
		t.Errorf("search: %v", got)
	}
	if got := ids(filterModels(models, "gpt", "image")); !reflect.DeepEqual(got, []string{"gpt-5-image"}) {
		t.Errorf("type: %v", got)
	}
	if got := ids(filterModels(models, "qwen 9b", "text")); !reflect.DeepEqual(got, []string{"qwen3.5-9b"}) {
		t.Errorf("words: %v", got)
	}
}

func TestModelTableAlignsPersianFreeColumns(t *testing.T) {
	ctx := 1_000_000.0
	models := []Model{{ID: "qwen3.5-9b", OwnedBy: "Qwen", ContextLength: &ctx, OutputModalities: []string{"text"}, Pricing: Pricing{Prompt: "12.5", Completion: "50"}}}
	var b strings.Builder
	writeModelTable(&b, models, palette{})
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "MODEL") || !strings.Contains(lines[1], "1M") || !strings.HasSuffix(lines[1], "50") {
		t.Fatalf("table:\n%s", b.String())
	}
}

func TestSuggestModels(t *testing.T) {
	models := []Model{{ID: "qwen3.5-9b"}, {ID: "qwen3.5-plus"}, {ID: "gpt-5-mini"}}
	got := suggestModels(models, "qwen3.5", 5)
	if len(got) != 2 || got[0] != "qwen3.5-9b" {
		t.Fatalf("got %v", got)
	}
}
