// ============ WebShell Backend API Types ============
//
// This declaration file describes the data shapes of all WebShell backend HTTP
// APIs and the WebSocket control-frame protocol. All field names strictly match
// the json tags of the Go source; time fields are ISO8601 strings serialized by
// Go time.Time. Error code values are transcribed verbatim from
// backend/pkg/response/response.go.
//
// Derived from:
//   - backend/pkg/response/response.go        (Envelope + ErrXxx)
//   - backend/internal/model/types.go         (User/Template/Session/SsoToken/KV)
//   - backend/internal/handler/auth.go        (setup/login/logout/me/check-setup)
//   - backend/internal/handler/sso.go         (sso_login / sso/tokens)
//   - backend/internal/handler/template.go    (templates CRUD)
//   - backend/internal/handler/session.go     (sessions lifecycle + ws bridge)
//   - backend/internal/handler/sysinfo.go     (system/stats)
//   - backend/internal/ws/frames.go           (WS control-frame parsing)
//   - backend/internal/ws/conn.go             (WS closed frame sending)
//   - backend/internal/router/router.go       (route table)

// ============ Unified Response Envelope ============

/**
 * Unified response envelope.
 *
 * All non-WebSocket HTTP endpoints return HTTP 200, distinguishing success
 * from business errors via the code field in the body: code === 0 means
 * success, non-zero means business error. On failure data is null.
 */
export interface ApiResponse<T = unknown> {
  /** Business code: 0=success, non-zero=business error (see ErrorCode) */
  code: number;
  /** Human-readable description; "ok" or a custom message on success */
  message: string;
  /** Business data on success; null on failure */
  data: T | null;
}

// ============ Error Code Constants ============
//
// Values correspond verbatim to the ErrXxx constants in
// backend/pkg/response/response.go. All non-zero values are business errors
// (HTTP remains 200). The frontend should branch on code first, and only read
// message when it needs to be displayed.

/** Business error codes; values match the Go ErrXxx verbatim */
export const enum ErrorCode {
  /** Success */
  OK = 0,
  /** System not initialized (no admin user yet) */
  NotInitialized = 1001,
  /** setup called after the system was already initialized */
  AlreadyInitialized = 1002,
  /** Invalid username or password */
  InvalidCredentials = 1003,
  /** Unauthenticated / authentication expired (missing or invalid JWT) */
  Unauthenticated = 1004,
  /** Authenticated but lacks permission (e.g. accessing someone else's session) */
  Forbidden = 1005,
  /** Resource not found */
  NotFound = 1006,
  /** Request parameter validation failed */
  InvalidParam = 1007,
  /** Template name duplicate */
  TemplateNameDuplicate = 2001,
  /** Session does not exist or does not belong to the current user */
  SessionNotFound = 3001,
  /** Operation performed on an already-closed session */
  SessionClosed = 3002,
  /** Internal error */
  Internal = 5000,
}

// ============ Data Models ============

/** Environment variable key-value pair; corresponds to model.KV */
export interface KV {
  /** Variable name */
  key: string;
  /** Variable value */
  value: string;
}

/**
 * Registered user. The first registered user becomes admin (IsAdmin=true);
 * subsequent registrations require authentication.
 *
 * This type describes the user in API responses (the user field returned by
 * setup / login / sso_login / me), not the internal Go model.User. The backend
 * never returns password_hash via service.sanitizeUser (the bcrypt hash is
 * cleared), so this field is not declared here; the frontend must not read
 * password_hash either.
 */
export interface User {
  /** Unique user ID (UUID) */
  id: string;
  /** Login username, unique */
  username: string;
  /** Whether the user is an administrator */
  is_admin: boolean;
  /** Creation time (ISO8601) */
  created_at: string;
}

/**
 * Terminal template: describes how to launch a session. Name is unique.
 * Corresponds to model.Template.
 */
export interface Template {
  /** Unique template ID (UUID) */
  id: string;
  /** Template name, unique */
  name: string;
  /** Launch command, defaults to "bash" */
  command: string;
  /** Command arguments */
  args: string[];
  /** Working directory, defaults to "$HOME" or "/" */
  cwd: string;
  /** List of environment variable key-value pairs */
  env: KV[];
  /** Creation time (ISO8601) */
  created_at: string;
  /** Last update time (ISO8601) */
  updated_at: string;
}

/** Session status */
export type SessionStatus = "running" | "closed";

/**
 * Terminal session metadata. The actual PTY lifecycle is managed in memory by
 * pty.Manager; this struct only persists metadata and status. Corresponds to
 * model.Session.
 */
