// Package server exposes the JSON API, the SSE event stream and the embedded UI.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/agent"
	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

//go:embed all:web
var webFS embed.FS

// Server holds everything one running instance needs.
type Server struct {
	cfg     *config.Settings
	provs   *config.Providers
	st      *store.Store
	mgr     *agent.Manager
	hub     *hub
	desk    *deskProc
	bind    string
	port    int
	mu      sync.Mutex
	httpSrv *http.Server
	token   string
}

// New builds a server. When bound to a non-loopback address, a random auth
// token is generated automatically unless overridden via SetAuthToken.
func New(cfg *config.Settings, provs *config.Providers, st *store.Store, mgr *agent.Manager, bind string, port int) *Server {
	s := &Server{cfg: cfg, provs: provs, st: st, mgr: mgr, hub: newHub(), desk: &deskProc{}, bind: bind, port: port}
	if !isLoopbackBind(bind) {
		s.token = GenerateToken()
	}
	mgr.SetDeltaHook(func(botID, delta string) {
		s.hub.broadcast(event{Type: "delta", BotID: botID, Text: delta})
	})
	return s
}

// SetAuthToken configures the bearer/session token required by /api/* routes.
func (s *Server) SetAuthToken(token string) {
	s.mu.Lock()
	s.token = strings.TrimSpace(token)
	s.mu.Unlock()
}

// AuthToken returns the active access token (empty when disabled).
func (s *Server) AuthToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// GenerateToken returns a cryptographically random 32-byte hex token.
func GenerateToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func isLoopbackBind(bind string) bool {
	b := strings.ToLower(strings.TrimSpace(bind))
	if b == "127.0.0.1" || b == "::1" || b == "localhost" {
		return true
	}
	if ip := net.ParseIP(b); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.Handle("POST /api/auth", s.protect(http.HandlerFunc(s.auth), false))

	api := func(h http.HandlerFunc) http.Handler {
		return s.protect(h, true)
	}

	mux.Handle("GET /api/state", api(s.state))
	mux.Handle("GET /api/bots", api(s.listBots))
	mux.Handle("POST /api/bots", api(s.createBot))
	mux.Handle("PATCH /api/bots/{id}", api(s.updateBot))
	mux.Handle("DELETE /api/bots/{id}", api(s.deleteBot))
	mux.Handle("GET /api/bots/{id}/messages", api(s.messages))
	mux.Handle("POST /api/bots/{id}/messages", api(s.send))
	mux.Handle("POST /api/bots/{id}/interrupt", api(s.interrupt))
	mux.Handle("DELETE /api/bots/{id}/messages", api(s.clearThread))
	mux.Handle("GET /api/engines", api(s.engines))
	mux.Handle("GET /api/settings", api(s.getSettings))
	mux.Handle("PATCH /api/settings", api(s.patchSettings))
	mux.Handle("GET /api/events", api(s.events))

	// Providers (API-key models)
	mux.Handle("GET /api/providers", api(s.listProviders))
	mux.Handle("POST /api/providers", api(s.createProvider))
	mux.Handle("PATCH /api/providers/{id}", api(s.updateProvider))
	mux.Handle("DELETE /api/providers/{id}", api(s.deleteProvider))
	mux.Handle("DELETE /api/providers/{id}/key", api(s.clearProviderKey))
	mux.Handle("POST /api/providers/restore", api(s.restoreProviders))
	mux.Handle("GET /api/free-models", api(s.freeModels))
	mux.Handle("POST /api/providers/{id}/use-free", api(s.useFreeModel))
	mux.Handle("POST /api/providers/{id}/test", api(s.testProvider))
	mux.Handle("GET /api/providers/{id}/models", api(s.providerModels))
	mux.Handle("POST /api/providers/{id}/models/sync", api(s.syncProviderModels))
	mux.Handle("POST /api/providers/{id}/check-model", api(s.checkModel))

	// 6-Agent Trading Desk controls (start/stop/1-pass/status)
	mux.Handle("GET /api/desk/status", api(s.deskStatus))
	mux.Handle("POST /api/desk/start", api(s.deskStart))
	mux.Handle("POST /api/desk/stop", api(s.deskStop))
	mux.Handle("POST /api/desk/pass", api(s.deskPass))

	ui, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Printf("embedded UI missing: %v", err)
	} else {
		files := http.FileServer(http.FS(ui))
		mux.Handle("GET /", s.uiHandler(files))
	}
	return noCache(mux)
}

// Listen starts the HTTP server and blocks until it stops.
func (s *Server) Listen() error {
	addr := net.JoinHostPort(s.bind, strconv.Itoa(s.port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: the SSE stream is long-lived by design.
	}
	s.mu.Lock()
	s.httpSrv = srv
	s.mu.Unlock()
	return srv.Serve(ln)
}

// Shutdown gracefully stops the HTTP listener and any managed desk dashboard process.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopManagedDesk()
	s.mu.Lock()
	srv := s.httpSrv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// URL is the address the UI is served on.
func (s *Server) URL() string {
	host := s.bind
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(s.port)))
}

