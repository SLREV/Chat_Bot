package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// TestNewBotGetsADistinctAvatar keeps two bots from ever looking identical.
func TestNewBotGetsADistinctAvatar(t *testing.T) {
	ts, _ := newTestServer(t)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		res, err := http.Post(ts.URL+"/api/bots", "application/json", strings.NewReader(`{"name":"Baru"}`))
		if err != nil {
			t.Fatal(err)
		}
		var b config.Bot
		_ = json.NewDecoder(res.Body).Decode(&b)
		res.Body.Close()
		if b.Avatar == "" {
			t.Fatalf("bot %d got no avatar", i)
		}
		if seen[b.Avatar] {
			t.Fatalf("avatar %q reused — bots would look alike", b.Avatar)
		}
		seen[b.Avatar] = true
	}
	// The seeded bot already owns "maus", so the fresh ones must avoid it.
	if seen["maus"] {
		t.Fatal("a new bot stole the seeded bot's avatar")
	}
}

func TestAvatarCanBeChangedAndCleared(t *testing.T) {
	ts, cfg := newTestServer(t)
	id := cfg.Bots[0].ID

	patch := func(body string) config.Bot {
		req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/api/bots/"+id, strings.NewReader(body))
		req.Header.Set("content-type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var b config.Bot
		_ = json.NewDecoder(res.Body).Decode(&b)
		return b
	}

	if got := patch(`{"avatar":"clover"}`).Avatar; got != "clover" {
		t.Fatalf("avatar change ignored: %q", got)
	}
	// An unrelated edit must not wipe the avatar.
	if got := patch(`{"name":"Maus II"}`).Avatar; got != "clover" {
		t.Fatalf("rename wiped the avatar: %q", got)
	}
	if got := patch(`{"avatar":"__none__"}`).Avatar; got != "" {
		t.Fatalf("explicit clear ignored: %q", got)
	}
}

// TestAvatarPackIsServed guards the embed: if avatars.js is missing from the
// binary, every bot silently falls back to emoji.
func TestAvatarPackIsServed(t *testing.T) {
	ts, _ := newTestServer(t)
	res, err := http.Get(ts.URL + "/avatars/avatars.js")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("avatars.js not served: %d", res.StatusCode)
	}
	body := make([]byte, 4096)
	n, _ := res.Body.Read(body)
	got := string(body[:n])
	if !strings.Contains(got, "OMB_AVATARS") || !strings.Contains(got, "data:image/png;base64,") {
		t.Fatalf("avatar pack looks wrong: %q", got[:min(len(got), 120)])
	}

	// Every id the server hands out must exist in the pack.
	raw, err := osReadFile(filepath.Join("web", "avatars", "avatars.js"))
	if err != nil {
		t.Skip("generated pack not present in this checkout")
	}
	for _, id := range config.AvatarIDs {
		if !strings.Contains(string(raw), `id:"`+id+`"`) {
			t.Fatalf("avatar id %q is offered by the server but missing from the pack", id)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
