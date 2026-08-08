// session-boundary.test.js — Boundaries and negative paths for the session lifecycle.
//
// Covers:
//   - creating a session with a non-existent template_id → 1006
//   - creating a session missing template_id → 1007
//   - GET a non-existent session → 1006
//   - deleting a non-existent session → 1006 (service.Get fails first)
//   - command exits naturally → an active WS receives {"type":"closed"} then closes; the
//     session is removed from the in-memory pool (GET → 1006)
//   - WS connecting to an already-ended session → 1006 (rejected before upgrade; no persisted
//     closed record)
//   - WS without a token → 1004

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import {
  startBackend,
  apiFetch,
  connectWS,
  waitFor,
  waitForClose,
  collectUntil,
} from "./helpers.js";

describe("session boundary", () => {
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

  test("create: missing template_id → 1007", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { title: "no-tpl" },
    });
    assert.equal(body.code, 1007);
  });

  test("create: non-existent template_id → 1006", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: fakeId, title: "ghost-tpl" },
    });
    assert.equal(body.code, 1006);
  });

  test("GET/DELETE: non-existent session → 1006", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const got = await apiFetch(
      b.baseUrl,
      "GET",
      `/api/v1/sessions/${fakeId}`,
      { token },
    );
    assert.equal(got.body.code, 1006);
    const del = await apiFetch(
      b.baseUrl,
      "DELETE",
      `/api/v1/sessions/${fakeId}`,
      { token },
    );
    assert.equal(del.body.code, 1006);
  });

  test("WS without a token → 1004 (rejected before upgrade)", async () => {
    // First create a running session to get a real id
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "ws-notoken-tpl", command: "/bin/bash", args: ["-c", "sleep 5"], cwd: "/tmp" },
    });
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: tpl.body.data.id, title: "ws-notoken" },
    });
    const sid = sess.body.data.id;
    // WS handshake without a token: WSAuth directly response.Fail 1004, no upgrade
    const res = await fetch(`${b.baseUrl}/api/v1/ws/${sid}`);
    const body = await res.json();
    assert.equal(body.code, 1004);
    // Cleanup
    await apiFetch(b.baseUrl, "DELETE", `/api/v1/sessions/${sid}`, { token });
  });

  test("natural exit: active WS receives a closed frame, then the session is gone (GET → 1006)", async () => {
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: {
        name: "exit-tpl",
        command: "/bin/bash",
        args: ["-c", "echo DONE && exit 0"],
        cwd: "/tmp",
      },
    });
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: tpl.body.data.id, title: "exit-session" },
    });
    assert.equal(sess.body.code, 0);
    const sid = sess.body.data.id;

    const ws = await connectWS(b.wsBaseUrl, sid, token);
    // Expect to receive the DONE output, and then the server sends {"type":"closed"} and closes
    const seen = [];
    ws.on("message", (d) => seen.push(d.toString("utf8")));
    const closedInTime = await waitForClose(ws, 6000);
    assert.ok(closedInTime, "WS should be closed after the short-lived command ends");
    const all = seen.join("");
    assert.ok(all.includes("DONE"), `should receive DONE output before close; actual: ${JSON.stringify(all)}`);
    const sawClosed = seen.some((s) => {
      try {
        return JSON.parse(s).type === "closed";
      } catch {
        return false;
      }
    });
    assert.ok(sawClosed, "should receive a {\"type\":\"closed\"} frame before close");

    // The natural-exit pump removes the session from the in-memory pool, so once the WS has
    // closed the session is no longer observable via GET (1006). There is no persisted
    // "closed" record.
    await waitFor(
      async () => {
        const { body } = await apiFetch(
          b.baseUrl,
          "GET",
          `/api/v1/sessions/${sid}`,
          { token },
        );
        return body.code === 1006;
      },
      3000,
      100,
    );
  });

  test("connecting to a naturally ended session: WS is rejected with 1006 before upgrade", async () => {
    // A session that exits immediately
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "ended-tpl", command: "/bin/bash", args: ["-c", "true"], cwd: "/tmp" },
    });
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: tpl.body.data.id, title: "ended" },
    });
    const sid = sess.body.data.id;
    // Wait for the short-lived command to exit and the pump to remove it from the in-memory
    // pool (GET → 1006).
    await waitFor(
      async () => {
        const { body } = await apiFetch(
          b.baseUrl,
          "GET",
          `/api/v1/sessions/${sid}`,
          { token },
        );
        return body.code === 1006;
      },
      3000,
      80,
    );
    // Under the pure in-memory model a naturally ended session is gone from the live pool, so
    // handler.WS -> svc.Get returns ErrNotFound *before* the live-pool nil check — the request
    // is answered with a 1006 JSON envelope and the connection is never upgraded. (No persisted
    // closed record exists that could yield a 3002; "already ended" is indistinguishable from
    // "never existed" — see AGENTS §8.)
    const res = await fetch(`${b.baseUrl}/api/v1/ws/${sid}?token=${token}`);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.code, 1006);
  });

  test("interactive session: list correctly contains the current user's session", async () => {
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "list-tpl", command: "/bin/bash", args: ["-c", "sleep 5"], cwd: "/tmp" },
    });
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token,
      json: { template_id: tpl.body.data.id, title: "list-session" },
    });
    const sid = sess.body.data.id;
    const { body: list } = await apiFetch(
      b.baseUrl,
      "GET",
      "/api/v1/sessions",
      { token },
    );
    assert.equal(list.code, 0);
    const mine = list.data.sessions.find((s) => s.id === sid);
    assert.ok(mine, "list should contain the just-created session");
    assert.equal(mine.status, "running");
    // Cleanup
    await apiFetch(b.baseUrl, "DELETE", `/api/v1/sessions/${sid}`, { token });
  });
});