// Package agent turns "user typed in a chat" into "model ran" and streams the
// answer back. One goroutine per bot: a second message while a turn is running
// queues instead of starting a second job, which is what keeps a 4 GB laptop
// from running three agents at once.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/engine"
	"github.com/milind-soni/openmausbot-go/internal/provider"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

// Manager owns the runner goroutines and the per-bot queues.
type Manager struct {
	cfg   *config.Settings
	provs *config.Providers
	store *store.Store

	mu        sync.Mutex
	queues    map[string]chan job
	running   map[string]bool
	cancel    map[string]context.CancelFunc
	deltaHook func(botID, delta string)
}

type job struct {
	prompt string
}

// NewManager wires a manager to settings, providers and a transcript store.
func NewManager(cfg *config.Settings, provs *config.Providers, st *store.Store) *Manager {
	return &Manager{
		cfg:     cfg,
		provs:   provs,
		store:   st,
		queues:  make(map[string]chan job),
		running: make(map[string]bool),
		cancel:  make(map[string]context.CancelFunc),
	}
}

// ErrBusy means the bot already has a full queue (bounded to 8).
var ErrBusy = errors.New("bot sedang sibuk; terlalu banyak pesan mengantre")

// Tell queues a user message for a bot and returns immediately. The answer
// arrives over the store's subscription as assistant messages.
func (m *Manager) Tell(botID, prompt string) error {
	bot, ok := m.bot(botID)
	if !ok {
		return errors.New("bot tidak ditemukan")
	}
	if _, err := m.store.Append(store.Message{Thread: bot.ID, Role: store.RoleUser, Text: prompt}); err != nil {
		return err
	}

	m.mu.Lock()
	q := m.queues[botID]
	if q == nil {
		q = make(chan job, 8)
		m.queues[botID] = q
	}
	m.mu.Unlock()

	select {
	case q <- job{prompt: prompt}:
	default:
		return ErrBusy
	}
	go m.ensureRunner(botID)
	return nil
}

// Interrupt cancels the running turn for a bot, if any.
func (m *Manager) Interrupt(botID string) bool {
	m.mu.Lock()
	cancel := m.cancel[botID]
	m.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// Busy reports whether a bot has a turn in flight.
func (m *Manager) Busy(botID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[botID]
}

func (m *Manager) bot(id string) (config.Bot, bool) {
	for _, b := range m.cfg.Snapshot() {
		if b.ID == id {
			return b, true
		}
	}
	return config.Bot{}, false
}

// ensureRunner starts the single consumer goroutine for a bot if it is absent.
func (m *Manager) ensureRunner(botID string) {
	m.mu.Lock()
	q := m.queues[botID]
	if q == nil {
		m.mu.Unlock()
		return
	}
	if m.running[botID] {
		m.mu.Unlock()
		return
	}
	m.running[botID] = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running[botID] = false
		delete(m.cancel, botID)
		m.mu.Unlock()
	}()

	for {
		var next job
		select {
		case next = <-q:
		default:
			return // queue drained: the next Tell restarts this goroutine
		}
		m.runTurn(botID, next)
	}
}

