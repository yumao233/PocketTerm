# WebShell Backend

A Go-implemented Webshell backend: manage multiple Shell windows simultaneously in the browser, where each Shell is a real local SSH terminal session, and the browser interacts bidirectionally via WebSocket (supports running commands like `bash`, `ping`, etc.).

> Backend only. Frontend type contract at [`backend/web/types/api.d.ts`](backend/web/types/api.d.ts).

## Architecture

```
Browser ──HTTP/JSON──> Gin ──> handler ──> service ──> model.Store (JSON file)
   │                    │
   │                    └─(start terminal)──> pty.Manager.Start(template)
   │                                          │ SSH client (golang.org/x/crypto/ssh)
   │                                          ▼
   │                                  Local SSH server (gliderlabs/ssh, 127.0.0.1)
   │                                          │ PTY + shell ($SHELL / bash)
   │
   └──WebSocket──> Gin (upgrade) ──> ws.Conn ──> pty.Session (stdin/stdout bidirectional bridge)
```

### Layering (high cohesion, low coupling)

| Layer | Directory | Responsibility |
|---|---|---|
| Entry | `cmd/webshell/main.go` | Dependency wiring, start HTTP + SSH, graceful shutdown |
| Router | `internal/router` | Route registration, grouping, mounting middleware |
| Middleware | `internal/middleware` | Recovery / Logger / CORS / Auth / WSAuth |
| Handler | `internal/handler` | HTTP request parsing, response envelope (no business logic) |
| Service | `internal/service` | Business orchestration: auth / template / pty |
| PTY Core | `internal/pty` | Session pool + SSH client bridge + output broadcast |
| WebSocket | `internal/ws` | WS connection management, control-frame/data-frame classification |
| SSH | `internal/ssh` | Local SSH server (PTY interactive shell) |
| Data | `internal/model` | Types + Store interface + JSON file implementation |
| Config | `internal/config` | Config loading/defaults/auto-generation |
| Security | `internal/auth` | bcrypt password + JWT(HS256) |
| Utility | `pkg/response` | Unified response envelope + error codes |

Dependencies are strictly unidirectional: `router → handler → service → pty/ws/ssh → model`.

## Tech Stack

