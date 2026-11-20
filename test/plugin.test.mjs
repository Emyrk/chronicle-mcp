import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, mkdtemp, rm, access, mkdir, writeFile, copyFile, symlink } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
const exec = promisify(execFile);

const root = fileURLToPath(new URL('../', import.meta.url));
const read = file => readFile(new URL(`../${file}`, import.meta.url), 'utf8');

test('plugin uses standard component locations and a thin discoverable skill', async () => {
  const manifest = JSON.parse(await read('.claude-plugin/plugin.json'));
  assert.equal(manifest.name, 'chronicle');
  assert.match(manifest.version, /^\d+\.\d+\.\d+$/);
  // Skills use default discovery; MCP is explicit to avoid project registration.
  for (const key of ['skills', 'hooks', 'agents']) assert.equal(manifest[key], undefined);
  for (const dir of ['hooks', 'agents']) {
    await assert.rejects(access(path.join(root, dir)), { code: 'ENOENT' });
  }
  const skill = await read('skills/chronicle/SKILL.md');
  assert.match(skill, /^---\nname: chronicle\ndescription: [^\n]+\n---\n/);
  for (const tool of ['whereami', 'list_connections', 'select_connection', 'release_connection', 'gen_page_context', 'list_skill', 'read_skill']) assert.ok(skill.includes(tool));
  assert.match(skill, /stale-state/);
  assert.match(skill, /live schemas/);
  assert.match(skill, /browser approval/);
});

test('plugin uses authenticated HTTP with a side-effect-free headers helper', async () => {
  const manifest = JSON.parse(await read('.claude-plugin/plugin.json'));
  assert.equal(manifest.mcpServers, './plugin.mcp.json');
  await assert.rejects(access(path.join(root, '.mcp.json')), { code: 'ENOENT' });
  const config = JSON.parse(await read(manifest.mcpServers));
  assert.deepEqual(config, { mcpServers: { 'chronicle-mcp': {
    type: 'http', url: 'http://127.0.0.1:17643/mcp',
    headersHelper: 'CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA}" sh "${CLAUDE_PLUGIN_ROOT}/scripts/chronicle.sh" headers',
  } } });
  for (const name of ['setup', 'teardown']) {
    const skill = await read(`skills/${name}/SKILL.md`);
    assert.ok(skill.startsWith(`---\nname: ${name}\ndescription: `));
    assert.match(skill, /disable-model-invocation: true/);
    assert.doesNotMatch(skill, /allowed-tools:/);
  }
  assert.match(await read('skills/teardown/SKILL.md'), /renewed explicit confirmation/);
  assert.equal(await read('scripts/release.tsv'), 'unreleased unreleased\n');
});

async function fixture(t) {
  const dir = await mkdtemp(path.join(os.tmpdir(), 'chronicle plugin '));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const script = path.join(dir, 'chronicle.sh');
  await copyFile(path.join(root, 'scripts/chronicle.sh'), script);
  await copyFile(path.join(root, 'scripts/release.tsv'), path.join(dir, 'release.tsv'));
  const data = path.join(dir, 'plugin data');
  const env = { ...process.env, CLAUDE_PLUGIN_DATA: data, CHRONICLE_DEV_BINARY: '', CHRONICLE_TEST_ALLOW_HTTP: '' };
  const run = (args, extra = {}) => exec('sh', [script, ...args], { env: { ...env, ...extra }, timeout: 10000 });
  return { dir, data, run };
}
const posix = { skip: !['linux', 'darwin'].includes(process.platform) };

test('unreleased fails clearly without installing; explicit local development override forwards commands', posix, async t => {
  const f = await fixture(t);
  await assert.rejects(f.run(['start']), /unreleased/);
  await assert.rejects(access(f.data), { code: 'ENOENT' });
  const binary = path.join(f.dir, 'local executable');
  await writeFile(binary, '#!/bin/sh\nprintf "%s\\n" "$@"\n', { mode: 0o700 });
  const dev = { CHRONICLE_DEV_BINARY: binary };
  assert.equal((await f.run(['start'], dev)).stdout, 'start\n');
  assert.equal((await f.run(['status'], dev)).stdout, 'status\n');
  assert.equal((await f.run(['stop', '--force'], dev)).stdout, 'stop\n--force\n');
  assert.equal((await f.run(['headers'], dev)).stdout, 'auth\nheaders\n');
  assert.match((await f.run(['start'], dev)).stderr, /NOT verified/);
  await assert.rejects(f.run(['start'], { CHRONICLE_DEV_BINARY: 'relative' }), /absolute path/);
  await assert.rejects(f.run(['start', '--force'], dev), /only stop/);
});

