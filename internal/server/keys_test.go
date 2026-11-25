package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProviderSaveCleansPastedKey(t *testing.T) {
	ts, _ := newTestServer(t)
	// A key pasted with a Bearer prefix and quotes, as people actually do it.
	body := `{"name":"OpenRouter","kind":"openai","baseUrl":"https://openrouter.ai/api/v1","apiKey":"  \"Bearer sk-or-v1-abc123\"  ","defaultModel":"openai/gpt-4o-mini"}`
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()

	if cleaned, _ := out["cleaned"].([]any); len(cleaned) == 0 {
		t.Fatalf("response should say what was cleaned: %+v", out)
	}
	if _, leaked := out["apiKey"]; leaked {
		t.Fatal("key echoed back")
	}
	if w, _ := out["keyWarning"].(string); w != "" {
		t.Fatalf("sk-or-v1 is the right shape, should not warn: %q", w)
	}

	// The stored value must be the clean key.
	newID, _ := out["id"].(string)
	var list []map[string]any
	getJSON(t, ts.URL+"/api/providers", &list)
	found := false
	for _, p := range list {
		if p["id"] != newID {
			continue
		}
		found = true
		hint, _ := p["keyHint"].(string)
		if hint == "" || strings.Contains(hint, "Bearer") || strings.Contains(hint, "\"") {
			t.Fatalf("stored key still has junk: %q", hint)
		}
		if !strings.HasSuffix(hint, "c123") {
			t.Fatalf("unexpected key hint: %q", hint)
		}
	}
	if !found {
		t.Fatal("created provider not listed")
	}
}

func TestWrongVendorKeyIsWarnedNotRejected(t *testing.T) {
	ts, _ := newTestServer(t)
	body := `{"name":"OR","kind":"openai","baseUrl":"https://openrouter.ai/api/v1","apiKey":"sk-proj-abc","defaultModel":"m"}`
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("a suspicious key is a warning, not an error: %d", res.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	w, _ := out["keyWarning"].(string)
	if !strings.Contains(w, "OpenAI") {
		t.Fatalf("should name the suspected vendor: %q", w)
	}
}
