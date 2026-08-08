// helpers.js — Backend lifecycle management for Node.js integration tests.
//
// startBackend(opts) creates a temporary working directory, writes a "controlled"
// config.json into it (random HTTP/SSH ports, known passcode), then launches the
// prebuilt (or freshly `go build`-ed) backend binary with that directory as CWD,
// polls /healthz until the service is ready, and finally returns
// { baseUrl, wsBaseUrl, port, tempDir, proc, stdout, stderr, stop() }.
//
// Why write a controlled config: when config.json is absent the backend auto-generates
// one (with random ports/keys). Tests need deterministic ports + a known JWT secret +
// a known passcode, so we write the config before starting the process; config.Load()
// then only decodes it and does not regenerate.
//
// Port allocation: listen on port 0, read the OS-assigned ephemeral port, then close
// the socket and hand that port to the config. There is a small TOCTOU race in between
// (the port may be grabbed by another process), which is acceptable for a test
// scaffolding; if the backend fails to bind, we expose the captured stderr.

import { spawn, spawnSync } from 'node:child_process';
import {
  existsSync,
  openSync as fsOpenSync,
  readSync as fsReadSync,
  closeSync as fsCloseSync,
  unlinkSync as fsUnlinkSync,
} from 'node:fs';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createServer } from 'node:net';
import { randomBytes } from 'node:crypto';
import { WebSocket } from 'ws';

const REPO_ROOT = path.resolve(new URL('..', import.meta.url).pathname);

