// security.test.js — 端到端安全性边界集合（黑盒真实后端）。
//
// 聚焦那些需要"完整 HTTP/WS 后端 + 真实 JWT 解析/校验链路"才能体现的安全属性，
// 刻意不与 Go 单测（auth_test / middleware_test / handler_test 已在进程内伪造 token
// 覆盖）重复，而是从浏览器/外部系统视角再次钉死契约：
//   - sanitizeUser 不泄漏 password_hash：setup / login / sso_login / me 四条身份路径
//     端到端均不携带 bcrypt hash（AGENTS §10 契约）。
//   - JWT alg=none 伪造被拒（§14 红线：Parse 的 keyFunc 强制 *SigningMethodHMAC）。
//   - JWT payload 篡改（改 uid）因签名失配被拒 —— 证明签名覆盖全部 claims。
//   - 跨后端 secret 隔离：A 后端签发的 token 对 B 后端无效（helpers 随机 secret 的意义）。
//   - logout 是 stateless no-op：登出后同 token 仍可鉴权（无服务端黑名单，§6 契约）。
//   - 已过期 token（jwtTtl=-1 → 签发即过期）端到端被拒。
//   - WS 错误/伪造 query token 在升级前被拒（1004）。
//
// 每个 test 起独立后端，finally stop，无交叉污染。

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

// b64urlEncodeObj 将对象 JSON 化后做 base64url 编码（Node 原生 Buffer 支持 'base64url'）。
function b64urlEncodeObj(obj) {
  return Buffer.from(JSON.stringify(obj), "utf8").toString("base64url");
}

// forgeAlgNoneToken 构造一个 alg=none 的伪造 JWT：header alg 置为 "none"，签名段为空。
// jwt.v5 的 Parse 在 keyFunc 中校验 method 必须是 *SigningMethodHMAC；alg=none 对应
// *SigningMethodNone —— 即便"无签名"逻辑上可验证通过，keyFunc 也会先返回 error，
// 从而映射到 1004。这是 §14 明确禁止移除的防御。
function forgeAlgNoneToken(overrides = {}) {
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: "none", typ: "JWT" };
  const payload = {
    uid: "forged-uid",
    usr: "forged",
    adm: true,
    iat: now,
    exp: now + 3600,
    sub: "forged",
    ...overrides,
  };
  return `${b64urlEncodeObj(header)}.${b64urlEncodeObj(payload)}.`;
}

// tamperPayload 解码真实 JWT 的 payload 段，用 mutator 改写后重新编码，拼回原 header +
// 原签名。因为 payload 改变而签名未变，HMAC 校验必然失败 → 1004。
function tamperPayload(token, mutator) {
  const parts = token.split(".");
  assert.equal(parts.length, 3, "JWT must have 3 segments");
  const claims = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8"));
  mutator(claims);
  const tamperedPayload = b64urlEncodeObj(claims);
  return `${parts[0]}.${tamperedPayload}.${parts[2]}`;
}

// assertNoHash 断言 user 对象里 password_hash 为空或不存在。model.User 的 json tag 无
// omitempty，sanitizeUser 把它清成 ""，所以同时接受 "" 与 undefined。
function assertNoHash(user, label) {
  const ph = user && user.password_hash;
  assert.ok(
    ph === "" || ph === undefined || ph === null,
    `${label}: password_hash must not leak, actual: ${JSON.stringify(ph)}`,
  );
}

async function fresh() {
  return startBackend();
}

