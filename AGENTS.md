# WebShell Project Development Guidelines

> Single source of truth. `CLAUDE.md` includes this file via `@AGENTS.md`, shared by Claude Code / Codex / Cursor.
> **Read first: §1 layering, §8 PTY/WS/SSH core domain, §14 forbidden actions list**——violation causes bugs. Read §11 before changing HTTP interfaces/fields; run the §2 verification trio before committing.

## 0. Project Positioning

Go-implemented WebShell backend: manage multiple Shell windows on a web page simultaneously, where each Shell is a **real local SSH terminal session** (in-process `gliderlabs/ssh` server listening on `127.0.0.1`, backend dials in as an SSH client to obtain a PTY), with the browser bridged bidirectionally to the PTY over WebSocket.

- Stack: Go 1.25, Gin, gorilla/websocket, gliderlabs/ssh, golang.org/x/crypto/ssh, golang-jwt/jwt v5, creack/pty, google/uuid
- Persistence: JSON files (`backend/data/`), **no database introduced**
- Frontend contract: `backend/web/types/api.d.ts` (hand-maintained, fields aligned verbatim with Go json tags)

## 1. Architecture and Layering (hard constraint)

Dependencies are strictly unidirectional: `router → handler → service → pty/ws/ssh → model`; `model` does not depend backward on upper layers.

| Layer     | Directory             | Responsibility boundary                         |
| --------- | --------------------- | ----------------------------------------------- |
| Entry     | `cmd/webshell`        | Wire dependencies, start HTTP+SSH, graceful shutdown |
| Router    | `internal/router`     | The **only** layer that knows the URL shape     |
| Middleware | `internal/middleware` | Recovery / Logger / CORS / Auth / WSAuth        |
| Handler   | `internal/handler`    | Request binding + call service + response enveloping, **no business logic** |
| Service   | `internal/service`    | Business orchestration, invariants, ownership, JWT signing, sanitize |
| PTY       | `internal/pty`        | Session pool + SSH client bridge + output broadcast (**in-memory only**) |
| WebSocket | `internal/ws`         | WS connection management, control-frame/data-frame classification |
| SSH       | `internal/ssh`        | Local SSH server (PTY interactive shell)        |
| Data      | `internal/model`      | Types + Store interface + JSON file implementation |
| Config    | `internal/config`     | Config loading/defaults/atomic generation       |
| Security  | `internal/auth`       | bcrypt + JWT(HS256)                             |
| Utility   | `pkg/response`        | Unified response envelope + error codes         |

Layering iron rules:

- **handler does not directly access store / does not directly `auth.Sign` / does not directly operate `pty.Manager`** (WS upgrade is the exception, requires a live session).
- **service does not import `gin`/`handler`/`middleware`**, does not write HTTP. Return value fixed as `(data, code int, err error)`, `code` uses `response.ErrXxx` constants.
- **store implementation does not import `response`**. `model.ErrNotFound`/`ErrDuplicate` and `response.ErrXxx` are two separate systems; service is responsible for translating.
- Store interface lives in `model/store.go`, implementation in `*_store.go`, separated to allow mocking.

## 2. Development + Test Commands

```bash
npm test                      # One-click full run (build → Go unit tests → Node integration), non-zero on any failure, CI-friendly
./tests/run-all.sh --no-go    # Skip Go unit tests
npm run test:backend          # cd backend && go test ./...
npm run test:integration      # cd tests && node --test --test-concurrency=1
cd backend && go test -race ./...    # PTY/store concurrency must use -race

cd backend && go build -o bin/webshell ./cmd/webshell && ./bin/webshell   # First run auto-generates config.json + data/
npm run dev                   # backend:8080 + frontend:5173
curl -s http://127.0.0.1:8080/healthz   # {"code":0,"message":"ok","data":{"status":"ok"}}
```

**Must do before committing**: `go build ./...` + `go test ./...` + `npm test` (or at least `npm run test:integration`) all green.

## 3. Response Envelope and Error Codes

All non-WS HTTP endpoints return **HTTP 200** + `{code, message, data}`. HTTP status is decoupled from business code; business errors use `code != 0`, HTTP is always 200, `code=0` means success.

