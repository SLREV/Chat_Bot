package config

import (
	"strings"
)

// Key format knowledge. It exists because the most common 401 is not a wrong
// account: it is a key that was pasted with quotes, a newline, a "Bearer "
// prefix — or a key from a different vendor.
var keyFormats = []struct {
	match  func(Provider) bool
	prefix string
	label  string
}{
	{func(p Provider) bool {
		return p.ID == "openrouter" || strings.Contains(strings.ToLower(p.BaseURL), "openrouter")
	},
		"sk-or-v1-", "OpenRouter"},
	{func(p Provider) bool {
		return p.ID == "anthropic-api" || strings.Contains(strings.ToLower(p.BaseURL), "anthropic")
	},
		"sk-ant-", "Anthropic"},
	{func(p Provider) bool { return p.ID == "groq" || strings.Contains(strings.ToLower(p.BaseURL), "groq") },
		"gsk_", "Groq"},
	{func(p Provider) bool {
		return p.ID == "deepseek" || strings.Contains(strings.ToLower(p.BaseURL), "deepseek")
	},
		"sk-", "DeepSeek"},
	{func(p Provider) bool {
		return p.ID == "openai" || strings.Contains(strings.ToLower(p.BaseURL), "api.openai.com")
	},
		"sk-", "OpenAI"},
}

// ExpectedKeyPrefix returns the prefix a provider's keys normally carry, and a
// human label. Empty prefix means "no shape to check" (custom or local setups).
func ExpectedKeyPrefix(p Provider) (string, string) {
	for _, f := range keyFormats {
		if f.match(p) {
			return f.prefix, f.label
		}
	}
	return "", ""
}

// SanitizeKey cleans a pasted credential and reports what it changed.
//
// People paste "Bearer sk-…", `"sk-…"`, or a value with a trailing newline all
// the time; sending those verbatim returns a 401 that looks like a bad account.
func SanitizeKey(raw string) (key string, notes []string) {
	key = strings.TrimSpace(raw)
	key = strings.Trim(key, "\"'`")
	key = strings.TrimSpace(key)
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		key = strings.TrimSpace(key[7:])
		notes = append(notes, "awalan \"Bearer \" dibuang")
	}
	// A key is one line: anything after the first newline is paste noise.
	if i := strings.IndexAny(key, "\r\n\t "); i > 0 {
		key = key[:i]
		notes = append(notes, "spasi/baris baru di ujung dibuang")
	}
	return key, compact(notes)
}

// KeyWarning explains an obvious mismatch between a key and its provider.
func KeyWarning(p Provider, key string) string {
	want, label := ExpectedKeyPrefix(p)
	if want == "" || key == "" || strings.HasPrefix(key, "$") {
		return ""
	}
	if strings.HasPrefix(key, want) {
		return ""
	}
	// Name the vendor the key probably belongs to, when it is recognisable.
	if owner := keyOwner(key); owner != "" && owner != label {
		return "Key ini tampak milik " + owner + ", bukan " + label + " (yang diawali " + want + "…)."
	}
	return "Format key tidak seperti " + label + " biasanya (diawali " + want + "…). Tetap disimpan, tapi kemungkinan ditolak (401)."
}

func keyOwner(key string) string {
	switch {
	case strings.HasPrefix(key, "sk-or-v1-"):
		return "OpenRouter"
	case strings.HasPrefix(key, "sk-ant-"):
		return "Anthropic"
	case strings.HasPrefix(key, "gsk_"):
		return "Groq"
	case strings.HasPrefix(key, "sk-proj-"), strings.HasPrefix(key, "sk-svcacct-"):
		return "OpenAI"
	case strings.HasPrefix(key, "sk-"):
		return "OpenAI/DeepSeek"
	case strings.HasPrefix(key, "AIza"):
		return "Google"
	case strings.HasPrefix(key, "xai-"):
		return "xAI"
	}
	return ""
}

func compact(in []string) []string {
	out := in[:0]
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
