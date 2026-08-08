// integration.test.js — Full end-to-end chain test corresponding to spec section 12.
//
// Structure:
//   - One large "full e2e flow" test that runs through register → login → template CRUD →
//     start session → WS ping → close → delete → SSO on a single backend + single admin
//     account (matching the "end-to-end" intent of spec section 12).
//   - Several focused standalone tests for boundary scenarios that need a "fresh backend":
//     unauthenticated rejection, template name duplication, SSO reuse.
//
// Timing strategy: SSH/PTY/WS are asynchronous, so we use timeout-based wait helpers
// (waitFor) rather than fixed sleeps. The ping assertion uses the command template
// `bash -c "ping -c 3 127.0.0.1"` (instead of an interactive $SHELL); the command runs
// immediately when the session starts and its output streams back over WS — this avoids
// timing jitter from zsh/prompt and matches the proven Go test approach.

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { WebSocket } from "ws";
import { startBackend, sleep, apiFetch } from "./helpers.js";

// Collect WS messages (text + binary) within at most timeoutMs; ends when the predicate hits
// or on timeout. Returns { messages, ok }.
async function collectUntil(ws, predicate, timeoutMs = 8000) {
  const messages = [];
  const text = (m) => (typeof m === "string" ? m : m.toString("utf8"));
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve({ messages, ok });
    };
    const timer = setTimeout(() => finish(false), timeoutMs);
    ws.on("message", (data, isBinary) => {
      const s = isBinary ? data.toString("utf8") : data.toString("utf8");
      messages.push(s);
      try {
        if (predicate(s, messages)) finish(true);
      } catch {
        /* keep collecting */
      }
    });
    ws.on("close", () => finish(false));
    ws.on("error", () => finish(false));
  });
}

// Wait for the WS 'open' event, with a timeout.
function waitForOpen(ws, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const to = setTimeout(
      () => reject(new Error("WS open timeout")),
      timeoutMs,
    );
    ws.once("open", () => {
      clearTimeout(to);
      resolve();
    });
    ws.once("error", (e) => {
      clearTimeout(to);
      reject(e);
    });
    ws.once("close", () => {
      clearTimeout(to);
      reject(new Error("WS closed before open"));
    });
  });
}

// Wait for the WS 'close' event, with a timeout.
function waitForClose(ws, timeoutMs = 4000) {
  return new Promise((resolve) => {
    const to = setTimeout(() => resolve(false), timeoutMs);
    ws.once("close", () => {
      clearTimeout(to);
      resolve(true);
    });
  });
}

