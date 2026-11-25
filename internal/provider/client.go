// Package provider talks HTTP to an AI API with a key: either an
// OpenAI-compatible /chat/completions endpoint or Anthropic's /messages.
//
// Both stream over SSE and are normalised into the same Chat* callbacks the
// rest of the app uses, so a bot does not care which vendor answers it.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// Message is one turn of conversation history sent to the model.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// Request is a single completion.
type Request struct {
	Provider  config.Provider
	System    string
	Model     string
	Messages  []Message
	MaxTokens int
	Timeout   time.Duration
	OnChunk   func(string)
	// OnModelsStale is called when the vendor says a model does not exist. It
	// should refresh the list from the vendor's API and return the new models;
	// the first one is retried automatically.
	OnModelsStale func(missing string) []string
}

// Result carries the finished text and token usage.
type Result struct {
	Text         string
	PromptTokens int
	OutputTokens int
	Millis       int64
	// ReducedTo is set when a 402 forced a smaller max_tokens on retry.
	ReducedTo int
	// RecoveredModel is set when a retired model was replaced by one the vendor
	// does list; RecoveredFrom is the model that had disappeared.
	RecoveredModel string
	RecoveredFrom  string
}

// ErrNoKey is returned when a provider has no usable credential.
var ErrNoKey = errors.New("api key tidak tersedia")

// CreditsError is a 402: the account cannot afford the requested max_tokens.
// It is separate from a generic API error so the caller can retry smaller
// instead of showing the person a wall.
type CreditsError struct {
	Provider   string
	Requested  int
	Affordable int    // >0 when the vendor said how many tokens the balance covers
	Kind       string // "budget" (request too big) | "balance" (account empty)
	Message    string
	TopUp      string // where to add credit at this vendor
	Free       string // a free way to keep working, "" when none
	FreeHint   string
}

func (e *CreditsError) Error() string {
	var base string
	if e.Kind == "balance" {
		base = e.Provider + ": saldo akun habis"
	} else {
		base = fmt.Sprintf("%s: saldo lebih kecil dari max_tokens=%d", e.Provider, e.Requested)
		if e.Affordable > 0 {
			base += fmt.Sprintf(" (mampu ~%d token)", e.Affordable)
		}
	}
	if e.Message != "" && !strings.Contains(strings.ToLower(e.Message), "insufficient") {
		base += " — " + e.Message
	}
	return base
}

// ModelNotFoundError is a 404 that names a model: the provider does not serve
// it (retired, renamed, or not on this plan). The fix is to refresh the model
// list, not to change the key.
type ModelNotFoundError struct {
	Provider string
	Model    string
	Message  string
}

func (e *ModelNotFoundError) Error() string {
	msg := fmt.Sprintf("%s tidak menyediakan model %q", e.Provider, e.Model)
	if e.Message != "" {
		msg += " — " + e.Message
	}
	return msg
}

// ErrUnreachable marks a network-level failure (connection refused, DNS, TLS),
// so callers can tell "the endpoint is down" apart from "the model said no".
var ErrUnreachable = errors.New("endpoint tidak bisa dihubungi")

var httpClient = &http.Client{
	// No global timeout: the per-request context owns the deadline so a long
	// stream is not cut at 30 s the way a default client would.
	Transport: &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     60 * time.Second,
	},
}

