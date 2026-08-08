// Package model defines the data model and the JSON file-backed store
// implementations used by the Webshell backend.
//
// All persistence is based on JSON files written atomically (temp file in the
// same directory followed by os.Rename) so that a crash never leaves a
// half-written file. Each store keeps an in-memory copy guarded by a single
// sync.RWMutex: reads take the read lock, writes take the write lock and
// flush to disk before releasing it.
package model

import "time"

// KV is a single environment-variable key/value pair used by Template.Env.
type KV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// User is a registered operator. The first registered user becomes the
// administrator (IsAdmin=true); subsequent registration requires an
// authenticated session.
type User struct {
	ID           string    `json:"id"`            // UUID
	Username     string    `json:"username"`      // unique
	PasswordHash string    `json:"password_hash"` // bcrypt
	IsAdmin      bool      `json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
}

// Template describes how a terminal session is launched: the command, its
// arguments, working directory and environment. Name is unique.
type Template struct {
	ID        string    `json:"id"`      // UUID
	Name      string    `json:"name"`    // unique
	Command   string    `json:"command"` // launch command, default "bash"
	Args      []string  `json:"args"`    // command arguments
	Cwd       string    `json:"cwd"`     // working directory, default "$HOME" or "/"
	Env       []KV      `json:"env"`     // environment variables
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Session records a terminal session's metadata. The actual PTY lifetime is
// managed in memory by the pty.Manager; this struct only persists status.
type Session struct {
	ID         string    `json:"id"` // UUID, used by the frontend as the WS channel id
	UserID     string    `json:"user_id"`
	TemplateID string    `json:"template_id"`
	Title      string    `json:"title"`  // display name, default template.name + index
	Status     string    `json:"status"` // running | closed
	CreatedAt  time.Time `json:"created_at"`
}

// SsoToken is a single-use, expiring pre-authenticated login token. An
// administrator pre-generates a token bound to a username and a plaintext
// one-time password; an external system presents both to /sso_login to obtain
// a JWT directly. Once used, the token is marked and cannot be reused.
type SsoToken struct {
	ID        string    `json:"id"` // UUID
	Username  string    `json:"username"`
	Password  string    `json:"password"` // plaintext, one-time
	Used      bool      `json:"used"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SystemStats is a real-time resource-usage snapshot of the host machine,
// surfaced to the frontend for the monitoring strip. All fields degrade to 0
// (or -1 for RTT) when the underlying collection fails; Stats never returns a
// non-OK code for partial collection failures.
type SystemStats struct {
	CpuPercent float64 `json:"cpu_percent"`  // 0-100 host-wide CPU utilization
	CpuCores   int     `json:"cpu_cores"`    // logical core count
	MemUsedGb  float64 `json:"mem_used_gb"`  // used memory, GB
	MemTotalGb float64 `json:"mem_total_gb"` // total memory, GB
	MemPercent float64 `json:"mem_percent"`  // 0-100
	RttMs      int64   `json:"rtt_ms"`       // min observable monotonic round-trip (ms), trend only
}