test('pinned artifact installation verifies hash, reuses cache, and refuses unsafe paths or corruption', posix, async t => {
  const f = await fixture(t);
  const artifact = '#!/bin/sh\nprintf "%s\\n" "$@"\n';
  const digest = createHash('sha256').update(artifact).digest('hex');
  let requests = 0;
  const server = createServer((req, res) => { requests++; res.end(artifact); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise(resolve => server.close(resolve)));
  const platform = `${process.platform === 'darwin' ? 'darwin' : 'linux'}-${process.arch === 'x64' ? 'amd64' : 'arm64'}`;
  const metadata = hash => `v0.0.0-fixture released\n${platform} http://127.0.0.1:${server.address().port}/binary ${hash}\n`;
  await writeFile(path.join(f.dir, 'release.tsv'), metadata(digest));
  const local = { CHRONICLE_TEST_ALLOW_HTTP: '1' };
  await assert.rejects(f.run(['start']), /requires HTTPS/);
  await assert.rejects(f.run(['headers'], local), /not installed/);
  await assert.rejects(access(f.data), { code: 'ENOENT' });
  await assert.rejects(f.run(['start'], { ...local, CLAUDE_PLUGIN_DATA: '/' }), /unsafe|root/);
  await assert.rejects(f.run(['start'], { ...local, CLAUDE_PLUGIN_DATA: `${f.dir}/../escape` }), /unsafe/);
  const link = path.join(f.dir, 'link');
  await symlink(f.dir, link);
  await assert.rejects(f.run(['start'], { ...local, CLAUDE_PLUGIN_DATA: `${link}/data` }), /symlinks/);
  await writeFile(path.join(f.dir, 'release.tsv'), metadata('0'.repeat(64)));
  await assert.rejects(f.run(['start'], local), /SHA256 mismatch/);
  await assert.rejects(access(path.join(f.data, 'bin/v0.0.0-fixture/chronicle-mcp')), { code: 'ENOENT' });
  await writeFile(path.join(f.dir, 'release.tsv'), metadata(digest));
  assert.equal((await f.run(['start'], local)).stdout, 'start\n');
  const downloaded = requests;
  assert.equal((await f.run(['start'], local)).stdout, 'start\n');
  assert.equal((await f.run(['headers'], local)).stdout, 'auth\nheaders\n');
  assert.equal((await f.run(['stop'], local)).stdout, 'stop\n');
  assert.equal(requests, downloaded);
  const binary = path.join(f.data, 'bin/v0.0.0-fixture/chronicle-mcp');
  await writeFile(binary, 'corrupt');
  await assert.rejects(f.run(['start'], local), /installed binary SHA256 mismatch/);
  assert.equal(requests, downloaded, 'corruption must not trigger silent replacement');
  await rm(binary);
  await mkdir(path.join(f.data, '.install-lock'));
  await assert.rejects(f.run(['start'], local), /already in progress/);
  await rm(path.join(f.data, '.install-lock'), { recursive: true });
  await symlink(path.join(f.dir, 'elsewhere'), binary);
  await assert.rejects(f.run(['start'], local), /binary must not be a symlink/);
});

test('unsupported platforms and malformed pins fail; CLI refusals propagate without escalation', posix, async t => {
  const f = await fixture(t);
  const mocks = path.join(f.dir, 'mock commands');
  await mkdir(mocks);
  await writeFile(path.join(mocks, 'uname'), '#!/bin/sh\necho Unsupported\n', { mode: 0o700 });
  await assert.rejects(f.run(['start'], { PATH: `${mocks}:${process.env.PATH}` }), /Linux and macOS only/);
  await writeFile(path.join(mocks, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; *) echo unsupported;; esac\n', { mode: 0o700 });
  await assert.rejects(f.run(['start'], { PATH: `${mocks}:${process.env.PATH}` }), /unsupported CPU/);
  await writeFile(path.join(f.dir, 'release.tsv'), '../escape released\n');
  await assert.rejects(f.run(['start']), /invalid pinned version/);
  const log = path.join(f.dir, 'calls');
  const binary = path.join(f.dir, 'refusing daemon');
  await writeFile(binary, '#!/bin/sh\nprintf "%s\\n" "$*" >> "$CALL_LOG"\necho "active work or incompatible daemon" >&2\nexit 7\n', { mode: 0o700 });
  for (const action of ['start', 'stop']) {
    await assert.rejects(f.run([action], { CHRONICLE_DEV_BINARY: binary, CALL_LOG: log }), error => {
      assert.equal(error.code, 7);
      assert.match(error.stderr, /active work or incompatible/);
      return true;
    });
  }
  assert.equal(await readFile(log, 'utf8'), 'start\nstop\n');
});