export interface Session {
  /** Unique session ID (UUID); used by the frontend as the WS channel id */
  id: string;
  /** Owning user ID */
  user_id: string;
  /** Template ID used at startup */
  template_id: string;
  /** Display name; defaults to template.name + sequence number */
  title: string;
  /** Status: running | closed */
  status: SessionStatus;
  /** Creation time (ISO8601) */
  created_at: string;
}

/**
 * One-time, expirable pre-authenticated login token. An admin pre-generates a
 * token bound to a username together with a one-time plaintext password; an
 * external system uses both to call /sso_login in exchange for a JWT. Invalidated
 * after use. Corresponds to model.SsoToken. Note: {uuid, password, username} is
 * only exposed in the /sso/tokens response.
 */
export interface SsoToken {
  /** Unique token ID (UUID); used as the uuid input to /sso_login */
  id: string;
  /** Bound username */
  username: string;
  /** One-time plaintext password */
  password: string;
  /** Whether it has been used */
  used: boolean;
  /** Creation time (ISO8601) */
  created_at: string;
  /** Expiry time (ISO8601) */
  expires_at: string;
}

// ============ Auth Endpoint DTOs ============

/** POST /api/v1/setup request body */
export interface SetupRequest {
  /** Administrator username */
  username: string;
  /** Administrator password (plaintext, used only for this request) */
  password: string;
}

/** POST /api/v1/login request body */
export interface LoginRequest {
  /** Username */
  username: string;
  /** Password (plaintext, used only for this request) */
  password: string;
}

/**
 * Authentication success response: the data body of setup / login / sso_login
 * (non-redirect mode). Corresponds to handler.authResp / ssoLoginResp
 * {token, user}.
 */
export interface AuthUserResponse {
  /** Newly issued JWT */
  token: string;
  /** Authenticated user (sanitized) */
  user: User;
}

/** GET /api/v1/check-setup response body */
export interface CheckSetupResponse {
  /** Whether the system is initialized (an admin user exists) */
  initialized: boolean;
}

/** GET /api/v1/me response body (handler wraps it as gin.H{"user": user}) */
export interface MeResponse {
  /** Current authenticated user */
  user: User;
}

// ============ SSO Endpoint DTOs ============

/**
 * POST /api/v1/sso_login input.
 *
 * Note: uuid and password may be placed in the JSON body or in query parameters
 * (query takes priority, to support being concatenated directly into a redirect
 * link). When redirect=1, the success response is an HTTP 302 redirect to
 * "/?token=<jwt>" rather than JSON.
 */
export interface SsoLoginRequest {
  /** UUID of the one-time SSO token (query name is also uuid) */
  uuid: string;
  /** One-time plaintext password (query name is also password) */
  password: string;
  /**
   * Whether to use the 302 redirect mode. Triggered by the query parameter
   * redirect=1; in this case the success response is not JSON but a 302
   * Location: /?token=<jwt>. JSON callers on the frontend should omit it or
   * pass a value other than "1".
   */
  redirect?: number | string;
}

/** POST /api/v1/sso/tokens request body */
export interface SsoTokenCreateRequest {
  /** Target username to generate one-time login credentials for (must already exist) */
  username: string;
}

/**
 * POST /api/v1/sso/tokens response body. Returns the newly minted one-time
 * credential; password is plaintext and returned only this once. The caller
 * must deliver it securely to the external system. Corresponds to
 * handler.createTokenResp {uuid, password, username}.
 */
export interface SsoTokenResponse {
  /** UUID of the new token; used as the uuid input to /sso_login */
  uuid: string;
  /** One-time plaintext password */
  password: string;
  /** Bound username */
  username: string;
}

// ============ Template Endpoint DTOs ============

/**
 * POST/PUT /api/v1/templates request body. Corresponds to handler.templateReq.
 * All fields except name are optional (the server fills in defaults such as
 * Command).
 */
export interface TemplateCreateRequest {
  /** Template name (required, unique) */
  name: string;
  /** Launch command, defaults to "bash" */
  command?: string;
  /** Command arguments */
  args?: string[];
  /** Working directory */
  cwd?: string;
  /** Environment variable key-value pairs */
  env?: KV[];
}

/**
 * PUT /api/v1/templates/:id request body. Reuses the TemplateCreateRequest shape.
 * The server preserves the original CreatedAt and only overwrites these mutable
 * fields.
 */
export type TemplateUpdateRequest = TemplateCreateRequest;

