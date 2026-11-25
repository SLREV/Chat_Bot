package store

import (
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("OMB_DATA_DIR", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestAppendAndHistoryRoundTrip(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 5; i++ {
		if _, err := s.Append(Message{Thread: "bot-a", Role: RoleUser, Text: "hi"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := s.History("bot-a", 10, 0)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 messages, got %d", len(got))
	}
	if got[0].ID == "" {
		t.Fatal("expected generated ids")
	}
}

func TestHistoryPaginatesNewestFirstWindow(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < 30; i++ {
		_, _ = s.Append(Message{Thread: "bot-p", Role: RoleUser, Text: string(rune('a' + i%26))})
	}
	page, err := s.History("bot-p", 10, 20) // the oldest ten
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 10 {
		t.Fatalf("want 10, got %d", len(page))
	}
}

func TestRingIsBounded(t *testing.T) {
	s := tempStore(t)
	for i := 0; i < ringSize+50; i++ {
		_, _ = s.Append(Message{Thread: "bot-r", Role: RoleAssistant, Text: "x"})
	}
	if got := len(s.Recent("bot-r")); got > ringSize {
		t.Fatalf("ring grew past cap: %d", got)
	}
}

func TestThreadsSortedByRecency(t *testing.T) {
	s := tempStore(t)
	_, _ = s.Append(Message{Thread: "older", Role: RoleUser, Text: "1", TS: time.Now().Add(-time.Hour)})
	_, _ = s.Append(Message{Thread: "newer", Role: RoleUser, Text: "2"})
	th := s.Threads()
	if len(th) != 2 || th[0].BotID != "newer" {
		t.Fatalf("unexpected order: %+v", th)
	}
}

func TestTornLineIsSkipped(t *testing.T) {
	s := tempStore(t)
	_, _ = s.Append(Message{Thread: "bot-t", Role: RoleUser, Text: "ok"})
	// Simulate a crash mid-write: a partial JSON line with no newline.
	f, err := openAppend(filepath.Join(s.root, "bot-t.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"id":"broken","text":`)
	_ = f.Close()

	got, err := s.History("bot-t", 10, 0)
	if err != nil {
		t.Fatalf("history should tolerate a torn line: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want the one good message, got %d", len(got))
	}
}

func TestSubscriberSeesLiveMessages(t *testing.T) {
	s := tempStore(t)
	ch, cancel := s.Subscribe()
	defer cancel()
	_, _ = s.Append(Message{Thread: "live", Role: RoleUser, Text: "ping"})
	select {
	case m := <-ch:
		if m.Text != "ping" {
			t.Fatalf("unexpected message %q", m.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message on the subscriber channel")
	}
}

func TestIDStaysInsideDataDir(t *testing.T) {
	s := tempStore(t)
	got := s.Path("../../etc/passwd")
	if filepath.Dir(got) != s.root {
		t.Fatalf("path escaped the data dir: %s", got)
	}
}
