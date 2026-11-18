import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, mkdir, symlink, rm, stat, readFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { fileURLToPath } from 'node:url';
import { unzipSync } from 'fflate';
import { ToolListChangedNotificationSchema } from '@modelcontextprotocol/sdk/types.js';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { startServer } from '../tools/server.mjs';
import { snapshot, digest } from '../tools/package.mjs';
import { runMcp } from '../tools/mcp.mjs';
import { PassThrough } from 'node:stream';
import { InMemoryTransport } from '@modelcontextprotocol/sdk/inMemory.js';
import { parseCliArgs } from '../tools/args.mjs';

const origin = 'https://chronicleclassic.com';
const page = { url: `${origin}/modules/test/edit?tab=code`, generation: 'generation-1', sessionId: 'session-1' };
const tools = [{ name: 'preview', description: 'Browser-owned preview', inputSchema: { type: 'object', properties: { limit: { type: 'integer', minimum: 1 } }, additionalProperties: false }, capability: { id: 'module.preview', version: 1, scopeType: 'module', scopeId: 'test', stateRevision: 1 }, effect: 'read', confirmation: 'never' }];
async function fixture(t) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'chronicle-module-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await writeFile(path.join(root, 'project.json'), JSON.stringify({ manifest_version: 1, kind: 'panel', entrypoint: 'main.lua', assets: [] }));
  await writeFile(path.join(root, 'README.md'), 'test');
  await writeFile(path.join(root, 'main.lua'), 'return {}');
  return root;
}
async function serve(t, options = {}) {
  const root = await fixture(t);
  const server = await startServer({ root, port: 0, writeSession: false, ...options });
  t.after(() => server.close());
  function request(route, { method = 'GET', data, headers = {}, signal } = {}) {
    return fetch(`http://127.0.0.1:${server.port}/v1/${route}`, {
      method, headers: { Authorization: `Bearer ${server.token}`, ...headers }, signal,
      body: data === undefined ? undefined : JSON.stringify(data),
    });
  }
  const heartbeat = (clientId = 'browser-1', result) => request('browser', { method: 'POST', headers: { Origin: origin }, data: { clientId, page, tools, ...(result ? { result } : {}) } });
  const state = () => request('state', { headers: { Origin: origin, 'X-Chronicle-Client': 'browser-1' } });
  return { root, server, request, heartbeat, state };
}
async function eventually(fn) {
  const deadline = Date.now() + 3000;
  while (Date.now() < deadline) { const result = await fn(); if (result) return result; await new Promise(r => setTimeout(r, 10)); }
  throw new Error('Condition not reached within 3 seconds');
}

test('deterministic bounded ZIP includes only package files and declared rasters', async t => {
  const root = await fixture(t);
  await mkdir(path.join(root, 'tools'));
  await writeFile(path.join(root, 'tools/secret.lua'), 'not packaged');
  await mkdir(path.join(root, 'lua-types'));
  await writeFile(path.join(root, 'lua-types/api.lua'), 'not packaged');
  await writeFile(path.join(root, 'unused.png'), 'not packaged');
  const a = await snapshot(root);
  assert.equal(a.error, undefined);
  const zip = Buffer.from(a.archive, 'base64');
  assert.equal(a.revision, digest(zip));
  assert.deepEqual(Object.keys(unzipSync(zip)), ['README.md', 'main.lua', 'project.json']);
  assert.deepEqual(await snapshot(root), a);
  await writeFile(path.join(root, 'main.lua'), 'return {changed=true}');
  assert.notEqual((await snapshot(root)).revision, a.revision);
  const limited = await snapshot(root, { files: 2, fileBytes: 1024, bytes: 2048 });
  assert.equal(limited.archive, ''); assert.match(limited.error, /limit/);
  await symlink('main.lua', path.join(root, 'linked.lua'));
  assert.match((await snapshot(root)).error, /Symlink/);
});

