// ws-frames.test.js — "Protocol consistency" module test corresponding to spec section 12.
//
// ParseInputFrame on the Go side is already covered by Go unit tests; this Node test's job is
// to verify the end-to-end behavior of the "frame disambiguation" contract via a real backend:
//   a. Raw data frame (first byte not '{') → echoed back via the PTY (data round-trip, not
//      treated as a control frame).
//   b. {"type":"resize",...} → control frame; no error, connection stays open.
//   c. First byte is '{' but invalid JSON → degrades to data (sent to stdin), the shell tries
//      to execute it. Here we only assert the connection does not drop or crash — the exact
//      shell output is environment-dependent.
//   d. {"type":"close"} → server replies {"type":"closed"} and closes the WS.
//
// Reuses the interactive read-loop template (bash -c 'while read ...; do echo got:$line;
// done'), so we can both send data and observe its echo, clearly distinguishing data frames
// (echoed) from control frames (not echoed).

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { WebSocket } from "ws";
import { startBackend, waitFor, sleep, apiFetch } from "./helpers.js";

function waitForOpen(ws, timeoutMs = 5000) {
  return new Promise((resolve, reject) => {
    const to = setTimeout(() => reject(new Error("WS connect timeout")), timeoutMs);
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

function waitForClose(ws, timeoutMs = 4000) {
  return new Promise((resolve) => {
    const to = setTimeout(() => resolve(false), timeoutMs);
    ws.once("close", () => {
      clearTimeout(to);
      resolve(true);
    });
  });
}

// Collect WS messages until the predicate hits or times out.
function collectUntil(ws, predicate, timeoutMs = 6000) {
  const messages = [];
  return new Promise((resolve) => {
    let done = false;
    const finish = (ok) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      resolve({ messages, ok });
    };
    const timer = setTimeout(() => finish(false), timeoutMs);
    ws.on("message", (data) => {
      const s = data.toString("utf8");
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

describe("WS frame disambiguation (protocol consistency)", () => {
  let backend;
  let token;
  let echoTplId;

  before(async () => {
    backend = await startBackend();
    // Register admin
    const setup = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    token = setup.body.data.token;
    // Create a read-loop template: read stdin line by line, echo "got:<line>"
    const tpl = await apiFetch(backend.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: {
        name: "ws-frames-loop",
        command: "/bin/bash",
        args: ["-c", 'while IFS= read -r line; do echo "got:$line"; done'],
        cwd: "/tmp",
      },
    });
    echoTplId = tpl.body.data.id;
  });

  after(async () => {
    if (backend) await backend.stop();
  });

  async function startSessionAndConnect() {
    const started = await apiFetch(
      backend.baseUrl,
      "POST",
      "/api/v1/sessions",
      {
        token,
        json: { template_id: echoTplId, title: "ws-frames-session" },
      },
    );
    assert.equal(started.body.code, 0);
    const sessionId = started.body.data.id;
    const ws = new WebSocket(
      `${backend.wsBaseUrl}/api/v1/ws/${sessionId}?token=${token}`,
    );
    await waitForOpen(ws);
    return { ws, sessionId };
  }

  // a. Raw data frame round-trips via the PTY
  test("data frame (first byte not {) is forwarded to PTY stdin", async () => {
    const { ws } = await startSessionAndConnect();
    try {
      ws.send("echo HELLO-FRAME\n");
      const { ok, messages } = await collectUntil(
        ws,
        (s) => s.includes("got:echo HELLO-FRAME"),
        6000,
      );
      assert.ok(
        ok,
        `shell should echo the data line; actual: ${JSON.stringify(messages.join(""))}`,
      );
    } finally {
      ws.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws, 4000);
    }
  });

  // b. resize control frame is recognized — no error, connection stays open
  test("resize control frame keeps the connection intact", async () => {
    const { ws } = await startSessionAndConnect();
    try {
      ws.send(JSON.stringify({ type: "resize", cols: 120, rows: 50 }));
      // Give the server a moment to process (rejection would disconnect)
      await sleep(600);
      assert.equal(
        ws.readyState,
        WebSocket.OPEN,
        "WS must remain OPEN after a resize frame",
      );
    } finally {
      ws.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws, 4000);
    }
  });

  // c. First byte is '{' but invalid JSON degrades to data — connection survives
  test("invalid JSON with leading { degrades to data (no crash)", async () => {
    const { ws } = await startSessionAndConnect();
    try {
      // First byte is '{' but invalid JSON → ParseInputFrame returns InputData,
      // and the bytes go to stdin. The shell treats it as a command (syntax error or
      // "command not found"), but the connection must not drop.
      ws.send("{malformed json with leading brace\n");
      await sleep(700);
      // The connection must still be open — this is a contract requirement.
      assert.equal(
        ws.readyState,
        WebSocket.OPEN,
        "WS must remain OPEN after invalid-{ data",
      );
    } finally {
      ws.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws, 4000);
    }
  });

  // d. close control frame → server first sends {"type":"closed"} then closes the connection
  test("close control frame → closed frame + connection close", async () => {
    const { ws } = await startSessionAndConnect();
    // Collect all frames; expect to see a {"type":"closed"} text frame before close.
    const seen = [];
    ws.on("message", (data) => seen.push(data.toString("utf8")));

    ws.send(JSON.stringify({ type: "close" }));
    const closedInTime = await waitForClose(ws, 4000);
    assert.ok(closedInTime, "WS should close quickly after a close control frame");
    const sawClosedFrame = seen.some((s) => {
      try {
        return JSON.parse(s).type === "closed";
      } catch {
        return false;
      }
    });
    assert.ok(
      sawClosedFrame,
      `should receive a {"type":"closed"} frame before close; actual: ${JSON.stringify(seen)}`,
    );
  });

  // e. Binary messages are also treated as raw terminal data (transport equivalence)
  test("binary message is forwarded as raw terminal data", async () => {
    const { ws } = await startSessionAndConnect();
    try {
      ws.send(Buffer.from("echo BINARY-MARK\n", "utf8"));
      const { ok } = await collectUntil(
        ws,
        (s) => s.includes("got:echo BINARY-MARK"),
        6000,
      );
      assert.ok(ok, "binary data frames should be echoed just like text data");
    } finally {
      ws.send(JSON.stringify({ type: "close" }));
      await waitForClose(ws, 4000);
    }
  });
});
