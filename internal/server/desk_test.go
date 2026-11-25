package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeskStatusAndAuthProtection(t *testing.T) {
	// Protected when auth token is active
	tsAuth, _ := newAuthTestServer(t, "0.0.0.0", "secret-token-xyz")
	resUnauth, err := http.Get(tsAuth.URL + "/api/desk/status")
	if err != nil {
		t.Fatal(err)
	}
	resUnauth.Body.Close()
	if resUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated /api/desk/status, got %d", resUnauth.StatusCode)
	}

	// Valid desk dir + state.json + token should surface in /api/desk/status
	tmpDesk := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDesk, "dashboard.py"), []byte("# stub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDesk, "crew.py"), []byte("# stub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(tmpDesk, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, ".auth_token"), []byte("desk-tok-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "state.json"), []byte(`{"mode":"live","paper":true,"equity_usd":162.5,"scanned":42,"open_positions":[{}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("OMB_DESK_DIR", tmpDesk)
	t.Setenv("DESK_DATA_DIR", dataDir)
	t.Setenv("DESK_PORT", "18788")

	ts, _ := newTestServer(t)
	var out deskStateView
	getJSON(t, ts.URL+"/api/desk/status", &out)
	if !out.Available {
		t.Fatalf("expected Available=true, got error=%q", out.Error)
	}
	if out.EquityUSD != 162.5 || out.Scanned != 42 || out.OpenCount != 1 {
		t.Fatalf("unexpected state snapshot: %+v", out)
	}
	if !strings.Contains(out.URL, "token=desk-tok-123") {
		t.Fatalf("expected token in URL, got %q", out.URL)
	}

	// Invalid JSON on POST /api/desk/start returns 400
	resBad, err := http.Post(ts.URL+"/api/desk/start", "application/json", strings.NewReader("{bad"))
	if err != nil {
		t.Fatal(err)
	}
	defer resBad.Body.Close()
	if resBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on bad JSON, got %d", resBad.StatusCode)
	}
	var errObj map[string]any
	_ = json.NewDecoder(resBad.Body).Decode(&errObj)
}
