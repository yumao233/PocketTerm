# Webshell Test Suite

Webshell tests are split into two layers: backend Go unit tests (white-box) + Node.js integration tests (black-box end-to-end).
These tests cover **all HTTP routes and WebSocket routes** of the backend and run through one complete business chain.

## One-Click Run

From the project root:

```bash
npm test                 # Chains: build backend → Go unit tests → Node integration tests
# or directly
./tests/run-all.sh
./tests/run-all.sh --no-go   # Skip Go unit tests, only build + Node integration
```

`run-all.sh` has three steps; any failure exits non-zero immediately, unattended end-to-end, ready for CI.

Run one layer alone:

```bash
npm run test:backend        # cd backend && go test ./...
npm run test:integration    # Node integration tests (serial)
cd tests && node --test full-lifecycle.test.js   # single file
```

## Backend Route Overview (Test Coverage Targets)

### HTTP Routes (gin)

| Method | Path | Auth | Description |
|------|------|------|------|
| GET  | `/healthz` | none | Health check |
| GET  | `/api/v1/check-setup` | none | Whether system is initialized |
| POST | `/api/v1/setup` | none | Register first admin, returns JWT |
| POST | `/api/v1/login` | none | Username/password login, returns JWT |
| POST | `/api/v1/sso_login` | none | SSO one-time token for JWT (supports body / query / redirect) |
| POST | `/api/v1/logout` | Bearer | Stateless logout |
| GET  | `/api/v1/me` | Bearer | Current user |
| GET/POST | `/api/v1/templates` | Bearer | Template list/create |
| GET/PUT/DELETE | `/api/v1/templates/:id` | Bearer | Template detail/update/delete |
| GET/POST | `/api/v1/sessions` | Bearer | Session list/start |
| GET/DELETE | `/api/v1/sessions/:id` | Bearer | Session detail/destroy |
| POST | `/api/v1/sso/tokens` | Bearer(admin) | Mint SSO one-time token |

### WebSocket Routes

| Path | Auth | Frames |
|------|------|----|
| `GET /api/v1/ws/:id?token=<jwt>` | query-token | Data frames (raw terminal bytes) / control frames (`resize`, `close`); server sends `{"type":"closed"}` |

## Error Codes (envelope `code`)

| code | Meaning |
|------|------|
| 0 | Success |
| 1001 | System not initialized |
| 1002 | System already initialized (duplicate setup) |
| 1003 | Invalid credentials (login failed / SSO token invalid or used) |
| 1004 | Unauthenticated (missing/bad JWT) |
| 1005 | Forbidden |
| 1006 | Resource not found |
| 1007 | Invalid parameter |
| 2001 | Template name duplicate |
| 3001 | Session not found / not owned by user |
| 3002 | Session closed |
| 5000 | Internal error |

## File Organization

| File | Responsibility |
|------|------|
| `helpers.js` | Backend lifecycle management (random port, random JWT secret, temp working directory) + HTTP/WS common utilities |
| `full-lifecycle.test.js` | **Full-chain integration**: register→login→create template→start session→WS→rapid resize→input command→check output→destroy→verify destroy success→idempotent |
| `integration.test.js` | Existing e2e main chain + three independent boundary cases (unauthenticated/duplicate name/SSO reuse/healthz) |
| `ws-frames.test.js` | WS frame ambiguity classification (data frame / resize / close / illegal `{` / binary) |
| `auth-boundary.test.js` | setup/login/me boundaries, JWT forgery and tampering, check-setup semantics |
| `template-boundary.test.js` | Template CRUD boundaries, default command backfill, duplicate name 2001, unauthenticated rejection |
| `session-boundary.test.js` | Session params/not-found/natural-exit/WS rejection/list ownership |
| `ownership.test.js` | SSO identity and admin identity consistency, session ownership |
| `sso-boundary.test.js` | Mint/login/reuse/redirect variants/query and body equivalence |
| `security.test.js` | End-to-end security: password_hash non-leak across four identity paths (setup/login/sso_login/me), JWT `alg=none` forgery, payload tampering, cross-backend secret isolation, stateless logout (no blacklist), already-expired token, forged WS query token |
| `routes-cors.test.js` | Unknown route 404, OPTIONS preflight through the full middleware chain (Auth not triggered), CORS three-branch response headers (wildcard / specific + Vary + credentials / no-match), `/system/stats` endpoint shape + auth guard, non-JSON body → 1007 |
| `ws-advanced.test.js` | Multi-subscriber fan-out, disconnect≠Kill with HTTP `/sessions` list confirmation + reconnect, resize missing-field/type-error degradation (then a real resize still applies), unknown control type → data, WS to a non-existent session id → 1006 |

## Isolation and Stability

- Each `describe` starts an **independent backend instance** (independent temp directory, random HTTP/SSH port, random JWT secret), no cross-pollution.
- `--test-concurrency=1` serial execution: avoids port collisions and SSH process buildup across multiple backends under high concurrency.
- `scrubMacBinary` processes each backend binary only once (in-process memoized), preventing concurrent `codesign --force` from corrupting the Mach-O header and causing `ENOEXEC`.
- When the backend process exits abnormally, `startBackend` throws the captured stdout/stderr along with the error for diagnosis.