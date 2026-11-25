package config

import "testing"

func TestKeyStatus(t *testing.T) {
	cloud := Provider{ID: "openai", Name: "OpenAI", BaseURL: "https://api.openai.com/v1"}
	local := Provider{ID: "ollama", Name: "Ollama", BaseURL: "http://127.0.0.1:11434/v1"}

	// No key at all.
	if st, _ := KeyStatus(cloud); st != KeyMissing {
		t.Fatalf("cloud without a key should be %q, got %q", KeyMissing, st)
	}
	if st, _ := KeyStatus(local); st != KeyNotNeeded {
		t.Fatalf("a local endpoint needs no key, got %q", st)
	}

	// Env indirection: this is the case that produced "api key tidak tersedia".
	p := cloud
	p.APIKey = "$OMB_TEST_KEY_NOT_SET"
	if st, env := KeyStatus(p); st != KeyEnvEmpty || env != "OMB_TEST_KEY_NOT_SET" {
		t.Fatalf("unset env should be %q with the name, got %q/%q", KeyEnvEmpty, st, env)
	}
	if p.Active() {
		t.Fatal("a provider pointing at an empty env var is not active")
	}

	t.Setenv("OMB_TEST_KEY_SET", "sk-test-value")
	p.APIKey = "$OMB_TEST_KEY_SET"
	if st, env := KeyStatus(p); st != KeyOK || env != "OMB_TEST_KEY_SET" {
		t.Fatalf("set env should be ok, got %q/%q", st, env)
	}
	if !p.Active() {
		t.Fatal("a provider with a real env key is active")
	}

	// A literal key is always ok.
	q := cloud
	q.APIKey = "sk-literal"
	if st, _ := KeyStatus(q); st != KeyOK || !q.Active() {
		t.Fatalf("literal key should be ok, got %q", st)
	}
}
