// Package engine runs one agent turn on a local CLI and streams its output.
//
// There is deliberately no plugin ABI: an engine is "a binary plus the args
// that make it read a prompt and print an answer". Anything that speaks that
// shape (claude -p, codex exec, grok -p, a shell script, ollama run …) plugs in
// from config.json without recompiling.
package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/provider"
)

// Request is one turn.
type Request struct {
	Prompt    string
	System    string
	Model     string
	Workdir   string
	Timeout   time.Duration
	Engine    config.Engine
	Provider  *config.Provider   // set → the turn goes over HTTPS instead of a CLI
	History   []provider.Message // prior turns; provider mode only
	MaxTokens int                // provider mode: 0 = default
	OnChunk   func(string)       // streamed text, called from the reader goroutine
	// OnModelsStale refreshes the provider's model list when the vendor says a
	// model does not exist (provider mode only).
	OnModelsStale func(missing string) []string
}

// Result is what a turn produced.
type Result struct {
	Text         string
	Millis       int64
	PromptTokens int
	OutputTokens int
	ReducedTo    int // >0 when a 402 forced a smaller max_tokens
	// RecoveredModel is set when a retired model was swapped for one that exists.
	RecoveredModel string
	RecoveredFrom  string
}

// ErrTimeout marks a turn the deadline stopped.
var ErrTimeout = errors.New("engine timed out")

// Run executes one turn. The context cancels the child process group.
func Run(ctx context.Context, r Request) (Result, error) {
	if r.Timeout <= 0 {
		r.Timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	start := time.Now()

	if r.Provider != nil {
		msgs := append([]provider.Message{}, r.History...)
		msgs = append(msgs, provider.Message{Role: "user", Content: r.Prompt})
		out, err := provider.Stream(ctx, provider.Request{
			Provider:      *r.Provider,
			System:        r.System,
			Model:         r.Model,
			Messages:      msgs,
			MaxTokens:     r.MaxTokens,
			Timeout:       r.Timeout,
			OnChunk:       r.OnChunk,
			OnModelsStale: r.OnModelsStale,
		})
		return Result{Text: out.Text, Millis: out.Millis, PromptTokens: out.PromptTokens,
			OutputTokens: out.OutputTokens, ReducedTo: out.ReducedTo,
			RecoveredModel: out.RecoveredModel, RecoveredFrom: out.RecoveredFrom}, err
	}

	if r.Engine.ID == "echo" || r.Engine.CLI == "" {
		return demoTurn(ctx, r, start)
	}

	if _, err := exec.LookPath(r.Engine.CLI); err != nil {
		return Result{}, fmt.Errorf("%s CLI not found in PATH (%s) — install it or pick another engine in Settings", r.Engine.Name, r.Engine.CLI)
	}

	prompt := r.Prompt
	if r.System != "" {
		prompt = r.System + "\n\n" + prompt
	}

	var helpText string
	if strings.EqualFold(r.Engine.CLI, "opencode") || strings.EqualFold(r.Engine.ID, "opencode") {
		helpText = probeCLIHelp(ctx, r.Engine.CLI)
	}
	args, useStdin := buildCLIArgsWithHelp(r, prompt, helpText)

	cmd := exec.CommandContext(ctx, r.Engine.CLI, args...)
	cmd.Env = append(os.Environ(),
		"OMB=1",
		"OMB_MODEL="+r.Model,
		"NO_COLOR=1",
	)
	if r.Workdir != "" {
		if st, err := os.Stat(r.Workdir); err == nil && st.IsDir() {
			cmd.Dir = r.Workdir
		}
	}
	if useStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second

	var (
		mu        sync.Mutex
		buf       bytes.Buffer
		stderrBuf bytes.Buffer
	)
	out := &lockedWriter{mu: &mu, sink: func(p []byte) {
		buf.Write(p)
		if r.OnChunk != nil {
			r.OnChunk(string(p))
		}
	}}
	cmd.Stdout = out
	cmd.Stderr = &lockedWriter{mu: &mu, sink: func(p []byte) {
		const maxStderr = 4 << 10
		if stderrBuf.Len() >= maxStderr {
			return
		}
		rem := maxStderr - stderrBuf.Len()
		if len(p) > rem {
			p = p[:rem]
		}
		stderrBuf.Write(p)
	}}

	err := cmd.Run()
	_ = killGroup(cmd)

	res := Result{Text: clean(buf.String()), Millis: time.Since(start).Milliseconds()}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if res.Text != "" {
			return res, fmt.Errorf("%w after %s", ErrTimeout, r.Timeout)
		}
		return res, ErrTimeout
	}
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if detail := truncate(strings.TrimSpace(stderrBuf.String()), 500); detail != "" {
			msg = detail
		}
		if res.Text != "" {
			// Partial answer with a non-zero exit is common when a CLI is
			// interrupted; keep what the user can read, flag the exit.
			return res, fmt.Errorf("%s (exit: %s)", msg, err)
		}
		return res, errors.New(msg)
	}
	return res, nil
}

