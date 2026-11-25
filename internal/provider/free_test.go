package provider

import (
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

func TestFreeKindOf(t *testing.T) {
	cases := []struct {
		p    config.Provider
		want string
	}{
		{config.Provider{ID: "ollama", BaseURL: "http://127.0.0.1:11434/v1"}, FreeAll},
		{config.Provider{ID: "groq", BaseURL: "https://api.groq.com/openai/v1"}, FreeTier},
		{config.Provider{ID: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"}, FreeTier},
		{config.Provider{ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, FreeSuffix},
		{config.Provider{ID: "deepseek", BaseURL: "https://api.deepseek.com/v1"}, FreePaid},
		{config.Provider{ID: "openai", BaseURL: "https://api.openai.com/v1"}, FreePaid},
		{config.Provider{ID: "anthropic-api", BaseURL: "https://api.anthropic.com/v1"}, FreePaid},
	}
	for _, c := range cases {
		if got := FreeKindOf(c.p); got != c.want {
			t.Fatalf("%s: FreeKindOf = %q, want %q", c.p.ID, got, c.want)
		}
	}
}

func TestFreeModelsUsesTheProvidersOwnList(t *testing.T) {
	// A tier-free vendor: everything it lists is free.
	groq := config.Provider{ID: "groq", BaseURL: "https://api.groq.com/openai/v1",
		Models: []string{"llama-3.1-8b-instant", "openai/gpt-oss-20b"}}
	if got := FreeModels(groq); len(got) != 2 {
		t.Fatalf("groq should offer both models free, got %v", got)
	}

	// OpenRouter: only the :free suffix.
	or := config.Provider{ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1",
		Models: []string{"openai/gpt-4o-mini", "deepseek/deepseek-chat-v3.1:free", "meta-llama/llama-3.3-70b-instruct:free"}}
	got := FreeModels(or)
	if len(got) != 2 {
		t.Fatalf("only :free models count at OpenRouter, got %v", got)
	}
	for _, m := range got {
		if !strings.HasSuffix(m, ":free") {
			t.Fatalf("%q is not free", m)
		}
	}

	// A paid vendor offers nothing free — no invented names.
	ds := config.Provider{ID: "deepseek", BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"}}
	if got := FreeModels(ds); len(got) != 0 {
		t.Fatalf("DeepSeek has no free models, got %v", got)
	}

	// Local runtime without a synced list falls back to the built-in list.
	ol := config.Provider{ID: "ollama", BaseURL: "http://127.0.0.1:11434/v1"}
	if got := FreeModels(ol); len(got) == 0 {
		t.Fatal("ollama should report its built-in models as free")
	}
}

func TestIsFree(t *testing.T) {
	or := config.Provider{ID: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	if !IsFree(or, "deepseek/deepseek-chat-v3.1:free") {
		t.Fatal(":free model should be free")
	}
	if IsFree(or, "openai/gpt-4o-mini") {
		t.Fatal("a paid model must not be reported free")
	}
	groq := config.Provider{ID: "groq", BaseURL: "https://api.groq.com/openai/v1"}
	if !IsFree(groq, "anything-at-all") {
		t.Fatal("the whole Groq catalogue runs on the free tier")
	}
	ds := config.Provider{ID: "deepseek", BaseURL: "https://api.deepseek.com/v1"}
	if IsFree(ds, "deepseek-chat") {
		t.Fatal("DeepSeek is paid")
	}
}

func TestSortForChatSkipsNonChatModels(t *testing.T) {
	got := SortForChat([]string{"whisper-large-v3", "llama-guard-3-8b", "openai/gpt-oss-120b", "llama-3.1-8b-instant"})
	if got[0] != "llama-3.1-8b-instant" {
		t.Fatalf("a small chat model should come first, got %v", got)
	}
	tail := strings.Join(got[len(got)-2:], ",")
	if !strings.Contains(tail, "whisper-large-v3") || !strings.Contains(tail, "llama-guard-3-8b") {
		t.Fatalf("speech/guard models must sink to the bottom, got %v", got)
	}
	if ChatRank("text-embedding-3-small") != 9 {
		t.Fatal("embeddings are not chat models")
	}
	for _, m := range []string{"llama-3.1-8b-instant", "gemini-2.0-flash", "qwen/qwen3-32b"} {
		if ChatRank(m) >= 9 {
			t.Fatalf("%q should be usable for chat", m)
		}
	}
}
