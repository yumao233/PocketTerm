# Pocket Term

[简体中文](README.zh-CN.md) | English

[![Status](https://img.shields.io/badge/Go-v1.24.4-blue.svg)](https://go.dev/)
[![Status](https://img.shields.io/badge/Node-v22.16.0-blue.svg)](https://nodejs.org/en/download/)
[![Status](https://img.shields.io/badge/License-Apache%202.0-red.svg)](https://github.com/MCSManager)

A minimal, lightweight server management application written in Go. It lets you manage multiple Shell windows simultaneously in the browser, where each Shell is a **real local SSH service**, giving you quick and easy access to your servers from your phone, laptop, or tablet.

<img alt="Image" src="./docs/index.webp" />

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

## Quick Start

```bash
# Build the binary
cd backend
go build -o bin/webshell ./cmd/webshell

# Run (auto-generates config.json and data/ on first run)
./bin/webshell
# → webshell listening on 0.0.0.0:8080 (ssl=false), ssh backend on 127.0.0.1:2222

# Access the web panel
curl -s http://127.0.0.1:8080
```

## Tech Stack

- Go 1.24+ (built with the 1.25 toolchain, auto-resolved)
- [Gin](https://github.com/gin-gonic/gin) HTTP framework
- [gorilla/websocket](https://github.com/gorilla/websocket) WebSocket
- [gliderlabs/ssh](https://github.com/gliderlabs/ssh) local SSH server
- [golang.org/x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) SSH client
- [golang-jwt/jwt/v5](https://github.com/golang-jwt/jwt/v5) JWT
- [creack/pty](https://github.com/creack/pty) server-side PTY allocation
- bcrypt password hashing; `google/uuid` for ID generation
- Testing: Go `testing` + Node.js (`ws` + built-in `fetch`)

## Configuration (`config.json`, auto-generated in the run directory)

```json
{
  "server": {
    "host": "0.0.0.0",
    "port": 8080,
    "ssl": { "enabled": false, "cert_file": "", "key_file": "" }
  },
  "ssh": {
    "listen": "127.0.0.1:2222",
    "passcode": "<auto>",
    "host_signer_file": ""
  },
  "jwt": { "secret": "<auto>", "ttl_minutes": 1440 },
  "storage": { "dir": "./data" },
  "cors": { "allowed_origins": ["*"] }
}
```

### SSO Quick Login

1. An admin calls `POST /api/v1/sso/tokens {username}` to generate a one-time token → returns `{uuid, password, username}` (returned only once).
2. An external system uses `POST /api/v1/sso_login?uuid=<uuid>&password=<pwd>` (or `?redirect=1`) to directly enter the logged-in state.
3. The token is single-use and invalidated after use; reuse returns `1003`.

## Testing

### Go Unit / Integration Tests

```bash
cd backend
go test ./...                 # all packages
go test -race ./...           # with race detection (PTY / store concurrency)
```

Coverage: config loading, auth (JWT + bcrypt), model concurrent store, local SSH server (including the natural-exit no-leak regression), PTY manager broadcast, WS frame parsing + bridging, service business logic, handler + middleware, router.

### Node.js End-to-End Tests

```bash
cd tests
npm install                   # install ws
npm test                      # equivalent to node --test
```

Full chain: `check-setup → setup → login → me → template CRUD → start terminal → WebSocket runs ping and receives PING → interactive echo → resize / close control frames → session status becomes closed → destroy → SSO mint / login / reuse-rejected / 302 redirect`. `ws-frames.test.js` verifies the control-frame / data-frame parsing contract against a real backend.

> **macOS sandbox / CI note**: `npm test` spawns the backend binary. In restricted environments (sandbox, some CI), macOS TCC may SIGKILL the child process due to the `com.apple.provenance` xattr. `helpers.js` best-effort clears the xattr and performs ad-hoc signing; if it still fails, run it in a normal interactive terminal, or run `cd backend && go build -o bin/webshell ./cmd/webshell` before `npm test`. You can also specify the binary via `WEBSHELL_TEST_BIN=/path/to/webshell npm test`.

## License

This project is licensed under the [MIT License](LICENSE), Copyright 2026 [yumao233](https://github.com/yumao233).
