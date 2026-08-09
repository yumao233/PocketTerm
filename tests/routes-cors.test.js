// routes-cors.test.js — routing / CORS / system endpoints / request-binding boundaries (black-box real backend).
//
// Fills the HTTP-layer gaps that Go unit tests do not cover, or that do not exercise the full
// middleware chain (Recovery/Logger/CORS + real router assembly):
//   - unknown route → 404 (gin default, non-envelope)
//   - OPTIONS preflight through the full middleware chain → 204 without triggering Auth
//     (even when the target is an authed-group route)
//   - CORS three branches with real response headers: wildcard echoes `*` (no credentials);
//     specific origin match → echo + Allow-Credentials:true + Vary:Origin; no match / no Origin
//     → no ACAO
//   - /api/v1/system/stats: no token → 1004, with admin token → code 0 + a structured snapshot
//   - non-JSON body failing ShouldBindJSON → 1007 (setup / login / template create), and the
//     bind failure must not accidentally initialize the system (check-setup stays false)
//
// The CORS three branches rely on the helpers cors option (default ['*']; pass a specific origin
// list to exercise the echo branch).

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

const SPECIFIC_ORIGIN = "http://app.example.com";
const EVIL_ORIGIN = "http://evil.example.com";

describe("CORS & preflight", () => {
  // wc: wildcard backend (allowed_origins: ['*']) — default config.
  // sp: specific-origin backend — exercises the echo + credentials + Vary branch.
  let wc;
  let sp;

  before(async () => {
    wc = await startBackend();
    sp = await startBackend({ cors: [SPECIFIC_ORIGIN] });
  });

  after(async () => {
    await Promise.all([wc.stop(), sp.stop()]);
  });

  test("wildcard origin: any Origin echoed as '*', no Allow-Credentials", async () => {
    const res = await fetch(`${wc.baseUrl}/api/v1/check-setup`, {
      headers: { Origin: EVIL_ORIGIN },
    });
    assert.equal(res.headers.get("access-control-allow-origin"), "*");
    // The CORS spec forbids credentials together with a wildcard origin.
    assert.equal(res.headers.get("access-control-allow-credentials"), null);
  });

  test("specific match: echoes origin + Allow-Credentials:true + Vary:Origin", async () => {
    const res = await fetch(`${sp.baseUrl}/api/v1/check-setup`, {
      headers: { Origin: SPECIFIC_ORIGIN },
    });
    assert.equal(res.headers.get("access-control-allow-origin"), SPECIFIC_ORIGIN);
    assert.equal(res.headers.get("access-control-allow-credentials"), "true");
    const vary = res.headers.get("vary") || "";
    assert.ok(vary.includes("Origin"), `Vary should include Origin, actual: ${vary}`);
  });

  test("specific no-match: disallowed Origin gets no ACAO / no credentials", async () => {
    const res = await fetch(`${sp.baseUrl}/api/v1/check-setup`, {
      headers: { Origin: EVIL_ORIGIN },
    });
    assert.equal(res.headers.get("access-control-allow-origin"), null);
    assert.equal(res.headers.get("access-control-allow-credentials"), null);
  });

  test("no Origin header on specific backend: no ACAO set", async () => {
    const res = await fetch(`${sp.baseUrl}/api/v1/check-setup`);
    assert.equal(res.headers.get("access-control-allow-origin"), null);
  });

  test("preflight OPTIONS on an authed route: 204 + headers, Auth NOT triggered", async () => {
    // /api/v1/templates lives in the authed group, but OPTIONS is short-circuited to 204 by
    // the engine-level CORS middleware BEFORE the group's Auth runs — no Bearer required.
    const res = await fetch(`${sp.baseUrl}/api/v1/templates`, {
      method: "OPTIONS",
      headers: { Origin: SPECIFIC_ORIGIN },
    });
    assert.equal(res.status, 204);
    assert.equal(res.headers.get("access-control-allow-origin"), SPECIFIC_ORIGIN);
    assert.ok(
      (res.headers.get("access-control-allow-methods") || "").includes("DELETE"),
      "preflight should advertise DELETE",
    );
    assert.ok(
      (res.headers.get("access-control-allow-headers") || "").includes("Authorization"),
      "preflight should allow Authorization",
    );
  });

  test("preflight OPTIONS on wildcard backend: 204 + ACAO '*'", async () => {
    const res = await fetch(`${wc.baseUrl}/api/v1/sessions`, {
      method: "OPTIONS",
      headers: { Origin: EVIL_ORIGIN },
    });
    assert.equal(res.status, 204);
    assert.equal(res.headers.get("access-control-allow-origin"), "*");
  });
});

describe("misc routes & request binding", () => {
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

  test("unknown route → 404 (gin default, non-envelope)", async () => {
    const res = await fetch(`${b.baseUrl}/api/v1/no-such-route`);
    assert.equal(res.status, 404);
  });

  test("system/stats: no token → 1004", async () => {
    const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/system/stats");
    assert.equal(body.code, 1004);
  });

  test("system/stats: with admin token → code 0 and a structured snapshot", async () => {
    const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/system/stats", { token });
    assert.equal(body.code, 0);
    const s = body.data;
    assert.equal(typeof s.cpu_percent, "number");
    assert.equal(typeof s.cpu_cores, "number");
    assert.ok(s.cpu_cores >= 1, `cpu_cores should be >=1, actual: ${s.cpu_cores}`);
    assert.equal(typeof s.mem_total_gb, "number");
    assert.ok(s.mem_total_gb > 0, `mem_total_gb should be >0, actual: ${s.mem_total_gb}`);
    assert.equal(typeof s.mem_percent, "number");
    assert.ok(s.mem_percent >= 0 && s.mem_percent <= 100, `mem_percent out of range: ${s.mem_percent}`);
    // rtt_ms degrades to -1 on collection failure, so only assert it is a number.
    assert.equal(typeof s.rtt_ms, "number");
  });

  // The three binding tests each start a fresh backend so the "system stays uninitialized"
  // assertion is clean and so a failed bind on a shared backend cannot perturb later tests.
  test("binding: setup with non-JSON body → 1007 (system stays uninitialized)", async () => {
    const backend = await startBackend();
    try {
      const { body } = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        raw: "this is not json",
      });
      assert.equal(body.code, 1007);
      const chk = await apiFetch(backend.baseUrl, "GET", "/api/v1/check-setup");
      assert.equal(chk.body.data.initialized, false);
    } finally {
      await backend.stop();
    }
  });

  test("binding: login with non-JSON body → 1007 (after setup)", async () => {
    const backend = await startBackend();
    try {
      await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const { body } = await apiFetch(backend.baseUrl, "POST", "/api/v1/login", {
        raw: "{bad json",
      });
      assert.equal(body.code, 1007);
    } finally {
      await backend.stop();
    }
  });

  test("binding: template create with non-JSON body → 1007", async () => {
    const backend = await startBackend();
    try {
      const setup = await apiFetch(backend.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const tok = setup.body.data.token;
      const { body } = await apiFetch(backend.baseUrl, "POST", "/api/v1/templates", {
        token: tok,
        raw: "not-json",
      });
      assert.equal(body.code, 1007);
    } finally {
      await backend.stop();
    }
  });
});