/**
 * GET /api/v1/templates response body.
 * The handler wraps it as gin.H{"templates": ts}, so it is an object with a
 * templates key rather than a bare array.
 */
export interface TemplateListResponse {
  /** List of all templates */
  templates: Template[];
}

/**
 * GET /api/v1/templates/:id / POST /api/v1/templates / PUT /api/v1/templates/:id
 * response body. The handler passes a single *model.Template directly to
 * response.OK, so data is the template itself.
 */
export type TemplateGetResponse = Template;

// ============ Session Endpoint DTOs ============

/** POST /api/v1/sessions request body. Corresponds to handler.createSessionReq. */
export interface SessionCreateRequest {
  /** Template ID used to launch */
  template_id: string;
  /** Optional display title; when absent the server generates one from template.name + sequence number */
  title?: string;
}

/**
 * GET /api/v1/sessions response body.
 * The handler wraps it as gin.H{"sessions": sessions}, so it is an object with
 * a sessions key.
 */
export interface SessionListResponse {
  /** List of the current user's sessions (metadata only) */
  sessions: Session[];
}

/**
 * GET /api/v1/sessions/:id / POST /api/v1/sessions response body.
 * The handler passes a single Session directly to response.OK, so data is the
 * session itself.
 */
export type SessionGetResponse = Session;

// ============ System Monitoring DTO ============

/**
 * Real-time host resource usage snapshot, shown on the frontend monitor bar.
 * Corresponds to model.SystemStats. Collection is best-effort: on single-field
 * failure that field degrades to 0 or -1 (RTT), no 5xx is thrown and code
 * remains 0. RTT is a monotonic-clock round-trip approximation within this
 * process (not a network ping), in ms; on modern hardware it is often 0 and is
 * for trend indication only.
 */
export interface SystemStats {
  /** Overall host CPU usage, 0-100 */
  cpu_percent: number;
  /** Number of logical cores */
  cpu_cores: number;
  /** Used memory (GB) */
  mem_used_gb: number;
  /** Total memory (GB) */
  mem_total_gb: number;
  /** Memory usage, 0-100 */
  mem_percent: number;
  /** Minimum observable latency approximation within this process (ms), for trend use */
  rtt_ms: number;
}

// ============ WebSocket Protocol ============
//
// WS endpoint GET /api/v1/ws/:id: browsers cannot set the Authorization header
// during a WS upgrade, so the JWT is passed via the ?token=<jwt> query parameter
// (verified by the WSAuth middleware).
//
// Frame classification rules (see backend/internal/ws/frames.go):
//   - Only when the first byte is '{' is it parsed as a JSON control frame;
//     otherwise it is always treated as raw terminal bytes.
//   - Only when parsing succeeds and type ∈ {"resize","close"} is it handled as
//     a control frame; all other JSON (including unknown types or malformed JSON)
//     degrades to raw data, so terminal bytes starting with '{' are never lost.
//
// Transport encoding:
//   - Raw terminal bytes: BinaryMessage
//   - Control frames: TextMessage (JSON)
//
// Client → server control frames (the handler does not send {"type":"error"};
// the server currently only sends closed).

/** Client resize control frame: adjusts the PTY terminal size */
export interface WsResizeControl {
  /** Control frame type: "resize" */
  type: "resize";
  /** New number of columns */
  cols: number;
  /** New number of rows */
  rows: number;
}

/** Client close control frame: requests destruction of the current session */
export interface WsCloseControl {
  /** Control frame type: "close" */
  type: "close";
  /** Optional human-readable close reason */
  reason?: string;
}

/** Union type of control frames sent by the client */
export type WsClientControl = WsResizeControl | WsCloseControl;

/**
 * Server-sent closed control frame. When the PTY session ends (natural exit or
 * the client sending close triggers a Kill), the server sends exactly one frame
 * {"type":"closed","reason":"session ended"} and then closes the connection.
 * Corresponds to the closedFrame constant in ws/conn.go.
 */
export interface WsClosedFrame {
  /** Control frame type: "closed" */
  type: "closed";
  /** Close reason (current implementation always uses "session ended") */
  reason?: string;
}

/**
 * Server-sent error control frame.
 *
 * Note: the current conn.go implementation does not actually send
 * {"type":"error"} frames (business errors are returned via the HTTP envelope
 * before the WS upgrade); this type is reserved for protocol completeness and
 * future extension.
 */
export interface WsErrorFrame {
  /** Control frame type: "error" */
  type: "error";
  /** Error description */
  message: string;
}

/** Union type of control frames sent by the server (current implementation only sends WsClosedFrame) */
export type WsServerControl = WsClosedFrame | WsErrorFrame;

