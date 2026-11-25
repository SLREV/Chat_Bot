// Package store keeps transcripts as append-only NDJSON, one file per thread.
//
// RAM note: the in-memory cache is a fixed-size ring (default 200 messages per
// thread). Older history stays on disk and is paged in on demand, so a laptop
// with 4 GB never holds a whole conversation log in memory.
package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/milind-soni/openmausbot-go/internal/config"
)

// Role values used in a transcript.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
	RoleTool      = "tool"
)

// Message is one line in a thread.
type Message struct {
	ID     string    `json:"id"`
	Thread string    `json:"thread"`
	Role   string    `json:"role"`
	Text   string    `json:"text"`
	TS     time.Time `json:"ts"`
	Engine string    `json:"engine,omitempty"`
	Model  string    `json:"model,omitempty"`
	Ms     int64     `json:"ms,omitempty"`
	Note   string    `json:"note,omitempty"`
	Err    string    `json:"error,omitempty"`
}

// Thread is the index entry shown in the sidebar.
type Thread struct {
	BotID     string    `json:"botId"`
	UpdatedAt time.Time `json:"updatedAt"`
	Count     int       `json:"count"`
	Preview   string    `json:"preview"`
}

const ringSize = 200

type ring struct {
	mu   sync.RWMutex
	msgs []Message
}

func (r *ring) add(m Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.msgs) == ringSize {
		copy(r.msgs, r.msgs[1:])
		r.msgs[len(r.msgs)-1] = m
		return
	}
	r.msgs = append(r.msgs, m)
}

func (r *ring) list() []Message {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Message, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// Store owns the data directory layout:
//
//	<data>/config.json
//	<data>/threads/<botId>.ndjson
//	<data>/threads/<botId>.meta.json
type Store struct {
	root   string
	mu     sync.Mutex
	rings  map[string]*ring
	meta   map[string]Thread
	subs   map[chan Message]struct{}
	subsMu sync.RWMutex
}

// Open prepares the data directory and warms the thread index.
func Open() (*Store, error) {
	root := filepath.Join(config.DataDir(), "threads")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		root:  root,
		rings: make(map[string]*ring),
		meta:  make(map[string]Thread),
		subs:  make(map[chan Message]struct{}),
	}
	s.loadIndex()
	return s, nil
}

// Path is the transcript file for a thread.
func (s *Store) Path(botID string) string {
	return filepath.Join(s.root, safe(botID)+".ndjson")
}

func (s *Store) metaPath(botID string) string {
	return filepath.Join(s.root, safe(botID)+".meta.json")
}

// safe keeps a bot id from escaping the data directory.
func safe(id string) string {
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, id)
	if clean == "" {
		return "bot"
	}
	return clean
}

// Append writes a message to disk and to the hot cache, then fans it out to
// every live SSE subscriber.
func (s *Store) Append(m Message) (Message, error) {
	if m.ID == "" {
		m.ID = config.NewID()
	}
	if m.TS.IsZero() {
		m.TS = time.Now()
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m, err
	}

	s.mu.Lock()
	f, err := os.OpenFile(s.Path(m.Thread), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.mu.Unlock()
		return m, err
	}
	_, err = f.Write(append(raw, '\n'))
	cerr := f.Close()
	if err != nil {
		s.mu.Unlock()
		return m, err
	}
	if cerr != nil {
		s.mu.Unlock()
		return m, cerr
	}

	r := s.rings[m.Thread]
	if r == nil {
		if msgs, terr := tailLines(s.Path(m.Thread), ringSize); terr == nil && len(msgs) > 0 {
			r = &ring{msgs: msgs}
		} else {
			r = &ring{msgs: []Message{m}}
		}
		s.rings[m.Thread] = r
	} else {
		r.add(m)
	}
	t := s.meta[m.Thread]
	t.BotID = m.Thread
	t.UpdatedAt = m.TS
	t.Count++
	t.Preview = oneLine(m.Text)
	s.meta[m.Thread] = t
	s.mu.Unlock()

	s.publish(m)
	return m, nil
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:117]) + "…"
	}
	return s
}

