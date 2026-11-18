import http from 'node:http';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { mkdir, writeFile, chmod, lstat, unlink, readFile } from 'node:fs/promises';
import path from 'node:path';

export const PORT = 17643;
const BODY_LIMIT = 128 * 1024;
class HttpError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}
export async function startServer({ root = process.cwd(), origin = 'https://chronicleclassic.com', leaseMs = 10_000, callTimeoutMs = 60_000, onChange = () => {}, port = PORT, writeSession = true, now = Date.now } = {}) {
  const parsedOrigin = new URL(origin);
  if (origin.includes('*') || parsedOrigin.origin !== origin || !['http:', 'https:'].includes(parsedOrigin.protocol)) throw new Error('--origin must be an exact HTTP(S) origin without a trailing slash');
  const origins = new Set(['https://chronicleclassic.com', 'http://localhost:4000', origin]);
  const token = randomBytes(32).toString('hex');
  let lease;
  let closed = false;
  const pending = new Map();
  const changed = () => { try { Promise.resolve(onChange()).catch(() => {}); } catch { /* Notifications cannot break the relay. */ } };
  const active = () => lease && now() - lease.seen < leaseMs;
  const send = (res, status, body) => {
    if (!res.destroyed) { res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(body)); }
  };
  function finish(id, status, payload) {
    const item = pending.get(id);
    if (!item) return;
    clearTimeout(item.timer);
    pending.delete(id);
    send(item.res, status, { id, generation: item.generation, ...payload });
  }
  function expire() {
    if (lease && !active()) {
      lease = undefined;
      changed();
      for (const id of pending.keys()) finish(id, 409, { error: 'browser_disconnected' });
    }
  }
  async function body(req) {
    let size = 0;
    const chunks = [];
    for await (const chunk of req) {
      size += chunk.length;
      if (size > BODY_LIMIT) throw new HttpError(413, 'Request body must not exceed 128 KiB');
      chunks.push(chunk);
    }
    try { return JSON.parse(Buffer.concat(chunks).toString() || '{}'); }
    catch { throw new HttpError(400, 'Invalid JSON'); }
  }
  const server = http.createServer(async (req, res) => {
    try {
      if (req.headers.host !== `127.0.0.1:${server.address().port}`) throw new HttpError(403, 'Invalid Host');
      const browser = req.headers.origin !== undefined;
      if (browser && !origins.has(req.headers.origin)) throw new HttpError(403, 'Origin not allowed');
      if (browser) {
        res.setHeader('Access-Control-Allow-Origin', req.headers.origin);
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
      if (req.method === 'POST' && req.url === '/v1/connect') {
        if (!browser) throw new HttpError(403, 'Connect requires approved Origin');
        const data = await body(req);
        if (!data || data.protocol !== 2) throw new HttpError(400, 'Expected protocol 2');
        send(res, 200, { protocol: 2, token });
        return;
      }
      const authorization = req.headers.authorization ?? '';
      const expected = `Bearer ${token}`;
      if (Buffer.byteLength(authorization) !== Buffer.byteLength(expected) || !timingSafeEqual(Buffer.from(authorization), Buffer.from(expected))) throw new HttpError(401, 'Bearer token required');
      if (closed) throw new HttpError(503, 'Dev server stopped');
      expire();
      const route = `${req.method} ${req.url}`;
      if (route === 'POST /v1/browser') {
        const data = await body(req);
        const string = (value, max = 256) => typeof value === 'string' && value.length > 0 && value.length <= max;
        const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
        if (!object(data) || !string(data.clientId, 128) || !object(data.page) || !string(data.page.url, 8192) || !string(data.page.generation) || !string(data.page.sessionId) || !Array.isArray(data.tools) || data.tools.length > 64) throw new HttpError(400, 'Invalid browser heartbeat');
        let url;
        try { url = new URL(data.page.url); } catch { throw new HttpError(400, 'Invalid page URL'); }
        if (!['http:', 'https:'].includes(url.protocol) || !origins.has(url.origin) || (browser && url.origin !== req.headers.origin) || url.username || url.password) throw new HttpError(400, 'Page URL origin not allowed');
        url.hash = '';
        const page = { url: url.href, generation: data.page.generation, sessionId: data.page.sessionId };
        const names = new Set();
        for (const tool of data.tools) {
          if (!object(tool) || !string(tool.name, 128) || !/^[a-zA-Z0-9_.-]+$/.test(tool.name) || ['whereami', 'status'].includes(tool.name) || names.has(tool.name) || typeof tool.description !== 'string' || tool.description.length > 8192 || !object(tool.inputSchema) || tool.inputSchema.type !== 'object' || !object(tool.capability) || !string(tool.capability.id) || (!Number.isSafeInteger(tool.capability.version) || tool.capability.version < 1) || !['read', 'view_mutation'].includes(tool.effect) || !['never', 'unless_yolo'].includes(tool.confirmation)) throw new HttpError(400, 'Invalid or duplicate tool descriptor');
          for (const key of ['scopeType', 'scopeId']) if (tool.capability[key] !== undefined && !string(tool.capability[key], 1024)) throw new HttpError(400, 'Invalid tool capability');
          if (tool.capability.stateRevision !== undefined && (!Number.isSafeInteger(tool.capability.stateRevision) || tool.capability.stateRevision < 0)) throw new HttpError(400, 'Invalid capability stateRevision');
          names.add(tool.name);
        }
        if (data.result !== undefined && (!object(data.result) || !string(data.result.id, 128) || !string(data.result.generation) || !Object.hasOwn(data.result, 'result'))) throw new HttpError(400, 'Invalid browser result');
        if (data.result && Buffer.byteLength(JSON.stringify(data.result.result)) > 64 * 1024) throw new HttpError(413, 'Browser result exceeds 64 KiB');
        expire();
        if (active() && lease.clientId !== data.clientId) throw new HttpError(409, 'Another browser client owns the active lease');
        const navigated = lease && (lease.page.generation !== page.generation || lease.page.sessionId !== page.sessionId);
        const catalogChanged = !lease || navigated || JSON.stringify(lease.tools) !== JSON.stringify(data.tools);
        if (navigated) for (const id of pending.keys()) finish(id, 409, { error: 'stale_page' });
        lease = { clientId: data.clientId, page, tools: data.tools, seen: now() };
        if (catalogChanged) changed();
        if (data.result) {
          const item = pending.get(data.result.id);
          // Late results never unpair a tab, including results from before navigation.
          if (!item || item.generation !== data.result.generation || item.generation !== page.generation || item.sessionId !== page.sessionId) {
            send(res, 200, { ok: true, resultAccepted: false });
            return;
          }
          finish(data.result.id, 200, { result: data.result.result });
        }
        send(res, 200, { ok: true, ...(data.result ? { resultAccepted: true } : {}) });
      } else if (route === 'GET /v1/state') {
        if (browser && (!active() || req.headers['x-chronicle-client'] !== lease.clientId)) throw new HttpError(409, 'Heartbeat required from lease owner before state');
        send(res, 200, { protocol: 2, commands: [...pending].map(([id, item]) => ({ id, type: 'tool', generation: item.generation, toolName: item.tool.name, arguments: item.arguments, capability: item.tool.capability })) });
      } else if (route === 'GET /v1/status') {
        send(res, 200, { protocol: 2, page: active() ? lease.page : null, tools: active() ? lease.tools : [], connected: !!active(), pending: pending.size });
      } else if (route === 'POST /v1/call') {
        const data = await body(req);
        expire();
        if (closed) throw new HttpError(503, 'Relay stopped');
        if (!active()) throw new HttpError(409, 'No browser connected');
        if (pending.size) throw new HttpError(409, 'A tool call is already pending');
        if (!data || typeof data.name !== 'string' || data.arguments === null || typeof data.arguments !== 'object' || Array.isArray(data.arguments)) throw new HttpError(400, 'Expected tool name and arguments object');
        const tool = lease.tools.find(tool => tool.name === data.name);
        if (!tool) throw new HttpError(404, 'Unknown browser tool');
        const capabilityKeys = ['id', 'version', 'scopeType', 'scopeId', 'stateRevision'];
        if (!data.page || data.page.generation !== lease.page.generation || data.page.sessionId !== lease.page.sessionId || !data.capability || capabilityKeys.some(key => data.capability[key] !== tool.capability[key])) throw new HttpError(409, 'stale_page');
        if (res.destroyed) return;
        const id = randomBytes(16).toString('hex');
        const timer = setTimeout(() => finish(id, 504, { error: 'Tool call timed out waiting for browser result' }), callTimeoutMs);
        pending.set(id, { generation: lease.page.generation, sessionId: lease.page.sessionId, tool, arguments: data.arguments, res, timer });
        res.on('close', () => { if (pending.get(id)?.res === res) { clearTimeout(timer); pending.delete(id); } });
      } else throw new HttpError(404, 'Not found');
    } catch (error) {
      send(res, error.status ?? 500, { error: error.status ? error.message : 'Internal server error' });
    }
  });
  try {
    await new Promise((resolve, reject) => { server.once('error', reject); server.listen(port, '127.0.0.1', resolve); });
  } catch (error) {
    if (error.code === 'EADDRINUSE') throw new Error(`Chronicle module port ${port} is already in use. Stop the other module server before starting this one.`, { cause: error });
    throw error;
  }
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
      await writeFile(sessionPath, JSON.stringify({ port: server.address().port, token }) + '\n', { mode: 0o600 });
      await chmod(sessionPath, 0o600);
    } catch (error) { await close(); throw error; }
  }
  return { token, port: server.address().port, close };
}
