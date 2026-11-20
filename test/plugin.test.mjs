import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFile, mkdtemp, rm, access } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';

const root = fileURLToPath(new URL('../', import.meta.url));
const read = file => readFile(new URL(`../${file}`, import.meta.url), 'utf8');

test('plugin uses standard component locations and a thin discoverable skill', async () => {
  const manifest = JSON.parse(await read('.claude-plugin/plugin.json'));
  assert.equal(manifest.name, 'chronicle');
  assert.match(manifest.version, /^\d+\.\d+\.\d+$/);
  // Default discovery avoids duplicate registration or hidden hooks/agents.
  for (const key of ['skills', 'mcpServers', 'hooks', 'agents']) assert.equal(manifest[key], undefined);
  for (const dir of ['hooks', 'agents']) {
    await assert.rejects(access(path.join(root, dir)), { code: 'ENOENT' });
  }
  const skill = await read('skills/chronicle/SKILL.md');
  assert.match(skill, /^---\nname: chronicle\ndescription: [^\n]+\n---\n/);
  for (const tool of ['whereami', 'gen_page_context', 'list_skill', 'read_skill']) assert.ok(skill.includes(tool));
  assert.match(skill, /stale-state/);
  assert.match(skill, /live schemas/);
  assert.match(skill, /browser approval/);
});

test('plugin command launches existing stdio relay outside the plugin checkout', async t => {
  const config = JSON.parse(await read('.mcp.json'));
  assert.deepEqual(Object.keys(config.mcpServers), ['chronicle-mcp']);
  const server = config.mcpServers['chronicle-mcp'];
  assert.equal(server.command, 'node');
  assert.deepEqual(server.args, ['${CLAUDE_PLUGIN_ROOT}/tools/cli.mjs', 'mcp', '--root', '${CLAUDE_PLUGIN_DATA}']);
  const temp = await mkdtemp(path.join(os.tmpdir(), 'chronicle-plugin-'));
  t.after(() => rm(temp, { recursive: true, force: true }));
  const data = path.join(temp, 'plugin data');
  const args = server.args.map(arg => arg.replaceAll('${CLAUDE_PLUGIN_ROOT}', root).replaceAll('${CLAUDE_PLUGIN_DATA}', data));
  // Simulate Claude's variable expansion; never contend with the user's relay.
  const transport = new StdioClientTransport({ command: server.command, args: [...args, '--port', '0'], cwd: temp, stderr: 'pipe' });
  const client = new Client({ name: 'plugin-packaging-test', version: '1.0.0' });
  t.after(() => client.close());
  await client.connect(transport);
  assert.deepEqual((await client.listTools()).tools.map(tool => tool.name), ['whereami']);
  const result = await client.callTool({ name: 'whereami', arguments: {} });
  assert.notEqual(result.isError, true);
  assert.deepEqual(JSON.parse(result.content[0].text), { page: null, connected: false });
  await access(path.join(data, '.chronicle/dev.json'));
  await client.close();
  await assert.rejects(access(path.join(data, '.chronicle/dev.json')), { code: 'ENOENT' });
});
