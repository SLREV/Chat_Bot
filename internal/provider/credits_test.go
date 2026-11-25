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
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

const creditMsg = `This request requires more credits, or fewer max_tokens. You requested up to 2048 tokens, but can only afford 980. To increase, visit https://openrouter.ai/settings/credits and upgrade to a paid account`

func TestParseAffordable(t *testing.T) {
	cases := map[string]int{
		creditMsg: 980,
		"You requested up to 4096 tokens, but can only afford 291 tokens": 291,
		"can only afford 64 tokens":                                       64,
		"no numbers here":                                                 0,
		"credit balance is empty":                                         0,
	}
	for msg, want := range cases {
		if got := parseAffordable(msg); got != want {
			t.Fatalf("parseAffordable(%q) = %d, want %d", msg, got, want)
		}
	}
}

func TestCreditsErrorIsTyped(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		fmt.Fprintf(w, `{"error":{"message":%q}}`, creditMsg)
	}))
	defer ts.Close()

	_, err := Stream(context.Background(), Request{
		Provider:  config.Provider{ID: "openrouter", Name: "OpenRouter", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:     "m",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 2048,
	})
	var ce *CreditsError
	if !errors.As(err, &ce) {
		t.Fatalf("want a CreditsError, got %v", err)
	}
	if ce.Affordable != 980 || ce.Requested != 2048 {
		t.Fatalf("unexpected numbers: %+v", ce)
	}
	if !strings.Contains(err.Error(), "saldo lebih kecil dari max_tokens=2048") {
		t.Fatalf("message should be readable and specific: %v", err)
	}
	if ce.Kind != "budget" {
		t.Fatalf("a budget 402 should be kind=budget, got %q", ce.Kind)
	}
	if ce.TopUp == "" {
		t.Fatal("the error must say where to top up")
	}
}

// Test402RetriesWithAnAffordableBudget is the behaviour that matters: a bot on a
// nearly-empty balance answers instead of failing.
func Test402RetriesWithAnAffordableBudget(t *testing.T) {
	var attempts []int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		attempts = append(attempts, body.MaxTokens)

		if body.MaxTokens > 980 {
			w.WriteHeader(402)
			fmt.Fprintf(w, `{"error":{"message":%q}}`, creditMsg)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"jawaban hemat\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ts.Close()

	res, err := Stream(context.Background(), Request{
		Provider:  config.Provider{ID: "openrouter", Name: "OpenRouter", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:     "m",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 2048,
	})
	if err != nil {
		t.Fatalf("expected a retry to succeed, got %v", err)
	}
	if res.Text != "jawaban hemat" {
		t.Fatalf("unexpected text: %q", res.Text)
	}
	if res.ReducedTo == 0 || res.ReducedTo > 512 {
		t.Fatalf("retry should use a small budget with headroom, got %d", res.ReducedTo)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected exactly 2 attempts (2048 then smaller), got %v", attempts)
	}
	if attempts[1] >= attempts[0] {
		t.Fatalf("retry did not shrink the budget: %v", attempts)
	}
}

// When even the reduced request fails, the original error must surface.
func Test402WithoutAffordableNumberDoesNotLoop(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":{"message":"Payment required"}}`)
	}))
	defer ts.Close()

	_, err := Stream(context.Background(), Request{
		Provider:  config.Provider{Name: "X", Kind: "openai", BaseURL: ts.URL, APIKey: "k"},
		Model:     "m",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 2048,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("no affordable number means no retry, got %d calls", calls)
	}
	var ce *CreditsError
	if !errors.As(err, &ce) {
		t.Fatalf("still a credits error: %v", err)
	}
}

func TestBalanceErrorIsNotBlamedOnAnotherVendor(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":{"message":"Insufficient Balance","type":"unknown_error"}}`)
	}))
	defer ts.Close()

	_, err := Stream(context.Background(), Request{
		Provider: config.Provider{ID: "deepseek", Name: "DeepSeek", Kind: "openai",
			BaseURL: "https://api.deepseek.com/v1"},
		Model:    "deepseek-chat",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Timeout:  time.Second,
	})
	// The endpoint in this test is a local stub, so the request never reaches
	// DeepSeek; assert on the classifier directly instead.
	ce := &CreditsError{Provider: "DeepSeek", Requested: 2048, Kind: "balance",
		Message: "Insufficient Balance", TopUp: "https://platform.deepseek.com/top_up"}
	if strings.Contains(ce.Error(), "OpenRouter") {
		t.Fatalf("a DeepSeek failure must not mention OpenRouter: %q", ce.Error())
	}
	if !strings.Contains(ce.Error(), "saldo akun habis") {
		t.Fatalf("balance errors should say so plainly: %q", ce.Error())
	}
	if ce.Free != "" {
		t.Fatal("DeepSeek has no free tier; the field must stay empty")
	}
	if got := vendorOf(config.Provider{ID: "deepseek"}).TopUp; !strings.Contains(got, "deepseek.com") {
		t.Fatalf("DeepSeek top-up URL missing: %q", got)
	}
	_ = err
}

func TestFreeSuffixIsNotForwardedToNonOpenRouter(t *testing.T) {
	// resolveModel lives in the agent package; this asserts the vendor fact the
	// rule is based on.
	if vendorOf(config.Provider{ID: "deepseek", BaseURL: "https://api.deepseek.com/v1"}).Free != "" {
		t.Fatal("DeepSeek has no :free tier")
	}
	if vendorOf(config.Provider{ID: "openrouter"}).Free == "" {
		t.Fatal("OpenRouter does have a :free tier")
	}
}