test('invalid traversal, assets, missing entrypoints and oversized content fail closed', async t => {
  const root = await fixture(t);
  const manifest = { manifest_version: 1, kind: 'panel', entrypoint: 'main.lua', assets: ['../private.png'] };
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  assert.match((await snapshot(root)).error, /Unsafe/);
  manifest.assets = ['image.svg'];
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  assert.match((await snapshot(root)).error, /raster/);
  manifest.assets = []; manifest.entrypoint = 'missing.lua';
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  assert.match((await snapshot(root)).error, /entrypoint/);
  manifest.entrypoint = 'main.lua';
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  await writeFile(path.join(root, 'main.lua'), Buffer.alloc(1024 * 1024 + 1));
  assert.match((await snapshot(root)).error, /large/);
});

test('HTTP authentication, exact origins, host and PNA preflight', async t => {
  const { server, request } = await serve(t);
  for (const route of ['state', 'status', 'browser', 'call']) {
    const response = await request(route, { method: ['browser', 'call'].includes(route) ? 'POST' : 'GET', headers: { Authorization: '' } });
    assert.equal(response.status, 401);
  }
  assert.equal((await request('status', { headers: { Origin: `${origin}.evil` } })).status, 403);
  assert.equal((await request('status')).status, 200);
  const preflight = await request('state', { method: 'OPTIONS', headers: { Origin: origin, Authorization: '', 'Access-Control-Request-Private-Network': 'true' } });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get('access-control-allow-private-network'), 'true');
  assert.match(preflight.headers.get('access-control-allow-headers'), /X-Chronicle-Client/);
  const status = await new Promise((resolve, reject) => {
    const req = http.get({ hostname: '127.0.0.1', port: server.port, path: '/v1/status', headers: { Host: 'localhost:17643', Authorization: `Bearer ${server.token}` } }, res => { res.resume(); resolve(res.statusCode); });
    req.on('error', reject);
  });
  assert.equal(status, 403);
});

test('browser heartbeat owns lease and calls are generation pinned', async t => {
  const { request, heartbeat, state, root } = await serve(t);
  assert.equal((await state()).status, 409);
  assert.equal((await request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } })).status, 409);
  assert.equal((await heartbeat()).status, 200);
  assert.equal((await heartbeat('other')).status, 409);
  assert.equal((await request('state', { headers: { Origin: origin, 'X-Chronicle-Client': 'other' } })).status, 409);
  const preview = request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } });
  const command = await eventually(async () => (await (await state()).json()).commands[0]);
  assert.equal((await request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } })).status, 409);
  assert.deepEqual(await (await heartbeat('browser-1', { ...command, generation: 'wrong', result: {} })).json(), { ok: true, resultAccepted: false });
  assert.equal((await heartbeat('browser-1', { ...command, result: { ok: true, value: 123 } })).status, 200);
  assert.deepEqual(await (await preview).json(), { id: command.id, generation: command.generation, result: { ok: true, value: 123 } });
  const stale = request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } });
  const staleCommand = await eventually(async () => (await (await state()).json()).commands[0]);
  const moved = { ...page, generation: 'generation-2' };
  await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: moved, tools } });
  assert.equal((await (await state()).json()).commands.length, 0);
  assert.equal((await (await stale).json()).error, 'stale_page');
  assert.deepEqual(await (await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: moved, tools, result: { ...staleCommand, result: {} } } })).json(), { ok: true, resultAccepted: false });
  assert.equal((await state()).status, 200);
  // Invalid local module files cannot influence the browser-owned relay.
  await writeFile(path.join(root, 'project.json'), '{invalid');
  assert.equal((await (await state()).json()).archive, undefined);
});

test('tool timeout returns pinned generation', async t => {
  const { request, heartbeat } = await serve(t, { callTimeoutMs: 20 });
  await heartbeat();
  const timed = await request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } });
  assert.equal(timed.status, 504);
  const result = await timed.json();
  assert.match(result.error, /timed out/);
  assert.equal(result.generation, page.generation);
});