// demoTurn keeps the app usable with no CLI installed, which matters on a
// fresh 4 GB laptop where the first thing a person wants is to see it run.
func demoTurn(ctx context.Context, r Request, start time.Time) (Result, error) {
	reply := demoReply(r.Prompt)
	var b strings.Builder
	for _, part := range chunk(reply, 24) {
		select {
		case <-ctx.Done():
			return Result{Text: b.String()}, ctx.Err()
		case <-time.After(18 * time.Millisecond):
		}
		b.WriteString(part)
		if r.OnChunk != nil {
			r.OnChunk(part)
		}
	}
	return Result{Text: b.String(), Millis: time.Since(start).Milliseconds()}, nil
}

func demoReply(prompt string) string {
	p := strings.TrimSpace(prompt)
	if p == "" {
		return "Halo! Saya bot demo offline. Pasang `claude`, `codex`, `grok`, `gemini`, atau `opencode` lalu pilih engine-nya di Settings untuk jawaban sungguhan."
	}
	p = truncate(p, 400)
	return fmt.Sprintf("Halo! Ini balasan demo (tanpa CLI terpasang), jadi saya hanya menggemakan pesanmu:\n\n> %s\n\nUntuk jawaban sungguhan: pasang salah satu CLI agent (`claude`, `codex`, `grok`, `gemini`, `opencode`), lalu pilih engine-nya untuk bot ini. Mesin ini memakai ~20 MB RAM, bukan ~600 MB seperti build Electron.", p)
}

var (
	cliHelpMu    sync.Mutex
	cliHelpCache = map[string]string{}
)

func probeCLIHelp(ctx context.Context, bin string) string {
	bin = strings.TrimSpace(bin)
	if bin == "" {
		return ""
	}
	cliHelpMu.Lock()
	if cached, ok := cliHelpCache[bin]; ok {
		cliHelpMu.Unlock()
		return cached
	}
	cliHelpMu.Unlock()

	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(hctx, bin, "--help").CombinedOutput()
	text := string(out)
	if err == nil || text != "" {
		cliHelpMu.Lock()
		cliHelpCache[bin] = text
		cliHelpMu.Unlock()
	}
	return text
}

func buildCLIArgs(r Request, prompt string) ([]string, bool) {
	return buildCLIArgsWithHelp(r, prompt, "")
}

func buildCLIArgsWithHelp(r Request, prompt, helpText string) ([]string, bool) {
	args := append([]string{}, r.Engine.Args...)
	useStdin := strings.EqualFold(r.Engine.Prompt, "stdin")
	model := strings.TrimSpace(r.Model)
	addModel := model != "" && !strings.EqualFold(model, "demo") && !containsAny(args, model) && !containsAny(args, "--model")

	// OpenCode has two CLI distributions in the wild:
	// 1) `--prompt "<text>"` (+ `--auto`, no `run` subcommand or `--model` flag)
	// 2) `run [--model <m>] "<text>"` (opencode-ai)
	// Adapting from `opencode --help` makes both work even with legacy config.json.
	if (strings.EqualFold(r.Engine.CLI, "opencode") || strings.EqualFold(r.Engine.ID, "opencode")) && helpText != "" {
		useStdin = false
		if strings.Contains(helpText, "--prompt") {
			args = nil
			if strings.Contains(helpText, "--auto") {
				args = append(args, "--auto")
			}
			if addModel && strings.Contains(helpText, "--model") {
				args = append(args, "--model", model)
			}
			args = append(args, "--prompt", prompt)
			return args, false
		}
		if strings.Contains(helpText, "\n  run") || strings.Contains(helpText, "\r\n  run") {
			args = []string{"run"}
			if addModel {
				args = append(args, "--model", model)
			}
			args = append(args, prompt)
			return args, false
		}
	}

	if addModel {
		// Gemini CLI's `-p` / `--prompt` flag takes the prompt string right after it,
		// so `--model` must precede the trailing prompt flag rather than sit between `-p` and `prompt`.
		if !useStdin && r.Engine.CLI == "gemini" && len(args) > 0 && (args[len(args)-1] == "-p" || args[len(args)-1] == "--prompt") {
			pFlag := args[len(args)-1]
			args = append(args[:len(args)-1], "--model", model, pFlag)
		} else {
			args = append(args, "--model", model)
		}
	}
	if !useStdin {
		args = append(args, prompt)
	}
	return args, useStdin
}

func chunk(s string, n int) []string {
	if n <= 0 {
		return []string{s}
	}
	r := []rune(s)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	if len(r) > 0 {
		out = append(out, string(r))
	}
	return out
}

// clean strips ANSI colour codes and trailing whitespace from CLI output.
func clean(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) {
			j := i + 1
			if s[j] == '[' {
				j++
				for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
					j++
				}
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return strings.TrimSpace(b.String())
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func containsAny(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

type lockedWriter struct {
	mu   *sync.Mutex
	sink func([]byte)
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.sink(p)
	w.mu.Unlock()
	return len(p), nil
}

// io is referenced to keep the import list honest across build tags.
var _ io.Writer = (*lockedWriter)(nil)