describe("security boundaries (end-to-end)", () => {
  // 1. 四条身份路径均不泄漏 password_hash
  test("sanitizeUser: setup/login/sso_login/me never leak password_hash", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      assert.equal(setup.body.code, 0);
      const adminToken = setup.body.data.token;
      assertNoHash(setup.body.data.user, "setup");

      const login = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
        json: { username: "admin", password: "admin123" },
      });
      assert.equal(login.body.code, 0);
      assertNoHash(login.body.data.user, "login");

      const minted = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
        token: adminToken,
        json: { username: "admin" },
      });
      assert.equal(minted.body.code, 0);
      const sso = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
        json: { uuid: minted.body.data.uuid, password: minted.body.data.password },
      });
      assert.equal(sso.body.code, 0);
      assertNoHash(sso.body.data.user, "sso_login");

      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: adminToken });
      assert.equal(me.body.code, 0);
      assertNoHash(me.body.data.user, "me");
    } finally {
      await b.stop();
    }
  });

  // 2. alg=none 伪造 → 1004
  test("JWT: alg=none forged token rejected → 1004", async () => {
    const b = await fresh();
    try {
      await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const noneToken = forgeAlgNoneToken({ uid: "anyone", usr: "admin", adm: true });
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: noneToken });
      assert.equal(body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  // 3. payload 篡改（改 uid）→ 1004，且原 token 仍有效（排除后端不可用）
  test("JWT: tampered payload (uid changed) rejected → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const real = setup.body.data.token;
      const tampered = tamperPayload(real, (c) => {
        c.uid = "attacker-uid";
      });
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: tampered });
      assert.equal(body.code, 1004);
      // 原 token 仍可用，证明 1004 来自签名失配而非环境问题
      const ok = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: real });
      assert.equal(ok.body.code, 0);
    } finally {
      await b.stop();
    }
  });

  // 4. 跨后端 secret 隔离：A 的 token → B /me 1004
  test("JWT: cross-backend token is invalid against a different secret → 1004", async () => {
    const a = await fresh();
    const bb = await fresh();
    try {
      const setup = await apiFetch(a.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const tokenFromA = setup.body.data.token;
      // B 也初始化，排除"未初始化"干扰，聚焦签名校验
      await apiFetch(bb.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const { body } = await apiFetch(bb.baseUrl, "GET", "/api/v1/me", { token: tokenFromA });
      assert.equal(body.code, 1004);
    } finally {
      await Promise.all([a.stop(), bb.stop()]);
    }
  });

  // 5. logout stateless：登出后同 token 仍可鉴权（无服务端黑名单，§6 契约）
  test("logout: stateless no-op — same token still authenticates after logout", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const token = setup.body.data.token;
      const out = await apiFetch(b.baseUrl, "POST", "/api/v1/logout", { token });
      assert.equal(out.body.code, 0);
      // 登出后同 token 仍能驱动 /me
      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token });
      assert.equal(me.body.code, 0);
      assert.equal(me.body.data.user.username, "admin");
    } finally {
      await b.stop();
    }
  });

  // 6. 已过期 token 端到端被拒（jwtTtl=-1 → exp = now - 60s，签发即过期）
  test("JWT: expired token rejected end-to-end → 1004", async () => {
    const b = await startBackend({ jwtTtl: -1 });
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      // setup 成功（Sign 只编码+签名，不校验 exp），但 token 已过期
      assert.equal(setup.body.code, 0);
      const expired = setup.body.data.token;
      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: expired });
      assert.equal(me.body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  // 7. WS 错误/伪造 query token 在升级前被拒（1004，fetch 拿到 JSON envelope）
  test("WS: garbage and alg=none query tokens are rejected before upgrade → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const token = setup.body.data.token;
      // 建一个真实 session 拿合法 id，确保 1004 是 token 问题而非 not-found
      const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
        token,
        json: { name: "ws-sectok-tpl", command: "/bin/bash", args: ["-c", "sleep 5"], cwd: "/tmp" },
      });
      const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
        token,
        json: { template_id: tpl.body.data.id, title: "ws-sectok" },
      });
      const sid = sess.body.data.id;

      const r1 = await fetch(`${b.baseUrl}/api/v1/ws/${sid}?token=not-a-jwt`);
      const j1 = await r1.json();
      assert.equal(j1.code, 1004);

      const forged = forgeAlgNoneToken({ uid: "x", usr: "x", adm: true });
      const r2 = await fetch(`${b.baseUrl}/api/v1/ws/${sid}?token=${encodeURIComponent(forged)}`);
      const j2 = await r2.json();
      assert.equal(j2.code, 1004);

      // 清理：合法 token DELETE
      await apiFetch(b.baseUrl, "DELETE", `/api/v1/sessions/${sid}`, { token });
    } finally {
      await b.stop();
    }
  });
});