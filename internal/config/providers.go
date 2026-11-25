package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Provider is an AI service reached over HTTPS with an API key — no CLI needed.
//
// Keys live in their own file (providers.json, 0600) and never in config.json,
// so sharing a config dump never leaks a key.
type Provider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"` // "openai" (chat/completions) | "anthropic" (messages)
	BaseURL      string   `json:"baseUrl"`
	APIKey       string   `json:"apiKey,omitempty"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models,omitempty"`
	// ModelsSource records where Models came from: "seed" (built in, may be
	// stale) or "live" (fetched from the vendor's /models). Strictness of model
	// validation depends on it.
	ModelsSource string `json:"modelsSource,omitempty"`
	MaxTokens    int    `json:"maxTokens,omitempty"` // 0 = default (2048)
	// Position in the fallback chain used when this provider cannot answer
	// (no credit, no key, unreachable). Lower runs first; 0 = not in the chain.
	Fallback int  `json:"fallback,omitempty"`
	Enabled  bool `json:"enabled"`
}

// IsEnvKey reports whether the stored value is an indirection to an env var
// (e.g. "$OPENAI_API_KEY") rather than the key itself.
func (p Provider) IsEnvKey() bool { return strings.HasPrefix(p.APIKey, "$") }

// HasKeyNeeded reports whether this provider needs a credential to work.
// A local endpoint (Ollama and friends) does not.
func (p Provider) HasKeyNeeded() bool {
	base := strings.ToLower(p.BaseURL)
	return !(strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") ||
		strings.Contains(base, "0.0.0.0") || strings.HasPrefix(base, "http://"))
}

// MaskedProvider is the only shape that may cross the HTTP boundary.
type MaskedProvider struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	BaseURL      string   `json:"baseUrl"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models"`
	ModelsSource string   `json:"modelsSource"`
	MaxTokens    int      `json:"maxTokens"`
	Fallback     int      `json:"fallback"`
	Enabled      bool     `json:"enabled"`
	HasKey       bool     `json:"hasKey"`
	KeyHint      string   `json:"keyHint"`
	KeyFromEnv   bool     `json:"keyFromEnv"`
}

// Providers is the provider registry, persisted to providers.json.
type Providers struct {
	mu   sync.Mutex
	list []Provider
	path string
}

// ProvidersPath is where provider records (and keys) are stored.
func ProvidersPath() string { return filepath.Join(DataDir(), "providers.json") }

// LoadProviders reads providers.json, seeding known providers on first run.
func LoadProviders() (*Providers, error) {
	p := &Providers{path: ProvidersPath()}
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		p.list = seedProviders()
		return p, p.Save()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &p.list); err != nil {
		return nil, err
	}
	for i := range p.list {
		if p.list[i].ModelsSource == "" {
			p.list[i].ModelsSource = "seed"
		}
		if p.list[i].ID == "" {
			p.list[i].ID = NewID()
		}
		if p.list[i].Kind == "" {
			p.list[i].Kind = "openai"
		}
	}
	p.mergeSeedModels()
	return p, nil
}

// Save writes providers.json atomically with 0600 permissions.
func (p *Providers) Save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, err := json.MarshalIndent(p.list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

// mergeSeedModels adds model names that newer builds know about to providers the
// person has not touched. Keys and default models are never modified.
func (p *Providers) mergeSeedModels() {
	seeds := seedProviders()
	changed := false
	for i := range p.list {
		for _, seed := range seeds {
			if seed.ID != p.list[i].ID {
				continue
			}
			have := map[string]bool{}
			for _, m := range p.list[i].Models {
				have[m] = true
			}
			// Only extend an untouched model list: if the person curated their
			// own, leave it alone.
			if len(p.list[i].Models) > 0 && len(p.list[i].Models) != len(seed.Models) {
				continue
			}
			for _, m := range seed.Models {
				if !have[m] {
					p.list[i].Models = append(p.list[i].Models, m)
					changed = true
				}
			}
		}
	}
	if changed {
		_ = p.Save()
	}
}

// RestoreDefaults re-adds built-in providers that are missing (deleted by
// mistake) without touching providers that already exist, and never overwrites a
// stored key. It returns the names it brought back.
func (p *Providers) RestoreDefaults() []string {
	p.mu.Lock()
	have := map[string]bool{}
	for _, pr := range p.list {
		have[pr.ID] = true
	}
	restored := []string{}
	for _, seed := range seedProviders() {
		if have[seed.ID] {
			continue
		}
		p.list = append(p.list, seed)
		restored = append(restored, seed.Name)
	}
	p.mu.Unlock()
	_ = p.Save()
	return restored
}

// Chain returns the fallback candidates in order: enabled, usable right now
// (key present or local), and not the provider that just failed.
func (p *Providers) Chain(excludeID string) []Provider {
	all := p.List()
	out := []Provider{}
	for _, pr := range all {
		if pr.ID == excludeID || !pr.Enabled || pr.Fallback <= 0 {
			continue
		}
		if !pr.Active() {
			continue
		}
		out = append(out, pr)
	}
	// Insertion sort by chain position; the list is tiny.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Fallback < out[j-1].Fallback; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// List returns a copy of every provider record (including keys — internal use).
func (p *Providers) List() []Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Provider, len(p.list))
	copy(out, p.list)
	return out
}

// Masked returns the safe projection served to the UI.
func (p *Providers) Masked() []MaskedProvider {
	out := []MaskedProvider{}
	for _, pr := range p.List() {
		out = append(out, Mask(pr))
	}
	return out
}

