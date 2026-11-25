package config

import (
	"strings"
	"testing"
)

func TestSanitizeKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sk-or-v1-abc", "sk-or-v1-abc"},
		{"  sk-or-v1-abc\n", "sk-or-v1-abc"},
		{`"sk-or-v1-abc"`, "sk-or-v1-abc"},
		{"'sk-or-v1-abc'", "sk-or-v1-abc"},
		{"`sk-or-v1-abc`", "sk-or-v1-abc"},
		{"Bearer sk-or-v1-abc", "sk-or-v1-abc"},
		{"bearer sk-or-v1-abc", "sk-or-v1-abc"},
		{"  Bearer   sk-or-v1-abc  ", "sk-or-v1-abc"},
		{"sk-or-v1-abc extra words", "sk-or-v1-abc"},
		{"\n\nsk-or-v1-abc\r\n", "sk-or-v1-abc"},
	}
	for _, c := range cases {
		got, _ := SanitizeKey(c.in)
		if got != c.want {
			t.Fatalf("SanitizeKey(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.Contains(got, "\n") || strings.Contains(got, "Bearer") || strings.Contains(got, `"`) {
			t.Fatalf("SanitizeKey(%q) left junk: %q", c.in, got)
		}
	}
}

func TestKeyWarningNamesTheWrongVendor(t *testing.T) {
	openrouter := Provider{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1"}

	if w := KeyWarning(openrouter, "sk-or-v1-123456"); w != "" {
		t.Fatalf("a correct OpenRouter key must not warn: %q", w)
	}
	w := KeyWarning(openrouter, "sk-proj-123456")
	if !strings.Contains(w, "OpenAI") || !strings.Contains(w, "sk-or-v1-") {
		t.Fatalf("a wrong-vendor key should be named: %q", w)
	}
	if w := KeyWarning(openrouter, "totally-random-value"); w == "" {
		t.Fatal("a key with no recognisable shape should still warn")
	}
	// A custom endpoint has no shape to check.
	custom := Provider{ID: "x", BaseURL: "https://my-proxy.test"}
	if w := KeyWarning(custom, "whatever"); w != "" {
		t.Fatalf("custom endpoints must not warn: %q", w)
	}
	// Env indirection is checked at resolution time, not here.
	if w := KeyWarning(openrouter, "$OPENROUTER_API_KEY"); w != "" {
		t.Fatalf("env indirection must not warn about shape: %q", w)
	}
}

func TestExpectedKeyPrefix(t *testing.T) {
	cases := []struct {
		p    Provider
		want string
	}{
		{Provider{ID: "openrouter"}, "sk-or-v1-"},
		{Provider{ID: "anthropic-api"}, "sk-ant-"},
		{Provider{ID: "groq"}, "gsk_"},
		{Provider{ID: "openai", BaseURL: "https://api.openai.com/v1"}, "sk-"},
		{Provider{ID: "local", BaseURL: "http://127.0.0.1:11434/v1"}, ""},
	}
	for _, c := range cases {
		if got, _ := ExpectedKeyPrefix(c.p); got != c.want {
			t.Fatalf("%s: got prefix %q want %q", c.p.ID, got, c.want)
		}
	}
}
