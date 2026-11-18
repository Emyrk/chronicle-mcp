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
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import { startServer } from '../tools/server.mjs';
import { snapshot, digest } from '../tools/package.mjs';

const origin = 'https://chronicleclassic.com';
const context = { instanceId: 'instance-1', encounterIds: ['encounter-1'] };
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
  const heartbeat = (clientId = 'browser-1', result) => request('browser', { method: 'POST', headers: { Origin: origin }, data: { clientId, context, ...(result ? { result } : {}) } });
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
  for (const route of ['state', 'status', 'browser', 'preview']) {
    const response = await request(route, { method: ['browser', 'preview'].includes(route) ? 'POST' : 'GET', headers: { Authorization: '' } });
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

test('browser heartbeat owns lease and preview is revision pinned', async t => {
  const { request, heartbeat, state, root } = await serve(t);
  assert.equal((await state()).status, 409);
  assert.equal((await request('preview', { method: 'POST', data: {} })).status, 409);
  assert.equal((await heartbeat()).status, 200);
  assert.equal((await heartbeat('other')).status, 409);
  assert.equal((await request('state', { headers: { Origin: origin, 'X-Chronicle-Client': 'other' } })).status, 409);
  const preview = request('preview', { method: 'POST', data: {} });
  const command = await eventually(async () => (await (await state()).json()).commands[0]);
  assert.equal((await request('preview', { method: 'POST', data: {} })).status, 409);
  assert.deepEqual(await (await heartbeat('browser-1', { ...command, revision: 'wrong', result: {} })).json(), { ok: true, resultAccepted: false });
  assert.equal((await heartbeat('browser-1', { ...command, result: { ok: true, value: 123 } })).status, 200);
  assert.deepEqual(await (await preview).json(), { id: command.id, revision: command.revision, result: { ok: true, value: 123 } });
  const stale = request('preview', { method: 'POST', data: {} });
  const staleCommand = await eventually(async () => (await (await state()).json()).commands[0]);
  await writeFile(path.join(root, 'main.lua'), 'changed');
  assert.equal((await (await state()).json()).commands.length, 0);
  assert.match((await (await stale).json()).error, /Source changed/);
  assert.deepEqual(await (await heartbeat('browser-1', { ...staleCommand, result: { ok: false } })).json(), { ok: true, resultAccepted: false });
  assert.equal((await state()).status, 200);
  await writeFile(path.join(root, 'project.json'), '{invalid');
  const invalid = await (await state()).json();
  assert.equal(invalid.archive, ''); assert.ok(invalid.error);
  assert.equal((await request('preview', { method: 'POST', data: {} })).status, 422);
});

test('preview timeout returns pinned revision', async t => {
  const { request, heartbeat } = await serve(t, { previewTimeoutMs: 20 });
  await heartbeat();
  const timed = await request('preview', { method: 'POST', data: {} });
  assert.equal(timed.status, 504);
  const result = await timed.json();
  assert.match(result.error, /timed out/);
  assert.match(result.revision, /^[a-f0-9]{64}$/);
});

test('disconnect cancellation, result size bounds and lease expiration', async t => {
  let clock = 0;
  const { request, heartbeat, state } = await serve(t, { now: () => clock });
  await heartbeat();
  const controller = new AbortController();
  const pending = request('preview', { method: 'POST', data: {}, signal: controller.signal });
  await eventually(async () => (await (await state()).json()).commands[0]);
  controller.abort(); await assert.rejects(pending);
  await eventually(async () => (await (await request('status')).json()).pending === 0);
  assert.equal((await heartbeat('browser-1', { id: 'x', revision: 'x', result: 'x'.repeat(128 * 1024) })).status, 413);
  const abandoned = request('preview', { method: 'POST', data: {} });
  await eventually(async () => (await (await state()).json()).commands.length);
  clock = 10_001;
  assert.equal((await (await request('status')).json()).browser, null);
  assert.match((await (await abandoned).json()).error, /lease expired/);
  assert.equal((await heartbeat('new-browser')).status, 200);
});

test('actual official MCP stdio client to HTTP browser roundtrip and private session', async t => {
  const { root, heartbeat, state, request } = await serve(t, { port: 17643, writeSession: true });
  assert.equal((await stat(path.join(root, '.chronicle/dev.json'))).mode & 0o777, 0o600);
  assert.equal(JSON.parse(await readFile(path.join(root, '.chronicle/dev.json'))).port, 17643);
  const transport = new StdioClientTransport({ command: process.execPath, args: [fileURLToPath(new URL('../tools/cli.mjs', import.meta.url)), 'mcp', '--root', root], stderr: 'pipe' });
  const client = new Client({ name: 'integration-test', version: '1' });
  await client.connect(transport);
  t.after(() => client.close());
  assert.deepEqual((await client.listTools()).tools.map(tool => tool.name).sort(), ['preview', 'status']);
  const absent = await client.callTool({ name: 'preview', arguments: {} });
  assert.equal(absent.isError, true); assert.match(absent.content[0].text, /No browser/);
  await heartbeat();
  const preview = client.callTool({ name: 'preview', arguments: {} });
  const command = await eventually(async () => (await (await state()).json()).commands[0]);
  await heartbeat('browser-1', { ...command, result: { ok: true, metrics: [42] } });
  const output = JSON.parse((await preview).content[0].text);
  assert.equal(output.revision, command.revision); assert.deepEqual(output.result.metrics, [42]);
  const status = JSON.parse((await client.callTool({ name: 'status', arguments: {} })).content[0].text);
  assert.deepEqual(status.browser.context, context);
  const canceled = client.callTool({ name: 'preview', arguments: {} }).catch(error => error);
  await eventually(async () => (await (await state()).json()).commands.length);
  await client.close();
  await canceled;
  await eventually(async () => (await (await request('status')).json()).pending === 0);
});


test('CLI prints only real editor pairing links and infers their origin', async t => {
  const root = await fixture(t);
  const script = fileURLToPath(new URL('../tools/cli.mjs', import.meta.url));
  async function run(args) {
    const child = spawn(process.execPath, [script, 'dev', '--root', root, ...args], { stdio: ['ignore', 'pipe', 'pipe'] });
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
