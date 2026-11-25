package config

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// NewID returns a short random identifier acceptable in URLs and filenames.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is effectively fatal elsewhere; a fixed fallback
		// keeps the process alive rather than panicking mid-turn.
		return "bot-fallback"
	}
	return "bot-" + hex.EncodeToString(b[:])
}

// NewProviderID returns an id prefixed so a provider id is never mistaken for
// a bot id (they share one namespace in a bot's `engine` field).
func NewProviderID() string {
	return "prov-" + strings.TrimPrefix(NewID(), "bot-")
}

// Slug turns a display name into a filesystem-safe thread name.
func Slug(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			sb.WriteByte('-')
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "bot"
	}
	return out
}

func ensureID(b Bot) string {
	if b.ID != "" {
		return b.ID
	}
	return NewID()
}
