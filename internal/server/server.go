// Package server exposes the JSON API, the SSE event stream and the embedded UI.
package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/agent"
	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

//go:embed all:web
var webFS embed.FS

// Server holds everything one running instance needs.
type Server struct {
	cfg   *config.Settings
	provs *config.Providers
	st    *store.Store
	mgr   *agent.Manager
	hub   *hub
	bind  string
	port  int
}

// New builds a server.
func New(cfg *config.Settings, provs *config.Providers, st *store.Store, mgr *agent.Manager, bind string, port int) *Server {
	s := &Server{cfg: cfg, provs: provs, st: st, mgr: mgr, hub: newHub(), bind: bind, port: port}
	mgr.SetDeltaHook(func(botID, delta string) {
		s.hub.broadcast(event{Type: "delta", BotID: botID, Text: delta})
	})
	return s
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/bots", s.listBots)
	mux.HandleFunc("POST /api/bots", s.createBot)
	mux.HandleFunc("PATCH /api/bots/{id}", s.updateBot)
	mux.HandleFunc("DELETE /api/bots/{id}", s.deleteBot)
	mux.HandleFunc("GET /api/bots/{id}/messages", s.messages)
	mux.HandleFunc("POST /api/bots/{id}/messages", s.send)
	mux.HandleFunc("POST /api/bots/{id}/interrupt", s.interrupt)
	mux.HandleFunc("DELETE /api/bots/{id}/messages", s.clearThread)
	mux.HandleFunc("GET /api/engines", s.engines)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PATCH /api/settings", s.patchSettings)
	mux.HandleFunc("GET /api/events", s.events)

	// Providers (API-key models)
	mux.HandleFunc("GET /api/providers", s.listProviders)
	mux.HandleFunc("POST /api/providers", s.createProvider)
	mux.HandleFunc("PATCH /api/providers/{id}", s.updateProvider)
	mux.HandleFunc("DELETE /api/providers/{id}", s.deleteProvider)
	mux.HandleFunc("DELETE /api/providers/{id}/key", s.clearProviderKey)
	mux.HandleFunc("POST /api/providers/restore", s.restoreProviders)
	mux.HandleFunc("GET /api/free-models", s.freeModels)
	mux.HandleFunc("POST /api/providers/{id}/use-free", s.useFreeModel)
	mux.HandleFunc("POST /api/providers/{id}/test", s.testProvider)
	mux.HandleFunc("GET /api/providers/{id}/models", s.providerModels)
	mux.HandleFunc("POST /api/providers/{id}/models/sync", s.syncProviderModels)
	mux.HandleFunc("POST /api/providers/{id}/check-model", s.checkModel)

	ui, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Printf("embedded UI missing: %v", err)
	} else {
		files := http.FileServer(http.FS(ui))
		mux.Handle("GET /", noCache(files))
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
	return srv.Serve(ln)
}

// URL is the address the UI is served on.
func (s *Server) URL() string {
	host := s.bind
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(s.port)))
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
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
		"engines":   s.cfg.Engines,
		"providers": s.providerViews(r),
		"threads":   s.st.Threads(),
		"settings":  s.publicSettings(),
		"serverUrl": s.URL(),
	})
}

func (s *Server) publicSettings() map[string]any {
	return map[string]any{
		"bindAddr":    s.cfg.BindAddr,
		"port":        s.cfg.Port,
		"autoApprove": s.cfg.AutoApprove,
		"timeoutSec":  s.cfg.TimeoutSec,
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
		in.Model = "sonnet"
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
	var patch config.Bot
	if err := readJSON(r, &patch); err != nil {
		writeErr(w, 400, "body bukan JSON yang valid")
		return
	}
	var found config.Bot
	ok := false
	for _, b := range s.cfg.Snapshot() {
		if b.ID == id {
			found, ok = b, true
			break
		}
	}
	if !ok {
		writeErr(w, 404, "bot tidak ditemukan")
		return
	}
	if patch.Name != "" {
		found.Name = patch.Name
	}
	if patch.Emoji != "" {
		found.Emoji = patch.Emoji
	}
	// Avatar may be cleared on purpose, so an explicit "__none__" is honoured.
	switch patch.Avatar {
	case "":
		// leave unchanged
	case "__none__":
		found.Avatar = ""
	default:
		found.Avatar = patch.Avatar
	}
	if patch.Color != "" {
		found.Color = patch.Color
	}
	if patch.Engine != "" {
		found.Engine = patch.Engine
	}
	if patch.Model != "" {
		found.Model = patch.Model
	}
	if patch.Personality != "" {
		found.Personality = patch.Personality
	}
	if patch.Workdir != "" {
		found.Workdir = patch.Workdir
	}
	s.cfg.UpsertBot(found)
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
	for _, e := range s.cfg.Engines {
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
	if in.AutoApprove != nil {
		s.cfg.AutoApprove = *in.AutoApprove
	}
	if in.TimeoutSec != nil && *in.TimeoutSec > 0 {
		s.cfg.TimeoutSec = *in.TimeoutSec
	}
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
