package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// TestEnvKeyStateIsReported covers the confusing case: a provider looks like it
// "has a key" because it points at $OPENAI_API_KEY, but the variable is empty.
func TestEnvKeyStateIsReported(t *testing.T) {
	ts, cfg := newTestServer(t)
	t.Setenv("OPENAI_API_KEY", "")

	var list []providerView
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p.ID != "openai" {
			continue
		}
		if p.KeyState != config.KeyEnvEmpty {
			t.Fatalf("openai should report %q, got %q", config.KeyEnvEmpty, p.KeyState)
		}
		if p.Active {
			t.Fatal("a provider with an empty env var must not be active")
		}
		if p.EnvName != "OPENAI_API_KEY" {
			t.Fatalf("the env name must be named so the UI can say it: %q", p.EnvName)
		}
	}

	// A bot wired to it must not be painted green.
	var bots []botView
	getJSON(t, ts.URL+"/api/bots", &bots)
	patch := `{"engine":"openai"}`
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/bots/"+cfg.Bots[0].ID, strings.NewReader(patch))
	req.Header.Set("content-type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	getJSON(t, ts.URL+"/api/bots", &bots)
	if bots[0].EngineOK {
		t.Fatal("a bot on a provider without a usable key must not show as ready")
	}

	// Once the variable is set, the same bot becomes ready.
	t.Setenv("OPENAI_API_KEY", "sk-set")
	getJSON(t, ts.URL+"/api/bots", &bots)
	if !bots[0].EngineOK {
		t.Fatal("with the env var set the bot should be ready")
	}
}
