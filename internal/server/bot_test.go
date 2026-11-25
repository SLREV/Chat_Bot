package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCreateAndUpdateBotAllowsClearingModel(t *testing.T) {
	ts, cfg := newTestServer(t)

	// Creating an opencode bot with an empty model must not coerce it to "sonnet".
	res, err := http.Post(ts.URL+"/api/bots", "application/json", strings.NewReader(`{"name":"OC","engine":"opencode","model":""}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("createBot status = %d", res.StatusCode)
	}
	var created botView
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Model != "" {
		t.Fatalf("expected empty model for opencode bot, got %q", created.Model)
	}

	// Clearing an existing bot's model via PATCH must clear it instead of keeping old value.
	botID := cfg.Bots[0].ID
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/api/bots/"+botID, strings.NewReader(`{"engine":"opencode","model":""}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("updateBot status = %d", res2.StatusCode)
	}
	var updated botView
	if err := json.NewDecoder(res2.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Engine != "opencode" || updated.Model != "" {
		t.Fatalf("expected engine=opencode and cleared model, got engine=%q model=%q", updated.Engine, updated.Model)
	}
}