func (m *Manager) runTurn(botID string, j job) {
	bot, ok := m.bot(botID)
	if !ok {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel[botID] = cancel
	m.mu.Unlock()
	defer cancel()

	onChunk := func(s string) { m.streamDelta(botID, s) }

	req := engine.Request{
		Prompt:  j.prompt,
		System:  systemPrompt(bot),
		Model:   bot.Model,
		Workdir: bot.Workdir,
		Timeout: time.Duration(m.cfg.TimeoutSec) * time.Second,
		OnChunk: onChunk,
	}

	// An engine id that matches a configured provider means "HTTP + API key";
	// anything else spawns a CLI. That single rule keeps bots portable: switch
	// a bot from Claude CLI to an OpenAI key without touching its transcript.
	route := "cli"
	msgModel, msgNote := bot.Model, ""
	maxTokens := 0
	if pr, ok := m.provs.ByID(bot.Engine); ok {
		// A model left over from another provider (e.g. gpt-4o-mini on Ollama)
		// would 404 at the endpoint. Only a model the provider actually lists —
		// or its own default — is sent; otherwise the provider's default wins.
		req.Model = resolveModel(bot.Model, pr)
		maxTokens = pr.MaxTokens
		req.Provider = &pr
		req.History = m.history(bot.ID, pr)
		prID := pr.ID
		req.OnModelsStale = func(missing string) []string { return m.refreshModels(prID) }
		modelNote := ""
		if bot.Model != "" && req.Model != bot.Model {
			if strings.HasSuffix(strings.ToLower(bot.Model), ":free") && !isOpenRouter(pr) {
				modelNote = " (" + pr.Name + " tidak memakai akhiran :free, dipakai " + req.Model + ")"
			} else {
				modelNote = " (model " + bot.Model + " tidak ada di daftar API " + pr.Name + ", dipakai " + req.Model + ")"
			}
		}
		msgModel = req.Model
		msgNote = modelNote
		route = "api"
	} else if eng, ok := m.cfg.EngineByID(bot.Engine); ok {
		req.Engine = eng
	} else {
		req.Engine = config.Engine{ID: "echo", Name: "Demo (offline)"}
	}

	if maxTokens > 0 && req.Provider != nil {
		req.MaxTokens = maxTokens
	}

	res, err := engine.Run(ctx, req)

	// Fallback chain: when the chosen provider cannot answer for an account-level
	// reason (no credit, no key, server down), try the next usable provider so a
	// bot keeps working instead of dying on an empty wallet.
	fellBackTo := ""
	var failures []string
	if err != nil && req.Provider != nil && retryable(err) {
		failures = append(failures, shortReason(req.Provider.Name, err))
		for _, alt := range m.provs.Chain(req.Provider.ID) {
			altCopy := alt
			attempt := req
			attempt.Provider = &altCopy
			attempt.Model = resolveModel("", altCopy)
			attempt.MaxTokens = altCopy.MaxTokens
			altRes, altErr := engine.Run(ctx, attempt)
			if altErr == nil && altRes.Text != "" {
				res, err = altRes, nil
				fellBackTo = altCopy.Name
				msgModel = attempt.Model
				break
			}
			if altErr != nil {
				failures = append(failures, shortReason(altCopy.Name, altErr))
			}
		}
		// Every provider failed: the person needs to see all the reasons at
		// once, not just the first one.
		if err != nil && len(failures) > 1 {
			err = &chainError{reasons: failures}
		}
	}

	if res.RecoveredModel != "" {
		msgModel = res.RecoveredModel
		label := bot.Engine
		if req.Provider != nil && req.Provider.Name != "" {
			label = req.Provider.Name
		}
		msgNote += " (model " + res.RecoveredFrom + " sudah tidak tersedia di " + label + "; dipakai " + res.RecoveredModel + " — setelan bot ikut diperbarui)"
		if bot, ok := m.bot(botID); ok {
			bot.Model = res.RecoveredModel
			m.cfg.UpsertBot(bot)
			_ = m.cfg.Save()
		}
	}
	if fellBackTo != "" {
		msgNote += " (dialihkan ke " + fellBackTo + " karena provider utama tidak bisa menjawab)"
	}
	if res.ReducedTo > 0 {
		msgNote += " (kredit habis: jawaban dipendekkan ke " + itoa(res.ReducedTo) + " token)"
	}
	msg := store.Message{
		Thread: bot.ID,
		Role:   store.RoleAssistant,
		Text:   res.Text,
		Engine: route + ":" + bot.Engine,
		Model:  msgModel,
		Ms:     res.Millis,
	}
	if msgNote != "" {
		msg.Note = msgNote
	}
	if err != nil {
		msg.Err = err.Error()
		if msg.Text == "" {
			msg.Text = friendlyError(err, bot, m)
		}
	}
	if msg.Text == "" {
		msg.Text = "(tidak ada keluaran)"
	}
	_, _ = m.store.Append(msg)
}

// resolveModel picks a model the provider can actually serve.
//
// The rule that matters: only a model list fetched from the vendor's API is
// authoritative. The built-in lists are a convenience and go stale (a vendor
// renames a model, adds one, or calls it "V4.1 Flash" in its UI while the API id
// stays "deepseek-chat"), so when the list is still a guess the person's model
// is passed through untouched and the vendor is the judge.
func resolveModel(want string, pr config.Provider) string {
	want = strings.TrimSpace(want)
	if want == "" {
		return pr.DefaultModel
	}

	// A ":free" suffix is an OpenRouter convention. DeepSeek and friends reject
	// it, so never forward it to them even if it leaked into the configuration.
	if strings.HasSuffix(strings.ToLower(want), ":free") && !isOpenRouter(pr) {
		trimmed := want[:len(want)-len(":free")]
		for _, m := range pr.Models {
			if strings.EqualFold(m, trimmed) {
				return m
			}
		}
		return pr.DefaultModel
	}

	for _, m := range pr.Models {
		if strings.EqualFold(m, want) {
			return m // canonical spelling, even from a guessed list
		}
	}

	if !pr.ModelsAreLive() {
		// Still a guess: trust the person and let the vendor answer. Guessed
		// lists go stale — a bot using a brand-new model must not be quietly
		// switched to something else.
		return want
	}

	// The list came from the vendor's API: an unknown name really is unknown.
	return pr.DefaultModel
}

func isOpenRouter(pr config.Provider) bool {
	return pr.ID == "openrouter" || strings.Contains(strings.ToLower(pr.BaseURL), "openrouter")
}

// history builds the last few turns for a provider call. Long transcripts are
// trimmed (characters, not messages) so a big thread cannot blow the context
// window or the memory budget of a small laptop.
func (m *Manager) history(botID string, pr config.Provider) []provider.Message {
	const (
		maxTurns = 12
		maxChars = 12000
	)
	recent := m.store.Recent(botID)
	msgs := []provider.Message{}
	budget := maxChars
	for i := len(recent) - 1; i >= 0 && len(msgs) < maxTurns; i-- {
		msg := recent[i]
		if msg.Role != store.RoleUser && msg.Role != store.RoleAssistant {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if text == "" || msg.Err != "" {
			continue
		}
		if len(text) > budget {
			if budget < 500 {
				break
			}
			text = text[:budget]
		}
		budget -= len(text)
		// Prepending keeps chronological order without a second pass.
		msgs = append([]provider.Message{{Role: msg.Role, Content: text}}, msgs...)
	}
	// The just-sent user message is appended by engine.Run, so drop the copy
	// that is already in Recent to avoid sending it twice.
	if n := len(msgs); n > 0 && msgs[n-1].Role == store.RoleUser {
		msgs = msgs[:n-1]
	}
	_ = pr
	return msgs
}

// refreshModels asks the vendor for its current models, stores them (marked
// "live") and returns ids suitable for a retry. This is what keeps a retired
// model name from breaking a bot forever.
func (m *Manager) refreshModels(providerID string) []string {
	pr, ok := m.provs.ByID(providerID)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	models, err := provider.ListModels(ctx, pr)
	if err != nil || len(models) == 0 {
		return nil
	}
	pr.Models = provider.SortForChat(models)
	pr.ModelsSource = "live"
	models = pr.Models
	if pr.DefaultModel == "" || !containsFold(models, pr.DefaultModel) {
		pr.DefaultModel = models[0]
	}
	_ = m.provs.Upsert(pr)
	return models
}

func containsFold(list []string, want string) bool {
	for _, m := range list {
		if strings.EqualFold(m, want) {
			return true
		}
	}
	return false
}

// streamDelta forwards in-progress text to subscribers of the live stream.
func (m *Manager) streamDelta(botID, s string) {
	m.mu.Lock()
	h := m.deltaHook
	m.mu.Unlock()
	if h != nil {
		h(botID, s)
	}
}

// SetDeltaHook installs the live-stream callback used by the HTTP/SSE layer.
func (m *Manager) SetDeltaHook(fn func(botID, delta string)) {
	m.mu.Lock()
	m.deltaHook = fn
	m.mu.Unlock()
}

// creditsHelp writes the next step for THIS vendor: where to top up, and which
// free path exists (if any). One message, three concrete choices.
func creditsHelp(err error) string {
	var ce *provider.CreditsError
	errors.As(err, &ce)
	lines := []string{"💳 " + ce.Error()}
	if ce.Kind == "budget" {
		lines = append(lines, "Permintaan lebih besar dari saldo. Bot sudah mencoba ulang dengan budget lebih kecil; kalau masih gagal, turunkan `max_tokens` di Settings → Provider (mis. 512).")
	}
	if ce.Free != "" {
		lines = append(lines, "• Gratis: "+ce.Free+" — "+ce.FreeHint)
	}
	lines = append(lines, "• Isi saldo: "+ce.TopUp)
	lines = append(lines, "• Atau pindah ke yang ada tier gratis: Groq (console.groq.com/keys) atau Google Gemini (aistudio.google.com/apikey).")
	return strings.Join(lines, "\n")
}

// retryable reports whether another provider might succeed for this failure.
func retryable(err error) bool {
	var ce *provider.CreditsError
	return errors.As(err, &ce) ||
		errors.Is(err, provider.ErrNoKey) ||
		errors.Is(err, provider.ErrUnreachable)
}

func isCredits(err error) bool {
	var ce *provider.CreditsError
	return errors.As(err, &ce)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func systemPrompt(b config.Bot) string {
	base := b.Personality
	if base == "" {
		base = "You are " + b.Name + ", a helpful assistant."
	}
	return base + "\n\nAnswer in the language the person used. Be concise. " +
		"You are running inside OpenMausBot (Go build), a local-first chat app."
}

// chainError lists why every provider in the fallback chain failed.
type chainError struct{ reasons []string }

func (e *chainError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "semua provider gagal (%d dicoba):", len(e.reasons))
	for _, r := range e.reasons {
		b.WriteString("\n• " + r)
	}
	b.WriteString("\n\nBuka **Settings → 🎁 Model gratis** untuk memilih jalur yang masih hidup,")
	b.WriteString(" atau jalankan `omb check` di terminal — perintah itu mencoba setiap provider")
	b.WriteString(" dengan satu permintaan sungguhan dan menyarankan yang berhasil.")
	return b.String()
}

// shortReason turns one provider's failure into a short, actionable phrase.
func shortReason(name string, err error) string {
	msg := err.Error()
	var mnf *provider.ModelNotFoundError
	switch {
	case errors.As(err, &mnf):
		return name + ": model `" + mnf.Model + "` tidak ada di akun ini"
	case errors.Is(err, provider.ErrNoKey):
		return name + ": API key belum ada"
	case isCredits(err):
		return name + ": saldo habis"
	case errors.Is(err, provider.ErrUnreachable):
		return name + ": tidak bisa dihubungi (server mati / Base URL salah)"
	}
	if len(msg) > 120 {
		msg = msg[:120] + "…"
	}
	return name + ": " + msg
}

func isModelMissing(err error) bool {
	var mnf *provider.ModelNotFoundError
	return errors.As(err, &mnf)
}

// modelMissingHelp never says "cek Base URL dan nama model": it names the model,
// shows what the vendor actually offers, and fixes it when asked.
func modelMissingHelp(err error, bot config.Bot, m *Manager) string {
	var mnf *provider.ModelNotFoundError
	_ = errors.As(err, &mnf)
	name := mnf.Provider
	missing := mnf.Model
	if missing == "" {
		missing = bot.Model
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧩 %s tidak menyediakan model `%s`.", name, missing)
	fmt.Fprintf(&b, "\n\nBukan soal API key — mengganti key tidak akan mengubah apa pun.")

	if pr, ok := m.provs.ByID(bot.Engine); ok {
		live := []string{}
		if pr.ModelsAreLive() {
			live = pr.Models
		}
		if len(live) == 0 {
			free := provider.FreeModels(pr)
			_ = free
			if fresh := m.refreshModels(pr.ID); len(fresh) > 0 {
				live = fresh
			}
		}
		if len(live) > 0 {
			top := live
			if len(top) > 5 {
				top = top[:5]
			}
			fmt.Fprintf(&b, "\n\nModel yang benar-benar ada di %s: %s", name, "`"+strings.Join(top, "`, `")+"`")
			b.WriteString("\n\nSaya sudah memperbarui daftar provider ini — klik **⟳ Sinkron** untuk melihat semuanya,")
			b.WriteString(" atau **Settings → 🎁 Model gratis** untuk memilih satu dengan sekali klik.")
			return b.String()
		}
		fmt.Fprintf(&b, "\n\nUntuk melihat daftar model aslinya: `omb providers` lalu `curl` ke `/models` provider ini.")
		b.WriteString("\nBuka **Settings → Provider → ⟳ Sinkron** supaya daftar bawaan diganti daftar dari API.")
		return b.String()
	}
	b.WriteString("\n\nBuka **Settings → Provider → ⟳ Sinkron** lalu pilih model dari daftar itu.")
	return b.String()
}

func friendlyError(err error, bot config.Bot, m *Manager) string {
	switch {
	case errors.Is(err, provider.ErrUnreachable):
		return "🔌 " + err.Error()
	case isCredits(err):
		return creditsHelp(err)
	case errors.Is(err, provider.ErrNoKey):
		return "🔑 " + err.Error() + "\n\nBuka **Settings → Provider**, tempel API key untuk provider ini (atau pakai tombol “Uji key”)."
	case errors.Is(err, engine.ErrTimeout):
		return "⏱️ Turn dihentikan karena melewati batas waktu. Naikkan `timeoutSec` di Settings."
	case errors.Is(err, context.Canceled):
		return "⏹️ Turn dihentikan."
	case isModelMissing(err):
		return modelMissingHelp(err, bot, m)
	}
	return "⚠️ " + bot.Name + ": " + err.Error()
}
