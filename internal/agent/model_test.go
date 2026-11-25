package agent

import (
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

func TestResolveModelNeverSendsAForeignModel(t *testing.T) {
	ollama := config.Provider{
		Name: "Ollama (lokal, tanpa key)", DefaultModel: "llama3.2",
		Models: []string{"llama3.2", "qwen2.5"},
	}
	openai := config.Provider{
		Name: "OpenAI", DefaultModel: "gpt-4o-mini",
		Models: []string{"gpt-4o-mini", "gpt-4o"},
	}
	custom := config.Provider{Name: "Custom", DefaultModel: "my-model"}

	cases := []struct {
		name string
		want string
		prov config.Provider
		got  string
	}{
		{"known model is kept", "qwen2.5", ollama, "qwen2.5"},
		{"case-insensitive match returns canonical spelling", "Llama3.2", ollama, "llama3.2"},
		{"guessed list passes an unknown model through to the vendor", "gpt-4o-mini", ollama, "gpt-4o-mini"},
		{"empty model uses the provider default", "", ollama, "llama3.2"},
		{"openai keeps its own model", "gpt-4o", openai, "gpt-4o"},
		{"provider with no list trusts the person", "whatever-v2", custom, "whatever-v2"},
		{"provider with no list and no model uses the default", "", custom, "my-model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveModel(c.want, c.prov); got != c.got {
				t.Fatalf("resolveModel(%q on %s) = %q, want %q", c.want, c.prov.Name, got, c.got)
			}
		})
	}
}

// A ":free" suffix is OpenRouter-only: forwarding it to DeepSeek produced a 404
// in practice ("model deepseek-chat-v3.1:free tidak ada").
func TestFreeSuffixNeverReachesOtherVendors(t *testing.T) {
	deepseek := config.Provider{
		ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1",
		DefaultModel: "deepseek-chat", Models: []string{"deepseek-chat", "deepseek-reasoner"},
	}
	if got := resolveModel("deepseek-chat-v3.1:free", deepseek); got != "deepseek-chat" {
		t.Fatalf("free suffix leaked to DeepSeek: %q", got)
	}
	if got := resolveModel("deepseek-chat:free", deepseek); got != "deepseek-chat" {
		t.Fatalf("should strip :free and match the plain model, got %q", got)
	}

	openrouter := config.Provider{
		ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1",
		DefaultModel: "openai/gpt-4o-mini",
		Models:       []string{"deepseek/deepseek-chat-v3.1:free"},
	}
	if got := resolveModel("deepseek/deepseek-chat-v3.1:free", openrouter); got != "deepseek/deepseek-chat-v3.1:free" {
		t.Fatalf("OpenRouter must keep the :free model: %q", got)
	}
}

// Once the list comes from the vendor's API it is authoritative.
func TestLiveModelListIsAuthoritative(t *testing.T) {
	live := config.Provider{
		ID: "deepseek", Name: "DeepSeek", DefaultModel: "deepseek-chat",
		Models: []string{"deepseek-chat", "deepseek-reasoner"}, ModelsSource: "live",
	}
	if got := resolveModel("deepseek-chat", live); got != "deepseek-chat" {
		t.Fatalf("a listed model must pass through: %q", got)
	}
	if got := resolveModel("deepseek-v4.1-flash", live); got != "deepseek-chat" {
		t.Fatalf("an unlisted model on a live list falls back to the default, got %q", got)
	}
	// Seed lists are a guess: the person's model wins.
	seed := live
	seed.ModelsSource = "seed"
	if got := resolveModel("deepseek-v4.1-flash", seed); got != "deepseek-v4.1-flash" {
		t.Fatalf("a guessed list must not override the person, got %q", got)
	}
}