- `response.OK` / `OKMsg` / `Fail(c, code, msg)` / `FailData(c, code, msg, data)` (see `pkg/response/response.go`).
- Exceptions: `Recovery` writes panics with HTTP 500 (`AbortWithStatusJSON`, **does not call `Fail`**); SsoLogin with `redirect=1` returns 302 on success.
- **Register new error codes in two places**: `ErrXxx` constants in `response.go` + `ErrorCode` enum in `web/types/api.d.ts` (copy numeric values verbatim).

| Constant                   | Value | Trigger scenario                                              |
| -------------------------- | ----- | ------------------------------------------------------------ |
| `ErrOK`                    | 0     | Success                                                      |
| `ErrNotInitialized`        | 1001  | No admin at login time                                       |
| `ErrAlreadyInitialized`    | 1002  | Admin already exists at setup time                           |
| `ErrInvalidCredentials`    | 1003  | login password wrong / any SSO failure (intentionally conflated to prevent enumeration) |
| `ErrUnauthenticated`       | 1004  | Bearer/WS token missing or invalid                           |
| `ErrForbidden`             | 1005  | Authenticated but no permission (non-admin calling SSO minting, accessing another user's session) |
| `ErrNotFound`              | 1006  | Resource not found                                           |
| `ErrInvalidParam`          | 1007  | Binding failure or field validation failure                  |
| `ErrTemplateNameDuplicate` | 2001  | Template name duplicate (checked on both create/update)      |
| `ErrSessionNotFound`       | 3001  | session does not exist / does not belong to current user     |
| `ErrSessionClosed`         | 3002  | Live PTY has exited at WS upgrade time                       |
| `ErrInternal`              | 5000  | Internal error; same code used when Recovery writes a panic  |

## 4. HTTP Handler Paradigm (copy this for new handlers)

1. At file top, unexported `xxxReq`/`xxxResp`; json tags snake_case aligned with model.
2. `c.ShouldBindJSON(&req)`; on failure `Fail(ErrInvalidParam, "invalid request body")` + **`return`** (`ShouldBindJSON` does not abort; missing return writes a second response causing gin panic).
3. Get identity via `ctxUser(c)` (`handler/session.go`); context keys via `middleware.CtxXxxKey()` accessors, **do not hardcode strings**.
4. Call service to get `(data, code, err)`; `err != nil` → `Fail(c, code, err.Error())` + return; on success `OK(c, data)`, for pure actions use `OKMsg(c, "...", nil)` (Logout/Delete).
5. admin-only: assert `c.Get("is_admin")` as bool, non-true → `Fail(ErrForbidden)+Abort()`. **`is_admin` comes from JWT `adm` claim (frozen at login time), trust the token only, do not reverse-lookup store**.

## 5. Routes and Middleware

- Engine-level middleware order: `Recovery → Logger → CORS`.
- Three groups: public (`/healthz`, `/check-setup`, `/setup`, `/login`, `/sso_login`), `authed` (Bearer JWT), WS.
- **WS route `/api/v1/ws/:id` is registered outside the `authed` group**, mounting only `WSAuth` (otherwise Auth rejects first due to missing Bearer).
- `Auth` reads `Authorization: Bearer <token>` (case-insensitive); `WSAuth` reads query `token` (browser WS upgrade cannot set headers). Both on failure do `Fail(ErrUnauthenticated)+Abort`, injecting the same three context keys.
- CORS three states: contains `"*"` → echo `*` without credentials; specific origin matches → echo that origin + `Allow-Credentials: true` + `Vary: Origin`; no match → no headers. Changing CORS requires testing all three branches.

## 6. Authentication (JWT / Password / SSO)

- JWT claims: `uid`/`usr`/`adm` + embedded `RegisteredClaims` (`exp`/`iat`/`sub`). `Sign` is fixed **HS256**; `Parse`'s `keyFunc` enforces `*jwt.SigningMethodHMAC` validation to prevent `alg=none` forgery.
- Secret/TTL come from `config.json` (`jwt.secret` 32 random bytes → 64 hex; `jwt.ttl_minutes` default 1440), injected via `NewAuthService`, **not hardcoded in service / read from env**.
- Password bcrypt cost=10. `CheckPassword` returns false on any mismatch/error; callers **must not use error to distinguish "hash corrupted" from "password wrong"**. login maps both "user not found" and "password wrong" to `ErrInvalidCredentials` (prevents enumeration).
- **Logout is a stateless no-op**, no server-side blacklist, still returns `OKMsg` so frontend can judge via `code==0`.
- **SSO one-time token** TTL=5min; plaintext password generated by `crypto/rand` (`randomHex`), returned only once. Validation goes through `tokens.Consume(uuid)`——the only TOCTOU-safe primitive, completing used+expiry check+mark+persist within a single `Lock`. **Password validation happens after Consume** (wrong password "burns" the token, prevents brute-force, do not change the order); all failures uniformly return `ErrInvalidCredentials`, exposing no used/expired/not-found/bad-pwd differences. Expiry cleanup runs once at `main.go` startup + an hourly ticker, do not stuff it into the SsoLogin hot path.
- **setup guard is at service layer** (`users.Count()==0`), handler adds no guard; the first user gets `IsAdmin=true`. `CheckSetup` returns `count==0`, handler inverts to `Initialized: !initialized`——**this inversion is a contract**, changes must be synced with frontend.
- `SsoLogin` params `uuid`/`password` may come from query or JSON body, **query takes precedence** (`firstNonEmpty`); `redirect=1` returns 302 `/?token=<jwt>` on success, the only non-envelope success response.

## 7. Data Storage (model / Store)

- Each store holds a `sync.RWMutex`, read `RLock`/write `Lock`. **Write operations complete mutate+persist within the same `Lock` range**, "mutate memory first then persist separately" is forbidden. Persist function named `persistLocked`, caller must already hold the lock.
- **Atomic persist** goes through `atomicWriteJSON` (jsonutil.go): `MarshalIndent` → same-directory `CreateTemp` → `Write` → `Chmod 0o600` → `os.Rename`. Same directory is the prerequisite for atomic rename, `0o600` protects password hash / SSO plaintext.
- `Get*`/`List` return **value copies** (`cp := *u`), preventing lock-piercing memory mutation.
- JSON fields snake_case, `time.Time` serialized as ISO8601; `atomicWriteJSON` uses 2-space indent.
- **Store Delete is idempotent** (deleting non-existent returns nil), so service Delete must **first do a GetByID existence check** before it can return `ErrNotFound`.
- Template **PUT full replacement** (no PATCH), fields not passed are overwritten by zero values; `CreatedAt` is preserved from existing record, only `UpdatedAt` is refreshed; name uniqueness excludes self (`dup.ID != t.ID`). Empty `Command` defaults to `"bash"` (injected by service); `ID`/`CreatedAt`/`UpdatedAt` are always generated on the service side.

## 8. PTY / WebSocket / SSH Core Domain (violation causes bugs)

> Detailed prohibitions in §14; this section explains **why** and positive essentials.

### PTY Manager (`internal/pty`)

- **In-memory only**: session pool is just `Manager.sessions map` (commit `a8ea411` removed session storage). On session end immediately `delete`, no ghost left; after restart the pool is empty, "restoring historical sessions" must be emulated in frontend/outer layer.
- **Register into pool before Start/Shell**: register into the pool **before** `sess.Start`/`Shell` (x/crypto/ssh spawns a reader goroutine inside Start; assigning writer later causes a data race); failure path must `delete`.
- `Kill`: `delete` first then `Close`. `CloseAll`: **swap in a new map first, then iterate to close** (concurrent with pump's delete writing the same map).
- `manager.Kill` returns `ErrSessionNotFound` for an id not in the pool. **Service `Kill` gates on `Get` first**, so in the pure in-memory model a session that has already ended is gone from the pool → `Get` returns `ErrNotFound` → the common "click Kill after natural exit" / "second DELETE" case surfaces **1006, not 0**. The `ErrSessionNotFound → ErrOK` branch only fires in the narrow race where the session exits *between* `Get` and `manager.Kill` (treat as success — the PTY is already tearing down). This is consistent with the §14 rule "DELETE without existence check → frontend can't get NotFound": because "already ended" is indistinguishable from "never existed" in memory, there is **no** idempotent "return 0" contract for already-ended sessions.
- `RequestPty(term, rows, cols, modes)`: **rows first, cols second** (swap makes cols=24, TUI garbled until first resize).
- `sess.Setenv("WEBSESSION_ID", sessionID)` must be set: it is the unique key for the server-side resizer registry; missing it degrades resize to no-op.
- **Multiple conn subscribers** to the same session (each a buffered chan cap=64). `broadcast` first snapshots subscribers, then **non-blocking** send outside the lock; full channel drops frames + `log.Printf`——slow clients must not stall the pump and affect all subscribers. `safeSend` uses recover to guard against panic when chan is Close after snapshot.
- Cwd/Args spliced into the shell must go through `shellQuote` single-quote escaping (prevent injection).
- Before `Manager.Start`, **SSH server must already be listening** (assembly layer polls via `waitForListen`); `Server.Start` is blocking, must use `go srv.Start()`.

### SSH server (`internal/ssh`)

- **Listens on `127.0.0.1` only**. `PasswordHandler` only validates passcode, any username is accepted (audit only, not used as authz).
- **PTY resize dual-trigger mutual exclusion**: when `s.sizer != nil && s.ID != ""`, only call `sizer.Resize` and return. Reason: gliderlabs/ssh v0.3.8 pushes window-change into an internal cap=1 winch chan and never consumes it; the second request blocks `handleRequests` → blocks mux read loop → **freezes all stdin**. Regression test `manager_resize_stdin_test.go` guards this. Only when no sizer present, call `sess.WindowChange`.
- The `ssh.Pty` captured by `PtyCallback` must be a **value copy** (no `s.Pty()`, v0.3.8 mutates `sess.pty.Window` lock-free on window-change, causing a race). Before upgrading gliderlabs, run `-race` on `TestSSHServer_EchoInteractive` + `TestServeWS_Resize`.
- PTY Read/Write **no mutex** (locking once caused stdin writer to block on pty→client Read then deadlock; creack/pty `*os.File` concurrent R/W is safe at the kernel level).
- `teardown` uses `sync.Once`; both copy goroutines + both select branches go through teardown to close (avoids each close double-closing the fd). **`teardown(code)` must be called before `copyDone.Wait()`**, reversed order deadlocks (`TestSSHServer_NaturalExitDoesNotLeak`).
- `SysProcAttr{Setsid: true, Setctty: true}` may not be removed (otherwise SIGINT/Ctrl-C fails). Host key persistence uses tmp file + `Rename` atomic write, `0600`.
- `ptyHandle` has its own `mu` (separate from `Server.mu`; Resize is high-frequency and must not block Server.mu); any path touching `ptyFile.Fd()` goes through `ptyHandle.mu`.

### WebSocket (`internal/ws`) and frame protocol (`frames.go` + `api.d.ts`)

- **Single writer**: all `conn.WriteMessage` happens inside the writer goroutine (gorilla forbids concurrent writers, violation panics). New "active push" reuses the writer's select to add a case.
- reader's `ReadMessage` is **not context-aware**, unblocked by "writer closes conn → ReadMessage errors"; reader exits via `defer close(stop)` to unlock the writer.
- Close order: reader returns → `conn.Close()` (idempotent) → `writerDone.Wait()`. **ServeWS must not return before the writer has exited**, tail cleanup goes after `Wait()`.
- **Disconnect ≠ Kill**: abrupt disconnect (no close frame) → **only withdraw subscription, PTY stays alive**, no closed frame sent (`conn_test.go` guards this); sending `{"type":"close"}` → `onExplicitClose` → caller does `manager.Kill`. New "timeout auto-Kill"/"switch tab close PTY" must explicitly call `onExplicitClose`. `closedFrame` fixed text `{"type":"closed","reason":"session ended"}`, changes must sync frontend.
- **Frame classification**: `len(p)==0 || p[0]!='{'` → `InputData` (original bytes); only when first byte is `{` does it try JSON parse, **parse failure also degrades to InputData** (`ParseInputFrame` never drops bytes). Whitelist contains only `"resize"`, `"close"`, others all degrade.
- Encoding: terminal bytes → BinaryMessage; control frames → TextMessage (JSON). Backend→browser has only these two (BinaryMessage + `closedFrame`). New client→server control frame types must change `frames.go` switch + `ControlFrame` + **sync frontend parsing**.

### Resource Closing / Error Handling General

- Uniformly **idempotent + best-effort `_ =`**: `Session.Close`/`teardown` use `sync.Once`, `Server.Close`/`CloseAll` use closed flag/swap map. pump+Kill+CloseAll concurrent in three ways is the norm.
- Errors wrapped `%w` layer by layer to preserve sentinels (e.g. `ErrSessionNotFound`) for `errors.Is`; best-effort paths (e.g. `Setenv`, `client.Close` on Start failure) use `_ =`.
- **happy path forbids `log.Printf`** (PTY pushes thousands of frames per second); only exceptions like dropped frames log, relying on error return values to convey information.
- `defer` order: `defer sess.Unsubscribe(ch)` before `defer conn.Close()`.

## 9. Configuration (`config.json`)

- On startup, if file does not exist → `config.Load` auto-generates default config with random `jwt.secret`/`ssh.passcode` (atomic write tmp+`Rename`, `0600`). If exists → decode as-is, do not regenerate secret.
- `ssl.enabled=true` → `ListenAndServeTLS`, WS auto-upgrades to wss.
- `ssh.host_signer_file`: empty → in-memory ed25519 (lost on restart); set but missing → generate and persist (`0600`); set and exists → load. Other I/O errors do not regenerate (to avoid covering up corrupted-file issues by overwriting).
- **`config.json` and `data/` are already `.gitignore`d**, contain sensitive info.

## 10. Frontend Contract Sync (`backend/web/types/api.d.ts`)

When adding HTTP endpoints or changing fields, sync **all** of these (missing one causes bugs):

1. `router/router.go` registers the route + puts it in the correct group (public/authed/WS).
2. `api.d.ts` adds type declarations; the file header comment lists derived source files, new handler files need registration.
3. New error codes → sync `ErrorCode` enum (copy values verbatim).
4. `model/types.go` field changes → sync the corresponding interface (json tags aligned verbatim).
5. store interface method changes → check all implementations + mocks (`service_test.go`) + service callers.
6. New middleware context keys → export via `CtxXxxKey()` accessors, do not let handlers hardcode.
7. SSO TTL / password minimum length / bcrypt cost / JWT TTL and other constant changes → check whether tests and frontend copy the same values.
8. response data shape changes → sync `ApiResponse<T>` generic parameter.

The `api.d.ts` comment specifies that `User` via `sanitizeUser` never returns `password_hash`, frontend should not read that field. WS query token may appear in logs/proxy logs——use short-lived tokens in production.

## 11. Testing Conventions

Two layers: **Go white-box unit tests** + **Node black-box end-to-end**, covering all HTTP/WS routes.

- Go: `go test ./...` / `go test -race ./...` (PTY/store concurrency must use `-race`). Each package guards invariants like lifecycle/ownership/frame-classification/resize-deadlock/SSH natural-exit.
- Node: under `tests/` run `node --test --test-concurrency=1` (**serial**, avoids backend port collisions and SSH process build-up). Files: `helpers.js` (backend lifecycle: random ports/random JWT secret/temp dir + HTTP/WS tools), `full-lifecycle.test.js`, `integration.test.js`, `ws-frames.test.js` (frame ambiguity matrix), `*-boundary.test.js` (by domain: auth/template/session/ownership/sso).
- Each `describe` starts an independent backend instance (independent temp dir, random HTTP/SSH ports, random JWT secret), no cross-pollution.
- macOS sandbox: `npm test` spawns the backend binary; in restricted contexts TCC may SIGKILL the subprocess due to `com.apple.provenance` xattr; `helpers.js` best-effort clears xattr + ad-hoc signing (memoized). Run in a normal interactive terminal for restricted environments, or pre-`go build` then use `WEBSHELL_TEST_BIN=/path npm test`.
- **New features must come with tests**; changes to PTY/WS/SSH core domain must ensure regression tests (`manager_resize_stdin_test.go`, `conn_test.go` disconnect-stays-alive, `server_test.go` natural-exit) stay green.

## 13. Frontend Code Conventions (`frontend/`)

React + TypeScript SPA, directory layering: `hooks/` (state/effect encapsulation), `components/` (presentation and interaction), `context/` (global store), `lib/` (pure functions/encapsulated singletons), `routes/` (routing). When changing frontend, follow:

- **Avoid wrapping functions with `useCallback` as much as possible, to reduce mental overhead**. Unless you think performance optimization is needed here.
- Directory responsibilities: `hooks` only exports custom hooks, does not render JSX; `components` does not directly fetch HTTP (calls methods exposed by `hooks`); `lib` pure functions + stateless singletons (e.g. WS client, api wrapper), does not depend on React.
- `useEffect` dependencies: **either honestly list them all, or explicitly use `[]` and read the latest value via ref inside**; "intentionally omitting dependencies to bypass lint" is forbidden. Polling-style effects use ref to mirror the mutable token, avoiding resetting the timer on logout/login (see `useSessions`/`useSystemStats`).
- HTTP always goes through `lib/` wrapper, unified `ApiError` + `ErrorCode` enum (aligned verbatim with `api.d.ts`); 401 triggers unified logout handled by route guard redirect to login, do not check code inside components.
- Terminal components reuse the `components/terminal/` layering (Header / MonitorBar / SessionTabs / XTerm container); when adding terminal UI first check whether a reusable hook already exists (session lifecycle, system monitoring, WS frames).

## 14. Forbidden Actions List (red lines, AI scan entry point)

- ❌ Change SSH `Listen` to `0.0.0.0` (passcode is non-TLS, exposed bare on the public network).
- ❌ Re-introduce session storage/persistence into the PTY layer (in-memory only is a design decision).
- ❌ Send SSH `WindowChange` when a sizer exists (freezes all stdin).
- ❌ `conn.WriteMessage` outside the writer goroutine (gorilla panic); **reader must not write to conn**.
- ❌ reader sending closed frames or any control frame to frontend (breaks single-writer).
- ❌ Change `ParseInputFrame` to drop bytes (first byte not `{` or parse failure must be treated as data as-is).
- ❌ Call `s.Pty()` (gliderlabs race); add mutex to PTY Read/Write (deadlock round-trip).
- ❌ Move WS route into the `authed` group (rejected by Auth first).
- ❌ `ShouldBindJSON` failure without `return` (gin writes second response panic).
- ❌ store implementation imports `response`; service imports `gin`/`handler`.
- ❌ Directly `os.WriteFile` to persist store (must use `atomicWriteJSON` + `0o600`).
- ❌ Change broadcast to blocking send (slow client freezes all subscribers); delete `safeSend`'s recover.
- ❌ Change SSO validation to "verify password first then Consume" (breaks anti-brute-force semantics).
- ❌ Add server-side blacklist to Logout (breaks stateless contract).
- ❌ Remove JWT `Parse`'s `SigningMethodHMAC` validation (`alg=none` forgery).
- ❌ Add `log.Printf` to PTY happy path (frame-rate logs will explode).
- ❌ Assume WS disconnect = Kill PTY (disconnect only withdraws subscription, PTY stays alive).
- ❌ Change `RequestPty` argument order (rows first, cols second).
- ❌ Missing `WEBSESSION_ID` env (resize degrades to no-op).
- ❌ Store Delete without existence check (frontend can't get NotFound).
- ❌ handler passing `ID`/`CreatedAt` into Create/Update (service-side generated).
- ❌ New error codes changed only in Go without syncing `api.d.ts`'s `ErrorCode`.
- ❌ Frontend wrapping functions with `useCallback` (write functions directly, stable references go via ref/`useEffect` event-pattern/extracted to module pure functions, see §13).