// ============ Endpoints Index ============
//
// Endpoint index: maps "METHOD /path" to { request?, response?, notes? }.
// The frontend API client can use it to generate type-safe calls. Path
// parameters are annotated in :name form. All HTTP endpoint responses are
// wrapped in ApiResponse<T> (the envelope); the response field here records the
// type of the data inside the envelope (T).

export interface ApiEndpoints {
  // --- Health check (no auth; through the unified envelope, data is {status:'ok'}) ---
  "GET /healthz": {
    response: { status: string };
    notes: "For load balancing / liveness probes; through the unified envelope, data is {status:'ok'}";
  };

  // --- Public endpoints (no auth) ---
  "GET /api/v1/check-setup": {
    response: CheckSetupResponse;
    notes: "Probes whether the system is initialized";
  };
  "POST /api/v1/setup": {
    request: SetupRequest;
    response: AuthUserResponse;
    notes: "Registers the first admin and returns a JWT; returns AlreadyInitialized when already initialized";
  };
  "POST /api/v1/login": {
    request: LoginRequest;
    response: AuthUserResponse;
    notes: "Validates username and password and issues a JWT";
  };
  "POST /api/v1/sso_login": {
    request: SsoLoginRequest;
    response: AuthUserResponse;
    notes:
      | "uuid/password can come from the JSON body or query parameters (query takes priority)"
      | "When redirect=1 the success response is an HTTP 302 redirect to /?token=<jwt>, not JSON";
  };

  // --- Authenticated endpoints (Bearer JWT) ---
  "POST /api/v1/logout": {
    response: null;
    notes: "Stateless: the server does not track the JWT; the client just discards it; data is null";
  };
  "GET /api/v1/me": {
    response: MeResponse;
    notes: "Returns the sanitized record of the current authenticated user";
  };

  // --- Templates CRUD (Bearer JWT) ---
  "GET /api/v1/templates": {
    response: TemplateListResponse;
  };
  "POST /api/v1/templates": {
    request: TemplateCreateRequest;
    response: Template;
    notes: "Returns TemplateNameDuplicate when name is duplicated";
  };
  "GET /api/v1/templates/:id": {
    response: TemplateGetResponse;
  };
  "PUT /api/v1/templates/:id": {
    request: TemplateUpdateRequest;
    response: Template;
    notes: "Overwrites mutable fields; the server preserves CreatedAt";
  };
  "DELETE /api/v1/templates/:id": {
    response: null;
    notes: "Idempotent delete; on success data is null and message is 'deleted'";
  };

  // --- Sessions lifecycle (Bearer JWT) ---
  "GET /api/v1/sessions": {
    response: SessionListResponse;
    notes: "Returns only session metadata owned by the current user";
  };
  "POST /api/v1/sessions": {
    request: SessionCreateRequest;
    response: Session;
  };
  "GET /api/v1/sessions/:id": {
    response: SessionGetResponse;
    notes: "Non-owner access returns Forbidden or NotFound";
  };
  "DELETE /api/v1/sessions/:id": {
    response: null;
    notes: "Kills the session and sets the persisted status to closed; on success data is null and message is 'killed'";
  };

  // --- System monitoring (Bearer JWT) ---
  "GET /api/v1/system/stats": {
    response: SystemStats;
    notes:
      | "Real-time host CPU/RAM snapshot"
      | "Failure of any field degrades to 0/-1, no 5xx thrown, code remains 0"
      | "rtt_ms is a monotonic-clock round-trip approximation within this process (not a network ping), truncated to an integer in ms, for trend use";
  };

  // --- SSO Token minting (Bearer JWT) ---
  "POST /api/v1/sso/tokens": {
    request: SsoTokenCreateRequest;
    response: SsoTokenResponse;
    notes:
      | "Mints a one-time credential for an existing user; the plaintext password is returned only once"
      | "admin only: only a JWT with adm=true may call this; non-admin callers get Forbidden (1005)";
  };

  // --- WebSocket (query-token auth) ---
  "GET /api/v1/ws/:id": {
    notes:
      | "WebSocket upgrade; auth via the ?token=<jwt> query parameter (verified by WSAuth)"
      | "Upstream: BinaryMessage=raw terminal bytes, TextMessage=JSON control frame (resize|close)"
      | "Downstream: BinaryMessage=PTY output, TextMessage={\"type\":\"closed\",\"reason\":\"session ended\"}"
      | "Verifies ownership and session liveness before upgrade; on failure returns an envelope error instead of upgrading";
  };
}