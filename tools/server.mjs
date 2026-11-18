import http from 'node:http';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { mkdir, writeFile, chmod, lstat, unlink, readFile } from 'node:fs/promises';
import path from 'node:path';
import { snapshot } from './package.mjs';

export const PORT = 17643;
const BODY_LIMIT = 128 * 1024 - 1;
class HttpError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}
export async function startServer({ root, origin = 'https://chronicleclassic.com', leaseMs = 10_000, previewTimeoutMs = 60_000, port = PORT, writeSession = true, now = Date.now } = {}) {
  const parsedOrigin = new URL(origin);
  if (parsedOrigin.origin !== origin || !['http:', 'https:'].includes(parsedOrigin.protocol)) throw new Error('--origin must be an exact HTTP(S) origin without a trailing slash');
  const token = randomBytes(32).toString('hex');
  let lease;
  let closed = false;
  const pending = new Map();
  const active = () => lease && now() - lease.seen < leaseMs;
  const send = (res, status, body) => {
    if (!res.destroyed) { res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(body)); }
  };
  function finish(id, status, payload) {
    const item = pending.get(id);
    if (!item) return;
    clearTimeout(item.timer);
    pending.delete(id);
    send(item.res, status, { id, revision: item.revision, ...payload });
  }
  function expire() {
    if (lease && !active()) {
      lease = undefined;
      for (const id of pending.keys()) finish(id, 409, { error: 'Browser disconnected or lease expired' });
    }
  }
  async function body(req) {
    let size = 0;
    const chunks = [];
    for await (const chunk of req) {
      size += chunk.length;
      if (size > BODY_LIMIT) throw new HttpError(413, 'Request body must be smaller than 128 KiB');
      chunks.push(chunk);
    }
    try { return JSON.parse(Buffer.concat(chunks).toString() || '{}'); }
    catch { throw new HttpError(400, 'Invalid JSON'); }
  }
  const server = http.createServer(async (req, res) => {
    try {
      if (req.headers.host !== `127.0.0.1:${server.address().port}`) throw new HttpError(403, 'Invalid Host');
      const browser = req.headers.origin !== undefined;
      if (browser && req.headers.origin !== origin) throw new HttpError(403, 'Origin not allowed');
      if (browser) {
        res.setHeader('Access-Control-Allow-Origin', origin);
        res.setHeader('Vary', 'Origin');
      }
      // Preflights cannot carry bearer credentials. They disclose no state.
      if (req.method === 'OPTIONS') {
        if (!browser) throw new HttpError(403, 'Preflight requires approved Origin');
        res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
        res.setHeader('Access-Control-Allow-Headers', 'Authorization, Content-Type, X-Chronicle-Client');
        res.setHeader('Access-Control-Allow-Private-Network', 'true');
        res.writeHead(204); res.end(); return;
      }
      const authorization = req.headers.authorization ?? '';
      const expected = `Bearer ${token}`;
      if (Buffer.byteLength(authorization) !== Buffer.byteLength(expected) || !timingSafeEqual(Buffer.from(authorization), Buffer.from(expected))) throw new HttpError(401, 'Bearer token required');
      expire();
      const route = `${req.method} ${req.url}`;
      if (route === 'POST /v1/browser') {
        const data = await body(req);
        if (!data || typeof data.clientId !== 'string' || !data.clientId.length || data.clientId.length > 128 || !data.context || typeof data.context.instanceId !== 'string' || data.context.instanceId.length > 256 || !Array.isArray(data.context.encounterIds) || data.context.encounterIds.length > 1024 || !data.context.encounterIds.every(x => typeof x === 'string' && x.length <= 256)) throw new HttpError(400, 'Invalid browser heartbeat');
        expire();
        if (active() && lease.clientId !== data.clientId) throw new HttpError(409, 'Another browser client owns the active lease');
        if (data.result !== undefined && (!data.result || typeof data.result.id !== 'string' || typeof data.result.revision !== 'string' || !Object.hasOwn(data.result, 'result'))) throw new HttpError(400, 'Invalid browser result');
        lease = { clientId: data.clientId, context: data.context, seen: now() };
        if (data.result) {
          const item = pending.get(data.result.id);
          // A result can race source edits, timeout, cancellation, or lease expiry.
          // Accept the heartbeat regardless so a stale result never unpairs a tab.
          if (!item || item.revision !== data.result.revision) {
            send(res, 200, { ok: true, resultAccepted: false });
            return;
          }
          finish(data.result.id, 200, { result: data.result.result });
        }
        send(res, 200, { ok: true, ...(data.result ? { resultAccepted: true } : {}) });
      } else if (route === 'GET /v1/state') {
        if (browser && (!active() || req.headers['x-chronicle-client'] !== lease.clientId)) throw new HttpError(409, 'Heartbeat required from lease owner before state');
        const state = await snapshot(root);
        // Retire commands if their source changed, even before the browser executes.
        for (const [id, item] of pending) if (item.revision !== state.revision) finish(id, 409, { error: 'Source changed; retry preview' });
        send(res, 200, { ...state, commands: [...pending].map(([id, item]) => ({ id, type: 'preview', revision: item.revision })) });
      } else if (route === 'GET /v1/status') {
        const state = await snapshot(root);
        send(res, 200, { protocol: 1, revision: state.revision, error: state.error, browser: active() ? { clientId: lease.clientId, context: lease.context } : null, pending: pending.size });
      } else if (route === 'POST /v1/preview') {
        await body(req);
        if (!active()) throw new HttpError(409, 'No browser connected. Approve pairing in Chronicle first.');
        if (pending.size) throw new HttpError(409, 'A preview is already pending');
        const clientId = lease.clientId;
        const state = await snapshot(root);
        if (state.error) throw new HttpError(422, `Invalid module: ${state.error}`);
        // Recheck after asynchronous filesystem work.
        expire();
        if (!active() || lease.clientId !== clientId) throw new HttpError(409, 'Browser disconnected or lease expired');
        if (res.destroyed) return;
        if (pending.size) throw new HttpError(409, 'A preview is already pending');
        const id = randomBytes(16).toString('hex');
        const timer = setTimeout(() => finish(id, 504, { error: 'Preview timed out after waiting for browser result' }), previewTimeoutMs);
        pending.set(id, { revision: state.revision, res, timer });
        res.on('close', () => { if (pending.get(id)?.res === res) { clearTimeout(timer); pending.delete(id); } });
      } else throw new HttpError(404, 'Not found');
    } catch (error) {
      send(res, error.status ?? 500, { error: error.status ? error.message : 'Internal server error' });
    }
  });
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
  const sweeper = setInterval(expire, Math.min(1000, leaseMs));
  sweeper.unref();
  const sessionDir = path.join(root, '.chronicle');
  const sessionPath = path.join(sessionDir, 'dev.json');
  async function close() {
    if (closed) return;
    closed = true;
    clearInterval(sweeper);
    for (const id of pending.keys()) finish(id, 503, { error: 'Dev server stopped' });
    await new Promise(resolve => { server.close(resolve); server.closeAllConnections(); });
    if (writeSession) {
      try { if (JSON.parse(await readFile(sessionPath, 'utf8')).token === token) await unlink(sessionPath); } catch { /* Already removed. */ }
    }
  }
  if (writeSession) {
    try {
      await mkdir(sessionDir, { mode: 0o700, recursive: true });
      if ((await lstat(sessionDir)).isSymbolicLink()) throw new Error('.chronicle must not be a symlink');
      // Refuse existing symlinks and keep credentials private even on a preexisting file.
      try { if ((await lstat(sessionPath)).isSymbolicLink()) throw new Error('dev.json must not be a symlink'); } catch (e) { if (e.code !== 'ENOENT') throw e; }
      await writeFile(sessionPath, JSON.stringify({ port: PORT, token }) + '\n', { mode: 0o600 });
      await chmod(sessionPath, 0o600);
    } catch (error) { await close(); throw error; }
  }
  return { token, port: server.address().port, close };
}
