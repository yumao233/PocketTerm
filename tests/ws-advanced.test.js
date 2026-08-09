// ws-advanced.test.js — WebSocket concurrency and protocol boundaries (black-box real backend).
//
// Focuses on the end-to-end concurrency and degradation behavior of the PTY/WS core domain on a
// real backend, filling the black-box gaps beyond the Go unit tests (manager_test at the channel
// layer, frames_test at the pure-function layer, conn_test on an httptest local server):
//   - multi-WS fan-out subscribing to the same session: both connections receive the same output
//     line; one disconnecting does not affect the other, and the PTY stays alive (disconnect≠Kill,
//     AGENTS §8).
//   - disconnect≠Kill confirmed together with the HTTP /sessions list: after an abrupt WS close,
//     GET /sessions/:id is still running, the list still contains it, and reconnecting the WS
//     still works interactively.
//   - resize degradation on missing fields / wrong types: `{"type":"resize"}` (0×0) and
//     string-typed cols both do not break the connection; a subsequent normal resize still changes
//     the PTY size (proving the degradation did not break the session).
//   - unknown control type degrades to data: `{"type":"frobnicate"}` enters stdin and is echoed
//     by the read loop.
//   - WS connecting to a non-existent session id (with a valid token) → 1006 before the upgrade.

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

const ECHO_LOOP = ["-c", 'while IFS= read -r line; do echo "got:$line"; done'];
const STTY_LOOP = ["-c", 'while IFS= read -r line; do echo "SIZE=$(stty size)"; done'];

