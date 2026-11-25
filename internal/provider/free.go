package provider

import (
	"sort"
	"strings"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// How a vendor gives away capacity.
const (
	FreeAll    = "all"    // every model is free (local runtime)
	FreeTier   = "tier"   // the whole catalogue runs on a free tier (rate-limited)
	FreeSuffix = "suffix" // only ids with a marker are free (:free)
	FreePaid   = ""       // no free capacity
)

// FreeKindOf reports how this provider gives away capacity. It is a fact about
// the vendor, kept next to the vendor table so the UI can label models honestly.
func FreeKindOf(p config.Provider) string {
	switch {
	case isLocal(p):
		return FreeAll
	case p.ID == "groq" || strings.Contains(strings.ToLower(p.BaseURL), "groq.com"):
		return FreeTier
	case p.ID == "gemini" || strings.Contains(strings.ToLower(p.BaseURL), "generativelanguage"):
		return FreeTier
	case isOpenRouterLike(p):
		return FreeSuffix
	}
	return FreePaid
}

// FreeModels lists the models at this provider that cost nothing, using the
// provider's own model list (synced from the API when possible). It never
// invents names: an empty list means "we do not know", not "there are none".
func FreeModels(p config.Provider) []string {
	models := p.Models
	if len(models) == 0 {
		if seed, ok := seedModelsFor(p.ID); ok {
			models = seed
		}
	}
	switch FreeKindOf(p) {
	case FreeAll, FreeTier:
		out := make([]string, 0, len(models))
		out = append(out, models...)
		return out
	case FreeSuffix:
		out := []string{}
		for _, m := range models {
			if strings.HasSuffix(strings.ToLower(m), ":free") {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// IsFree reports whether one model id is free at this provider.
func IsFree(p config.Provider, model string) bool {
	switch FreeKindOf(p) {
	case FreeAll, FreeTier:
		return true
	case FreeSuffix:
		return strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), ":free")
	}
	return false
}

func seedModelsFor(id string) ([]string, bool) {
	for _, s := range config.SeedProvidersForFree() {
		if s.ID == id {
			return s.Models, true
		}
	}
	return nil, false
}

func isOpenRouterLike(p config.Provider) bool {
	return p.ID == "openrouter" || strings.Contains(strings.ToLower(p.BaseURL), "openrouter")
}

// ChatRank scores how likely a model id is a normal chat completion model that
// a fresh account can call: 0 is best. Vendors list speech, embedding and guard
// models in the same endpoint, and picking one of those just fails again.
func ChatRank(id string) int {
	m := strings.ToLower(id)
	switch {
	case strings.Contains(m, "whisper"), strings.Contains(m, "tts"),
		strings.Contains(m, "embed"), strings.Contains(m, "rerank"),
		strings.Contains(m, "guard"), strings.Contains(m, "moderation"),
		strings.Contains(m, "distil"), strings.Contains(m, "prompt-guard"):
		return 9
	case strings.Contains(m, "preview"), strings.Contains(m, "exp"),
		strings.Contains(m, "experimental"), strings.Contains(m, "research"):
		return 3
	case strings.Contains(m, "instant"), strings.Contains(m, "flash"),
		strings.Contains(m, "mini"), strings.Contains(m, "8b"),
		strings.Contains(m, "gpt-oss-20b"), strings.Contains(m, "small"):
		return 1
	}
	return 2
}

// SortForChat orders a model list so the most usable chat models come first,
// keeping the vendor's own order inside each rank.
func SortForChat(models []string) []string {
	out := append([]string(nil), models...)
	rank := make(map[string]int, len(out))
	for _, m := range out {
		rank[m] = ChatRank(m)
	}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}