- Go 1.24+ (built with 1.25 toolchain auto-resolved)
- [Gin](https://github.com/gin-gonic/gin) HTTP framework
- [gorilla/websocket](https://github.com/gorilla/websocket) WebSocket
- [gliderlabs/ssh](https://github.com/gliderlabs/ssh) local SSH server
- [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) SSH client
- [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt/v5) JWT
- [creack/pty](https://github.com/creack/pty) server-side PTY allocation
- bcrypt password hashing; `google/uuid` ID generation
- Testing: Go `testing` + Node.js (`ws` + built-in `fetch`)

## Quick Start

```bash
# Build binary
cd backend
go build -o bin/webshell ./cmd/webshell

# Run (auto-generates config.json and data/ on first run)
./bin/webshell
# → webshell listening on 0.0.0.0:8080 (ssl=false), ssh backend on 127.0.0.1:2222

# Health check
curl -s http://127.0.0.1:8080/healthz
# {"code":0,"message":"ok","data":{"status":"ok"}}

# Initialized check
curl -s http://127.0.0.1:8080/api/v1/check-setup
```

On first visit `check-setup` returns `initialized=false`, and the frontend guides registration of an admin account:
```bash
curl -s -XPOST http://127.0.0.1:8080/api/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"your-password"}'
```

## Configuration (`config.json`, auto-generated in the run directory)

```json
{
  "server":  { "host": "0.0.0.0", "port": 8080,
               "ssl": { "enabled": false, "cert_file": "", "key_file": "" } },
  "ssh":     { "listen": "127.0.0.1:2222", "passcode": "<auto>", "host_signer_file": "" },
  "jwt":     { "secret": "<auto>", "ttl_minutes": 1440 },
  "storage": { "dir": "./data" },
  "cors":    { "allowed_origins": ["*"] }
}
```

- On first startup, `jwt.secret` and `ssh.passcode` are randomly generated automatically.
- **HTTPS/WSS**: fill in `server.ssl.cert_file` / `key_file` and set `enabled: true`, then restart (the HTTP server switches to `ListenAndServeTLS`, and WebSocket automatically upgrades to wss with TLS).
- The local SSH server listens only on `127.0.0.1` and is not exposed externally.
- `config.json` and `data/` are ignored in `.gitignore`.

## API Overview

All HTTP interfaces (except WS) return the unified envelope `{"code","message","data"}`, where `code=0` means success. See [`backend/web/types/api.d.ts`](backend/web/types/api.d.ts) for full field definitions.

### Public
| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Health check |
| GET | `/api/v1/check-setup` | Whether initialized |
| POST | `/api/v1/setup` | First admin registration (only when no admin exists) |
| POST | `/api/v1/login` | Username/password login → `{token, user}` |
| POST | `/api/v1/sso_login` | SSO quick login (`uuid`+`password`, query or body), `?redirect=1` → 302 `/?token=` |

### Authenticated (`Authorization: Bearer <jwt>`)
| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/logout` | Logout (stateless, client discards token) |
| GET | `/api/v1/me` | Current user |
| GET/POST | `/api/v1/templates` | Template list / create |
| GET/PUT/DELETE | `/api/v1/templates/:id` | Template detail/update/delete |
| GET/POST | `/api/v1/sessions` | Session list / start terminal |
| GET/DELETE | `/api/v1/sessions/:id` | Session detail / destroy |
| POST | `/api/v1/sso/tokens` | Generate one-time SSO token |
| GET | `/api/v1/ws/:id?token=<jwt>` | WebSocket (query-param auth) |

> `PUT /templates/:id` uses **full replacement** semantics; pass complete fields (consistent with `POST`).

### WebSocket Protocol
- **Browser → backend**: directly send string/bytes as PTY stdin (e.g. `ping -c 3 127.0.0.1\n`); control-frame JSON — `{"type":"resize","cols":80,"rows":24}`, `{"type":"close"}`.
- **Backend → browser**: terminal output is sent as raw bytes (BinaryMessage); on session end sends `{"type":"closed","reason":"..."}`.
- Classification rule: only when the first byte is `{` and it parses as a known control frame is it treated as control; otherwise it is always treated as terminal data (prevents xterm output containing `{` from being misparsed).

### SSO Quick Login
1. Admin calls `POST /api/v1/sso/tokens {username}` to generate a one-time token → returns `{uuid, password, username}` (returned only once).
2. External system uses `POST /api/v1/sso_login?uuid=<uuid>&password=<pwd>` (or `?redirect=1`) to directly enter logged-in state.
3. The token is one-time and invalidated after use; reuse returns `1003`.

## Testing

### Go unit/integration tests
```bash
cd backend
go test ./...                 # all packages
go test -race ./...           # with race detection (PTY/store concurrency)
```
Coverage: config loading, auth(JWT+bcrypt), model concurrent store, local SSH server (including natural-exit no-leak regression), PTY manager broadcast, WS frame parsing + bridge, service business, handler + middleware, router.

### Node.js end-to-end tests
```bash
cd tests
npm install                   # install ws
npm test                      # equivalent to node --test
```
Full chain: `check-setup → setup → login → me → template CRUD → start terminal → WebSocket execute ping receive PING → interactive echo → resize/close control frames → session status becomes closed → destroy → SSO mint/login/reuse-rejected/302 redirect`. And `ws-frames.test.js` verifies the control-frame/data-frame parsing contract via a real backend.

> **macOS sandbox/CI note**: `npm test` spawns the backend binary. When running in a restricted context (sandbox, some CI), macOS TCC may SIGKILL the child process due to `com.apple.provenance` xattr. `helpers.js` best-effort clears xattr and ad-hoc signs; if it still fails, run in a normal interactive terminal, or pre-run `cd backend && go build -o bin/webshell ./cmd/webshell` before `npm test`. You can also specify the binary via `WEBSHELL_TEST_BIN=/path/to/webshell npm test`.

## Acceptance

- ✅ `go build ./...` passes
- ✅ `go test ./...` all 12 packages pass
- ✅ Node.js full chain 27/27 assertions pass (register→login→template CRUD→start→WS interactive ping→destroy→SSO)
- ✅ `config.json` auto-generated when missing; SSL config set to true + certificate enables https/wss
- ✅ `backend/web/types/api.d.ts` is complete, every field includes JSDoc