// Stream runs one completion and streams text through OnChunk.
func Stream(ctx context.Context, r Request) (Result, error) {
	start := time.Now()
	if r.Timeout <= 0 {
		r.Timeout = 5 * time.Minute
	}
	if r.MaxTokens <= 0 {
		r.MaxTokens = 2048
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	switch strings.ToLower(r.Provider.Kind) {
	case "openai", "", "anthropic":
	default:
		return Result{}, fmt.Errorf("kind provider tidak dikenal: %q (pakai \"openai\" atau \"anthropic\")", r.Provider.Kind)
	}

	key, err := r.Provider.Secret()
	if err != nil && !isLocal(r.Provider) {
		if state, env := config.KeyStatus(r.Provider); state == config.KeyEnvEmpty {
			return Result{}, fmt.Errorf("%w: %s membaca key dari environment variable $%s, dan variabel itu kosong di proses omb ini. Dua jalan keluar: (1) tempel key di Settings → Provider → %s → Simpan, atau (2) jalankan `export %s=sk-…` lalu restart omb (export di terminal lain tidak terbaca)",
				ErrNoKey, r.Provider.Name, env, r.Provider.Name, env)
		}
		return Result{}, fmt.Errorf("%w: %v. Tempel key di Settings → Provider → %s → Simpan, atau `omb key %s -`",
			ErrNoKey, err, r.Provider.Name, r.Provider.ID)
	}

	call := func(rr Request) (Result, error) {
		// Resolve per call: a retry may carry a different model than the first try.
		m := rr.Model
		if m == "" {
			m = rr.Provider.DefaultModel
		}
		switch strings.ToLower(rr.Provider.Kind) {
		case "anthropic":
			return streamAnthropic(ctx, rr, m, key)
		case "openai", "":
			return streamOpenAI(ctx, rr, m, key)
		}
		return Result{}, fmt.Errorf("kind provider tidak dikenal: %q (pakai \"openai\" atau \"anthropic\")", rr.Provider.Kind)
	}

	res, err := call(r)
	// A 404 naming a model means the vendor retired it (Groq does this often).
	// Refresh the list from the API and retry once with a model that exists, so
	// a stale built-in list never blocks a conversation.
	var mnf *ModelNotFoundError
	if errors.As(err, &mnf) && r.OnModelsStale != nil {
		var nextMissing *ModelNotFoundError
		if fresh := r.OnModelsStale(mnf.Model); len(fresh) > 0 {
			// Try the refreshed list in order: the first entries may be chat
			// models on another plan, so one refusal must not end the attempt.
			for _, candidate := range fresh {
				if strings.EqualFold(candidate, mnf.Model) {
					continue // the vendor just said this one does not exist
				}
				next := r
				next.Model = candidate
				res2, err2 := call(next)
				if err2 == nil {
					res2.Millis = time.Since(start).Milliseconds()
					res2.RecoveredModel = candidate
					res2.RecoveredFrom = mnf.Model
					return res2, nil
				}
				if !errors.As(err2, &nextMissing) {
					return res2, err2 // a different failure: stop, report it
				}
			}
		}
	}

	// A 402 tells us exactly what the balance can afford. Retrying once with
	// that budget turns "your bot is broken" into "your bot answers, shorter".
	var ce *CreditsError
	if errors.As(err, &ce) && ce.Affordable > 0 {
		retry := r
		budget := ce.Affordable
		if budget > 512 {
			budget = 512 // leave room for the prompt, which also costs tokens
		}
		if budget < 64 {
			budget = 64
		}
		if budget < r.MaxTokens {
			retry.MaxTokens = budget
			if res2, err2 := call(retry); err2 == nil {
				res2.Millis = time.Since(start).Milliseconds()
				res2.ReducedTo = budget
				return res2, nil
			}
		}
	}
	res.Millis = time.Since(start).Milliseconds()
	return res, err
}

// VerifyModel sends the smallest possible request with a specific model, so the
// UI can answer "does 'DeepSeek V4.1 Flash' exist here?" with the vendor's own
// words instead of a built-in guess.
func VerifyModel(ctx context.Context, p config.Provider, model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("nama model kosong")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := Stream(ctx, Request{
		Provider:  p,
		Model:     model,
		System:    "Reply with exactly: ok",
		Messages:  []Message{{Role: "user", Content: "ping"}},
		MaxTokens: 8,
		Timeout:   25 * time.Second,
	})
	return err
}

// ListModels asks the provider what it serves; falls back to the configured
// list when the endpoint does not support it.
func ListModels(ctx context.Context, p config.Provider) ([]string, error) {
	if strings.ToLower(p.Kind) == "anthropic" {
		return p.Models, nil // Anthropic has no cheap public list endpoint
	}
	key, _ := p.Secret()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return p.Models, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return p.Models, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return p.Models, fmt.Errorf("models: HTTP %d", res.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return p.Models, err
	}
	out := []string{}
	for _, m := range body.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	if len(out) == 0 {
		return p.Models, nil
	}
	return out, nil
}

// Probe reports whether a provider is reachable right now. Remote providers are
// reported as fine without a network call: only a local endpoint can be down
// because the person forgot to start it.
func Probe(ctx context.Context, p config.Provider) (bool, string) {
	return probeLocal(ctx, p)
}

// --- OpenAI-compatible ------------------------------------------------------

func streamOpenAI(ctx context.Context, r Request, model, key string) (Result, error) {
	payload := map[string]any{
		"model":      model,
		"stream":     true,
		"max_tokens": r.MaxTokens,
		"messages":   openAIMessages(r),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(r.Provider.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return Result{}, describeNetErr(err, r.Provider)
	}
	defer res.Body.Close()
	if res.StatusCode == 402 {
		return Result{}, creditsError(res, r.Provider, r.MaxTokens)
	}
	if res.StatusCode != 200 {
		return Result{}, apiError(res, r.Provider, model)
	}

	var (
		out   strings.Builder
		usage struct {
			PromptTokens int `json:"prompt_tokens"`
			Completion   int `json:"completion_tokens"`
		}
	)
	err = scanSSE(res.Body, func(data string) error {
		if data == "[DONE]" {
			return nil
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens int `json:"prompt_tokens"`
				Completion   int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil // ignore keep-alives and non-JSON frames
		}
		if ev.Usage != nil {
			usage.PromptTokens = ev.Usage.PromptTokens
			usage.Completion = ev.Usage.Completion
		}
		for _, c := range ev.Choices {
			text := c.Delta.Content
			if text == "" {
				text = c.Message.Content
			}
			if text != "" {
				out.WriteString(text)
				if r.OnChunk != nil {
					r.OnChunk(text)
				}
			}
		}
		return nil
	})
	return Result{Text: strings.TrimSpace(out.String()), PromptTokens: usage.PromptTokens, OutputTokens: usage.Completion}, err
}

func openAIMessages(r Request) []map[string]string {
	msgs := []map[string]string{}
	if r.System != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": r.System})
	}
	for _, m := range r.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	return msgs
}

// --- Anthropic --------------------------------------------------------------

func streamAnthropic(ctx context.Context, r Request, model, key string) (Result, error) {
	msgs := []map[string]string{}
	for _, m := range r.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": r.MaxTokens,
		"stream":     true,
		"messages":   msgs,
	}
	if r.System != "" {
		payload["system"] = r.System
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	url := strings.TrimRight(r.Provider.BaseURL, "/") + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key != "" {
		req.Header.Set("x-api-key", key)
	}

	res, err := httpClient.Do(req)
	if err != nil {
		return Result{}, describeNetErr(err, r.Provider)
	}
	defer res.Body.Close()
	if res.StatusCode == 402 {
		return Result{}, creditsError(res, r.Provider, r.MaxTokens)
	}
	if res.StatusCode != 200 {
		return Result{}, apiError(res, r.Provider, model)
	}

	var (
		out   strings.Builder
		usage struct{ In, Out int }
	)
	err = scanSSE(res.Body, func(data string) error {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		switch ev.Type {
		case "content_block_delta":
			if ev.Delta.Text != "" {
				out.WriteString(ev.Delta.Text)
				if r.OnChunk != nil {
					r.OnChunk(ev.Delta.Text)
				}
			}
		case "message_start":
			usage.In = ev.Message.Usage.InputTokens
		case "message_delta":
			if ev.Usage.OutputTokens > 0 {
				usage.Out = ev.Usage.OutputTokens
			}
		}
		return nil
	})
	return Result{Text: strings.TrimSpace(out.String()), PromptTokens: usage.In, OutputTokens: usage.Out}, err
}

// --- shared plumbing --------------------------------------------------------

// scanSSE reads "data: …" frames from a streaming response.
func scanSSE(body io.Reader, fn func(data string) error) error {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if err := fn(data); err != nil {
			return err
		}
	}
	return sc.Err()
}