test('disconnect cancellation, result size bounds and lease expiration', async t => {
  let clock = 0;
  const { request, heartbeat, state } = await serve(t, { now: () => clock });
  await heartbeat();
  const controller = new AbortController();
  const pending = request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability }, signal: controller.signal });
  await eventually(async () => (await (await state()).json()).commands[0]);
  controller.abort(); await assert.rejects(pending);
  await eventually(async () => (await (await request('status')).json()).pending === 0);
  assert.equal((await heartbeat('browser-1', { id: 'x', generation: 'x', result: 'x'.repeat(128 * 1024) })).status, 413);
  const abandoned = request('call', { method: 'POST', data: { name: 'preview', arguments: {}, page, capability: tools[0].capability } });
  await eventually(async () => (await (await state()).json()).commands.length);
  clock = 10_001;
  assert.equal((await (await request('status')).json()).page, null);
  assert.match((await (await abandoned).json()).error, /browser_disconnected/);
  assert.equal((await heartbeat('new-browser')).status, 200);
});

test('actual official MCP stdio client to HTTP browser roundtrip and private session', async t => {
  const root = await fixture(t);
  const mcpUrl = new URL('../tools/mcp.mjs', import.meta.url).href;
  // The official stdio client starts MCP, which owns its own ephemeral listener.
  const transport = new StdioClientTransport({ command: process.execPath, args: ['--input-type=module', '-e', `import { runMcp } from ${JSON.stringify(mcpUrl)}; await runMcp(process.argv[1], { port: 0, origin: 'https://custom.example' });`, root], stderr: 'pipe' });
  const client = new Client({ name: 'integration-test', version: '1' });
  await client.connect(transport);
  t.after(() => client.close());
  const sessionPath = path.join(root, '.chronicle/dev.json');
  assert.equal((await stat(sessionPath)).mode & 0o777, 0o600);
  const session = JSON.parse(await readFile(sessionPath));
  assert.ok(session.port > 0);
  const request = (route, options = {}) => fetch(`http://127.0.0.1:${session.port}/v1/${route}`, {
    ...options, headers: { Authorization: `Bearer ${session.token}`, ...options.headers },
  });
  const heartbeat = (clientId = 'browser-1', result) => request('browser', { method: 'POST', headers: { Origin: origin }, body: JSON.stringify({ clientId, page, tools, ...(result ? { result } : {}) }) });
  const state = () => request('state', { headers: { Origin: origin, 'X-Chronicle-Client': 'browser-1' } });
  const connect = await request('connect', { method: 'POST', headers: { Authorization: '', Origin: 'https://custom.example' }, body: '{"protocol":2}' });
  assert.deepEqual(await connect.json(), { protocol: 2, token: session.token });
  assert.deepEqual((await client.listTools()).tools.map(tool => tool.name).sort(), ['whereami']);
  const absent = await client.callTool({ name: 'preview', arguments: {} });
  assert.equal(absent.isError, true); assert.match(absent.content[0].text, /whereami or tools\/list/);
  let notifications = 0;
  client.setNotificationHandler(ToolListChangedNotificationSchema, () => { notifications++; });
  await heartbeat();
  await eventually(() => notifications > 0);
  assert.deepEqual((await client.listTools()).tools[1], { name: tools[0].name, description: tools[0].description, inputSchema: tools[0].inputSchema });
  const preview = client.callTool({ name: 'preview', arguments: {} });
  const command = await eventually(async () => (await (await state()).json()).commands[0]);
  await heartbeat('browser-1', { ...command, result: { ok: true, metrics: [42] } });
  const output = JSON.parse((await preview).content[0].text);
  assert.deepEqual(output.metrics, [42]);
  const status = JSON.parse((await client.callTool({ name: 'whereami', arguments: {} })).content[0].text);
  assert.deepEqual(status.page, page);
  const canceled = client.callTool({ name: 'preview', arguments: {} }).catch(error => error);
  await eventually(async () => (await (await state()).json()).commands.length);
  await client.close();
  await canceled;
  await eventually(async () => { try { await stat(sessionPath); return false; } catch (error) { return error.code === 'ENOENT'; } });
  await assert.rejects(request('status'));
  const rebound = await startServer({ root, port: session.port });
  await rebound.close();
});


