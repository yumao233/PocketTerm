// template-boundary.test.js — Boundaries and negative paths for template CRUD.

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

describe("template boundary", () => {
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

  test("create: empty name → 1007", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "", command: "/bin/bash", args: [] },
    });
    assert.equal(body.code, 1007);
  });

  test("create: empty command is backfilled with the bash default, env/cwd retained", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: {
        name: "default-cmd",
        command: "",
        args: [],
        cwd: "/tmp",
        env: [{ key: "FOO", value: "bar" }],
      },
    });
    assert.equal(body.code, 0);
    assert.equal(body.data.command, "bash");
    assert.equal(body.data.cwd, "/tmp");
    assert.deepEqual(body.data.env, [{ key: "FOO", value: "bar" }]);
    assert.ok(body.data.id);
    assert.ok(body.data.created_at);
    assert.ok(body.data.updated_at);
  });

  test("list: initially an empty array", async () => {
    // A prior case has already created default-cmd, so we no longer assume empty here — only
    // assert the structure.
    const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/templates", {
      token,
    });
    assert.equal(body.code, 0);
    assert.ok(Array.isArray(body.data.templates));
  });

  test("unauthenticated template access → 1004", async () => {
    const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/templates");
    assert.equal(body.code, 1004);
    const { body: post } = await apiFetch(
      b.baseUrl,
      "POST",
      "/api/v1/templates",
      { json: { name: "x", command: "", args: [] } },
    );
    assert.equal(post.code, 1004);
  });

  test("detail/update/delete: non-existent id → 1006", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const got = await apiFetch(b.baseUrl, "GET", `/api/v1/templates/${fakeId}`, {
      token,
    });
    assert.equal(got.body.code, 1006);

    const upd = await apiFetch(b.baseUrl, "PUT", `/api/v1/templates/${fakeId}`, {
      token,
      json: { name: "renamed", command: "", args: [] },
    });
    assert.equal(upd.body.code, 1006);

    const del = await apiFetch(
      b.baseUrl,
      "DELETE",
      `/api/v1/templates/${fakeId}`,
      { token },
    );
    assert.equal(del.body.code, 1006);
  });

  test("update: renaming to an existing name → 2001", async () => {
    const a = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "dup-a", command: "", args: [] },
    });
    const bb = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "dup-b", command: "", args: [] },
    });
    // Rename b to a → 2001
    const upd = await apiFetch(
      b.baseUrl,
      "PUT",
      `/api/v1/templates/${bb.body.data.id}`,
      { token, json: { name: "dup-a", command: "", args: [] } },
    );
    assert.equal(upd.body.code, 2001);
  });

  test("update: same name same id is allowed (field refresh, not a conflict); CreatedAt retained", async () => {
    const c = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "keep-self", command: "", args: [] },
    });
    const id = c.body.data.id;
    const origCreatedAt = c.body.data.created_at;
    // Updating self with the same name → allowed
    const upd = await apiFetch(b.baseUrl, "PUT", `/api/v1/templates/${id}`, {
      token,
      json: { name: "keep-self", command: "/bin/sh", args: ["-c", "true"] },
    });
    assert.equal(upd.body.code, 0);
    assert.equal(upd.body.data.command, "/bin/sh");
    assert.deepEqual(upd.body.data.args, ["-c", "true"]);
    // CreatedAt must be retained; UpdatedAt should be refreshed
    assert.equal(upd.body.data.created_at, origCreatedAt);
  });

  test("GET after delete → 1006", async () => {
    const c = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token,
      json: { name: "to-delete", command: "", args: [] },
    });
    const id = c.body.data.id;
    const del = await apiFetch(b.baseUrl, "DELETE", `/api/v1/templates/${id}`, {
      token,
    });
    assert.equal(del.body.code, 0);
    assert.equal(del.body.message, "deleted");
    const got = await apiFetch(b.baseUrl, "GET", `/api/v1/templates/${id}`, {
      token,
    });
    assert.equal(got.body.code, 1006);
  });
});