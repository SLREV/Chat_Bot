package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/provider"
)

var errBadInput = errors.New("bad input")

// providerInput is what the UI may send. A key field that is absent or empty
// means "keep the stored one", so editing a model never wipes a credential.
type providerInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	BaseURL      string   `json:"baseUrl"`
	APIKey       *string  `json:"apiKey"`
	DefaultModel string   `json:"defaultModel"`
	Models       []string `json:"models"`
	MaxTokens    *int     `json:"maxTokens"`
	Fallback     *int     `json:"fallback"`
	Enabled      *bool    `json:"enabled"`
}

// providerView adds live reachability, so the UI can say "Ollama belum jalan"
// before a person sends a message into a dead endpoint.
type providerView struct {
	config.MaskedProvider
	Reachable bool   `json:"reachable"`
	ProbeNote string `json:"probeNote,omitempty"`
	KeyState  string `json:"keyState"` // ok | env_empty | missing | not_needed
	Active    bool   `json:"active"`   // can this provider authenticate right now?
	EnvName   string `json:"envName,omitempty"`
}

func (s *Server) providerViews(r *http.Request) []providerView {
	ctx, cancel := context.WithTimeout(r.Context(), 1200*time.Millisecond)
	defer cancel()
	out := []providerView{}
	for _, m := range s.provs.Masked() {
		v := providerView{MaskedProvider: m}
		pr, _ := s.provs.ByID(m.ID)
		v.KeyState, v.EnvName = config.KeyStatus(pr)
		v.Active = pr.Active()
		ok, note := provider.Probe(ctx, pr)
		v.Reachable, v.ProbeNote = ok, note
		if note == "ok" || note == "remote" {
			v.ProbeNote = ""
		}
		out = append(out, v)
	}
	return out
}

// freeModels reports, per provider, which models cost nothing and whether the
// provider is usable right now — the practical answer to "which free model?".
func (s *Server) freeModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
	defer cancel()
	out := []map[string]any{}
	for _, pr := range s.provs.List() {
		kind := provider.FreeKindOf(pr)
		if kind == provider.FreePaid {
			continue
		}
		models := provider.FreeModels(pr)
		reachable, note := provider.Probe(ctx, pr)
		state, env := config.KeyStatus(pr)
		out = append(out, map[string]any{
			"id": pr.ID, "name": pr.Name, "kind": kind, "freeKind": kind,
			"models": models, "modelCount": len(models),
			"keyState": state, "envName": env, "active": pr.Active(),
			"reachable": reachable, "probeNote": note,
			"baseUrl": pr.BaseURL,
		})
	}
	writeJSON(w, 200, map[string]any{"providers": out})
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.providerViews(r))
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var in providerInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "nama provider wajib diisi")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = "openai"
	}
	if kind != "openai" && kind != "anthropic" {
		writeErr(w, 400, "kind harus \"openai\" atau \"anthropic\"")
		return
	}
	base := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if base == "" {
		writeErr(w, 400, "base URL wajib diisi (mis. https://api.openai.com/v1)")
		return
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		writeErr(w, 400, "base URL harus diawali http:// atau https://")
		return
	}

	pr := config.Provider{
		ID:           config.NewProviderID(),
		Name:         strings.TrimSpace(in.Name),
		Kind:         kind,
		BaseURL:      base,
		DefaultModel: strings.TrimSpace(in.DefaultModel),
		Models:       cleanModels(in.Models),
		MaxTokens:    clampMaxTokens(in.MaxTokens, 0),
		ModelsSource: "seed", // hand-typed lists are a guess until synced
		Enabled:      true,
	}
	notes := []string{}
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		key, n := config.SanitizeKey(*in.APIKey)
		pr.APIKey = key
		notes = append(notes, n...)
	}
	if in.Enabled != nil {
		pr.Enabled = *in.Enabled
	}
	if err := s.provs.Upsert(pr); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	// The masked projection never contains the key itself.
	writeJSON(w, 201, withNotes(config.Mask(pr), pr, notes))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in providerInput
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	var notes []string
	var badBaseURL bool
	var baseURLSwapNeedsKey bool
	cur, ok, err := s.provs.Update(id, func(cur *config.Provider) error {
		if strings.TrimSpace(in.Name) != "" {
			cur.Name = strings.TrimSpace(in.Name)
		}
		if k := strings.ToLower(strings.TrimSpace(in.Kind)); k == "openai" || k == "anthropic" {
			cur.Kind = k
		}
		if b := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"); b != "" {
			if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
				badBaseURL = true
				return errBadInput
			}
			// Prevent exfiltrating a stored key by pointing an existing provider
			// at a different host without explicitly re-entering or clearing the key.
			if b != strings.TrimRight(cur.BaseURL, "/") && cur.APIKey != "" &&
				(in.APIKey == nil || strings.TrimSpace(*in.APIKey) == "") {
				baseURLSwapNeedsKey = true
				return errBadInput
			}
			cur.BaseURL = b
		}
		if in.DefaultModel != "" {
			cur.DefaultModel = strings.TrimSpace(in.DefaultModel)
		}
		if in.Models != nil {
			cur.Models = cleanModels(in.Models)
			cur.ModelsSource = "custom"
		}
		if in.MaxTokens != nil {
			cur.MaxTokens = clampMaxTokens(in.MaxTokens, cur.MaxTokens)
		}
		if in.Fallback != nil {
			n := *in.Fallback
			if n < 0 {
				n = 0
			}
			if n > 99 {
				n = 99
			}
			cur.Fallback = n
		}
		if in.Enabled != nil {
			cur.Enabled = *in.Enabled
		}
		if in.APIKey != nil {
			raw := strings.TrimSpace(*in.APIKey)
			switch {
			case raw == "__clear__":
				cur.APIKey = ""
			case raw != "":
				key, n := config.SanitizeKey(raw)
				notes = append(notes, n...)
				cur.APIKey = key
				// A pasted key must not linger in the browser: nothing echoes it.
			}
		}
		return nil
	})
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	if badBaseURL {
		writeErr(w, 400, "base URL harus diawali http:// atau https://")
		return
	}
	if baseURLSwapNeedsKey {
		writeErr(w, 400, "mengubah Base URL pada provider yang sudah memiliki key harus menyertakan API key baru atau \"__clear__\"")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, withNotes(config.Mask(cur), cur, notes))
}