test('CLI prints only real editor pairing links and infers their origin', async t => {
  const root = await fixture(t);
  const script = fileURLToPath(new URL('../tools/cli.mjs', import.meta.url));
  async function run(args) {
    const child = spawn(process.execPath, [script, 'dev', ...args, '--root', root, '--port', '0'], { stdio: ['ignore', 'pipe', 'pipe'] });
    let stdout = '', stderr = '';
    child.stdout.on('data', data => { stdout += data; });
    child.stderr.on('data', data => { stderr += data; });
    const exited = once(child, 'exit');
    t.after(() => { if (child.exitCode === null) child.kill('SIGTERM'); });
    await eventually(() => stderr.includes('Stop with Ctrl+C.') && true);
    child.kill('SIGTERM');
    const [code] = await exited;
    assert.equal(code, 0);
    assert.equal(stdout, '');
    return stderr;
  }
  const noUrl = await run([]);
  assert.match(noUrl, /restart with the full module editor URL/);
  assert.doesNotMatch(noUrl, /#module-dev=/);
  const forwarded = await run(['--', '--url', 'http://localhost:4000/modules/emyrk-2/test/edit']);
  assert.match(forwarded, /http:\/\/localhost:4000\/modules\/emyrk-2\/test\/edit#module-dev=[a-f0-9]{64}/);
  const withUrl = await run(['--url', 'https://example.com/modules/alice/damage/edit']);
  assert.match(withUrl, /https:\/\/example.com\/modules\/alice\/damage\/edit#module-dev=[a-f0-9]{64}/);
});


test('README is optional but symlinks and non-files are still invalid', async t => {
  const root = await fixture(t);
  const readme = path.join(root, 'README.md');
  await rm(readme);
  const missing = await snapshot(root);
  assert.equal(missing.error, undefined);
  assert.deepEqual(Object.keys(unzipSync(Buffer.from(missing.archive, 'base64'))), ['main.lua', 'project.json']);
  await symlink('nonexistent.md', readme);
  assert.match((await snapshot(root)).error, /Symlink/);
  await rm(readme);
  await mkdir(readme);
  assert.match((await snapshot(root)).error, /Not a regular file/);
});

test('GIF assets fail while declared PNG JPEG and WebP assets are packaged', async t => {
  const root = await fixture(t);
  const manifest = JSON.parse(await readFile(path.join(root, 'project.json')));
  manifest.assets = ['image.gif'];
  await writeFile(path.join(root, 'image.gif'), 'GIF89a');
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  const rejected = await snapshot(root);
  assert.equal(rejected.archive, '');
  assert.match(rejected.error, /raster/);
  manifest.assets = ['image.png', 'image.jpg', 'image.jpeg', 'image.webp'];
  for (const name of manifest.assets) await writeFile(path.join(root, name), 'image fixture');
  await writeFile(path.join(root, 'project.json'), JSON.stringify(manifest));
  const accepted = await snapshot(root);
  assert.equal(accepted.error, undefined);
  const names = Object.keys(unzipSync(Buffer.from(accepted.archive, 'base64')));
  for (const name of manifest.assets) assert.ok(names.includes(name));
  assert.ok(!names.includes('image.gif'));
});


test('CLI argument parsing supports npm and pnpm separators without a listening server', () => {
  const url = 'http://localhost:4000/modules/emyrk-2/test/edit';
  for (const command of ['dev', 'mcp']) {
    const direct = parseCliArgs([command, '--url', url]);
    const forwarded = parseCliArgs([command, '--', '--url', url]);
    assert.deepEqual(forwarded, direct);
    assert.equal(forwarded.values.url, url);
    assert.deepEqual(forwarded.positionals, [command]);
  }
  assert.equal(parseCliArgs(['dev', '--root=--']).values.root, '--');
  assert.throws(() => parseCliArgs(['dev', '--', '--unknown']), /Unknown option/);
});

test('bootstrap requires exact approved Origin, loopback Host and protocol without bearer', async t => {
  const { server, request } = await serve(t, { origin: 'https://custom.example' });
  const connect = headers => request('connect', { method: 'POST', headers: { Authorization: '', ...headers }, data: { protocol: 2 } });
  for (const approved of [origin, 'http://localhost:4000', 'https://custom.example']) {
    const response = await connect({ Origin: approved });
    assert.equal(response.status, 200);
    assert.equal(response.headers.get('access-control-allow-origin'), approved);
    assert.equal(response.headers.get('cache-control'), 'no-store');
    assert.deepEqual(await response.json(), { protocol: 2, token: server.token });
    const preflight = await request('connect', { method: 'OPTIONS', headers: { Authorization: '', Origin: approved, 'Access-Control-Request-Private-Network': 'true' } });
    assert.equal(preflight.status, 204);
    assert.equal(preflight.headers.get('access-control-allow-private-network'), 'true');
  }
  for (const denied of [undefined, 'null', '*', `${origin}.evil`, `${origin}/`, 'http://localhost:4001', 'http://127.0.0.1:4000']) {
    const response = await connect(denied === undefined ? {} : { Origin: denied });
    assert.equal(response.status, 403);
    assert.equal(response.headers.get('access-control-allow-origin'), null);
    assert.ok(!(await response.text()).includes(server.token));
  }
  for (const protocol of [undefined, 1, '2']) {
    assert.equal((await request('connect', { method: 'POST', headers: { Authorization: '', Origin: origin }, data: { protocol } })).status, 400);
  }
  assert.equal((await request('connect', { headers: { Authorization: '', Origin: origin } })).status, 401);
  assert.equal((await request('connect', { method: 'OPTIONS', headers: { Authorization: '' } })).status, 403);
  const status = await new Promise((resolve, reject) => {
    const req = http.request({ hostname: '127.0.0.1', port: server.port, path: '/v1/connect', method: 'POST', headers: { Host: `localhost:${server.port}`, Origin: origin } }, res => { res.resume(); resolve(res.statusCode); });
    req.on('error', reject); req.end('{"protocol":2}');
  });
  assert.equal(status, 403);
  for (const invalid of ['*', 'https://*.example.com', 'https://example.com/', 'null']) await assert.rejects(startServer({ origin: invalid, port: 0 }));
});

test('MCP refuses an occupied port without replacing another module session', async t => {
  const { server, root } = await serve(t, { writeSession: true });
  const before = await readFile(path.join(root, '.chronicle/dev.json'), 'utf8');
  const otherRoot = await fixture(t);
  await assert.rejects(runMcp(otherRoot, { port: server.port }), /already in use.*other module/);
  assert.equal(await readFile(path.join(root, '.chronicle/dev.json'), 'utf8'), before);
  await assert.rejects(stat(path.join(otherRoot, '.chronicle/dev.json')), { code: 'ENOENT' });
});

test('MCP input disconnect cancels pending previews and releases port and session', async t => {
  for (const event of ['end', 'close', 'error']) {
    const root = await fixture(t);
    const input = new PassThrough();
    const [clientTransport, serverTransport] = InMemoryTransport.createLinkedPair();
    const owned = await runMcp(root, { port: 0, input, transport: serverTransport });
    t.after(() => owned.close());
    const client = new Client({ name: 'lifecycle-test', version: '1' });
    await client.connect(clientTransport);
    const { token } = JSON.parse(await readFile(path.join(root, '.chronicle/dev.json')));
    const request = (route, options = {}) => fetch(`http://127.0.0.1:${owned.port}/v1/${route}`, { ...options, headers: { Authorization: `Bearer ${token}` } });
    await request('browser', { method: 'POST', body: JSON.stringify({ clientId: 'browser', page, tools }) });
    await client.listTools();
    const pending = client.callTool({ name: 'preview', arguments: {} }).catch(error => error);
    await eventually(async () => (await (await request('status')).json()).pending === 1);
    input.emit(event, ...(event === 'error' ? [new Error('disconnected')] : []));
    await owned.close();
    await pending;
    await assert.rejects(stat(path.join(root, '.chronicle/dev.json')), { code: 'ENOENT' });
    await assert.rejects(request('status'));
    const rebound = await startServer({ root, port: owned.port });
    await rebound.close();
    await client.close();
  }
});

test('MCP CLI owns ephemeral listener, forwards origin and cleans up on SIGTERM', async t => {
  const root = await fixture(t);
  const child = spawn(process.execPath, [fileURLToPath(new URL('../tools/cli.mjs', import.meta.url)), 'mcp', '--port', '0', '--root', root, '--origin', 'https://custom.example'], { stdio: ['pipe', 'pipe', 'pipe'] });
  const exited = once(child, 'exit');
  let stdout = '', stderr = '';
  child.stdout.on('data', data => { stdout += data; });
  child.stderr.on('data', data => { stderr += data; });
  t.after(() => { if (child.exitCode === null) child.kill('SIGTERM'); });
  // File creation can precede its contents; wait for a readable session, not just stat.
  const { port } = await eventually(async () => {
    try { return JSON.parse(await readFile(path.join(root, '.chronicle/dev.json'))); } catch { return false; }
  });
  const response = await fetch(`http://127.0.0.1:${port}/v1/connect`, { method: 'POST', headers: { Origin: 'https://custom.example' }, body: '{"protocol":2}' });
  assert.equal(response.status, 200);
  child.kill('SIGTERM');
  assert.equal((await exited)[0], 0, stderr);
  assert.equal(stdout, '');
  await assert.rejects(stat(path.join(root, '.chronicle/dev.json')), { code: 'ENOENT' });
});


test('owned MCP process releases ephemeral listener and session on signals', async t => {
  for (const signal of ['SIGINT', 'SIGTERM']) {
    const root = await fixture(t);
    const mcpUrl = new URL('../tools/mcp.mjs', import.meta.url).href;
    const child = spawn(process.execPath, ['--input-type=module', '-e', `import { runMcp } from ${JSON.stringify(mcpUrl)}; await runMcp(process.argv[1], { port: 0 });`, root], { stdio: ['pipe', 'pipe', 'pipe'] });
    const exited = once(child, 'exit');
    let stdout = '', stderr = '';
    child.stdout.on('data', data => { stdout += data; });
    child.stderr.on('data', data => { stderr += data; });
    t.after(() => { if (child.exitCode === null) child.kill('SIGTERM'); });
    const sessionPath = path.join(root, '.chronicle/dev.json');
    const session = await eventually(async () => { try { return JSON.parse(await readFile(sessionPath)); } catch { return false; } });
    // Successful initialization proves signal handlers and stdio are ready.
    child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'signal-test', version: '1' } } }) + '\n');
    await eventually(() => stdout.includes('"id":1'));
    child.kill(signal);
    assert.equal((await exited)[0], 0, stderr);
    for (const line of stdout.trim().split('\n')) assert.equal(JSON.parse(line).jsonrpc, '2.0');
    await assert.rejects(stat(sessionPath), { code: 'ENOENT' });
    const rebound = await startServer({ root, port: session.port });
    await rebound.close();
  }
});