describe("WS advanced: fan-out, disconnect≠Kill, resize degradation", () => {
  let b;
  let token;

  before(async () => {
    b = await startBackend();
    const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    token = setup.body.data.token;
  });

  after(async () => {
    if (b) await b.stop();
  });

  // startSession creates a template + running session from the given bash args and returns
  // the session id. The bash -c command runs immediately and stays alive for the duration.
  async function startSession(args, title) {
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: `adv-${title}-${Date.now()}`, command: "/bin/bash", args, cwd: "/tmp" },
    });
    assert.equal(tpl.body.code, 0);
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: tpl.body.data.id, title },
    });
    assert.equal(sess.body.code, 0);
    return sess.body.data.id;
  }

  // 1. Multi-subscriber fan-out: both WS connections should receive the same output line; disconnecting one does not affect the other.
  test("multi-subscriber: two WS connections both receive the same output line", async () => {
    const sid = await startSession(ECHO_LOOP, "fanout");
    const wsA = await connectWS(b.wsBaseUrl, sid, token);
    let wsB;
    try {
      wsB = await connectWS(b.wsBaseUrl, sid, token);

      // Register both collectors before sending: avoid the echo arriving before the listener is registered.
      const a = collectUntil(wsA, (s) => s.includes("got:hello-fanout"), 6000);
      const bb = collectUntil(wsB, (s) => s.includes("got:hello-fanout"), 6000);
      wsA.send("hello-fanout\n");
      const [ra, rb] = await Promise.all([a, bb]);
      assert.ok(ra.ok, `wsA should receive echo; actual: ${JSON.stringify(ra.messages.join(""))}`);
      assert.ok(rb.ok, `wsB should receive echo; actual: ${JSON.stringify(rb.messages.join(""))}`);

      // Abruptly close wsA (no close frame): only unsubscribes, the PTY is not killed, and wsB is unaffected.
      wsA.close();
      await waitForClose(wsA, 3000);
      assert.equal(
        wsB.readyState,
        WebSocket.OPEN,
        "wsB must remain OPEN after wsA abrupt close",
      );
      // wsB sends a close frame for a clean exit and Kill.
      wsB.send(JSON.stringify({ type: "close" }));
      await waitForClose(wsB, 4000);
    } finally {
      try { wsA.close(); } catch { /* already closed */ }
      if (wsB) { try { wsB.close(); } catch { /* already closed */ } }
    }
    // The close frame already triggered Kill → the session is removed from the in-memory pool → GET 1006
    const after = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, { token });
    assert.equal(after.body.code, 1006);
  });

  // 2. disconnect≠Kill + HTTP list confirmation: after an abrupt WS close the PTY is still running and reconnectable interactively.
  test("disconnect≠Kill: abrupt WS close keeps the PTY alive and reconnectable", async () => {
    const sid = await startSession(ECHO_LOOP, "disc-kill");
    const listBefore = await apiFetch(b.baseUrl, "GET", "/api/v1/sessions", { token });
    assert.ok(listBefore.body.data.sessions.some((s) => s.id === sid));

    const ws = await connectWS(b.wsBaseUrl, sid, token);
    ws.close(); // abrupt client close, no close frame
    await waitForClose(ws, 3000);
    await sleep(150);

    // The PTY is still alive.
    const got = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, { token });
    assert.equal(got.body.code, 0);
    assert.equal(got.body.data.status, "running");
    const listAfter = await apiFetch(b.baseUrl, "GET", "/api/v1/sessions", { token });
    assert.ok(listAfter.body.data.sessions.some((s) => s.id === sid));

    // Reconnecting the WS still works interactively.
    const ws2 = await connectWS(b.wsBaseUrl, sid, token);
    try {
      ws2.send("still-alive\n");
      const { ok, messages } = await collectUntil(
        ws2,
        (s) => s.includes("got:still-alive"),
        6000,
      );
      assert.ok(ok, `reconnected WS should still echo; actual: ${JSON.stringify(messages.join(""))}`);
      ws2.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws2, 4000);
    } finally {
      try { ws2.close(); } catch { /* already closed */ }
    }
    const after = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, { token });
    assert.equal(after.body.code, 1006);
  });

  // 3. resize degradation on missing fields / wrong types: does not break the connection, and a subsequent normal resize still takes effect.
  test("resize degradation: missing fields and type errors do not break the session", async () => {
    const sid = await startSession(STTY_LOOP, "resize-degrade");
    const ws = await connectWS(b.wsBaseUrl, sid, token);
    try {
      // A single message accumulator; read "the latest SIZE=" instead of "the n-th" to avoid stacking listeners across multiple collectUntil calls.
      const seen = [];
      ws.on("message", (d) => seen.push(d.toString("utf8")));
      // Wait for the "most recent" SIZE= reading to equal the expected value; do not care which
      // occurrence it is, because a malformed resize is echoed as a line of stdin (or merged with
      // the next probe), producing an extra SIZE=24 80 that would pollute an index-based read.
      // Just look at the last match so far.
      const waitSizeEquals = async (want, timeoutMs = 5000) => {
        const deadline = Date.now() + timeoutMs;
        while (Date.now() < deadline) {
          const matches = [...seen.join("").matchAll(/SIZE=(\d+)\s+(\d+)/g)];
          if (matches.length) {
            const m = matches[matches.length - 1];
            const cur = { rows: Number(m[1]), cols: Number(m[2]) };
            if (cur.rows === want.rows && cur.cols === want.cols) return cur;
          }
          await sleep(40);
        }
        return null;
      };

      ws.send("p\n");
      const first = await waitSizeEquals({ rows: 24, cols: 80 });
      assert.ok(first, "should receive an initial SIZE=24 80 reading");

      // Missing cols/rows (→ InputResize, Cols=0, Rows=0): the initial lastCols/lastRows are also
      // 0,0, so it is skipped by the idempotent dedup and the PTY is unchanged; in any case the
      // connection must not break.
      ws.send(JSON.stringify({ type: "resize" }));
      await sleep(250);
      assert.equal(ws.readyState, WebSocket.OPEN, "must remain OPEN after resize w/o cols/rows");

      // cols is a string type → JSON parsing fails → degrades to data → enters stdin; the shell errors but the connection does not break.
      ws.send(JSON.stringify({ type: "resize", cols: "wide", rows: "tall" }));
      await sleep(250);
      assert.equal(ws.readyState, WebSocket.OPEN, "must remain OPEN after malformed resize types");

      // A normal resize still changes the PTY size (proving the degradation did not break the session).
      ws.send(JSON.stringify({ type: "resize", cols: 100, rows: 40 }));
      await sleep(300);
      ws.send("p\n");
      const second = await waitSizeEquals({ rows: 40, cols: 100 });
      assert.ok(
        second,
        "PTY should reflect 40x100 after a real resize (degradation did not break it)",
      );

      ws.send(JSON.stringify({ type: "close" }));
    } finally {
      await waitForClose(ws, 4000);
    }
  });

  // 4. Unknown control type degrades to data: valid JSON but type is not resize/close → enters stdin.
  test("unknown control type degrades to data (forwarded to PTY stdin)", async () => {
    const sid = await startSession(ECHO_LOOP, "unknown-type");
    const ws = await connectWS(b.wsBaseUrl, sid, token);
    try {
      ws.send(`{"type":"frobnicate"}\n`);
      const { ok, messages } = await collectUntil(
        ws,
        (s) => s.includes("got:") && s.includes("frobnicate"),
        6000,
      );
      assert.ok(
        ok,
        `unknown-type frame should be echoed as data; actual: ${JSON.stringify(messages.join(""))}`,
      );
      assert.equal(ws.readyState, WebSocket.OPEN, "must remain OPEN after unknown control type");
    } finally {
      ws.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws, 4000);
    }
  });

  // 5. WS to a non-existent session id (valid token) → 1006 before the upgrade.
  test("WS to a non-existent session id (valid token) → 1006 before upgrade", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const res = await fetch(`${b.baseUrl}/api/v1/ws/${fakeId}?token=${token}`);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.code, 1006);
  });
});