// clearProviderKey removes only the credential. The provider itself — its name,
// endpoint, models and fallback slot — stays configured. This is what people
// mean by "hapus API key".
// useFreeModel points a bot (or the provider default) at the first free model
// the provider actually offers — one click from "all my balances are empty".
func (s *Server) useFreeModel(w http.ResponseWriter, r *http.Request) {
	pr, ok := s.provs.ByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	var in struct {
		BotID string `json:"botId"`
		Model string `json:"model"`
	}
	_ = readJSON(r, &in)

	free := provider.FreeModels(pr)
	if len(free) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false,
			"error": pr.Name + " tidak punya model gratis yang diketahui — klik ⟳ Sinkron dulu, atau pakai Ollama/Groq/Gemini"})
		return
	}
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = free[0]
	} else if !containsFold(free, model) {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "model " + model + " bukan model gratis di " + pr.Name, "models": free})
		return
	}

	if in.BotID != "" {
		b, ok := s.cfg.UpdateBot(in.BotID, func(b *config.Bot) {
			b.Engine = pr.ID
			b.Model = model
		})
		if !ok {
			writeErr(w, 404, "bot tidak ditemukan")
			return
		}
		if err := s.cfg.Save(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.hub.broadcast(event{Type: "state"})
		writeJSON(w, 200, map[string]any{"ok": true, "botId": b.ID, "engine": pr.ID, "model": model})
		return
	}

	if _, ok, err := s.provs.Update(pr.ID, func(p *config.Provider) error {
		p.DefaultModel = model
		return nil
	}); !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	} else if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]any{"ok": true, "provider": pr.ID, "defaultModel": model})
}

func (s *Server) clearProviderKey(w http.ResponseWriter, r *http.Request) {
	pr, ok, err := s.provs.Update(r.PathValue("id"), func(p *config.Provider) error {
		p.APIKey = ""
		return nil
	})
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]any{
		"id": pr.ID, "name": pr.Name, "keyCleared": true,
		"hint": "API key dihapus; provider tetap ada. Tempel key baru kapan saja.",
	})
}

