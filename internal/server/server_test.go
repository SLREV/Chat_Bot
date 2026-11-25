package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/agent"
	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *config.Settings) {
	t.Helper()
	t.Setenv("OMB_DATA_DIR", t.TempDir())
	cfg := config.Default()
	cfg.Bots[0].Engine = "echo"
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	provs, err := config.LoadProviders()
	if err != nil {
		t.Fatal(err)
	}
	mgr := agent.NewManager(cfg, provs, st)
	srv := New(cfg, provs, st, mgr, "127.0.0.1", 0)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, cfg
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET %s → %d", url, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestHealthAndIndex(t *testing.T) {
	ts, _ := newTestServer(t)
	var h map[string]any
	getJSON(t, ts.URL+"/api/health", &h)
	if h["service"] != "openmausbot-go" {
		t.Fatalf("unexpected health payload: %v", h)
	}
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("UI not served: %d", res.StatusCode)
	}
}

func TestSendMessageRunsATurn(t *testing.T) {
	ts, cfg := newTestServer(t)
	botID := cfg.Bots[0].ID

	res, err := http.Post(ts.URL+"/api/bots/"+botID+"/messages", "application/json", strings.NewReader(`{"text":"halo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 202 {
		t.Fatalf("send → %d", res.StatusCode)
	}
	res.Body.Close()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var page struct {
			Messages []store.Message `json:"messages"`
		}
		getJSON(t, ts.URL+"/api/bots/"+botID+"/messages?limit=10", &page)
		if len(page.Messages) >= 2 && page.Messages[len(page.Messages)-1].Role == store.RoleAssistant {
			if !strings.Contains(page.Messages[1].Text, "halo") {
				t.Fatalf("unexpected answer: %q", page.Messages[1].Text)
			}
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Fatal("no assistant answer within the deadline")
}

func TestEmptyMessageIsRejected(t *testing.T) {
	ts, cfg := newTestServer(t)
	res, err := http.Post(ts.URL+"/api/bots/"+cfg.Bots[0].ID+"/messages", "application/json", strings.NewReader(`{"text":"   "}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
}

func TestBotLifecycle(t *testing.T) {
	ts, _ := newTestServer(t)
	body := `{"name":"Riset","engine":"echo","emoji":"🔎"}`
	res, err := http.Post(ts.URL+"/api/bots", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created config.Bot
	_ = json.NewDecoder(res.Body).Decode(&created)
	res.Body.Close()
	if created.ID == "" || created.Name != "Riset" {
		t.Fatalf("unexpected bot: %+v", created)
	}

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/bots/"+created.ID, nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != 200 {
		t.Fatalf("delete → %d", del.StatusCode)
	}

	var botList []botView
	getJSON(t, ts.URL+"/api/bots", &botList)
	for _, b := range botList {
		if b.ID == created.ID {
			t.Fatal("bot still listed after delete")
		}
	}
}

func TestStateReportsEngineAvailability(t *testing.T) {
	ts, _ := newTestServer(t)
	var st struct {
		Engines []config.Engine `json:"engines"`
		Bots    []botView       `json:"bots"`
	}
	getJSON(t, ts.URL+"/api/state", &st)
	if len(st.Engines) == 0 || len(st.Bots) == 0 {
		t.Fatalf("state should list engines and bots: %+v", st)
	}
	if !st.Bots[0].EngineOK {
		t.Fatal("the demo engine should always be available")
	}
}
