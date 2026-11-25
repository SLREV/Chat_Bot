package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/agent"
	"github.com/milind-soni/openmausbot-go/internal/config"
	"github.com/milind-soni/openmausbot-go/internal/store"
)

func newAuthTestServer(t *testing.T, bind, token string) (*httptest.Server, *Server) {
	t.Helper()
	t.Setenv("OMB_DATA_DIR", t.TempDir())
	cfg := config.Default()
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	provs, err := config.LoadProviders()
	if err != nil {
		t.Fatal(err)
	}
	mgr := agent.NewManager(cfg, provs, st)
	srv := New(cfg, provs, st, mgr, bind, 0)
	if token != "" {
		srv.SetAuthToken(token)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

func TestNonLoopbackBindEnforcesAuthAutomatically(t *testing.T) {
	ts, srv := newAuthTestServer(t, "0.0.0.0", "")
	if srv.AuthToken() == "" {
		t.Fatal("non-loopback bind must auto-generate an auth token")
	}

	// Health stays accessible.
	hres, err := http.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	hres.Body.Close()
	if hres.StatusCode != 200 {
		t.Fatalf("health should stay 200, got %d", hres.StatusCode)
	}

	// Unauthenticated API calls are rejected.
	res, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated /api/providers should return 401, got %d", res.StatusCode)
	}

	// Bearer token grants access.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/providers", nil)
	req.Header.Set("Authorization", "Bearer "+srv.AuthToken())
	okRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	okRes.Body.Close()
	if okRes.StatusCode != 200 {
		t.Fatalf("bearer auth should succeed, got %d", okRes.StatusCode)
	}
}

func TestBootstrapQueryAndAuthCookie(t *testing.T) {
	ts, _ := newAuthTestServer(t, "127.0.0.1", "secret-token-123")

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Visiting /?token=... sets HttpOnly SameSite=Strict cookie and redirects to /.
	res, err := client.Get(ts.URL + "/?token=secret-token-123")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", res.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "omb_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected HttpOnly SameSite=Strict omb_session cookie, got %+v", sessionCookie)
	}

	// Subsequent API call with jar succeeds.
	apiRes, err := client.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	apiRes.Body.Close()
	if apiRes.StatusCode != 200 {
		t.Fatalf("authenticated cookie request failed: %d", apiRes.StatusCode)
	}

	// POST /api/auth rejects wrong token and accepts right token.
	badRes, err := http.Post(ts.URL+"/api/auth", "application/json", strings.NewReader(`{"token":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	badRes.Body.Close()
	if badRes.StatusCode != 401 {
		t.Fatalf("wrong token should return 401, got %d", badRes.StatusCode)
	}
}

func TestCSRFOriginAndContentTypeProtection(t *testing.T) {
	ts, _ := newAuthTestServer(t, "127.0.0.1", "tok")

	// Cross-origin request is rejected even with valid token.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/bots", strings.NewReader(`{"name":"X"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatalf("cross-origin request must be 403, got %d", res.StatusCode)
	}

	// Sec-Fetch-Site: cross-site is rejected.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/state", nil)
	req2.Header.Set("Authorization", "Bearer tok")
	req2.Header.Set("Sec-Fetch-Site", "cross-site")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != 403 {
		t.Fatalf("Sec-Fetch-Site cross-site must be 403, got %d", res2.StatusCode)
	}

	// Non-JSON content type is rejected with 415.
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/bots", strings.NewReader(`{"name":"X"}`))
	req3.Header.Set("Authorization", "Bearer tok")
	req3.Header.Set("Content-Type", "text/plain")
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != 415 {
		t.Fatalf("text/plain POST must be 415, got %d", res3.StatusCode)
	}
}

func TestBaseURLSwapOnKeyedProviderRequiresKeyReset(t *testing.T) {
	ts, _ := newAuthTestServer(t, "127.0.0.1", "tok")

	// Save a key on Groq.
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/groq", strings.NewReader(`{"apiKey":"gsk_secret1234"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("set key failed: %d", res.StatusCode)
	}

	// Attempting to swap baseUrl without supplying or clearing the key must fail.
	req2, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/groq", strings.NewReader(`{"baseUrl":"https://attacker.example/v1"}`))
	req2.Header.Set("Authorization", "Bearer tok")
	req2.Header.Set("Content-Type", "application/json")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != 400 {
		t.Fatalf("baseUrl swap without key reset must be rejected with 400, got %d", res2.StatusCode)
	}

	// Swapping baseUrl while clearing the key is allowed.
	req3, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/providers/groq", strings.NewReader(`{"baseUrl":"https://custom.example/v1","apiKey":"__clear__"}`))
	req3.Header.Set("Authorization", "Bearer tok")
	req3.Header.Set("Content-Type", "application/json")
	res3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	if res3.StatusCode != 200 {
		t.Fatalf("baseUrl swap with __clear__ should succeed, got %d", res3.StatusCode)
	}
}
