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

func TestBuildCLIArgsForGeminiAndOpenCode(t *testing.T) {
	gemArgs, stdin := buildCLIArgs(Request{
		Model:  "gemini-2.5-flash",
		Engine: config.Engine{ID: "gemini-cli", CLI: "gemini", Args: []string{"-p"}, Prompt: "arg"},
	}, "cek desk")
	if stdin {
		t.Fatal("gemini-cli should use arg prompt")
	}
	wantGem := "gemini-2.5-flash"
	if len(gemArgs) != 4 || gemArgs[0] != "--model" || gemArgs[1] != wantGem || gemArgs[2] != "-p" || gemArgs[3] != "cek desk" {
		t.Fatalf("unexpected gemini args: %#v", gemArgs)
	}

	ocArgs, _ := buildCLIArgs(Request{
		Model:  "demo",
		Engine: config.Engine{ID: "opencode", CLI: "opencode", Args: []string{"run"}, Prompt: "arg"},
	}, "cek desk")
	if len(ocArgs) != 2 || ocArgs[0] != "run" || ocArgs[1] != "cek desk" {
		t.Fatalf("demo model must not be passed to CLI, got: %#v", ocArgs)
	}

	ocModelArgs, _ := buildCLIArgs(Request{
		Model:  "google/gemini-2.5-flash",
		Engine: config.Engine{ID: "opencode", CLI: "opencode", Args: []string{"run"}, Prompt: "arg"},
	}, "cek desk")
	if len(ocModelArgs) != 4 || ocModelArgs[0] != "run" || ocModelArgs[1] != "--model" || ocModelArgs[2] != "google/gemini-2.5-flash" || ocModelArgs[3] != "cek desk" {
		t.Fatalf("unexpected opencode args with model: %#v", ocModelArgs)
	}

	// OpenCode variant with `--auto` + `--prompt string` and no `--model` flag
	promptFlagHelp := "USAGE\n  opencode <subcommand> [flags]\nFLAGS\n  --auto\n  --prompt string\n"
	ocPromptArgs, _ := buildCLIArgsWithHelp(Request{
		Model:  "DeepSeek V4.1 Flash Free",
		Engine: config.Engine{ID: "opencode", CLI: "opencode", Args: []string{"run"}, Prompt: "arg"},
	}, "cek desk", promptFlagHelp)
	if len(ocPromptArgs) != 3 || ocPromptArgs[0] != "--auto" || ocPromptArgs[1] != "--prompt" || ocPromptArgs[2] != "cek desk" {
		t.Fatalf("expected --auto --prompt for prompt-flag opencode, got: %#v", ocPromptArgs)
	}
}
