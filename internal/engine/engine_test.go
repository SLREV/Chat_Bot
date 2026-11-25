package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

func TestCleanStripsANSI(t *testing.T) {
	got := clean("\x1b[31mred\x1b[0m text \n")
	if got != "red text" {
		t.Fatalf("got %q", got)
	}
}

func TestDemoTurnStreamsAndCompletes(t *testing.T) {
	var got strings.Builder
	res, err := Run(context.Background(), Request{
		Prompt:  "halo",
		Timeout: 5 * time.Second,
		Engine:  config.Engine{ID: "echo", Name: "Demo"},
		OnChunk: func(s string) { got.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.String() != res.Text {
		t.Fatal("streamed chunks should add up to the final text")
	}
	if !strings.Contains(res.Text, "halo") {
		t.Fatalf("expected echo of the prompt, got %q", res.Text)
	}
}

func TestMissingCLIIsAClearError(t *testing.T) {
	_, err := Run(context.Background(), Request{
		Prompt:  "x",
		Timeout: 2 * time.Second,
		Engine:  config.Engine{ID: "claude", Name: "Claude Code", CLI: "definitely-not-installed-cli"},
	})
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Fatalf("want a readable missing-CLI error, got %v", err)
	}
}

func TestRealProcessStreams(t *testing.T) {
	if _, err := execLookPath("sh"); err != nil {
		t.Skip("no sh on this machine")
	}
	var got strings.Builder
	res, err := Run(context.Background(), Request{
		Prompt:  "ignored",
		Timeout: 5 * time.Second,
		Engine:  config.Engine{ID: "sh", Name: "sh", CLI: "sh", Args: []string{"-c", "printf 'hello'"}, Prompt: "stdin"},
		OnChunk: func(s string) { got.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "hello" || got.String() != "hello" {
		t.Fatalf("text=%q stream=%q", res.Text, got.String())
	}
}

func TestTimeoutKillsTheTurn(t *testing.T) {
	if _, err := execLookPath("sleep"); err != nil {
		t.Skip("no sleep binary")
	}
	start := time.Now()
	_, err := Run(context.Background(), Request{
		Prompt:  "",
		Timeout: 250 * time.Millisecond,
		Engine:  config.Engine{ID: "sleep", Name: "sleep", CLI: "sleep", Args: []string{"10"}, Prompt: "stdin"},
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout did not stop the child promptly: %s", d)
	}
}
