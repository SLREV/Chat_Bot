package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// TestClearKeyKeepsProvider is the bug people hit: "hapus" must be able to mean
// "forget my key", not "delete Groq".
func TestClearKeyKeepsProvider(t *testing.T) {
	ts, _ := newTestServer(t)

	// Give Groq a key first.
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/groq",
		strings.NewReader(`{"apiKey":"gsk_realkey123456"}`))
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	// Clear the key.
	dreq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/providers/groq/key", nil)
	dres, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(dres.Body).Decode(&out)
	dres.Body.Close()
	if out["keyCleared"] != true {
		t.Fatalf("expected keyCleared: %+v", out)
	}

	var list []providerView
	getJSON(t, ts.URL+"/api/providers", &list)
	var groq *providerView
	for i := range list {
		if list[i].ID == "groq" {
			groq = &list[i]
		}
	}
	if groq == nil {
		t.Fatal("clearing the key deleted the provider — that is the bug")
	}
	if groq.HasKey {
		t.Fatal("the key should be gone")
	}
	if groq.Name != "Groq" || groq.BaseURL == "" || len(groq.Models) == 0 {
		t.Fatalf("provider config must survive: %+v", groq)
	}
}

// TestRestoreDefaultsBringsBackDeletedProviders covers recovery after a misclick.
func TestRestoreDefaultsBringsBackDeletedProviders(t *testing.T) {
	ts, _ := newTestServer(t)

	dreq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/providers/groq", nil)
	dres, err := http.DefaultClient.Do(dreq)
	if err != nil {
		t.Fatal(err)
	}
	dres.Body.Close()

	var list []providerView
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p.ID == "groq" {
			t.Fatal("groq should be gone before restore")
		}
	}

	rres, err := http.Post(ts.URL+"/api/providers/restore", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Count    int      `json:"count"`
		Restored []string `json:"restored"`
	}
	_ = json.NewDecoder(rres.Body).Decode(&out)
	rres.Body.Close()
	if out.Count != 1 || len(out.Restored) != 1 || out.Restored[0] != "Groq" {
		t.Fatalf("expected Groq back, got %+v", out)
	}

	getJSON(t, ts.URL+"/api/providers", &list)
	found := false
	for _, p := range list {
		if p.ID == "groq" {
			found = true
			if p.Name != "Groq" || p.KeyState != config.KeyEnvEmpty {
				t.Fatalf("restored provider should look like a fresh one: %+v", p)
			}
		}
	}
	if !found {
		t.Fatal("restore did not re-add Groq")
	}

	// Restoring twice is harmless and changes nothing.
	rres2, _ := http.Post(ts.URL+"/api/providers/restore", "application/json", strings.NewReader(`{}`))
	var out2 struct {
		Count int `json:"count"`
	}
	_ = json.NewDecoder(rres2.Body).Decode(&out2)
	rres2.Body.Close()
	if out2.Count != 0 {
		t.Fatalf("second restore should be a no-op, got %d", out2.Count)
	}
}

// A stored key must survive a restore of *other* providers.
func TestRestoreDoesNotTouchExistingKeys(t *testing.T) {
	ts, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/groq",
		strings.NewReader(`{"apiKey":"gsk_keepme"}`))
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if _, err := http.Post(ts.URL+"/api/providers/restore", "application/json", strings.NewReader(`{}`)); err != nil {
		t.Fatal(err)
	}

	var list []providerView
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p.ID == "groq" && (!p.HasKey || !strings.HasSuffix(p.KeyHint, "me")) {
			t.Fatalf("restore wiped an existing key: %+v", p)
		}
	}
}
