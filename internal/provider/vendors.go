package provider

import (
	"strings"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// vendorMeta is what a person needs when a provider says "no money": where to
// top up, and whether the vendor has a free way to keep working.
type vendorMeta struct {
	Label    string
	TopUp    string
	Free     string // a working free path at this vendor, "" when there is none
	FreeHint string // how to use it
}

var vendors = []struct {
	match func(config.Provider) bool
	meta  vendorMeta
}{
	{func(p config.Provider) bool {
		return p.ID == "openrouter" || strings.Contains(strings.ToLower(p.BaseURL), "openrouter")
	}, vendorMeta{"OpenRouter", "https://openrouter.ai/settings/credits",
		"model :free", "pilih model berakhiran `:free` (mis. deepseek/deepseek-chat-v3.1:free) — jalan dengan saldo 0"}},

	{func(p config.Provider) bool {
		return p.ID == "deepseek" || strings.Contains(strings.ToLower(p.BaseURL), "deepseek")
	}, vendorMeta{"DeepSeek", "https://platform.deepseek.com/top_up", "",
		"DeepSeek tidak punya tier gratis — saldo harus diisi, atau pindah provider"}},

	{func(p config.Provider) bool {
		return p.ID == "openai" || strings.Contains(strings.ToLower(p.BaseURL), "api.openai.com")
	}, vendorMeta{"OpenAI", "https://platform.openai.com/settings/organization/billing", "",
		"OpenAI tidak punya tier gratis; model termurah: gpt-4o-mini"}},

	{func(p config.Provider) bool {
		return strings.Contains(strings.ToLower(p.BaseURL), "anthropic")
	}, vendorMeta{"Anthropic", "https://console.anthropic.com/settings/billing", "",
		"Anthropic tidak punya tier gratis lewat API key"}},

	{func(p config.Provider) bool {
		return p.ID == "groq" || strings.Contains(strings.ToLower(p.BaseURL), "groq.com")
	}, vendorMeta{"Groq", "https://console.groq.com/settings/billing",
		"tier gratis", "Groq punya kuota gratis harian — pakai key gratis dari console.groq.com/keys"}},

	{func(p config.Provider) bool {
		return p.ID == "gemini" || strings.Contains(strings.ToLower(p.BaseURL), "generativelanguage")
	}, vendorMeta{"Google Gemini", "https://aistudio.google.com/apikey",
		"tier gratis", "Gemini punya tier gratis — buat key di aistudio.google.com/apikey, tanpa kartu kredit"}},
}

func vendorOf(p config.Provider) vendorMeta {
	for _, v := range vendors {
		if v.match(p) {
			return v.meta
		}
	}
	return vendorMeta{Label: p.Name}
}