// apiError turns a provider error body into something a person can act on.
func apiError(res *http.Response, p config.Provider, model string) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	msg := extractMessage(raw)
	switch res.StatusCode {
	case 401, 403:
		return fmt.Errorf("%s menolak API key (HTTP %d)%s%s", p.Name, res.StatusCode, suffix(msg), keyFixHint(p))
	case 402:
		return fmt.Errorf("%s: pembayaran/kredit diperlukan (HTTP 402)%s", p.Name, suffix(msg))
	case 429:
		return fmt.Errorf("%s: kena rate limit / kuota habis (HTTP 429)%s", p.Name, suffix(msg))
	case 404:
		// Distinguish "this model is gone" from "wrong URL": the first is fixed by
		// refreshing the model list, the second by fixing the endpoint.
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "model") &&
			(strings.Contains(lower, "does not exist") || strings.Contains(lower, "not found") ||
				strings.Contains(lower, "do not have access") || strings.Contains(lower, "unknown model")) {
			name := modelFromMessage(msg)
			if name == "" {
				name = model
			}
			return &ModelNotFoundError{Provider: p.Name, Model: name, Message: msg}
		}
		return fmt.Errorf("%s: endpoint atau model tidak ditemukan (HTTP 404)%s — cek Base URL dan nama model", p.Name, suffix(msg))
	case 400:
		return fmt.Errorf("%s: permintaan ditolak (HTTP 400)%s", p.Name, suffix(msg))
	default:
		return fmt.Errorf("%s: HTTP %d%s", p.Name, res.StatusCode, suffix(msg))
	}
}