describe("full e2e flow", () => {
  let backend;
  let baseUrl;
  let wsBaseUrl;
  let token; // Admin JWT used throughout the entire chain
  let adminUser;

  before(async () => {
    backend = await startBackend();
    baseUrl = backend.baseUrl;
    wsBaseUrl = backend.wsBaseUrl;
  });

  after(async () => {
    if (backend) await backend.stop();
  });

  // 1. check-setup (fresh backend) → not initialized
  test("check-setup: fresh backend is not initialized", async () => {
    const { status, body } = await apiFetch(
      baseUrl,
      "GET",
      "/api/v1/check-setup",
    );
    assert.equal(status, 200);
    assert.equal(body.code, 0);
    assert.equal(body.data.initialized, false);
  });

  // 2. setup → register the first admin + JWT
  test("setup: registers the first admin and returns a JWT", async () => {
    const { status, body } = await apiFetch(baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    assert.equal(status, 200);
    assert.equal(body.code, 0);
    assert.equal(typeof body.data.token, "string");
    assert.ok(body.data.token.length > 0);
    assert.equal(body.data.user.username, "admin");
    assert.equal(body.data.user.is_admin, true);
    // password_hash must never leak: sanitizeUser clears it to "" (the JSON tag has no
    // omitempty, so the key is still present but empty). Accept both missing and empty.
    const ph = body.data.user.password_hash;
    assert.ok(
      ph === "" || ph === undefined,
      `password_hash should be empty or absent, actual: ${JSON.stringify(ph)}`,
    );
    token = body.data.token;
    adminUser = body.data.user;
  });

  // 3. A second setup is rejected → AlreadyInitialized (1002)
  test("setup: a second setup returns 1002", async () => {
    const { body } = await apiFetch(baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin2", password: "admin123" },
    });
    assert.equal(body.code, 1002);
    assert.equal(body.data, null);
  });

  // 4. login → JWT
  test("login: correct credentials return a JWT", async () => {
    const { body } = await apiFetch(baseUrl, "POST", "/api/v1/login", {
      json: { username: "admin", password: "admin123" },
    });
    assert.equal(body.code, 0);
    assert.equal(typeof body.data.token, "string");
    assert.equal(body.data.user.username, "admin");
    // Keep using the setup token for chain continuity, but also assert login works here
  });

  // 5. me → current user
  test("me: returns the current authenticated user", async () => {
    const { body } = await apiFetch(baseUrl, "GET", "/api/v1/me", { token });
    assert.equal(body.code, 0);
    assert.equal(body.data.user.username, "admin");
    assert.equal(body.data.user.is_admin, true);
  });

  // 6. Template CRUD
  test("template CRUD: create / list / detail / update / delete / duplicate name", async () => {
    // Create two templates: a one-shot "echo hi" and an interactive shell
    const { body: createdEcho } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/templates",
      {
        token,
        json: {
          name: "bash-tpl",
          command: "/bin/bash",
          args: ["-c", "echo hi"],
          cwd: "/tmp",
        },
      },
    );
    assert.equal(createdEcho.code, 0);
    assert.equal(createdEcho.data.name, "bash-tpl");
    assert.ok(createdEcho.data.id, "template has an id");
    assert.deepEqual(createdEcho.data.args, ["-c", "echo hi"]);
    const echoId = createdEcho.data.id;

    // Interactive shell template (command="" → service defaults to "bash")
    const { body: createdShell } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/templates",
      {
        token,
        json: { name: "shell-tpl", command: "", args: [], cwd: "/tmp" },
      },
    );
    assert.equal(createdShell.code, 0);

    // List contains both
    const { body: listed } = await apiFetch(
      baseUrl,
      "GET",
      "/api/v1/templates",
      { token },
    );
    assert.equal(listed.code, 0);
    const names = listed.data.templates.map((t) => t.name);
    assert.ok(names.includes("bash-tpl"));
    assert.ok(names.includes("shell-tpl"));

    // Detail
    const { body: got } = await apiFetch(
      baseUrl,
      "GET",
      `/api/v1/templates/${echoId}`,
      { token },
    );
    assert.equal(got.code, 0);
    assert.equal(got.data.id, echoId);

    // Update (rename) — service preserves CreatedAt and overwrites mutable fields
    const { body: updated } = await apiFetch(
      baseUrl,
      "PUT",
      `/api/v1/templates/${echoId}`,
      {
        token,
        json: { name: "bash-tpl-renamed" },
      },
    );
    assert.equal(updated.code, 0);
    assert.equal(updated.data.name, "bash-tpl-renamed");
    assert.equal(updated.data.id, echoId);

    // Duplicate name create → 2001
    const { body: dup } = await apiFetch(baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "shell-tpl", command: "", args: [] },
    });
    assert.equal(dup.code, 2001);

    // (The interactive shell-tpl created above can be used for ad-hoc checks, but later tests do
    // not depend on it — each subsequent test creates its own template, so test order does not
    // matter.)

    // Delete the echo template → 0; then querying it → 1006
    const { body: deleted } = await apiFetch(
      baseUrl,
      "DELETE",
      `/api/v1/templates/${echoId}`,
      { token },
    );
    assert.equal(deleted.code, 0);
    const { body: afterDelete } = await apiFetch(
      baseUrl,
      "GET",
      `/api/v1/templates/${echoId}`,
      { token },
    );
    assert.equal(afterDelete.code, 1006);
  });

  // 8. Start a session + WS ping (core end-to-end)
  test("session + WS: ping round-trip and close control frame", async () => {
    // Ping assertion: use a command template that runs ping directly — much more reliable than
    // an interactive $SHELL. The command exits after ping finishes, but during its run the
    // output streams back over WS.
    const { body: pingTpl } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/templates",
      {
        token,
        json: {
          name: "ping-tpl",
          command: "/bin/bash",
          args: ["-c", "ping -c 3 127.0.0.1"],
          cwd: "/tmp",
        },
      },
    );
    assert.equal(pingTpl.code, 0);
    const pingTplId = pingTpl.data.id;

    const { body: started } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token,
        json: { template_id: pingTplId, title: "ping-session" },
      },
    );
    assert.equal(started.code, 0);
    assert.equal(started.data.status, "running");
    const sessionId = started.data.id;

    // List contains this session
    const { body: listed } = await apiFetch(
      baseUrl,
      "GET",
      "/api/v1/sessions",
      { token },
    );
    assert.equal(listed.code, 0);
    assert.ok(listed.data.sessions.some((s) => s.id === sessionId));

    // Connect WS
    const ws = new WebSocket(
      `${wsBaseUrl}/api/v1/ws/${sessionId}?token=${token}`,
    );
    await waitForOpen(ws);

    // ping runs at Start; collect until "PING" appears (macOS: "PING 127.0.0.1 ...")
    const { ok: pingOk, messages: pingMsgs } = await collectUntil(
      ws,
      (s) => s.includes("PING"),
      9000,
    );
    const allPingText = pingMsgs.join("");
    assert.ok(
      pingOk,
      `WS output should contain PING, actual: ${JSON.stringify(allPingText)}`,
    );
    // The session's bash -c exits after ping ends → the server sends a {"type":"closed"}
    // frame and closes the WS. Wait for that close event.
    await waitForClose(ws, 5000);
  });

  // 8b. Interactive write: send bytes via WS and observe the echo. Uses a read-while-echo loop
  // so it can be driven via WS stdin.
  test("session + WS: interactive stdin echo (MARKER round-trip)", async () => {
    const { body: echoTpl } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/templates",
      {
        token,
        json: {
          name: "echo-loop-tpl",
          command: "/bin/bash",
          args: ["-c", 'while IFS= read -r line; do echo "got:$line"; done'],
          cwd: "/tmp",
        },
      },
    );
    assert.equal(echoTpl.code, 0);

    const { body: started } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token,
        json: { template_id: echoTpl.data.id, title: "echo-session" },
      },
    );
    assert.equal(started.code, 0);
    const sessionId = started.data.id;

    const ws = new WebSocket(
      `${wsBaseUrl}/api/v1/ws/${sessionId}?token=${token}`,
    );
    await waitForOpen(ws);

    // Send the marker as raw terminal data. bash's read loop echoes it back.
    ws.send("MARKER-123\n");

    const { ok } = await collectUntil(
      ws,
      (s) => s.includes("got:MARKER-123"),
      8000,
    );
    assert.ok(ok, 'should receive the "got:MARKER-123" echo via WS');

    // close control frame → server replies {"type":"closed"} and closes the connection
    ws.send(JSON.stringify({ type: "close" }));
    const closedInTime = await waitForClose(ws, 4000);
    assert.ok(closedInTime, "WS should close after a close control frame");

    // The close control frame triggers an explicit Kill, which removes the session from the
    // in-memory pool. Under the pure in-memory model there is no persisted "closed" record, so
    // the follow-up GET reports the session as not-found (1006).
    const { body: after } = await apiFetch(
      baseUrl,
      "GET",
      `/api/v1/sessions/${sessionId}`,
      { token },
    );
    assert.equal(after.code, 1006);
  });

  // 8c. resize control frame does not disconnect
  test("WS resize control frame does not disconnect", async () => {
    const { body: tpl } = await apiFetch(baseUrl, "POST", "/api/v1/templates", {
      token,
      json: {
        name: "sleep-loop-tpl",
        command: "/bin/bash",
        args: ["-c", "while true; do sleep 1; done"],
        cwd: "/tmp",
      },
    });
    assert.equal(tpl.code, 0);

    const { body: started } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token,
        json: { template_id: tpl.data.id, title: "sleep-session" },
      },
    );
    assert.equal(started.code, 0);
    const sessionId = started.data.id;

    const ws = new WebSocket(
      `${wsBaseUrl}/api/v1/ws/${sessionId}?token=${token}`,
    );
    await waitForOpen(ws);

    ws.send(JSON.stringify({ type: "resize", cols: 100, rows: 40 }));
    // Give the server a moment to process (or reject — rejection would disconnect)
    await sleep(500);
    assert.equal(ws.readyState, WebSocket.OPEN, "WS should remain OPEN after resize");

    // Clean teardown
    ws.send(JSON.stringify({ type: "close" }));
    await waitForClose(ws, 4000);
  });

  // 9. Delete the session
  test("delete session: kill and remove from the active list", async () => {
    const { body: tpl } = await apiFetch(baseUrl, "POST", "/api/v1/templates", {
      token,
      json: {
        name: "del-tpl",
        command: "/bin/bash",
        args: ["-c", "while true; do sleep 1; done"],
        cwd: "/tmp",
      },
    });
    assert.equal(tpl.code, 0);

    const { body: started } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token,
        json: { template_id: tpl.data.id, title: "del-session" },
      },
    );
    assert.equal(started.code, 0);
    const sessionId = started.data.id;

    const { body: deleted } = await apiFetch(
      baseUrl,
      "DELETE",
      `/api/v1/sessions/${sessionId}`,
      { token },
    );
    assert.equal(deleted.code, 0);
    assert.equal(deleted.message, "killed");

    // DELETE removes the session from the in-memory pool; there is no persisted "closed"
    // record, so the follow-up GET reports it as not-found (1006).
    const { body: after } = await apiFetch(
      baseUrl,
      "GET",
      `/api/v1/sessions/${sessionId}`,
      { token },
    );
    assert.equal(after.code, 1006);
  });

  // 10. SSO flow: mint → login → reuse rejected → redirect variant
  test("SSO: mint, login, reuse rejected, 302 redirect variant", async () => {
    // Mint a one-time token for admin
    const { body: minted } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sso/tokens",
      {
        token,
        json: { username: "admin" },
      },
    );
    assert.equal(minted.code, 0);
    assert.equal(minted.data.username, "admin");
    assert.ok(minted.data.uuid, "SSO token has a uuid");
    assert.ok(minted.data.password, "SSO token has a password");
    const { uuid, password } = minted.data;

    // Exchange the token for a JWT (JSON body, no auth required)
    const { body: loggedIn } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sso_login",
      {
        json: { uuid, password },
      },
    );
    assert.equal(loggedIn.code, 0);
    assert.equal(typeof loggedIn.data.token, "string");
    assert.equal(loggedIn.data.user.username, "admin");

    // Reuse the same password → 1003 (ErrInvalidCredentials, already used)
    const { body: reused } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sso_login",
      {
        json: { uuid, password },
      },
    );
    assert.equal(reused.code, 1003);

    // Mint another fresh token for the redirect variant (one-time; the previous one is used)
    const { body: minted2 } = await apiFetch(
      baseUrl,
      "POST",
      "/api/v1/sso/tokens",
      {
        token,
        json: { username: "admin" },
      },
    );
    assert.equal(minted2.code, 0);

    // Redirect variant: query params + redirect=1 → HTTP 302 to /?token=
    const res = await fetch(
      `${baseUrl}/api/v1/sso_login?uuid=${encodeURIComponent(minted2.data.uuid)}&password=${encodeURIComponent(minted2.data.password)}&redirect=1`,
      { method: "POST", redirect: "manual" },
    );
    assert.equal(res.status, 302);
    const loc = res.headers.get("location") || "";
    assert.ok(
      loc.includes("/?token="),
      `Location should contain /?token=, actual: ${loc}`,
    );
  });
});

