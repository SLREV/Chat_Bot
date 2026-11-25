package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/provider"
)

func TestFreeModelsEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	var out struct {
		Providers []map[string]any `json:"providers"`
	}
	getJSON(t, ts.URL+"/api/free-models", &out)
	if len(out.Providers) == 0 {
		t.Fatal("expected at least the free-capable providers")
	}
	for _, p := range out.Providers {
		id := p["id"].(string)
		if id == "deepseek" || id == "openai" || id == "anthropic-api" {
			t.Fatalf("%s has no free tier and must not be listed", id)
		}
		if _, ok := p["models"]; !ok {
			t.Fatalf("%s should list its free models", id)
		}
	}
}

// TestUseFreeModelPointsABotAtIt is the one-click path out of "all balances empty".
func TestUseFreeModelPointsABotAtIt(t *testing.T) {
	ts, cfg := newTestServer(t)
	botID := cfg.Bots[0].ID

	body := `{"botId":"` + botID + `"}`
	res, err := http.Post(ts.URL+"/api/providers/openrouter/use-free", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if out["ok"] != true {
		t.Fatalf("expected success: %+v", out)
	}
	model, _ := out["model"].(string)
	if !strings.HasSuffix(model, ":free") {
		t.Fatalf("the bot must be moved to a :free model, got %q", model)
	}

	var bots []botView
	getJSON(t, ts.URL+"/api/bots", &bots)
	if bots[0].Engine != "openrouter" || bots[0].Model != model {
		t.Fatalf("bot not updated: %+v", bots[0])
	}

	// A paid model name must be refused.
	bad := `{"botId":"` + botID + `","model":"openai/gpt-4o-mini"}`
	res2, err := http.Post(ts.URL+"/api/providers/openrouter/use-free", "application/json", strings.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	var out2 map[string]any
	_ = json.NewDecoder(res2.Body).Decode(&out2)
	res2.Body.Close()
	if out2["ok"] != false {
		t.Fatalf("a paid model must be rejected: %+v", out2)
	}

	// A vendor with no free tier explains itself instead of failing silently.
	res3, err := http.Post(ts.URL+"/api/providers/deepseek/use-free", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out3 map[string]any
	_ = json.NewDecoder(res3.Body).Decode(&out3)
	res3.Body.Close()
	if out3["ok"] != false {
		t.Fatalf("deepseek has no free models: %+v", out3)
	}
	if msg, _ := out3["error"].(string); !strings.Contains(msg, "Ollama") {
		t.Fatalf("the error should point at a free alternative: %q", msg)
	}
	_ = provider.FreeAll
}
