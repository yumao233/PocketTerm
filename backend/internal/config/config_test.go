package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_GeneratesDefaultWhenMissing asserts that Load on a non-existent
// path creates the file with generated defaults and returns a usable Config.
func TestLoad_GeneratesDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}

	// File must have been created.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected config file created at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("config file is empty")
	}

	// Structural defaults.
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Server.Host = %q, want %q", cfg.Server.Host, "0.0.0.0")
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.SSL.Enabled {
		t.Errorf("Server.SSL.Enabled = true, want false")
	}
	if cfg.SSH.Listen != "127.0.0.1:2222" {
		t.Errorf("SSH.Listen = %q, want %q", cfg.SSH.Listen, "127.0.0.1:2222")
	}
	if cfg.JWT.TTLMinutes != 1440 {
		t.Errorf("JWT.TTLMinutes = %d, want 1440", cfg.JWT.TTLMinutes)
	}
	if cfg.Storage.Dir != "./data" {
		t.Errorf("Storage.Dir = %q, want %q", cfg.Storage.Dir, "./data")
	}
	if len(cfg.CORS.AllowedOrigins) != 1 || cfg.CORS.AllowedOrigins[0] != "*" {
		t.Errorf("CORS.AllowedOrigins = %v, want [\"*\"]", cfg.CORS.AllowedOrigins)
	}

	// Generated secrets must be present and non-empty, and hex-looking.
	if cfg.JWT.Secret == "" {
		t.Errorf("JWT.Secret is empty; expected generated value")
	}
	if len(cfg.JWT.Secret) != 64 { // 32 bytes -> 64 hex chars
		t.Errorf("JWT.Secret length = %d, want 64 hex chars", len(cfg.JWT.Secret))
	}
	if cfg.SSH.Passcode == "" {
		t.Errorf("SSH.Passcode is empty; expected generated value")
	}
	if len(cfg.SSH.Passcode) != 32 { // 16 bytes -> 32 hex chars
		t.Errorf("SSH.Passcode length = %d, want 32 hex chars", len(cfg.SSH.Passcode))
	}
}

// TestLoad_ReloadExisting verifies that a second Load on an existing file
// decodes the persisted values (no regeneration) and preserves the secrets.
func TestLoad_ReloadExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg1, err := Load(path)
	if err != nil {
		t.Fatalf("first Load: %v", err)
	}

	cfg2, err := Load(path)
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}

	// Secrets must be stable across reloads.
	if cfg2.JWT.Secret != cfg1.JWT.Secret {
		t.Errorf("JWT.Secret changed on reload: first=%q second=%q", cfg1.JWT.Secret, cfg2.JWT.Secret)
	}
	if cfg2.SSH.Passcode != cfg1.SSH.Passcode {
		t.Errorf("SSH.Passcode changed on reload: first=%q second=%q", cfg1.SSH.Passcode, cfg2.SSH.Passcode)
	}
	if cfg2.Server.SSL.Enabled {
		t.Errorf("Server.SSL.Enabled = true after reload, want false")
	}
	if cfg2.JWT.Secret == "" {
		t.Errorf("JWT.Secret empty after reload")
	}
}

// TestLoad_PersistsValidJSON confirms the generated file is valid JSON matching
// the documented field names (snake_case).
func TestLoad_PersistsValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("generated file is not valid JSON: %v", err)
	}

	// Top-level keys must use snake_case field names.
	for _, key := range []string{"server", "ssh", "jwt", "storage", "cors"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("generated JSON missing top-level key %q", key)
		}
	}
	server, _ := generic["server"].(map[string]any)
	if server == nil {
		t.Fatalf("server section not an object")
	}
	if _, ok := server["ssl"]; !ok {
		t.Errorf("generated JSON missing server.ssl")
	}
	ssh, _ := generic["ssh"].(map[string]any)
	if _, ok := ssh["host_signer_file"]; !ok {
		t.Errorf("expected snake_case key ssh.host_signer_file, got: %v", ssh)
	}
	jwt, _ := generic["jwt"].(map[string]any)
	if _, ok := jwt["ttl_minutes"]; !ok {
		t.Errorf("expected snake_case key jwt.ttl_minutes, got: %v", jwt)
	}
	cors, _ := generic["cors"].(map[string]any)
	if _, ok := cors["allowed_origins"]; !ok {
		t.Errorf("expected snake_case key cors.allowed_origins, got: %v", cors)
	}
}

// TestLoad_AtomicNoPartialFile ensures that even if the directory already
// existed, the file appears whole (the atomic write must not leave a temp file
// behind on success).
func TestLoad_AtomicNoLeftoverTemp(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed the directory so MkdirAll is exercised on existing dir.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "config.json")

	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".config-") && strings.HasSuffix(name, ".tmp") {
			t.Errorf("leftover temp file detected: %s", name)
		}
	}
}

// TestDefault_GeneratesDistinctSecrets verifies Default produces fresh,
// non-empty secrets on each call (crypto/rand backed).
func TestDefault_GeneratesDistinctSecrets(t *testing.T) {
	a, err := Default()
	if err != nil {
		t.Fatalf("Default (1): %v", err)
	}
	b, err := Default()
	if err != nil {
		t.Fatalf("Default (2): %v", err)
	}
	if a.JWT.Secret == "" || a.SSH.Passcode == "" {
		t.Fatalf("Default produced empty secret: jwt=%q passcode=%q", a.JWT.Secret, a.SSH.Passcode)
	}
	if a.JWT.Secret == b.JWT.Secret {
		t.Errorf("JWT.Secret identical across Default() calls; expected fresh randomness")
	}
	if a.SSH.Passcode == b.SSH.Passcode {
		t.Errorf("SSH.Passcode identical across Default() calls; expected fresh randomness")
	}
}