// LoginURL returns URL with the bootstrap token query parameter when auth is enabled.
func (s *Server) LoginURL() string {
	tok := s.AuthToken()
	if tok == "" {
		return s.URL()
	}
	return s.URL() + "/?token=" + url.QueryEscape(tok)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) uiHandler(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if qtok := r.URL.Query().Get("token"); qtok != "" {
				if tok := s.AuthToken(); tok != "" && tokenEqual(qtok, tok) {
					setSessionCookie(w, r, tok)
					http.Redirect(w, r, "/", http.StatusFound)
					return
				}
			}
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) protect(next http.Handler, requireAuth bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sfs := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))); sfs == "cross-site" {
			writeErr(w, 403, "permintaan lintas-situs ditolak")
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
				writeErr(w, 403, "origin tidak diizinkan")
				return
			}
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
			if ct := r.Header.Get("Content-Type"); ct != "" || r.ContentLength != 0 {
				mt, _, err := mime.ParseMediaType(ct)
				if err != nil || mt != "application/json" {
					writeErr(w, 415, "Content-Type harus application/json")
					return
				}
			}
		}
		if requireAuth {
			if tok := s.AuthToken(); tok != "" {
				if !tokenEqual(requestToken(r), tok) {
					w.Header().Set("WWW-Authenticate", `Bearer realm="OpenMausBot"`)
					writeErr(w, 401, "autentikasi diperlukan — sertakan token akses")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	tok := s.AuthToken()
	if tok == "" {
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !tokenEqual(strings.TrimSpace(in.Token), tok) {
		writeErr(w, 401, "token akses salah")
		return
	}
	setSessionCookie(w, r, tok)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func requestToken(r *http.Request) string {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if t := strings.TrimSpace(r.Header.Get("X-OMB-Token")); t != "" {
		return t
	}
	if c, err := r.Cookie("omb_session"); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return ""
}

func tokenEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, tok string) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     "omb_session",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

// --- handlers ---------------------------------------------------------------

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok":      true,
		"service": "openmausbot-go",
		"version": Version,
		"uptime":  time.Since(startedAt).Round(time.Second).String(),
	})
}

// Version is stamped by the build (see cmd/omb/main.go).
var Version = "dev"

var startedAt = time.Now()

type botView struct {
	config.Bot
	Busy       bool   `json:"busy"`
	Route      string `json:"route"` // "api" | "cli" | "demo"
	EngineName string `json:"engineName"`
	EngineOK   bool   `json:"engineOk"`
}

func (s *Server) botViews() []botView {
	out := []botView{}
	for _, b := range s.cfg.Snapshot() {
		v := botView{Bot: b, Busy: s.mgr.Busy(b.ID)}
		switch {
		case s.hasProvider(b.Engine):
			pr, _ := s.provs.ByID(b.Engine)
			v.Route = "api"
			v.EngineName = pr.Name
			// A key that points at an empty env var is not a working credential,
			// so the sidebar must not paint this bot green.
			v.EngineOK = pr.Enabled && pr.Active()
		default:
			if e, ok := s.cfg.EngineByID(b.Engine); ok {
				v.Route = "cli"
				v.EngineName = e.Name
				v.EngineOK = e.Enabled && (e.CLI == "" || lookPath(e.CLI))
				if e.CLI == "" {
					v.Route = "demo"
				}
			}
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) hasProvider(id string) bool {
	_, ok := s.provs.ByID(id)
	return ok
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"version":   Version,
		"bots":      s.botViews(),
		"engines":   s.cfg.EnginesSnapshot(),
		"providers": s.providerViews(r),
		"threads":   s.st.Threads(),
		"settings":  s.publicSettings(),
		"serverUrl": s.URL(),
	})
}

func (s *Server) publicSettings() map[string]any {
	bindAddr, port, autoApprove, timeoutSec := s.cfg.Runtime()
	return map[string]any{
		"bindAddr":    bindAddr,
		"port":        port,
		"autoApprove": autoApprove,
		"timeoutSec":  timeoutSec,
		"dataDir":     config.DataDir(),
	}
}

func (s *Server) listBots(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.botViews()) }

func (s *Server) createBot(w http.ResponseWriter, r *http.Request) {
	var in config.Bot
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	in.ID = config.NewID()
	if strings.TrimSpace(in.Name) == "" {
		in.Name = "Bot baru"
	}
	if in.Engine == "" {
		in.Engine = "echo"
	}
	if in.Model == "" {
		switch in.Engine {
		case "claude":
			in.Model = "sonnet"
		case "echo":
			in.Model = "demo"
		}
	}
	if in.Emoji == "" {
		in.Emoji = "🤖"
	}
	if in.Avatar == "" {
		in.Avatar = s.cfg.NextAvatar()
	}
	if in.Color == "" {
		in.Color = "#1084fe"
	}
	in.CreatedAt = time.Now()
	s.cfg.UpsertBot(in)
	if err := s.cfg.Save(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 201, in)
}