test('catalog bounds, reserved names, page origins and result bounds fail closed', async t => {
  const { request, heartbeat } = await serve(t);
  const post = data => request('browser', { method: 'POST', headers: { Origin: origin }, data: { clientId: 'browser-1', page, tools, ...data } });
  for (const bad of [
    { tools: [tools[0], tools[0]] },
    ...['whereami', 'status', 'bad name'].map(name => ({ tools: [{ ...tools[0], name }] })),
    { tools: Array.from({ length: 65 }, (_, i) => ({ ...tools[0], name: `tool_${i}` })) },
    { tools: [{ ...tools[0], inputSchema: [] }] },
    { tools: [{ ...tools[0], effect: 'arbitrary' }] },
    ...['1', 0, 1.5].map(version => ({ tools: [{ ...tools[0], capability: { ...tools[0].capability, version } }] })),
    ...['1', -1, 0.5].map(stateRevision => ({ tools: [{ ...tools[0], capability: { ...tools[0].capability, stateRevision } }] })),
    ...['https://evil.example/a', 'javascript:alert(1)', 'http://localhost:4000/a'].map(url => ({ page: { ...page, url } })),
  ]) assert.equal((await post(bad)).status, 400);
  assert.equal((await post({ tools: [{ ...tools[0], inputSchema: { type: 'object', description: 'x'.repeat(128 * 1024) } }] })).status, 413);
  assert.equal((await post({ result: { id: 'old', generation: page.generation, result: 'x'.repeat(64 * 1024) } })).status, 413);
  assert.equal((await post({ page: { ...page, url: `${page.url}#private-token` } })).status, 200);
  assert.equal((await (await request('status')).json()).page.url, page.url);
  assert.equal((await request('call', { method: 'POST', data: { name: 'unknown', arguments: {} } })).status, 404);
  assert.equal((await request('call', { method: 'POST', data: { name: 'preview', arguments: [] } })).status, 400);
  await heartbeat();
});

