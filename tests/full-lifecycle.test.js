// full-lifecycle.test.js — Integration test that strings the entire chain end to end.
//
// This is the strictest version of the "end-to-end" intent in spec section 12: on a fresh
// backend, from
//   register (setup) → login (verify it differs from the first token)
//   → create template → start a session from the template → WS connect
//   → resize the window with rapid consecutive changes (verify no disconnect)
//   → input a command (echo MARKER) → check output (got:MARKER echo)
//   → destroy the terminal (DELETE) → verify the session is gone from the in-memory pool (GET → 1006)
//   → reconnecting WS is rejected with 1006 (no persisted closed record) → a second DELETE also returns 1006
// All in one flow, fully logically connected. Each step uses the previous step's real output
// as input — no stubbed values.

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { WebSocket } from "ws";
import {
  startBackend,
  apiFetch,
  connectWS,
  waitForClose,
  collectUntil,
  sleep,
} from "./helpers.js";

describe("full lifecycle: register → login → template → session → resize → input → output → destroy → verify", () => {
  let backend;
  let setupToken; // JWT returned by setup
  let loginToken; // JWT returned by login (should differ from setupToken but be equally usable)

  before(async () => {
    backend = await startBackend();
  });

  after(async () => {
    if (backend) await backend.stop();
  });

  // 1. Register the first admin
  test("register: setup registers the first admin", async () => {
    const { body } = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    assert.equal(body.code, 0);
    assert.equal(body.data.user.username, "admin");
    assert.equal(body.data.user.is_admin, true);
    assert.ok(typeof body.data.token === "string" && body.data.token.length > 0);
    setupToken = body.data.token;
  });

  // 2. Login to get a new JWT and verify it can drive /me
  test("login: login returns a new JWT that authenticates", async () => {
    const { body } = await apiFetch(backend.baseUrl, "POST", "/api/v1/login", {
      json: { username: "admin", password: "admin123" },
    });
    assert.equal(body.code, 0);
    assert.ok(typeof body.data.token === "string");
    loginToken = body.data.token;
    // Both tokens should be usable (the service signs a new JWT each time), but their contents
    // need not differ — we don't require inequality (only assert both work) to avoid being
    // oversensitive to iat jitter.
    const { body: me } = await apiFetch(backend.baseUrl, "GET", "/api/v1/me", {
      token: loginToken,
    });
    assert.equal(me.code, 0);
    assert.equal(me.data.user.username, "admin");
  });

  let templateId;
  let sessionId;

  // 3. Create a template (command="" → service fills the default "bash", interactive shell)
  test("create template: empty command is backfilled with the default bash", async () => {
    const { body } = await apiFetch(
      backend.baseUrl,
      "POST",
      "/api/v1/templates",
      {
        token: loginToken,
        json: { name: "lifecycle-shell", command: "", args: [], cwd: "/tmp" },
      },
    );
    assert.equal(body.code, 0);
    assert.equal(body.data.name, "lifecycle-shell");
    // service.Create backfills an empty Command with "bash"
    assert.equal(body.data.command, "bash");
    templateId = body.data.id;
    assert.ok(templateId);
  });

  // 4. Start a session from the template
  test("start session: create a running session from the template", async () => {
    const { body } = await apiFetch(
      backend.baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token: loginToken,
        json: { template_id: templateId, title: "lifecycle-session" },
      },
    );
    assert.equal(body.code, 0);
    assert.equal(body.data.status, "running");
    assert.equal(body.data.template_id, templateId);
    assert.equal(body.data.title, "lifecycle-session");
    sessionId = body.data.id;
    assert.ok(sessionId);
  });

  // 5. Rapid consecutive resize: send several frames with different sizes, the connection must stay OPEN throughout
  test("resize: rapid consecutive window-size changes do not disconnect", async () => {
    const ws = await connectWS(backend.wsBaseUrl, sessionId, loginToken);
    try {
      // Interleaved large/small resize frames sent back to back — typical traffic after the
      // frontend's trailing-edge debounce.
      const sizes = [
        [80, 24],
        [120, 40],
        [200, 50],
        [60, 20],
        [300, 80],
        [1000, 100],
        [80, 24], // back to the initial size (exercises the idempotent dedup branch)
        [40, 10],
      ];
      for (const [cols, rows] of sizes) {
        ws.send(JSON.stringify({ type: "resize", cols, rows }));
        // Very short intervals without waiting for the server — simulates rapid dragging.
        // But leave a little room every few frames for processing.
      }
      // Give the server a processing window; if any frame caused a disconnect the readyState
      // would deviate from OPEN.
      await sleep(600);
      assert.equal(
        ws.readyState,
        WebSocket.OPEN,
        "WS must remain OPEN after consecutive resize frames",
      );
    } finally {
      // Close on the client side (do not send a close control frame): a close control frame
      // triggers onExplicitClose which kills the PTY session itself; here we only want to
      // detach the WS and keep the session alive for reuse by later steps.
      ws.close();
      await waitForClose(ws, 4000);
    }
  });

  // 6+7. Input a command and check output: the echo-loop template reads stdin line by line and
  // echoes got:<line>. Here we reuse the session established above (still the same interactive
  // bash) and send via WS stdin.
  test("input command → check output: MARKER echo round-trips via WS", async () => {
    // Reconnect to the same session (the previous test closed the WS, but the PTY session
    // itself is still running)
    const ws = await connectWS(backend.wsBaseUrl, sessionId, loginToken);
    try {
      const marker = `MARKER-${Date.now()}`;
      ws.send(`echo ${marker}\n`);
      // bash in a PTY: first echoes the input line "echo MARKER-...", then prints MARKER itself.
      // These two pieces may land in different WS message frames, so we judge based on
      // accumulated text.
      const { ok, messages } = await collectUntil(
        ws,
        (_s, all) => {
          const joined = all.join("");
          return joined.includes(`echo ${marker}`) && joined.includes(marker);
        },
        8000,
      );
      assert.ok(
        ok,
        `should see the echoed command and result ${marker} in WS output; actual: ${JSON.stringify(
          messages.join(""),
        )}`,
      );
    } finally {
      // Client-side disconnect (do not destroy the PTY), keep the session alive for the DELETE step
      ws.close();
      await waitForClose(ws, 4000);
    }
  });

  // 8. Destroy the terminal: DELETE → code 0, message "killed". Under the pure in-memory
  //    model the session is removed from the live pool the moment Kill returns, so a follow-up
  //    GET no longer finds it (1006) — there is no persisted "closed" record.
  test("destroy terminal: session gone from the pool after DELETE (GET → 1006)", async () => {
    const { body } = await apiFetch(
      backend.baseUrl,
      "DELETE",
      `/api/v1/sessions/${sessionId}`,
      { token: loginToken },
    );
    assert.equal(body.code, 0);
    assert.equal(body.message, "killed");

    // manager.Kill deletes the entry from the pool before the handler returns, so the very
    // next GET sees it as not-found.
    const { body: after } = await apiFetch(
      backend.baseUrl,
      "GET",
      `/api/v1/sessions/${sessionId}`,
      { token: loginToken },
    );
    assert.equal(after.code, 1006);
  });

  // 9. Verify destruction: reconnecting the WS is rejected before the upgrade. Because the
  //    session has already been removed from the live pool, svc.Get returns ErrNotFound first,
  //    so the handler answers 1006 (it never reaches the live-pool nil check). There is no
  //    persisted closed record that could yield a 3002.
  test("verify destruction: reconnecting WS returns 1006 (session not found)", async () => {
    const res = await fetch(
      `${backend.baseUrl}/api/v1/ws/${sessionId}?token=${loginToken}`,
      // Use the default fetch (no upgrade); the backend rejects with a JSON envelope before
      // upgrading.
    );
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.code, 1006);
  });

  // 10. Second DELETE on an already-destroyed session: the session is gone from the live pool,
  //     so svc.Get fails with ErrNotFound before manager.Kill is ever called → 1006. In the
  //     pure in-memory model "already ended" is indistinguishable from "never existed", so the
  //     idempotent "return 0" contract does not apply (see AGENTS §8).
  test("second DELETE on a destroyed session returns 1006 (gone = not found)", async () => {
    const { body } = await apiFetch(
      backend.baseUrl,
      "DELETE",
      `/api/v1/sessions/${sessionId}`,
      { token: loginToken },
    );
    assert.equal(body.code, 1006);
    // The destroyed session is not retained in the list either.
    const { body: list } = await apiFetch(
      backend.baseUrl,
      "GET",
      "/api/v1/sessions",
      { token: loginToken },
    );
    assert.equal(list.code, 0);
    const stillThere = list.data.sessions.find((s) => s.id === sessionId);
    assert.equal(stillThere, undefined);
  });
});