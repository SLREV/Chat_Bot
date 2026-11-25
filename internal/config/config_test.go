package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultIncludesGeminiAndOpenCode(t *testing.T) {
	cfg := Default()
	want := map[string]string{
		"claude":     "claude",
		"codex":      "codex",
		"grok":       "grok",
		"gemini-cli": "gemini",
		"opencode":   "opencode",
		"echo":       "",
	}
	got := map[string]string{}
	for _, e := range cfg.Engines {
		got[e.ID] = e.CLI
	}
	for id, cli := range want {
		gotCLI, ok := got[id]
		if !ok {
			t.Fatalf("missing built-in engine %q in Default().Engines", id)
		}
		if gotCLI != cli {
			t.Fatalf("engine %q CLI = %q, want %q", id, gotCLI, cli)
		}
	}
}

func TestLoadBackfillsMissingBuiltInEngines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OMB_DATA_DIR", dir)

	old := Settings{
		Version: 1,
		Engines: []Engine{
			{ID: "claude", Name: "Claude Code", CLI: "claude", Args: []string{"-p"}, Prompt: "arg", Enabled: true},
			{ID: "echo", Name: "Demo (offline)", CLI: "", Prompt: "arg", Enabled: true},
		},
		Bots: []Bot{{ID: "bot-maus", Name: "Maus", Engine: "echo", Model: "demo"}},
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := loaded.EngineByID("opencode"); !ok {
		t.Fatal("expected Load() to backfill opencode engine into legacy config.json")
	}
	if _, ok := loaded.EngineByID("gemini-cli"); !ok {
		t.Fatal("expected Load() to backfill gemini-cli engine into legacy config.json")
	}
}
