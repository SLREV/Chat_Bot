package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	for _, f := range frames {
		fmt.Fprintf(w, "data: %s\n\n", f)
	}
}

func TestOpenAIStreaming(t *testing.T) {
	var gotAuth, gotModel string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotModel = string(body)
		sse(w,
			`{"choices":[{"delta":{"content":"Halo"}}]}`,
			`{"choices":[{"delta":{"content":" dunia"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`,
			`[DONE]`,
		)
	}))
	defer ts.Close()

	var streamed strings.Builder
	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Fake", Kind: "openai", BaseURL: ts.URL, APIKey: "sk-test"},
		System:   "be nice",
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
		OnChunk:  func(s string) { streamed.WriteString(s) },
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if res.Text != "Halo dunia" || streamed.String() != "Halo dunia" {
		t.Fatalf("text=%q stream=%q", res.Text, streamed.String())
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("missing bearer auth, got %q", gotAuth)
	}
	if !strings.Contains(gotModel, `"model":"test-model"`) || !strings.Contains(gotModel, `"stream":true`) {
		t.Fatalf("unexpected request body: %s", gotModel)
	}
	if res.PromptTokens != 11 || res.OutputTokens != 3 {
		t.Fatalf("usage not parsed: %+v", res)
	}
}

func TestAnthropicStreaming(t *testing.T) {
	var headers http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		if r.URL.Path != "/messages" {
			t.Errorf("wrong path: %s", r.URL.Path)
		}
		sse(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":9}}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hai"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"!"}}`,
			`{"type":"message_delta","usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		)
	}))
	defer ts.Close()

	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Fake Anthropic", Kind: "anthropic", BaseURL: ts.URL, APIKey: "sk-ant"},
		Model:    "claude-x",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if res.Text != "Hai!" {
		t.Fatalf("text=%q", res.Text)
	}
	if headers.Get("x-api-key") != "sk-ant" {
		t.Fatal("anthropic needs x-api-key")
	}
	if headers.Get("anthropic-version") == "" {
		t.Fatal("anthropic-version header missing")
	}
	if res.PromptTokens != 9 || res.OutputTokens != 2 {
		t.Fatalf("usage: %+v", res)
	}
}

func TestBadKeyIsActionable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-abc***"}}`))
	}))
	defer ts.Close()

	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "OpenAI palsu", Kind: "openai", BaseURL: ts.URL, APIKey: "sk-wrong"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "menolak API key") {
		t.Fatalf("want an actionable 401 error, got %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("status should be named: %v", err)
	}
}

func TestMissingKey(t *testing.T) {
	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "OpenAI", Kind: "openai", BaseURL: "https://api.openai.com/v1"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

func TestEnvKeyIndirection(t *testing.T) {
	t.Setenv("MY_TEST_KEY", "sk-from-env")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-from-env" {
			t.Errorf("env key not used: %q", r.Header.Get("Authorization"))
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	}))
	defer ts.Close()

	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Env", Kind: "openai", BaseURL: ts.URL, APIKey: "$MY_TEST_KEY"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil || res.Text != "ok" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	// A variable that is not set must fail loudly, not send an empty bearer.
	// Checked on Secret() directly: this test server is on 127.0.0.1, and a
	// loopback endpoint is treated as keyless on purpose (Ollama), which would
	// mask the error at the Stream() level.
	p := config.Provider{Name: "Env", APIKey: "$NOT_SET_ANYWHERE"}
	if _, err := p.Secret(); err == nil || !strings.Contains(err.Error(), "NOT_SET_ANYWHERE") {
		t.Fatalf("want a naming error for the missing variable, got %v", err)
	}
}

func TestLocalEndpointNeedsNoKey(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("a keyless local endpoint should not receive auth")
		}
		sse(w, `{"choices":[{"delta":{"content":"dari ollama"}}]}`, `[DONE]`)
	}))
	defer ts.Close()

	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Ollama", Kind: "openai", BaseURL: ts.URL},
		Model:    "llama3.2",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("local endpoint should work without a key: %v", err)
	}
	if res.Text != "dari ollama" {
		t.Fatalf("text=%q", res.Text)
	}
}

func TestTimeoutDuringStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(2 * time.Second)
	}))
	defer ts.Close()

	start := time.Now()
	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Slow", Kind: "openai", BaseURL: ts.URL},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Timeout:  300 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("timeout did not fire promptly: %s", d)
	}
}

func TestUnreachableEndpointMessage(t *testing.T) {
	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Mati", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Timeout:  2 * time.Second,
	})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
	if !strings.Contains(err.Error(), "tidak ada server yang mendengarkan") {
		t.Fatalf("want a port hint, got %v", err)
	}
}

func TestKindMustBeKnown(t *testing.T) {
	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "X", Kind: "grpc-ish", BaseURL: "http://127.0.0.1:1", APIKey: "k"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("want a kind error, got %v", err)
	}
}

func TestOllamaDownGetsItsOwnHint(t *testing.T) {
	p := config.Provider{Name: "Ollama (lokal, tanpa key)", Kind: "openai", BaseURL: "http://127.0.0.1:11434/v1"}
	_, err := Stream(context.Background(), Request{
		Provider: p, Model: "llama3.2", Timeout: 2 * time.Second,
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
	for _, want := range []string{"ollama serve", "ollama pull", "11434"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("hint should mention %q: %v", want, err)
		}
	}
}

func TestProbeLocalEndpoint(t *testing.T) {
	// Nothing listens on port 1 → not reachable, with a reason.
	down := config.Provider{Name: "Down", Kind: "openai", BaseURL: "http://127.0.0.1:1/v1"}
	ok, note := Probe(context.Background(), down)
	if ok || note == "" {
		t.Fatalf("a dead local endpoint must report ok=false with a note: %v %q", ok, note)
	}

	// A live endpoint reports ok.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"llama3.2"}]}`)
	}))
	defer ts.Close()
	up := config.Provider{Name: "Up", Kind: "openai", BaseURL: ts.URL + "/v1"}
	if ok, note := Probe(context.Background(), up); !ok {
		t.Fatalf("a live endpoint should probe ok: %q", note)
	}

	// Remote providers are not probed at all.
	if ok, _ := Probe(context.Background(), config.Provider{Name: "R", BaseURL: "https://api.openai.com/v1"}); !ok {
		t.Fatal("remote providers should not be probed")
	}
}