// Listen on port 0, get the OS-assigned ephemeral port, then close. Returns a usable port
// (with a small TOCTOU race).
function getFreePort() {
  return new Promise((resolve, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

// Poll until the predicate returns a truthy value: check every intervalMs, wait at most timeoutMs.
// Throws on timeout (with the last error returned by the predicate).
export async function waitFor(predicate, timeoutMs = 10000, intervalMs = 100) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const v = await predicate();
      if (v) return v;
    } catch (e) {
      lastErr = e;
    }
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(
    `waitFor timed out after ${timeoutMs}ms${
      lastErr ? `: ${lastErr && lastErr.stack ? lastErr.stack : String(lastErr)}` : ''
    }`,
  );
}

// Async sleep utility.
export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Locate and prepare the backend binary: prefer the prebuilt ../backend/bin/webshell,
// otherwise build it on the fly with `go build`. If go is unavailable and the binary
// does not exist, throw a clear error.
//
// Key point: scrubMacBinary (xattr -c + codesign --force) runs **at most once** per file
// (memoized in-process). Otherwise concurrent tests would repeatedly `codesign --force`
// the same Mach-O and, under a race, corrupt the binary header, causing later spawns to
// report ENOEXEC. Re-running scrub on every startBackend was the historical root cause of
// the binary being corrupted.
function ensureBinary() {
  const binPath = path.join(REPO_ROOT, 'backend', 'bin', 'webshell');
  if (process.env.WEBSHELL_TEST_BIN && process.env.WEBSHELL_TEST_BIN.length) {
    return process.env.WEBSHELL_TEST_BIN;
  }

  // Binary exists — but may have been corrupted by a previous concurrent codesign (file reports "data").
  // If it is not executable, delete it and fall through to the rebuild branch.
  if (existsSync(binPath) && !isExecutableMachO(binPath)) {
    try { fsUnlinkSync(binPath); } catch { /* ignore */ }
  }

  if (!existsSync(binPath)) {
    // Not built yet — try building on the fly.
    const probeGo = spawnSync('go', ['version']);
    if (probeGo.status !== 0) {
      throw new Error(
        `Backend binary not found at ${binPath} and 'go' is not available on PATH. ` +
          `Run 'cd backend && go build -o bin/webshell ./cmd/webshell' first.`,
      );
    }
    const build = spawnSync(
      'go',
      ['build', '-o', 'bin/webshell', './cmd/webshell'],
      { cwd: path.join(REPO_ROOT, 'backend') },
    );
    if (build.status !== 0) {
      throw new Error(
        `go build failed (exit ${build.status}):\nstdout: ${build.stdout?.toString()}\nstderr: ${build.stderr?.toString()}`,
      );
    }
    // Scrub only once after a fresh build.
    scrubMacBinary(binPath);
  }
  return binPath;
}

// isExecutableMachO reads the first few bytes of the binary to determine whether it is a
// valid Mach-O (or ELF, for Linux). Used to detect artifacts corrupted by a codesign race
// (such a file has a damaged header, file reports "data", spawn reports ENOEXEC).
function isExecutableMachO(binPath) {
  let fd;
  try {
    fd = fsOpenSync(binPath, 'r');
    const buf = Buffer.alloc(8);
    const n = fsReadSync(fd, buf, 0, 8, 0);
    if (n < 4) return false;
    // Mach-O magic: 0xFEEDFACE/0xFEEDFACF (32/64-bit, BE per endianness), or reversed.
    // ELF: 0x7F 'E' 'L' 'F'.
    const be = buf.readUInt32BE(0);
    const le = buf.readUInt32LE(0);
    const isMachO =
      be === 0xfeedface || be === 0xfeedfacf ||
      le === 0xfeedface || le === 0xfeedfacf;
    const isElf = buf[0] === 0x7f && buf[1] === 0x45 && buf[2] === 0x4c && buf[3] === 0x46;
    return isMachO || isElf;
  } catch {
    return false;
  } finally {
    if (fd !== undefined) { try { fsCloseSync(fd); } catch { /* ignore */ } }
  }
}

// On macOS, binaries built in sandbox/CI environments may inherit the `com.apple.provenance`
// extended attribute; when the parent process (node) lacks the corresponding TCC runtime
// authorization, the spawned process is actively SIGKILLed by macOS TCC. Here we do a
// best-effort cleanup: strip xattr and apply an ad-hoc signature. All failures are non-fatal —
// if the environment does not support it (non-macOS, no codesign), the caller's spawn will
// still try and throw a clear error.
//
// In-process idempotent: scrubbedPaths prevents codesign from being applied to the same file
// multiple times (under concurrency this would corrupt the Mach-O header, causing later spawns
// to report ENOEXEC / exec format error).
const scrubbedPaths = new Set();
function scrubMacBinary(binPath) {
  if (process.platform !== 'darwin') return;
  if (scrubbedPaths.has(binPath)) return;
  scrubbedPaths.add(binPath);
  try {
    spawnSync('xattr', ['-c', binPath], { stdio: 'ignore' });
  } catch { /* xattr unavailable, ignore */ }
  try {
    spawnSync('codesign', ['-s', '-', '--force', binPath], { stdio: 'ignore' });
  } catch { /* codesign unavailable, ignore */ }
}

// Buffer: accumulates written chunks for convenient whole-text reads.
class LineBuf {
  constructor() {
    this.chunks = [];
    this.text = '';
  }
  push(chunk) {
    if (!chunk) return;
    this.chunks.push(chunk);
    this.text += chunk.toString();
  }
  toString() {
    return this.text;
  }
}

/**
 * Start a fresh backend instance.
 *
 * @param {object} [opts]
 * @param {number} [opts.port]   Explicit HTTP port (default: random available port).
 * @param {number} [opts.sshPort] Explicit SSH port (default: random available port).
 * @param {string} [opts.passcode] SSH passcode (default: random).
 * @param {string} [opts.jwtSecret] JWT signing secret (default: random). Cross-backend tokens
 *   must be invalid, so the default is randomized per backend.
 * @param {number} [opts.jwtTtl] JWT lifetime in minutes (default: 1440). Pass 0 to mint a token
 *   that is already-expired (the service turns ttl_minutes into Duration; 0 yields exp==now).
 * @param {string[]} [opts.cors] CORS allowed_origins list (default: ['*']). Pass specific origins
 *   to exercise the echo + Allow-Credentials + Vary: Origin branches of the CORS middleware.
 * @returns {Promise<{baseUrl:string, wsBaseUrl:string, port:number, sshPort:number, tempDir:string, proc:import('child_process').ChildProcess, stdout:LineBuf, stderr:LineBuf, stop:(opts?:{cleanup?:boolean})=>Promise<void>}>}
 */
export async function startBackend(opts = {}) {
  const port = opts.port ?? (await getFreePort());
  const sshPort = opts.sshPort ?? (await getFreePort());
  const passcode = opts.passcode ?? `test-pass-${randomBytes(4).toString('hex')}`;
  // JWT secret is randomized: it used to be fixed as 'test-jwt-secret', but when starting
  // multiple backends concurrently the TOCTOU race in getFreePort can cause two backends to
  // grab the same HTTP port — the later one fails to bind and exits, while the healthz poll
  // may accidentally hit the one that grabbed the port first. If both backends shared the
  // same secret, a token signed by the other backend would be accepted as valid by this
  // test under cross-talk. A random secret guarantees cross-backend tokens are always invalid,
  // turning "port collision"-style cross-talk into a harmless 1004 (invalid token) instead of
  // a false pass.
  const jwtSecret =
    opts.jwtSecret ?? `test-jwt-secret-${randomBytes(8).toString('hex')}`;
  // jwtTtl is expressed in minutes in config.json (main.go multiplies by time.Minute). The
  // default mirrors the production default; tests pass 0 to mint an already-expired token.
  const jwtTtlMinutes = opts.jwtTtl ?? 1440;
  // CORS allowed_origins: the default wildcard covers the common case; tests pass a concrete
  // origin list to exercise the echo + credentials + Vary branches (and the no-match branch).
  const allowedOrigins = opts.cors ?? ['*'];

  // Temporary working directory under the OS tmpdir. Uses the webshell-test-* prefix so the
  // repo .gitignore catches any leftover files.
  const tempDir = await mkdtemp(path.join(tmpdir(), 'webshell-test-'));

  const config = {
    server: {
      host: '127.0.0.1',
      port,
      ssl: { enabled: false, cert_file: '', key_file: '' },
    },
    ssh: {
      listen: `127.0.0.1:${sshPort}`,
      passcode,
      host_signer_file: '',
    },
    jwt: { secret: jwtSecret, ttl_minutes: jwtTtlMinutes },
    storage: { dir: './data' },
    cors: { allowed_origins: allowedOrigins },
  };

  await mkdir(path.join(tempDir, 'data'), { recursive: true });
  await writeFile(
    path.join(tempDir, 'config.json'),
    JSON.stringify(config, null, 2) + '\n',
  );

  const binPath = ensureBinary();
  const stdout = new LineBuf();
  const stderr = new LineBuf();

  const proc = spawn(binPath, [], {
    cwd: tempDir,
    env: { ...process.env, GIN_MODE: 'release' },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', (c) => stdout.push(c));
  proc.stderr.on('data', (c) => stderr.push(c));

  // If the process exits before becoming ready, expose the captured logs.
  let exitedEarly = null;
  const exitHandler = (code, signal) => {
    if (exitedEarly === null) {
      exitedEarly = { code, signal, stdout: stdout.toString(), stderr: stderr.toString() };
    }
  };
  proc.once('exit', exitHandler);

  const baseUrl = `http://127.0.0.1:${port}`;
  const wsBaseUrl = `ws://127.0.0.1:${port}`;

  // Poll /healthz until it returns 200 or times out.
  try {
    await waitFor(
      async () => {
        if (exitedEarly) {
          throw new Error(
            `backend exited early code=${exitedEarly.code} signal=${exitedEarly.signal}\n` +
              `--- stdout ---\n${exitedEarly.stdout}\n--- stderr ---\n${exitedEarly.stderr}\n` +
              (exitedEarly.signal === 'SIGKILL' && !exitedEarly.stdout && !exitedEarly.stderr
                ? `\nHint: the backend binary was SIGKILLed before producing any output. On macOS this ` +
                  `happens when the binary carries the com.apple.provenance xattr and the parent ` +
                  `process (node) lacks the corresponding TCC runtime authorization (e.g. when ` +
                  `running 'npm test' from a sandbox/CI environment). Re-run 'npm test' in a ` +
                  `normal interactive terminal, or rebuild the backend binary in that terminal ` +
                  `('cd backend && go build -o bin/webshell ./cmd/webshell').`
                : ''),
          );
        }
        try {
          const res = await fetch(`${baseUrl}/healthz`);
          if (!res.ok) return false;
          const body = await res.json();
          return body && body.code === 0;
        } catch {
          return false;
        }
      },
      15000,
      100,
    );
  } catch (e) {
    // Best-effort kill the process, then rethrow with the logs.
    try { proc.kill('SIGKILL'); } catch {}
    throw e;
  }

  const stop = async ({ cleanup = true } = {}) => {
    proc.removeListener('exit', exitHandler);
    if (!proc.killed && proc.exitCode === null && proc.signalCode === null) {
      proc.kill('SIGTERM');
      // Wait up to 3s for graceful shutdown.
      await new Promise((resolve) => {
        const to = setTimeout(() => {
          try { proc.kill('SIGKILL'); } catch {}
          resolve();
        }, 3000);
        proc.once('exit', () => { clearTimeout(to); resolve(); });
      });
    }
    if (cleanup) {
      // The backend may still be writing to ./data during SSH teardown; retry RemoveAll a few
      // times to avoid failing on not-yet-released file handles.
      for (let attempt = 0; attempt < 5; attempt++) {
        try {
          await rm(tempDir, { recursive: true, force: true });
          break;
        } catch {
          await sleep(200);
        }
      }
    }
  };

  return { baseUrl, wsBaseUrl, port, sshPort, passcode, tempDir, proc, stdout, stderr, stop };
}

// ============ WebSocket utilities ============
//
// WS primitives frequently needed in integration tests: wait for open/close, collect messages
// by predicate. Extracted from integration.test.js / ws-frames.test.js to deduplicate.

// Wait for the WS 'open' event within at most timeoutMs. Rejects on timeout or prior close.
export function waitForOpen(ws, timeoutMs = 5000) {
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

// Wait for the WS 'close' event within at most timeoutMs. Returns true on hit, false on timeout.
export function waitForClose(ws, timeoutMs = 4000) {
  return new Promise((resolve) => {
    const to = setTimeout(() => resolve(false), timeoutMs);
    ws.once("close", () => {
      clearTimeout(to);
      resolve(true);
    });
  });
}

// Collect WS messages (text + binary unified as a utf8 string) within at most timeoutMs; ends
// when the predicate hits or on timeout. Returns { messages, ok }.
// close/error are also treated as end (ok=false).
export function collectUntil(ws, predicate, timeoutMs = 8000) {
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
    ws.on("message", (data) => {
      const s = text(data);
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

// Connect to a session's WS and wait for open. Returns the ws.
export async function connectWS(wsBaseUrl, sessionId, token) {
  const ws = new WebSocket(`${wsBaseUrl}/api/v1/ws/${sessionId}?token=${token}`);
  await waitForOpen(ws);
  return ws;
}

// Convenience helper: perform an authenticated JSON fetch, returning { status, body }.
// body is the parsed ApiResponse envelope (or raw text when not JSON).
export async function apiFetch(baseUrl, method, path_, { token, json, raw, follow } = {}) {
  const headers = {};
  let body;
  if (token) headers['Authorization'] = `Bearer ${token}`;
  if (json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(json);
  } else if (raw !== undefined) {
    body = raw;
  }
  const res = await fetch(`${baseUrl}${path_}`, {
    method,
    headers,
    body,
    redirect: follow === undefined ? 'follow' : follow,
  });
  const ct = res.headers.get('content-type') || '';
  if (ct.includes('application/json')) {
    return { status: res.status, headers: res.headers, body: await res.json() };
  }
  return { status: res.status, headers: res.headers, body: await res.text() };
}