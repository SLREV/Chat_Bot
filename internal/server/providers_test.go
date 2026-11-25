package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

// fakeOpenAI answers /chat/completions with a streamed reply.
func fakeOpenAI(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-live-test" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// TestProviderKeyNeverLeavesTheServer is the security-critical one: a key
// pasted through the API must never come back in any response.
func TestProviderKeyNeverLeavesTheServer(t *testing.T) {
	ts, _ := newTestServer(t)

	body := `{"name":"Fake","kind":"openai","baseUrl":"https://api.example.test/v1","apiKey":"sk-live-test","defaultModel":"m"}`
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("create → %d: %s", res.StatusCode, raw)
	}
	if strings.Contains(string(raw), "sk-live-test") {
		t.Fatalf("create response leaked the key: %s", raw)
	}

	for _, path := range []string{"/api/providers", "/api/state"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if strings.Contains(string(raw), "sk-live-test") {
			t.Fatalf("%s leaked the key", path)
		}
		if !strings.Contains(string(raw), "hasKey") && path == "/api/providers" {
			t.Fatalf("%s should report hasKey metadata", path)
		}
	}

	// The key must be on disk in providers.json only, with 0600.
	pf := config.ProvidersPath()
	st, err := os.Stat(pf)
	if err != nil {
		t.Fatalf("providers.json missing: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("providers.json must be 0600, got %v", st.Mode().Perm())
	}
	onDisk, _ := os.ReadFile(pf)
	if !strings.Contains(string(onDisk), "sk-live-test") {
		t.Fatal("key should be persisted for the server to use")
	}
	cfgDisk, _ := os.ReadFile(config.Path())
	if strings.Contains(string(cfgDisk), "sk-live-test") {
		t.Fatal("config.json must never hold a provider key")
	}
}

func TestProviderTestEndpointReportsFailure(t *testing.T) {
	ts, _ := newTestServer(t)
	body := `{"name":"Down","kind":"openai","baseUrl":"http://127.0.0.1:1/v1","defaultModel":"m"}`
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created config.MaskedProvider
	_ = json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()

	out, err := http.Post(ts.URL+"/api/providers/"+created.ID+"/test", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Body.Close()
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(out.Body).Decode(&result)
	if result.OK || result.Error == "" {
		t.Fatalf("a dead endpoint should report ok=false with a reason: %+v", result)
	}
}

// TestBotOnProviderAnswersFromTheAPI wires a bot to a fake provider and checks
// the whole path: key → HTTP → stream → transcript.
func TestBotOnProviderAnswersFromTheAPI(t *testing.T) {
	stub := fakeOpenAI(t, "halo dari provider")
	defer stub.Close()

	ts, cfg := newTestServer(t)
	body := fmt.Sprintf(`{"name":"Fake","kind":"openai","baseUrl":%q,"apiKey":"sk-live-test","defaultModel":"gpt-test"}`, stub.URL)
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created config.MaskedProvider
	_ = json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()

	// Point the default bot at the provider.
	patch := fmt.Sprintf(`{"engine":%q,"model":"gpt-test"}`, created.ID)
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/bots/"+cfg.Bots[0].ID, strings.NewReader(patch))
	req.Header.Set("content-type", "application/json")
	pres, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	pres.Body.Close()
	if pres.StatusCode != 200 {
		t.Fatalf("patch bot → %d", pres.StatusCode)
	}

	// The bot view should now advertise the API route and be ready.
	var bots []botView
	getJSON(t, ts.URL+"/api/bots", &bots)
	if bots[0].Route != "api" || !bots[0].EngineOK {
		t.Fatalf("bot should be on the api route and ready: %+v", bots[0])
	}

	sres, err := http.Post(ts.URL+"/api/bots/"+cfg.Bots[0].ID+"/messages", "application/json", strings.NewReader(`{"text":"hai"}`))
	if err != nil {
		t.Fatal(err)
	}
	sres.Body.Close()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var page struct {
			Messages []store.Message `json:"messages"`
		}
		getJSON(t, ts.URL+"/api/bots/"+cfg.Bots[0].ID+"/messages?limit=10", &page)
		for _, m := range page.Messages {
			if m.Role == store.RoleAssistant {
				if m.Text != "halo dari provider" {
					t.Fatalf("unexpected answer %q (err=%q)", m.Text, m.Err)
				}
				if !strings.HasPrefix(m.Engine, "api:") {
					t.Fatalf("transcript should record the api route: %q", m.Engine)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no answer from the fake provider")
}

func TestProviderValidation(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		body string
		want int
	}{
		{`{"name":"","baseUrl":"https://x.test/v1"}`, 400},
		{`{"name":"X","baseUrl":""}`, 400},
		{`{"name":"X","baseUrl":"ftp://x"}`, 400},
		{`{"name":"X","kind":"grpc","baseUrl":"https://x.test/v1"}`, 400},
	}
	for _, c := range cases {
		res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != c.want {
			t.Fatalf("%s → %d, want %d", c.body, res.StatusCode, c.want)
		}
	}
}

func TestClearingAKey(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/providers", "application/json",
		strings.NewReader(`{"name":"K","kind":"openai","baseUrl":"https://x.test/v1","apiKey":"sk-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created config.MaskedProvider
	_ = json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()

	var list []config.MaskedProvider
	getJSON(t, ts.URL+"/api/providers", &list)
	var has bool
	for _, p := range list {
		if p.ID == created.ID {
			has = p.HasKey
		}
	}
	if !has {
		t.Fatal("expected hasKey=true after saving a key")
	}

	// Editing the model must not wipe the key…
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/"+created.ID, strings.NewReader(`{"defaultModel":"m2"}`))
	req.Header.Set("content-type", "application/json")
	r1, _ := http.DefaultClient.Do(req)
	r1.Body.Close()
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p.ID == created.ID && !p.HasKey {
			t.Fatal("a model edit wiped the key")
		}
	}

	// …but an explicit clear does.
	req, _ = http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/"+created.ID, strings.NewReader(`{"apiKey":"__clear__"}`))
	req.Header.Set("content-type", "application/json")
	r2, _ := http.DefaultClient.Do(req)
	r2.Body.Close()
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p.ID == created.ID && p.HasKey {
			t.Fatal("explicit clear did not remove the key")
		}
	}
}

// TestProviderReachabilityIsReported matters for the "Ollama belum jalan" case:
// the UI needs to know before a person types into a chat that cannot work.
func TestProviderReachabilityIsReported(t *testing.T) {
	ts, _ := newTestServer(t)
	var list []providerView
	getJSON(t, ts.URL+"/api/providers", &list)

	var sawOllama, sawRemote bool
	for _, p := range list {
		if p.ID == "ollama" {
			sawOllama = true
			if p.Reachable {
				t.Skip("something is actually listening on 127.0.0.1:11434")
			}
			if !strings.Contains(p.ProbeNote, "ollama serve") {
				t.Fatalf("a dead Ollama should say how to start it: %q", p.ProbeNote)
			}
		}
		if p.ID == "openai" {
			sawRemote = true
			if !p.Reachable {
				t.Fatal("remote providers are not probed and must report reachable")
			}
		}
	}
	if !sawOllama || !sawRemote {
		t.Fatalf("seeded providers missing: %+v", list)
	}
}

// TestModelSyncAndCheck covers the "why is my model not listed" question: the
// built-in list is a guess until it is synced from the vendor's API.
func TestModelSyncAndCheck(t *testing.T) {
	var askedModel string
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"deepseek-chat"},{"id":"deepseek-reasoner"}]}`)
		case r.Method == http.MethodPost:
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			askedModel = body.Model
			if body.Model != "deepseek-chat" {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"message":"Model Not Exist"}}`)
				return
			}
			w.Header().Set("content-type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer vendor.Close()

	ts, _ := newTestServer(t)
	body := fmt.Sprintf(`{"name":"DeepSeek","kind":"openai","baseUrl":%q,"apiKey":"sk-x","defaultModel":"deepseek-chat"}`, vendor.URL)
	res, err := http.Post(ts.URL+"/api/providers", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created config.MaskedProvider
	_ = json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()

	// Before syncing, the list is a guess.
	var list []map[string]any
	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p["id"] == created.ID {
			if p["modelsSource"] != "seed" {
				t.Fatalf("a fresh provider should be marked as a guessed list: %v", p["modelsSource"])
			}
		}
	}

	// Sync replaces it with the vendor's answer and marks it authoritative.
	sres, err := http.Post(ts.URL+"/api/providers/"+created.ID+"/models/sync", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var sync map[string]any
	_ = json.NewDecoder(sres.Body).Decode(&sync)
	sres.Body.Close()
	if sync["ok"] != true || sync["count"].(float64) != 2 {
		t.Fatalf("sync failed: %+v", sync)
	}

	getJSON(t, ts.URL+"/api/providers", &list)
	for _, p := range list {
		if p["id"] == created.ID && p["modelsSource"] != "live" {
			t.Fatalf("after sync the list must be live, got %v", p["modelsSource"])
		}
	}

	// Checking a model the vendor does not serve reports the vendor's message.
	check := func(model string) map[string]any {
		r, err := http.Post(ts.URL+"/api/providers/"+created.ID+"/check-model", "application/json",
			strings.NewReader(fmt.Sprintf(`{"model":%q}`, model)))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	if out := check("DeepSeek V4.1 Flash"); out["ok"] != false {
		t.Fatalf("a display name must be reported as not existing: %+v", out)
	}
	if out := check("deepseek-chat"); out["ok"] != true {
		t.Fatalf("a real model should verify: %+v", out)
	}
	if askedModel != "deepseek-chat" {
		t.Fatalf("the model name must reach the vendor untouched, got %q", askedModel)
	}
}
