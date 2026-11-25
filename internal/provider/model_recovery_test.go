package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

func TestModelFromMessage(t *testing.T) {
	cases := map[string]string{
		"The model `llama-3.3-70b-versatile` does not exist or you do not have access to it.": "llama-3.3-70b-versatile",
		`model "gpt-oss-20b" not found`:      "gpt-oss-20b",
		"model 'qwen-2.5' is unknown":        "qwen-2.5",
		"model deepseek-v4 is not available": "deepseek-v4",
		"nothing useful here":                "",
	}
	for msg, want := range cases {
		if got := modelFromMessage(msg); got != want {
			t.Fatalf("modelFromMessage(%q) = %q, want %q", msg, got, want)
		}
	}
}

// TestRetiredModelIsReplacedAndRetried is the Groq case: the built-in default is
// retired, the vendor 404s, we refresh the list and answer with a live model.
func TestRetiredModelIsReplacedAndRetried(t *testing.T) {
	const alive = "llama-3.1-8b-instant"
	var posted []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("content-type", "application/json")
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, alive)
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted = append(posted, body.Model)
		if body.Model != alive {
			w.WriteHeader(404)
			fmt.Fprintf(w, "{\"error\":{\"message\":\"The model `%s` does not exist or you do not have access to it.\"}}", body.Model)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"halo\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer ts.Close()

	refreshCalls := 0
	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Groq", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:    "llama-3.3-70b-versatile",
		Messages: []Message{{Role: "user", Content: "hi"}},
		OnModelsStale: func(missing string) []string {
			refreshCalls++
			if missing != "llama-3.3-70b-versatile" {
				t.Fatalf("refresh was told about the wrong model: %q", missing)
			}
			return []string{alive}
		},
	})
	if err != nil {
		t.Fatalf("expected recovery, got %v", err)
	}
	if res.Text != "halo" {
		t.Fatalf("text=%q", res.Text)
	}
	if res.RecoveredModel != alive || res.RecoveredFrom != "llama-3.3-70b-versatile" {
		t.Fatalf("recovery not reported: %+v", res)
	}
	if refreshCalls != 1 {
		t.Fatalf("expected exactly one refresh, got %d", refreshCalls)
	}
	if len(posted) != 2 || posted[0] == posted[1] {
		t.Fatalf("the retry must use the new model, posted=%v", posted)
	}
	if posted[1] != alive {
		t.Fatalf("retry used %q, want %q", posted[1], alive)
	}
}

// A 404 that is not about a model (wrong base URL) must not trigger a refresh loop.
func TestNonModel404IsNotTreatedAsRetiredModel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"message":"Not Found"}}`)
	}))
	defer ts.Close()

	refreshCalls := 0
	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "X", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		OnModelsStale: func(string) []string {
			refreshCalls++
			return []string{"other"}
		},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if refreshCalls != 0 {
		t.Fatal("a plain 404 (bad URL) must not be treated as a retired model")
	}
	if strings.Contains(err.Error(), "menyediakan model") {
		t.Fatalf("message should point at the endpoint, not the model: %v", err)
	}
}

// Two retired models in a row must not end the turn: the retry walks the fresh
// list and skips the non-chat entries vendors mix into /models.
func TestRecoverySkipsSecondRetiredModel(t *testing.T) {
	const good = "openai/gpt-oss-20b"
	var posted []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted = append(posted, body.Model)
		if body.Model != good {
			w.WriteHeader(404)
			fmt.Fprintf(w, "{\"error\":{\"message\":\"The model `%s` does not exist or you do not have access to it.\"}}", body.Model)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"oke\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer ts.Close()

	res, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Groq", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:    "llama-3.1-8b-instant",
		Messages: []Message{{Role: "user", Content: "hi"}},
		OnModelsStale: func(missing string) []string {
			return []string{"whisper-large-v3", missing, good}
		},
	})
	if err != nil {
		t.Fatalf("expected recovery after two dead models, got %v", err)
	}
	if res.Text != "oke" || res.RecoveredModel != good {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(posted) != 3 {
		t.Fatalf("expected 3 attempts (dead, whisper, good), got %v", posted)
	}
}

// When nothing in the refreshed list works, the error must say what to do.
func TestRecoveryFailureExplainsItself(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, "{\"error\":{\"message\":\"The model `gone` does not exist or you do not have access to it.\"}}")
	}))
	defer ts.Close()

	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{Name: "Groq", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:    "gone",
		Messages: []Message{{Role: "user", Content: "hi"}},
		OnModelsStale: func(string) []string {
			return []string{"also-gone"}
		},
	})
	var mnf *ModelNotFoundError
	if !errors.As(err, &mnf) {
		t.Fatalf("expected ModelNotFoundError, got %v", err)
	}
	if mnf.Model == "" || mnf.Provider != "Groq" {
		t.Fatalf("the error must name provider and model: %+v", mnf)
	}
}
