// Package config defines the Webshell runtime configuration model and the
// loading semantics for the JSON config file.
//
// The entry point is Load: it reads a config file from the given path and,
// when the file does not exist, generates a default configuration with fresh
// randomly generated secrets (jwt.secret, ssh.passcode), writes it back to the
// path atomically (temp file + rename) and returns the loaded value.
//
// JSON field names use snake_case (see struct tags). The on-disk layout matches
// the example in config.example.json.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the top-level configuration object. It is the value returned by
// Load and Default.
type Config struct {
	Server  ServerConfig  `json:"server"`
	SSH     SSHConfig     `json:"ssh"`
	JWT     JWTConfig     `json:"jwt"`
	Storage StorageConfig `json:"storage"`
	CORS    CORSConfig    `json:"cors"`
}

// ServerConfig holds the HTTP server bind parameters and optional TLS.
type ServerConfig struct {
	Host string    `json:"host"` // bind address, e.g. "0.0.0.0"
	Port int       `json:"port"` // bind port, e.g. 8080
	SSL  SSLConfig `json:"ssl"`
}

// SSLConfig toggles HTTPS and points to the PEM-encoded cert/key files.
type SSLConfig struct {
	Enabled  bool   `json:"enabled"`   // whether HTTPS is active
	CertFile string `json:"cert_file"` // path to TLS certificate file
	KeyFile  string `json:"key_file"`  // path to TLS private key file
}

// SSHConfig holds the optional embedded SSH server parameters used for the
// passcode-gated administration channel.
type SSHConfig struct {
	Listen         string `json:"listen"`           // SSH listen address, e.g. "127.0.0.1:2222"
	Passcode       string `json:"passcode"`         // hex-encoded passcode required to connect
	HostSignerFile string `json:"host_signer_file"` // optional path to a host key file
}

// JWTConfig holds the signing secret and token lifetime.
type JWTConfig struct {
	Secret     string `json:"secret"`      // HMAC signing secret (hex)
	TTLMinutes int    `json:"ttl_minutes"` // token lifetime in minutes
}

// StorageConfig points at the on-disk JSON storage directory.
type StorageConfig struct {
	Dir string `json:"dir"` // path to the data directory, e.g. "./data"
}

// CORSConfig lists the allowed Origin values for cross-origin requests.
type CORSConfig struct {
	AllowedOrigins []string `json:"allowed_origins"` // e.g. ["*"] or ["https://app.example.com"]
}

// Default returns a Config populated with sensible defaults. The jwt.secret and
// ssh.passcode are freshly generated on every call using crypto/rand, so each
// invocation yields distinct secrets. Default does NOT write to disk; use Load
// when you want the persisted form.
func Default() (*Config, error) {
	jwtSecret, err := randomHex(32)
	if err != nil {
		return nil, fmt.Errorf("config: generate jwt secret: %w", err)
	}
	passcode, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("config: generate ssh passcode: %w", err)
	}
	return &Config{
		Server: ServerConfig{
			Host: "0.0.0.0",
			Port: 8080,
			SSL:  SSLConfig{Enabled: false, CertFile: "", KeyFile: ""},
		},
		SSH: SSHConfig{
			Listen:         "127.0.0.1:2222",
			Passcode:       passcode,
			HostSignerFile: "",
		},
		JWT: JWTConfig{
			Secret:     jwtSecret,
			TTLMinutes: 1440,
		},
		Storage: StorageConfig{Dir: "./data"},
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
	}, nil
}

// Load reads the configuration file at path. If the file does not exist, a
// default configuration is generated (see Default), marshaled with indentation
// and written atomically to path (a temp file in the same directory is written
// then os.Rename'd over the target so the final file is never partially
// written). The loaded configuration is then returned.
//
// If the file exists it is decoded as-is; no secret regeneration occurs.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		// File exists: decode it.
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("config: decode %s: %w", path, err)
		}
		return &cfg, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	// File missing: generate defaults and persist.
	cfg, err := Default()
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, cfg); err != nil {
		return nil, fmt.Errorf("config: write %s: %w", path, err)
	}
	return cfg, nil
}

// writeFileAtomic marshals cfg as indented JSON and writes it to path via a
// temp file in the same directory followed by os.Rename, ensuring the target
// file is either fully written or untouched. The parent directory is created
// if needed.
func writeFileAtomic(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if any step below fails; a successful rename will
	// have already moved the file so Remove becomes a no-op.
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp -> %s: %w", path, err)
	}
	return nil
}

// randomHex returns n random bytes hex-encoded (2*n characters). It uses
// crypto/rand so the output is suitable for secrets.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