// Recent returns the hot cache for a thread (last ringSize messages).
func (s *Store) Recent(botID string) []Message {
	s.mu.Lock()
	r := s.rings[botID]
	if r != nil {
		s.mu.Unlock()
		return r.list()
	}
	// First touch: read the tail of the file under lock so concurrent Appends
	// cannot race with ring population.
	msgs, err := tailLines(s.Path(botID), ringSize)
	if err != nil {
		s.mu.Unlock()
		return nil
	}
	nr := &ring{msgs: msgs}
	s.rings[botID] = nr
	s.mu.Unlock()
	return nr.list()
}

// History pages through the whole transcript, newest page first.
// offset counts messages from the end of the file.
func (s *Store) History(botID string, limit, offset int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	want := limit + offset
	if want < limit {
		want = limit
	}
	all, err := tailLines(s.Path(botID), want)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Message{}, nil
		}
		return nil, err
	}
	// all is ordered oldest→newest for the requested window.
	end := len(all) - offset
	if end <= 0 {
		return []Message{}, nil
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	out := make([]Message, end-start)
	copy(out, all[start:end])
	return out, nil
}

// Threads lists per-bot index entries sorted by recency.
func (s *Store) Threads() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Thread, 0, len(s.meta))
	for _, t := range s.meta {
		out = append(out, t)
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.After(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Clear truncates a thread's transcript.
func (s *Store) Clear(botID string) error {
	s.mu.Lock()
	delete(s.meta, botID)
	delete(s.rings, botID)
	s.mu.Unlock()
	_ = os.Remove(s.metaPath(botID))
	if err := os.Remove(s.Path(botID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Subscribe returns a channel of live messages plus a cancel func.
func (s *Store) Subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 64)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
	return ch, func() {
		s.subsMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subsMu.Unlock()
	}
}

func (s *Store) publish(m Message) {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for ch := range s.subs {
		select {
		case ch <- m:
		default: // a stalled client must never block a turn
		}
	}
}

func (s *Store) loadIndex() {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".ndjson")
		s.meta[id] = Thread{BotID: id, UpdatedAt: info.ModTime()}
	}
}

// tailLines reads at most n trailing JSON lines from a file without loading
// whole large files into memory.
func tailLines(path string, n int) ([]Message, error) {
	if n <= 0 {
		return []Message{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read the file backwards in 32 KiB chunks until we have enough valid
	// messages or hit the start of the file.
	const chunk = 32 << 10
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	pos := stat.Size()
	if pos == 0 {
		return []Message{}, nil
	}

	var chunks [][]byte
	totalBytes := 0
	newlines := 0
	target := n + 1

	for pos > 0 {
		for pos > 0 && newlines <= target {
			step := int64(chunk)
			if pos-step < 0 {
				step = pos
			}
			pos -= step
			block := make([]byte, step)
			if _, err := f.ReadAt(block, pos); err != nil {
				return nil, err
			}
			chunks = append(chunks, block)
			totalBytes += len(block)
			for _, b := range block {
				if b == '\n' {
					newlines++
				}
			}
		}

		out := parseTailChunks(chunks, totalBytes, pos > 0)
		if len(out) >= n || pos == 0 {
			if len(out) > n {
				out = out[len(out)-n:]
			}
			return out, nil
		}
		// Blank or torn lines meant fewer than n valid messages; read more.
		target += (n - len(out)) + 4
	}

	out := parseTailChunks(chunks, totalBytes, false)
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

func parseTailChunks(chunks [][]byte, totalBytes int, dropPartialFirstLine bool) []Message {
	buf := make([]byte, 0, totalBytes)
	for i := len(chunks) - 1; i >= 0; i-- {
		buf = append(buf, chunks[i]...)
	}
	s := string(buf)
	if dropPartialFirstLine {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		} else {
			return nil
		}
	}
	out := []Message{}
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue // a torn last line from a crash is skipped, not fatal
		}
		out = append(out, m)
	}
	return out
}
