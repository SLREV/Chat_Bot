// Package config holds the on-disk settings and bot roster for the Go build of
// OpenMausBot. Everything lives in a single JSON file under the data directory
// (~/.openmausbot- go/config.json) so the whole footprint stays small.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DataDir returns the directory OpenMausBot keeps its state in.
func DataDir() string {
	if d := os.Getenv("OMB_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".openmausbot-go"
	}
	return filepath.Join(home, ".openmausbot-go")
}

// Engine describes one agent CLI that bots can run on.
type Engine struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	CLI     string   `json:"cli"`    // binary to spawn, e.g. "claude"
	Args    []string `json:"args"`   // extra args placed before the prompt
	Prompt  string   `json:"prompt"` // how the prompt is passed: "arg" | "stdin"
	Enabled bool     `json:"enabled"`
}

// Bot is one contact in the sidebar: a name, a personality, a model and an engine.
type Bot struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Emoji       string    `json:"emoji"`
	Avatar      string    `json:"avatar"` // id in web/avatars/avatars.js, "" = emoji only
	Color       string    `json:"color"`
	Engine      string    `json:"engine"`
	Model       string    `json:"model"`
	Personality string    `json:"personality"`
	Workdir     string    `json:"workdir"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Settings is the whole persisted state.
type Settings struct {
	Version     int      `json:"version"`
	BindAddr    string   `json:"bindAddr"`
	Port        int      `json:"port"`
	AutoApprove bool     `json:"autoApprove"`
	TimeoutSec  int      `json:"timeoutSec"`
	Engines     []Engine `json:"engines"`
	Bots        []Bot    `json:"bots"`

	path string
	mu   sync.Mutex
}

// Default returns the settings a first run starts with.
func Default() *Settings {
	return &Settings{
		Version:     1,
		BindAddr:    "127.0.0.1",
		Port:        8799,
		AutoApprove: false,
		TimeoutSec:  600,
		Engines: []Engine{
			{ID: "claude", Name: "Claude Code", CLI: "claude", Args: []string{"-p", "--output-format", "json"}, Prompt: "arg", Enabled: true},
			{ID: "codex", Name: "Codex CLI", CLI: "codex", Args: []string{"exec", "--json"}, Prompt: "arg", Enabled: true},
			{ID: "grok", Name: "Grok CLI", CLI: "grok", Args: []string{"-p"}, Prompt: "arg", Enabled: true},
			{ID: "echo", Name: "Demo (offline)", CLI: "", Prompt: "arg", Enabled: true},
		},
		Bots: []Bot{
			{ID: "bot-maus", Name: "Maus", Emoji: "🐭", Avatar: "maus", Color: "#1084fe", Engine: "echo", Model: "demo",
				Personality: "You are Maus, a calm and precise assistant."},
			{ID: "bot-chief", Name: "Chief of Staff", Emoji: "🧠", Avatar: "raven", Color: "#7c5cff", Engine: "echo", Model: "demo", Workdir: "desk",
				Personality: "You are the Chief of Staff orchestrating a 6-agent trading desk (Scout, Filter, Analyst, Executor, Risk, Logger) in PAPER MODE by default. Never fabricate live data; always verify Filter's 8 hard kill rules (fail-closed) before Analyst scoring (>=70), enforce max 8% equity sizing, -6% hard stop, trailing stop from +12%, and -15% daily circuit breaker. Reply concisely in the user's language (Indonesian/English) with structured status, positions, and exact CLI commands (python3 desk/crew.py / python3 desk/dashboard.py) when execution or data verification is needed."},
		},
	}
}

// Path is where the settings file lives.
func Path() string { return filepath.Join(DataDir(), "config.json") }

// Load reads the settings file, creating defaults on first run.
func Load() (*Settings, error) {
	s := Default()
	s.path = Path()
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.normalize()
		return s, s.Save()
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	s.path = Path()
	s.normalize()
	return s, nil
}

// Save writes the settings atomically with 0600 permissions.
func (s *Settings) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		s.path = Path()
		if err := os.MkdirAll(DataDir(), 0o700); err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Snapshot returns the list of bots (callers get a copy).
func (s *Settings) Snapshot() []Bot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Bot, len(s.Bots))
	copy(out, s.Bots)
	return out
}

// BotByID returns a copy of a single bot by id.
func (s *Settings) BotByID(id string) (Bot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.Bots {
		if b.ID == id {
			return b, true
		}
	}
	return Bot{}, false
}

// UpdateBot atomically applies fn to the matching bot under lock.
func (s *Settings) UpdateBot(id string, fn func(*Bot)) (Bot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Bots {
		if s.Bots[i].ID == id {
			fn(&s.Bots[i])
			return s.Bots[i], true
		}
	}
	return Bot{}, false
}

// EnginesSnapshot returns a copy of the configured engines.
func (s *Settings) EnginesSnapshot() []Engine {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Engine, len(s.Engines))
	for i, e := range s.Engines {
		if len(e.Args) > 0 {
			args := make([]string, len(e.Args))
			copy(args, e.Args)
			e.Args = args
		}
		out[i] = e
	}
	return out
}

// Runtime returns a thread-safe snapshot of scalar runtime settings.
func (s *Settings) Runtime() (bindAddr string, port int, autoApprove bool, timeoutSec int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.BindAddr, s.Port, s.AutoApprove, s.TimeoutSec
}

// UpdateRuntime updates scalar runtime settings under lock.
func (s *Settings) UpdateRuntime(autoApprove *bool, timeoutSec *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if autoApprove != nil {
		s.AutoApprove = *autoApprove
	}
	if timeoutSec != nil && *timeoutSec > 0 {
		s.TimeoutSec = *timeoutSec
	}
}

// Timeout returns the configured per-turn timeout as a Duration.
func (s *Settings) Timeout() time.Duration {
	s.mu.Lock()
	sec := s.TimeoutSec
	s.mu.Unlock()
	if sec <= 0 {
		sec = 600
	}
	return time.Duration(sec) * time.Second
}

// EngineByID looks an engine up.
func (s *Settings) EngineByID(id string) (Engine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.Engines {
		if e.ID == id {
			return e, true
		}
	}
	return Engine{}, false
}

// UpsertBot adds or replaces a bot.
func (s *Settings) UpsertBot(b Bot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Bots {
		if s.Bots[i].ID == b.ID {
			s.Bots[i] = b
			return
		}
	}
	s.Bots = append(s.Bots, b)
}

// DeleteBot removes a bot by id and reports whether it existed.
func (s *Settings) DeleteBot(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Bots {
		if s.Bots[i].ID == id {
			s.Bots = append(s.Bots[:i], s.Bots[i+1:]...)
			return true
		}
	}
	return false
}

// normalize fills in anything a hand-edited config.json left out.
func (s *Settings) normalize() {
	if s.Port == 0 {
		s.Port = 8799
	}
	if s.BindAddr == "" {
		s.BindAddr = "127.0.0.1"
	}
	if s.TimeoutSec <= 0 {
		s.TimeoutSec = 600
	}
	if s.Version == 0 {
		s.Version = 1
	}
	for i := range s.Bots {
		s.Bots[i].ID = ensureID(s.Bots[i])
		s.Bots[i].CreatedAt = ensureTime(s.Bots[i])
	}
}

func ensureTime(b Bot) time.Time {
	if b.CreatedAt.IsZero() {
		return time.Now()
	}
	return b.CreatedAt
}
