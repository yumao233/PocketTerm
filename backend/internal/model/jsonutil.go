package model

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteJSON marshals v as indented JSON and writes it to path atomically:
// the bytes are written to a temp file in the SAME directory as path and then
// os.Rename'd over it. Because rename is atomic on the same filesystem, the
// target file is either fully replaced or untouched — it never appears in a
// half-written state even if the process is killed mid-write. The parent
// directory is created with 0o755 perms if missing. The final file is chmod'd
// to 0o600 to protect potentially sensitive contents (e.g. password hashes,
// SSO plaintext passwords).
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("model: marshal %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("model: mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".json-*.tmp")
	if err != nil {
		return fmt.Errorf("model: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if any step below fails; after a successful rename
	// the file has moved so Remove is a no-op.
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("model: write temp %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("model: close temp %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("model: chmod temp %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("model: rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