// restoreProviders brings back built-in providers that were deleted by mistake.
func (s *Server) restoreProviders(w http.ResponseWriter, r *http.Request) {
	restored := s.provs.RestoreDefaults()
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]any{"ok": true, "restored": restored, "count": len(restored)})
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.provs.Delete(r.PathValue("id")); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

// testProvider sends one tiny completion so a person learns whether the key,
// the endpoint and the model name are right before wiring a bot to it.
func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
	pr, ok := s.provs.ByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	_ = readJSON(r, &in)
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = pr.DefaultModel
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	start := time.Now()
	res, err := provider.Stream(ctx, provider.Request{
		Provider: pr,
		System:   "Reply with exactly: ok",
		Model:    model,
		MaxTokens: func() int {
			if pr.MaxTokens > 0 && pr.MaxTokens < 16 {
				return pr.MaxTokens
			}
			return 16
		}(),
		Messages: []provider.Message{{Role: "user", Content: "ping"}},
		Timeout:  40 * time.Second,
	})
	ms := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "ms": ms, "model": model})
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":           true,
		"model":        model,
		"reply":        truncateRunes(res.Text, 120),
		"ms":           ms,
		"promptTokens": res.PromptTokens,
		"outputTokens": res.OutputTokens,
	})
}

// syncProviderModels replaces the (possibly stale) built-in list with what the
// vendor actually serves, and records that the list is now authoritative.
func (s *Server) syncProviderModels(w http.ResponseWriter, r *http.Request) {
	pr, ok := s.provs.ByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx, pr)
	if err != nil || len(models) == 0 {
		msg := "endpoint tidak mengembalikan daftar model"
		if err != nil {
			msg = err.Error()
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg, "models": pr.Models})
		return
	}
	models = provider.SortForChat(models)
	updated, ok, err := s.provs.Update(pr.ID, func(p *config.Provider) error {
		p.Models = models
		p.ModelsSource = "live"
		if p.DefaultModel == "" || !containsFold(models, p.DefaultModel) {
			p.DefaultModel = models[0]
		}
		return nil
	})
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]any{"ok": true, "models": updated.Models, "count": len(updated.Models), "defaultModel": updated.DefaultModel})
}

// checkModelAnswers verifies one model name against the vendor.
func (s *Server) checkModel(w http.ResponseWriter, r *http.Request) {
	pr, ok := s.provs.ByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	var in struct {
		Model string `json:"model"`
	}
	_ = readJSON(r, &in)
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = pr.DefaultModel
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	start := time.Now()
	if err := provider.VerifyModel(ctx, pr, model); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "model": model, "error": err.Error(), "ms": time.Since(start).Milliseconds()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "model": model, "ms": time.Since(start).Milliseconds()})
}

func containsFold(list []string, want string) bool {
	for _, m := range list {
		if strings.EqualFold(m, want) {
			return true
		}
	}
	return false
}

func (s *Server) providerModels(w http.ResponseWriter, r *http.Request) {
	pr, ok := s.provs.ByID(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "provider tidak ditemukan")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx, pr)
	if err != nil && len(models) == 0 {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": models, "live": err == nil})
}

// withNotes attaches "what we cleaned up" and "this key looks wrong" to a
// provider response without ever echoing the key.
func withNotes(m config.MaskedProvider, p config.Provider, notes []string) map[string]any {
	out := map[string]any{
		"id": m.ID, "name": m.Name, "kind": m.Kind, "baseUrl": m.BaseURL,
		"defaultModel": m.DefaultModel, "models": m.Models, "modelsSource": m.ModelsSource,
		"maxTokens": m.MaxTokens, "fallback": m.Fallback, "enabled": m.Enabled,
		"hasKey": m.HasKey, "keyHint": m.KeyHint, "keyFromEnv": m.KeyFromEnv,
	}
	if len(notes) > 0 {
		out["cleaned"] = notes
	}
	if w := config.KeyWarning(p, p.APIKey); w != "" {
		out["keyWarning"] = w
	}
	return out
}

// clampMaxTokens keeps the output budget sane: 0 means "use the build default",
// anything else is bounded so a typo cannot ask for a million tokens.
func clampMaxTokens(in *int, fallback int) int {
	if in == nil {
		return fallback
	}
	n := *in
	switch {
	case n <= 0:
		return 0
	case n < 64:
		return 64
	case n > 32000:
		return 32000
	}
	return n
}

func cleanModels(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
