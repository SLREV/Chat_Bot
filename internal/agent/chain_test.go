package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/provider"
)

// When every provider fails, the reply must list every reason, not just the
// first one — that is what makes "semua provider gagal" diagnosable.
func TestChainErrorListsEveryProvider(t *testing.T) {
	err := &chainError{reasons: []string{
		shortReason("Groq", &provider.ModelNotFoundError{Provider: "Groq", Model: "llama-3.1-8b-instant"}),
		shortReason("OpenRouter", provider.ErrNoKey),
		shortReason("Ollama", fmt.Errorf("%w: dial tcp 127.0.0.1:11434", provider.ErrUnreachable)),
	}}
	msg := err.Error()
	for _, want := range []string{"Groq", "llama-3.1-8b-instant", "OpenRouter", "API key", "Ollama", "tidak bisa dihubungi", "omb check"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message is missing %q:\n%s", want, msg)
		}
	}
	if got := strings.Count(msg, "•"); got != 3 {
		t.Fatalf("expected one bullet per provider, got %d:\n%s", got, msg)
	}
}

func TestShortReasonIsActionable(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&provider.ModelNotFoundError{Model: "x"}, "model `x` tidak ada"},
		{provider.ErrNoKey, "API key belum ada"},
		{fmt.Errorf("%w: boom", provider.ErrUnreachable), "tidak bisa dihubungi"},
	}
	for _, c := range cases {
		got := shortReason("Vendor", c.err)
		if !strings.Contains(got, c.want) || !strings.HasPrefix(got, "Vendor: ") {
			t.Fatalf("shortReason(%v) = %q, want mention of %q", c.err, got, c.want)
		}
	}
	if !errors.Is(provider.ErrNoKey, provider.ErrNoKey) {
		t.Fatal("sanity")
	}
}