test('generation/session invalidation, immutable capabilities and disconnect notifications', async t => {
  let notifications = 0, clock = 0;
  const { request, heartbeat, state } = await serve(t, { now: () => clock, onChange: () => { notifications++; } });
  await heartbeat();
  assert.equal(notifications, 1);
  await heartbeat();
  assert.equal(notifications, 1);
  const call = () => request('call', { method: 'POST', data: { name: 'preview', arguments: { limit: 3 }, page, capability: tools[0].capability, effect: 'view_mutation', confirmation: 'never' } });
  const pending = call();
  const command = await eventually(async () => (await (await state()).json()).commands[0]);
  assert.deepEqual(command.capability, tools[0].capability);
  assert.deepEqual(command.arguments, { limit: 3 });
  const updatedTools = [{ ...tools[0], capability: { ...tools[0].capability, stateRevision: 2 } }];
  await request('browser', { method: 'POST', data: { clientId: 'browser-1', page, tools: updatedTools } });
  assert.deepEqual((await (await state()).json()).commands[0].capability, tools[0].capability);
  assert.equal(notifications, 2);
  const newPage = { ...page, sessionId: 'session-2' };
  await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: newPage, tools, result: { ...command, result: {} } } });
  assert.equal((await pending).status, 409);
  assert.equal(notifications, 3);
  assert.equal((await (await request('status')).json()).connected, true);
  clock = 10_001;
  assert.equal((await (await request('status')).json()).connected, false);
  assert.equal(notifications, 4);
});