describe("standalone boundary tests (each on a fresh backend)", () => {
  // 6. Unauthenticated rejection — use a fresh backend to avoid coupling with the main chain
  test("unauthenticated: GET /me without a token → 1004", async () => {
    const backend = await startBackend();
    try {
      // Run setup first to initialize the system (otherwise /me is still Auth-protected; a
      // missing token returns Unauthenticated regardless of initialization)
      await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const { body } = await apiFetch(backend.baseUrl, "GET", "/api/v1/me");
      assert.equal(body.code, 1004);
    } finally {
      await backend.stop();
    }
  });

  // 7. Template name duplication on a fresh backend (standalone diagnosis)
  test("template duplicate name → 2001", async () => {
    const backend = await startBackend();
    try {
      const setup = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const tok = setup.body.data.token;
      await apiFetch(backend.baseUrl, "POST", "/api/v1/templates", {
        token: tok,
        json: { name: "dup", command: "/bin/bash", args: [] },
      });
      const { body } = await apiFetch(
        backend.baseUrl,
        "POST",
        "/api/v1/templates",
        {
          token: tok,
          json: { name: "dup", command: "/bin/bash", args: [] },
        },
      );
      assert.equal(body.code, 2001);
    } finally {
      await backend.stop();
    }
  });

  // SSO reuse on a fresh backend (standalone)
  test("SSO reuse rejected on a fresh backend → 1003", async () => {
    const backend = await startBackend();
    try {
      const setup = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const tok = setup.body.data.token;
      const m = await apiFetch(backend.baseUrl, "POST", "/api/v1/sso/tokens", {
        token: tok,
        json: { username: "admin" },
      });
      const { uuid, password } = m.body.data;
      const r1 = await apiFetch(backend.baseUrl, "POST", "/api/v1/sso_login", {
        json: { uuid, password },
      });
      assert.equal(r1.body.code, 0);
      const r2 = await apiFetch(backend.baseUrl, "POST", "/api/v1/sso_login", {
        json: { uuid, password },
      });
      assert.equal(r2.body.code, 1003);
    } finally {
      await backend.stop();
    }
  });

  // healthz smoke test (module-level test required by spec: unauthenticated endpoint)
  test("healthz: returns code 0 and status=ok", async () => {
    const backend = await startBackend();
    try {
      const { status, body } = await apiFetch(
        backend.baseUrl,
        "GET",
        "/healthz",
      );
      assert.equal(status, 200);
      assert.equal(body.code, 0);
      assert.equal(body.data.status, "ok");
    } finally {
      await backend.stop();
    }
  });
});