// keyFixHint names the exact fix for the vendor, which is what a 401 needs.
func keyFixHint(p config.Provider) string {
	where := "Settings → Provider"
	docs := map[string]string{
		"openrouter": "https://openrouter.ai/keys",
		"anthropic":  "https://console.anthropic.com/settings/keys",
		"groq":       "https://console.groq.com/keys",
		"deepseek":   "https://platform.deepseek.com/api_keys",
		"api.openai": "https://platform.openai.com/api-keys",
		"openai":     "https://platform.openai.com/api-keys",
	}
	base := strings.ToLower(p.BaseURL)
	for needle, url := range docs {
		if strings.Contains(base, needle) || strings.EqualFold(p.ID, needle) {
			return " — buat/ganti key di " + url + ", lalu perbarui di " + where
		}
	}
	return " — cek key di " + where
}

// creditsError turns a 402 body into a typed error, extracting the affordable
// token count the vendors put in the message.
func creditsError(res *http.Response, p config.Provider, requested int) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	msg := extractMessage(raw)
	// Keep the readable part short: providers paste URLs and sales copy.
	msg = truncateRunes(msg, 220)
	affordable := parseAffordable(msg)
	lower := strings.ToLower(msg)
	kind := "balance"
	switch {
	case affordable > 0:
		kind = "budget"
	case strings.Contains(lower, "fewer max_tokens"), strings.Contains(lower, "more credits"):
		kind = "budget" // vendor complains about the request size, not the wallet
	}
	v := vendorOf(p)
	return &CreditsError{
		Provider: p.Name, Requested: requested, Affordable: affordable, Kind: kind,
		Message: msg, TopUp: v.TopUp, Free: v.Free, FreeHint: v.FreeHint,
	}
}

// "…but can only afford 980." is the number we want; a bare "up to 2048 tokens"
// in the same sentence is what we must NOT pick up.
var affordableRe = regexp.MustCompile(`(?i)(?:can only afford|only afford|afford(?:ing)?)\D{0,20}(\d{2,7})`)

