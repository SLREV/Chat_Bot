package config

import (
	"os"
	"strings"
)

// Key states, in the order the UI shows them.
const (
	KeyOK        = "ok"         // a usable credential is in hand
	KeyEnvEmpty  = "env_empty"  // points at an env var that is not set
	KeyMissing   = "missing"    // needs a credential, has none
	KeyNotNeeded = "not_needed" // local endpoint (Ollama & friends)
)

// KeyStatus reports whether a provider can authenticate right now, without ever
// returning the secret. This is what the UI and `omb doctor` show, because
// "hasKey: true" alone is misleading when the key is an unset env var.
func KeyStatus(p Provider) (state string, envName string) {
	if p.APIKey == "" {
		if p.HasKeyNeeded() {
			return KeyMissing, ""
		}
		return KeyNotNeeded, ""
	}
	if p.IsEnvKey() {
		envName = strings.TrimPrefix(p.APIKey, "$")
		if strings.TrimSpace(os.Getenv(envName)) == "" {
			return KeyEnvEmpty, envName
		}
		return KeyOK, envName
	}
	return KeyOK, ""
}

// Active is the one-line summary the UI needs.
func (p Provider) Active() bool {
	state, _ := KeyStatus(p)
	return state == KeyOK || state == KeyNotNeeded
}