test('official stdio dynamic notifications, navigation failures and canceled requests', async t => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'chronicle-relay-empty-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const mcpUrl = new URL('../tools/mcp.mjs', import.meta.url).href;
  const transport = new StdioClientTransport({ command: process.execPath, args: ['--input-type=module', '-e', `import { runMcp } from ${JSON.stringify(mcpUrl)}; await runMcp(process.argv[1], { port: 0 });`, root], stderr: 'pipe' });
  const client = new Client({ name: 'dynamic-test', version: '1' });
  t.after(() => client.close());
  await client.connect(transport);
  let notifications = 0;
  client.setNotificationHandler(ToolListChangedNotificationSchema, () => { notifications++; });
  const session = JSON.parse(await readFile(path.join(root, '.chronicle/dev.json')));
  const request = (route, data) => fetch(`http://127.0.0.1:${session.port}/v1/${route}`, { method: data ? 'POST' : 'GET', headers: { Authorization: `Bearer ${session.token}` }, body: data ? JSON.stringify(data) : undefined });
  let currentPage = page;
  const heartbeat = (result, catalog = tools) => request('browser', { clientId: 'tab', page: currentPage, tools: catalog, ...(result ? { result } : {}) });
  const command = () => eventually(async () => (await (await request('state')).json()).commands[0]);
  const call = options => client.callTool({ name: 'preview', arguments: {} }, undefined, options);
  assert.deepEqual(JSON.parse((await client.callTool({ name: 'whereami' })).content[0].text), { page: null, connected: false });
  await heartbeat();
  await eventually(() => notifications === 1);
  await client.listTools();
  const navigating = call();
  const old = await command();
  currentPage = { ...page, generation: 'generation-2' }; // Same URL, new working copy.
  await heartbeat({ ...old, result: { status: 'success', result: 'late' } });
  const stale = await navigating;
  assert.equal(stale.isError, true);
  assert.deepEqual(JSON.parse(stale.content[0].text), { status: 'stale_state', result: { message: 'Page changed' } });
  await eventually(() => notifications === 2);
  assert.deepEqual(JSON.parse((await client.callTool({ name: 'whereami' })).content[0].text).page, currentPage);
  // A notification is not an observation: a delayed call must not target the new page.
  currentPage = { ...currentPage, generation: 'generation-3' };
  await heartbeat();
  await eventually(() => notifications === 3);
  const delayed = await call();
  assert.equal(delayed.isError, true);
  assert.equal(JSON.parse(delayed.content[0].text).status, 'stale_state');
  assert.equal((await (await request('status')).json()).pending, 0);
  await client.callTool({ name: 'whereami' });
  for (const status of ['success', 'user_rejected', 'capability_unavailable', 'stale_state', 'client_error']) {
    const pending = call();
    const cmd = await command();
    const result = { status, result: { message: status } };
    await heartbeat({ ...cmd, result });
    const response = await pending;
    assert.equal(response.isError === true, status !== 'success');
    assert.deepEqual(JSON.parse(response.content[0].text), result);
  }
  const controller = new AbortController();
  const canceled = call({ signal: controller.signal });
  const canceledCommand = await command();
  controller.abort();
  await assert.rejects(canceled);
  await eventually(async () => (await (await request('status')).json()).pending === 0);
  assert.equal((await (await heartbeat({ ...canceledCommand, result: {} })).json()).resultAccepted, false);
  await heartbeat(undefined, []);
  await eventually(() => notifications === 4);
  assert.deepEqual((await client.listTools()).tools.map(t => t.name), ['whereami']);
});

