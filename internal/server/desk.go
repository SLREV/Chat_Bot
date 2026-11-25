package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type deskProc struct {
	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

type deskStateView struct {
	Available   bool    `json:"available"`
	Running     bool    `json:"running"`
	Managed     bool    `json:"managed"`
	Port        int     `json:"port"`
	URL         string  `json:"url,omitempty"`
	Mode        string  `json:"mode"`
	Stopped     bool    `json:"stopped"`
	Paper       bool    `json:"paper"`
	LiveArmed   bool    `json:"liveArmed"`
	EquityUSD   float64 `json:"equityUsd"`
	PnlUSD      float64 `json:"pnlUsd"`
	PnlPct      float64 `json:"pnlPct"`
	Scanned     int     `json:"scanned"`
	Traded      int     `json:"traded"`
	OpenCount   int     `json:"openCount"`
	DeskDir     string  `json:"deskDir,omitempty"`
	Error       string  `json:"error,omitempty"`
}

func resolveDeskDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("OMB_DESK_DIR")); env != "" {
		if isDeskDir(env) {
			return env, nil
		}
		return "", fmt.Errorf("OMB_DESK_DIR tidak valid: %s", env)
	}
	var roots []string
	if wd, err := os.Getwd(); err == nil && wd != "" {
		roots = append(roots, wd)
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, root := range roots {
		cur := root
		for depth := 0; depth < 5; depth++ {
			cand := filepath.Join(cur, "desk")
			if isDeskDir(cand) {
				return cand, nil
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			cur = parent
		}
	}
	return "", errors.New("folder desk/ (dashboard.py & crew.py) tidak ditemukan")
}

func isDeskDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "dashboard.py")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "crew.py")); err != nil {
		return false
	}
	return true
}

func deskDataDir(deskDir string) string {
	if env := strings.TrimSpace(os.Getenv("DESK_DATA_DIR")); env != "" {
		return env
	}
	return filepath.Join(deskDir, "data")
}

