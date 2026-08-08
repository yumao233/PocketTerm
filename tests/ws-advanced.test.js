// ws-advanced.test.js — WebSocket 并发与协议边界（黑盒真实后端）。
//
// 聚焦 PTY/WS 核心域在真实后端下的端到端并发与降级行为，补 Go 单测（manager_test 在
// channel 层、frames_test 在纯函数层、conn_test 在 httptest 局部 server）之外的黑盒
// 缺口：
//   - 多 WS 订阅同一 session 的 fan-out：两个连接都收到同一行输出；一个断开不影响
//     另一个，PTY 仍存活（disconnect≠Kill，AGENTS §8）。
//   - disconnect≠Kill 配合 HTTP /sessions 列表确认：WS 突然断开后 GET /sessions/:id
//     仍 running、列表仍含它，重连 WS 仍能交互。
//   - resize 缺字段 / 类型错误降级：`{"type":"resize"}`（0×0）、字符串类型的 cols 均
//     不中断连接；随后一次正常 resize 仍能改变 PTY 尺寸（证明降级未破坏会话）。
//   - 未知 control type 降级为 data：`{"type":"frobnicate"}` 进 stdin 被 read-loop 回显。
//   - WS 连不存在的 session id（正确 token）→ 升级前 1006。

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

  // 1. 多订阅者 fan-out：两个 WS 都应收到同一条输出；断开一个不影响另一个。
  test("multi-subscriber: two WS connections both receive the same output line", async () => {
    const sid = await startSession(ECHO_LOOP, "fanout");
    const wsA = await connectWS(b.wsBaseUrl, sid, token);
    let wsB;
    try {
      wsB = await connectWS(b.wsBaseUrl, sid, token);

      // 先注册两个 collector，再发送：避免 echo 在 listener 注册前到达。
      const a = collectUntil(wsA, (s) => s.includes("got:hello-fanout"), 6000);
      const bb = collectUntil(wsB, (s) => s.includes("got:hello-fanout"), 6000);
      wsA.send("hello-fanout\n");
      const [ra, rb] = await Promise.all([a, bb]);
      assert.ok(ra.ok, `wsA should receive echo; actual: ${JSON.stringify(ra.messages.join(""))}`);
      assert.ok(rb.ok, `wsB should receive echo; actual: ${JSON.stringify(rb.messages.join(""))}`);

      // abrupt close wsA（不发 close frame）：仅退订，PTY 不杀，wsB 不受影响。
      wsA.close();
      await waitForClose(wsA, 3000);
      assert.equal(
        wsB.readyState,
        WebSocket.OPEN,
        "wsB must remain OPEN after wsA abrupt close",
      );
      // wsB 发 close frame 干净退出并 Kill
      wsB.send(JSON.stringify({ type: "close" }));
      await waitForClose(wsB, 4000);
    } finally {
      try { wsA.close(); } catch { /* already closed */ }
      if (wsB) { try { wsB.close(); } catch { /* already closed */ } }
    }
    // close frame 已触发 Kill → session 从内存池移除 → GET 1006
    const after = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, { token });
    assert.equal(after.body.code, 1006);
  });

  // 2. disconnect≠Kill + HTTP 列表确认：WS 突然断开后 PTY 仍 running、可重连交互。
  test("disconnect≠Kill: abrupt WS close keeps the PTY alive and reconnectable", async () => {
    const sid = await startSession(ECHO_LOOP, "disc-kill");
    const listBefore = await apiFetch(b.baseUrl, "GET", "/api/v1/sessions", { token });
    assert.ok(listBefore.body.data.sessions.some((s) => s.id === sid));

    const ws = await connectWS(b.wsBaseUrl, sid, token);
    ws.close(); // abrupt client close, no close frame
    await waitForClose(ws, 3000);
    await sleep(150);

    // PTY 仍存活
    const got = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, { token });
    assert.equal(got.body.code, 0);
    assert.equal(got.body.data.status, "running");
    const listAfter = await apiFetch(b.baseUrl, "GET", "/api/v1/sessions", { token });
    assert.ok(listAfter.body.data.sessions.some((s) => s.id === sid));

    // 重连 WS 仍能交互
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

  // 3. resize 缺字段 / 类型错误降级：不中断连接，且后续正常 resize 仍生效。
  test("resize degradation: missing fields and type errors do not break the session", async () => {
    const sid = await startSession(STTY_LOOP, "resize-degrade");
    const ws = await connectWS(b.wsBaseUrl, sid, token);
    try {
      // 单一 message 累积器 + 按"第 n 个 SIZE=" 读取，避免多次 collectUntil 叠加 listener。
      const seen = [];
      ws.on("message", (d) => seen.push(d.toString("utf8")));
      // 等待"最新一次" SIZE= 读取等于期望值；不计第几次，因为畸形 resize 会作为
      // 一行 stdin 被回显（或与下一条 probe 合并），额外产生 SIZE=24 80，按序号取
      // 会被污染。只看迄今为止最后一次匹配即可。
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

      // 缺 cols/rows（→ InputResize, Cols=0,Rows=0）：初始 lastCols/lastRows 也是 0,0，
      // 被幂等 dedup 跳过，PTY 不变；无论如何连接不能断。
      ws.send(JSON.stringify({ type: "resize" }));
      await sleep(250);
      assert.equal(ws.readyState, WebSocket.OPEN, "must remain OPEN after resize w/o cols/rows");

      // cols 为字符串类型 → JSON 解析失败 → 降级为 data → 进 stdin，shell 报错但不断连。
      ws.send(JSON.stringify({ type: "resize", cols: "wide", rows: "tall" }));
      await sleep(250);
      assert.equal(ws.readyState, WebSocket.OPEN, "must remain OPEN after malformed resize types");

      // 一次正常 resize 仍能改变 PTY 尺寸（证明降级未破坏会话）。
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

  // 4. 未知 control type 降级为 data：valid JSON 但 type 非 resize/close → 进 stdin。
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

  // 5. WS 连不存在的 session id（正确 token）→ 升级前 1006。
  test("WS to a non-existent session id (valid token) → 1006 before upgrade", async () => {
    const fakeId = "00000000-0000-0000-0000-000000000000";
    const res = await fetch(`${b.baseUrl}/api/v1/ws/${fakeId}?token=${token}`);
    assert.equal(res.status, 200);
    const body = await res.json();
    assert.equal(body.code, 1006);
  });
});