func parseAffordable(msg string) int {
	for _, re := range []*regexp.Regexp{
		affordableRe,
		regexp.MustCompile(`(?i)afford[^0-9]{0,20}(\d{2,7})\s*tokens?`),
	} {
		m := re.FindStringSubmatch(msg)
		if len(m) < 2 {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// modelFromMessage pulls the model id out of a vendor error like
// "The model `llama-3.3-70b-versatile` does not exist".
func modelFromMessage(msg string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile("`([^`]{2,80})`"),                          // Groq: `llama-…`
		regexp.MustCompile(`(?i)model\s+"([^"]{2,80})"`),              // quoted after "model"
		regexp.MustCompile(`(?i)model\s+'([^']{2,80})'`),              // single quotes
		regexp.MustCompile(`(?i)model\s+([A-Za-z0-9][\w.:/-]{1,79})`), // bare token
	} {
		if m := re.FindStringSubmatch(msg); len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func suffix(msg string) string {
	if msg == "" {
		return ""
	}
	return " — " + truncateRunes(msg, 300)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// extractMessage pulls the readable part out of an OpenAI- or Anthropic-style
// error body without ever echoing a key back.
func extractMessage(raw []byte) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err == nil {
		if body.Error.Message != "" {
			return body.Error.Message
		}
		if body.Message != "" {
			return body.Message
		}
	}
	s := strings.TrimSpace(string(raw))
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	if strings.HasPrefix(s, "<") {
		return "" // HTML error page (proxy / captive portal): not useful
	}
	return s
}

func describeNetErr(err error, p config.Provider) error {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s tidak menjawab sebelum batas waktu: %w", p.Name, err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("permintaan dibatalkan: %w", err)
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("tidak bisa menemukan %s — cek koneksi internet atau Base URL", p.BaseURL)
	case strings.Contains(msg, "connection refused"):
		if isLocal(p) {
			return fmt.Errorf("%w: %s — %s", ErrUnreachable, p.BaseURL, localHint(p))
		}
		return fmt.Errorf("%w: koneksi ke %s ditolak (host hidup, tapi tidak ada yang menerima di port itu)", ErrUnreachable, p.BaseURL)
	case strings.Contains(msg, "certificate"):
		return fmt.Errorf("sertifikat TLS %s tidak valid", p.BaseURL)
	}
	return fmt.Errorf("%s: %v", p.Name, err)
}

// localHint gives the exact command that fixes the common case: Ollama (or a
// compatible server) is not running on this machine yet.
func localHint(p config.Provider) string {
	base := strings.ToLower(p.BaseURL)
	port := portOf(p.BaseURL)
	switch {
	case strings.Contains(base, "11434") || strings.Contains(strings.ToLower(p.Name), "ollama"):
		return "Ollama belum jalan. Jalankan `ollama serve` (atau buka aplikasi Ollama), " +
			"lalu `ollama pull <model>` sekali. Cek cepat: `curl " + p.BaseURL + "/models`"
	case port != "":
		return "tidak ada server yang mendengarkan di port " + port +
			" — jalankan server modelnya, atau ubah Base URL di Settings → Provider"
	}
	return "pastikan server model lokalnya sudah jalan"
}

func portOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Port() == "" {
		return ""
	}
	return u.Port()
}

// probeLocal reports whether a local endpoint answers at all, so the UI can
// warn before a person types into a chat that cannot work.
func probeLocal(ctx context.Context, p config.Provider) (bool, string) {
	if !isLocal(p) {
		return true, "remote"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return false, err.Error()
	}
	if key, kerr := p.Secret(); kerr == nil && key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return false, describeNetErr(err, p).Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		return false, fmt.Sprintf("endpoint menjawab HTTP %d", res.StatusCode)
	}
	return true, "ok"
}

func isLocal(p config.Provider) bool {
	return config.IsLocalURL(p.BaseURL)
}