func deskPort() int {
	if raw := strings.TrimSpace(os.Getenv("DESK_PORT")); raw != "" {
		if p, err := strconv.Atoi(raw); err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	return 8788
}

func deskAuthToken(deskDir string) string {
	if env := strings.TrimSpace(os.Getenv("DESK_AUTH_TOKEN")); env != "" {
		return env
	}
	raw, err := os.ReadFile(filepath.Join(deskDataDir(deskDir), ".auth_token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func deskHealthOK(port int) bool {
	cli := &http.Client{Timeout: 450 * time.Millisecond}
	resp, err := cli.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func findPythonCmd(args []string) (string, []string, error) {
	for _, bin := range []string{"python3", "python"} {
		if _, err := exec.LookPath(bin); err == nil {
			return bin, args, nil
		}
	}
	if _, err := exec.LookPath("py"); err == nil {
		return "py", append([]string{"-3"}, args...), nil
	}
	return "", nil, errors.New("python3 / python tidak ditemukan di PATH")
}

func (s *Server) isDeskManagedRunning() bool {
	if s.desk == nil {
		return false
	}
	s.desk.mu.Lock()
	defer s.desk.mu.Unlock()
	if s.desk.cmd == nil || s.desk.done == nil {
		return false
	}
	select {
	case <-s.desk.done:
		return false
	default:
		return true
	}
}

func (s *Server) stopManagedDesk() {
	if s.desk == nil {
		return
	}
	s.desk.mu.Lock()
	cmd := s.desk.cmd
	done := s.desk.done
	s.desk.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	s.desk.mu.Lock()
	if s.desk.cmd == cmd {
		s.desk.cmd = nil
		s.desk.done = nil
	}
	s.desk.mu.Unlock()
}

func (s *Server) buildDeskStatus() deskStateView {
	port := deskPort()
	view := deskStateView{
		Port:    port,
		Mode:    "live",
		Paper:   true,
		Managed: s.isDeskManagedRunning(),
	}
	deskDir, err := resolveDeskDir()
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Available = true
	view.DeskDir = deskDir
	view.Running = deskHealthOK(port)

	if tok := deskAuthToken(deskDir); tok != "" {
		view.URL = fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, url.QueryEscape(tok))
	} else {
		view.URL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	}

	if raw, err := os.ReadFile(filepath.Join(deskDataDir(deskDir), "state.json")); err == nil {
		var st struct {
			Mode          string            `json:"mode"`
			Stopped       bool              `json:"stopped"`
			Paper         bool              `json:"paper"`
			LiveArmed     bool              `json:"live_armed"`
			EquityUSD     float64           `json:"equity_usd"`
			PnlUSD        float64           `json:"pnl_usd"`
			PnlPct        float64           `json:"pnl_pct"`
			Scanned       int               `json:"scanned"`
			Traded        int               `json:"traded"`
			OpenPositions []json.RawMessage `json:"open_positions"`
		}
		if json.Unmarshal(raw, &st) == nil {
			if st.Mode != "" {
				view.Mode = st.Mode
			}
			view.Stopped = st.Stopped
			view.Paper = st.Paper
			view.LiveArmed = st.LiveArmed
			view.EquityUSD = st.EquityUSD
			view.PnlUSD = st.PnlUSD
			view.PnlPct = st.PnlPct
			view.Scanned = st.Scanned
			view.Traded = st.Traded
			view.OpenCount = len(st.OpenPositions)
		}
	}
	return view
}

func (s *Server) deskStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.buildDeskStatus())
}

func (s *Server) deskStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode            string `json:"mode"`
		Interval        int    `json:"interval"`
		Grok            bool   `json:"grok"`
		GrokCooldownSec int    `json:"grokCooldownSec"`
		Restart         bool   `json:"restart"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			writeErr(w, 400, "body bukan JSON yang valid")
			return
		}
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode != "demo" {
		mode = "live"
	}
	interval := in.Interval
	if interval <= 0 {
		interval = 30
	}
	if interval < 2 {
		interval = 2
	}
	if interval > 86400 {
		interval = 86400
	}

	deskDir, err := resolveDeskDir()
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	port := deskPort()

	if deskHealthOK(port) {
		if !in.Restart {
			writeJSON(w, http.StatusOK, s.buildDeskStatus())
			return
		}
		s.stopManagedDesk()
	}

	args := []string{
		"dashboard.py",
		"--port", strconv.Itoa(port),
		"--mode", mode,
		"--run-crew",
		"--interval", strconv.Itoa(interval),
	}
	if in.Grok {
		args = append(args, "--grok")
	}
	bin, fullArgs, err := findPythonCmd(args)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	dataDir := deskDataDir(deskDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	logFile, err := os.OpenFile(filepath.Join(dataDir, "dashboard.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	cmd := exec.Command(bin, fullArgs...)
	cmd.Dir = deskDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	env := os.Environ()
	if in.GrokCooldownSec > 0 {
		env = append(env, "DESK_GROK_COOLDOWN_SEC="+strconv.Itoa(in.GrokCooldownSec))
	}
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		writeErr(w, 500, "gagal menjalankan dashboard.py: "+err.Error())
		return
	}

	done := make(chan struct{})
	s.desk.mu.Lock()
	s.desk.cmd = cmd
	s.desk.done = done
	s.desk.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if deskHealthOK(port) {
			break
		}
		select {
		case <-done:
			writeErr(w, 500, "dashboard.py berhenti saat startup — cek desk/data/dashboard.log")
			return
		case <-time.After(150 * time.Millisecond):
		}
	}

	st := s.buildDeskStatus()
	if !st.Running {
		writeErr(w, 500, "dashboard.py tidak merespons di port "+strconv.Itoa(port))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) deskStop(w http.ResponseWriter, _ *http.Request) {
	deskDir, err := resolveDeskDir()
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	port := deskPort()
	s.stopManagedDesk()

	// If an external dashboard instance is still listening on port, ask it to pause entries.
	if deskHealthOK(port) {
		if tok := deskAuthToken(deskDir); tok != "" {
			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/control", port), strings.NewReader(`{"action":"stop"}`))
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+tok)
				req.Header.Set("Content-Type", "application/json")
				cli := &http.Client{Timeout: 3 * time.Second}
				if resp, err := cli.Do(req); err == nil {
					resp.Body.Close()
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, s.buildDeskStatus())
}

func (s *Server) deskPass(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
		Grok bool   `json:"grok"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			writeErr(w, 400, "body bukan JSON yang valid")
			return
		}
	}
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode != "demo" {
		mode = "live"
	}

	deskDir, err := resolveDeskDir()
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	port := deskPort()

	if deskHealthOK(port) {
		tok := deskAuthToken(deskDir)
		if tok == "" {
			writeErr(w, 500, "token desk tidak ditemukan di data/.auth_token")
			return
		}
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/api/pass", port), strings.NewReader(`{}`))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		cli := &http.Client{Timeout: 30 * time.Second}
		resp, err := cli.Do(req)
		if err != nil {
			writeErr(w, 502, "gagal memanggil /api/pass desk: "+err.Error())
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode >= 400 {
			writeErr(w, resp.StatusCode, "desk /api/pass mengembalikan status "+strconv.Itoa(resp.StatusCode))
			return
		}
		writeJSON(w, http.StatusOK, s.buildDeskStatus())
		return
	}

	args := []string{"crew.py", "--once", "--mode", mode}
	if in.Grok {
		args = append(args, "--grok")
	}
	bin, fullArgs, err := findPythonCmd(args)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, fullArgs...)
	cmd.Dir = deskDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		writeErr(w, 500, "crew.py --once gagal: "+msg)
		return
	}
	writeJSON(w, http.StatusOK, s.buildDeskStatus())
}