test('calls require observed page and capability, allowing repeated same-revision saves', async t => {
  const { request, heartbeat, state } = await serve(t);
  await heartbeat();
  const observed = { name: 'preview', arguments: {}, page: { generation: page.generation, sessionId: page.sessionId }, capability: tools[0].capability };
  const call = data => request('call', { method: 'POST', data });
  assert.equal((await call({ name: 'preview', arguments: {} })).status, 409);
  for (const key of ['id', 'version', 'scopeType', 'scopeId', 'stateRevision']) {
    assert.equal((await call({ ...observed, capability: { ...observed.capability, [key]: 'different' } })).status, 409);
  }
  const moved = { ...page, generation: 'new-generation' };
  await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: moved, tools } });
  assert.equal((await call(observed)).status, 409);
  assert.equal((await (await state()).json()).commands.length, 0);
  const current = { ...observed, page: { generation: moved.generation, sessionId: moved.sessionId } };
  // Saving identical content need not advance stateRevision; matching observations stay valid.
  for (let i = 0; i < 2; i++) {
    const pending = call(current);
    const command = await eventually(async () => (await (await state()).json()).commands[0]);
    await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: moved, tools, result: { ...command, result: { status: 'success', result: {} } } } });
    assert.equal((await pending).status, 200);
  }
  const revisedTools = [{ ...tools[0], capability: { ...tools[0].capability, stateRevision: 2 } }];
  await request('browser', { method: 'POST', data: { clientId: 'browser-1', page: moved, tools: revisedTools } });
  assert.equal((await call(current)).status, 409);
});