func (s *Server) updateBot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch struct {
		Name        *string `json:"name"`
		Emoji       *string `json:"emoji"`
		Avatar      *string `json:"avatar"`
		Color       *string `json:"color"`
		Engine      *string `json:"engine"`
		Model       *string `json:"model"`
		Personality *string `json:"personality"`
		Workdir     *string `json:"workdir"`
	}
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	found, ok := s.cfg.UpdateBot(id, func(b *config.Bot) {
		if patch.Name != nil && *patch.Name != "" {
			b.Name = *patch.Name
		}
		if patch.Emoji != nil && *patch.Emoji != "" {
			b.Emoji = *patch.Emoji
		}
		// Avatar may be cleared on purpose, so an explicit "__none__" is honoured.
		if patch.Avatar != nil {
			switch *patch.Avatar {
			case "":
				// leave unchanged
			case "__none__":
				b.Avatar = ""
			default:
				b.Avatar = *patch.Avatar
			}
		}
		if patch.Color != nil && *patch.Color != "" {
			b.Color = *patch.Color
		}
		if patch.Engine != nil && *patch.Engine != "" {
			b.Engine = *patch.Engine
		}
		if patch.Model != nil {
			if *patch.Model == "__none__" {
				b.Model = ""
			} else {
				b.Model = *patch.Model
			}
		}
		if patch.Personality != nil {
			b.Personality = *patch.Personality
		}
		if patch.Workdir != nil {
			b.Workdir = *patch.Workdir
		}
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
	writeJSON(w, 200, found)
}

func (s *Server) deleteBot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.cfg.DeleteBot(id) {
		writeErr(w, 404, "bot tidak ditemukan")
		return
	}
	_ = s.cfg.Save()
	_ = s.st.Clear(id)
	s.hub.broadcast(event{Type: "state"})
	writeJSON(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	msgs, err := s.st.History(id, limit, offset)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs, "busy": s.mgr.Busy(id)})
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		writeErr(w, 400, "pesan kosong")
		return
	}
	if err := s.mgr.Tell(id, in.Text); err != nil {
		code := 400
		if err == agent.ErrBusy {
			code = 429
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, 202, map[string]bool{"queued": true})
}

func (s *Server) interrupt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, 200, map[string]bool{"stopped": s.mgr.Interrupt(id)})
}

func (s *Server) clearThread(w http.ResponseWriter, r *http.Request) {
	_ = s.st.Clear(r.PathValue("id"))
	writeJSON(w, 200, map[string]bool{"cleared": true})
}

func (s *Server) engines(w http.ResponseWriter, _ *http.Request) {
	type ev struct {
		config.Engine
		Found bool `json:"found"`
	}
	out := []ev{}
	for _, e := range s.cfg.EnginesSnapshot() {
		out = append(out, ev{Engine: e, Found: e.CLI == "" || lookPath(e.CLI)})
	}
	writeJSON(w, 200, out)
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.publicSettings())
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AutoApprove *bool `json:"autoApprove"`
		TimeoutSec  *int  `json:"timeoutSec"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	s.cfg.UpdateRuntime(in.AutoApprove, in.TimeoutSec)
	if err := s.cfg.Save(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.publicSettings())
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// --- SSE --------------------------------------------------------------------

type event struct {
	Type  string         `json:"type"`
	BotID string         `json:"botId,omitempty"`
	Text  string         `json:"text,omitempty"`
	Msg   *store.Message `json:"message,omitempty"`
}

type hub struct {
	add  chan chan event
	rm   chan chan event
	send chan event
}

func newHub() *hub {
	h := &hub{
		add:  make(chan chan event, 8),
		rm:   make(chan chan event, 8),
		send: make(chan event, 256),
	}
	go h.run()
	return h
}

func (h *hub) broadcast(e event) {
	select {
	case h.send <- e:
	default: // slow consumers are dropped rather than stalling a turn
	}
}

func (h *hub) run() {
	clients := map[chan event]struct{}{}
	for {
		select {
		case c := <-h.add:
			clients[c] = struct{}{}
		case c := <-h.rm:
			if _, ok := clients[c]; ok {
				delete(clients, c)
				close(c)
			}
		case e := <-h.send:
			for c := range clients {
				select {
				case c <- e:
				default:
				}
			}
		}
	}
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming tidak didukung")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": openmausbot-go stream\n\n")
	fl.Flush()

	ch := make(chan event, 32)
	s.hub.add <- ch
	defer func() { s.hub.rm <- ch }()

	// A single goroutine also mirrors finished transcript messages onto the
	// same stream so the UI never needs a second connection.
	msgs, unsub := s.st.Subscribe()
	defer unsub()

	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			writeEvent(w, e)
			fl.Flush()
		case m, ok := <-msgs:
			if !ok {
				return
			}
			writeEvent(w, event{Type: "message", BotID: m.Thread, Msg: &m})
			fl.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func writeEvent(w http.ResponseWriter, e event) {
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", raw)
}