// Mask projects one provider without its secret.
func Mask(p Provider) MaskedProvider {
	m := MaskedProvider{
		ID:           p.ID,
		Name:         p.Name,
		Kind:         p.Kind,
		BaseURL:      p.BaseURL,
		DefaultModel: p.DefaultModel,
		Models:       append([]string{}, p.Models...),
		ModelsSource: p.ModelsSource,
		MaxTokens:    p.MaxTokens,
		Fallback:     p.Fallback,
		Enabled:      p.Enabled,
		HasKey:       p.APIKey != "",
		KeyFromEnv:   p.IsEnvKey(),
	}
	if m.Models == nil {
		m.Models = []string{}
	}
	m.KeyHint = hint(p.APIKey)
	return m
}

func hint(key string) string {
	if key == "" {
		return ""
	}
	if strings.HasPrefix(key, "$") {
		return key // an env var name is not a secret
	}
	if len(key) <= 8 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}

// ModelsAreLive reports whether the model list came from the vendor's API. Only
// then is it safe to say "this model does not exist".
func (p Provider) ModelsAreLive() bool { return p.ModelsSource == "live" }

// ByID looks a provider up, keys included.
func (p *Providers) ByID(id string) (Provider, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pr := range p.list {
		if pr.ID == id {
			return pr, true
		}
	}
	return Provider{}, false
}

// Upsert stores or replaces a provider and saves.
func (p *Providers) Upsert(pr Provider) error {
	p.mu.Lock()
	replaced := false
	for i := range p.list {
		if p.list[i].ID == pr.ID {
			p.list[i] = pr
			replaced = true
			break
		}
	}
	if !replaced {
		p.list = append(p.list, pr)
	}
	p.mu.Unlock()
	return p.Save()
}

// Delete removes a provider by id.
func (p *Providers) Delete(id string) error {
	p.mu.Lock()
	for i := range p.list {
		if p.list[i].ID == id {
			p.list = append(p.list[:i], p.list[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	return p.Save()
}

// Secret resolves the key to send on the wire: a literal key, or the value of
// the environment variable it points at. Env indirection keeps plaintext keys
// off disk for people who prefer that.
func (p Provider) Secret() (string, error) {
	if p.APIKey == "" {
		return "", errors.New("API key belum diisi untuk " + p.Name)
	}
	if p.IsEnvKey() {
		name := strings.TrimPrefix(p.APIKey, "$")
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			return "", errors.New("environment variable " + name + " kosong (dipakai oleh " + p.Name + ")")
		}
		return v, nil
	}
	return p.APIKey, nil
}

// seedProviders lists well-known endpoints so a person only has to paste a key.
// Model names are editable in Settings; they are starting points, not promises.
func seedProviders() []Provider {
	return []Provider{
		{ID: "openai", Name: "OpenAI", Kind: "openai", BaseURL: "https://api.openai.com/v1",
			APIKey: "$OPENAI_API_KEY", DefaultModel: "gpt-4o-mini",
			Models: []string{"gpt-4o-mini", "gpt-4o", "gpt-4.1"}, Enabled: true, Fallback: 30},
		{ID: "anthropic-api", Name: "Anthropic (API key)", Kind: "anthropic", BaseURL: "https://api.anthropic.com/v1",
			APIKey: "$ANTHROPIC_API_KEY", DefaultModel: "claude-sonnet-4-5",
			Models: []string{"claude-sonnet-4-5", "claude-haiku-4-5"}, Enabled: true, Fallback: 50},
		{ID: "openrouter", Name: "OpenRouter", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1",
			APIKey: "$OPENROUTER_API_KEY", DefaultModel: "openai/gpt-4o-mini",
			// The :free variants work with an empty credit balance, which is what a
			// 402 means. They are rate-limited, but they answer.
			Models: []string{"openai/gpt-4o-mini", "anthropic/claude-sonnet-4.5", "google/gemini-2.5-flash",
				"deepseek/deepseek-chat-v3.1:free", "meta-llama/llama-3.3-70b-instruct:free",
				"google/gemini-2.0-flash-exp:free"}, Enabled: true, Fallback: 20},
		{ID: "groq", Name: "Groq", Kind: "openai", BaseURL: "https://api.groq.com/openai/v1",
			APIKey: "$GROQ_API_KEY", DefaultModel: "llama-3.1-8b-instant",
			Models:  []string{"llama-3.1-8b-instant", "openai/gpt-oss-20b", "qwen/qwen3-32b", "llama-3.3-70b-versatile"},
			Enabled: true, Fallback: 10},
		// Gemini's OpenAI-compatible endpoint has a free tier — the practical way
		// to keep working when every paid balance is empty.
		{ID: "gemini", Name: "Google Gemini (tier gratis)", Kind: "openai",
			BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
			APIKey:  "$GEMINI_API_KEY", DefaultModel: "gemini-2.0-flash",
			Models: []string{"gemini-2.0-flash", "gemini-2.0-flash-lite", "gemini-1.5-flash"}, Enabled: true, Fallback: 5},
		{ID: "deepseek", Name: "DeepSeek", Kind: "openai", BaseURL: "https://api.deepseek.com/v1",
			APIKey: "$DEEPSEEK_API_KEY", DefaultModel: "deepseek-chat",
			Models: []string{"deepseek-chat", "deepseek-reasoner"}, Enabled: true, Fallback: 40},
		// Ollama is the last resort: free, local, and works with no account. It is
		// last in the chain because it needs the server to be running.
		{ID: "ollama", Name: "Ollama (lokal, tanpa key)", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1",
			APIKey: "", DefaultModel: "llama3.2",
			Models: []string{"llama3.2", "llama3.2:1b", "qwen2.5", "mistral"}, Enabled: true, Fallback: 90},
	}
}

// SeedProvidersForFree exposes the built-in provider table for read-only use by
// other packages (free-model detection needs the model names).
func SeedProvidersForFree() []Provider { return seedProviders() }
