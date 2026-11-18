import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { PORT } from './server.mjs';

export async function sessionRequest(root, route, { signal } = {}) {
  let session;
  try { session = JSON.parse(await readFile(path.join(root, '.chronicle/dev.json'), 'utf8')); }
  catch { throw new Error('No dev session. Run npm run dev in the module root first.'); }
  if (session.port !== PORT || !/^[a-f0-9]{64}$/.test(session.token)) throw new Error('Invalid dev session file');
  let response;
  try {
    response = await fetch(`http://127.0.0.1:${PORT}/v1/${route}`, {
      method: route === 'preview' ? 'POST' : 'GET',
      headers: { Authorization: `Bearer ${session.token}`, 'Content-Type': 'application/json' },
      body: route === 'preview' ? '{}' : undefined,
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(65_000)]) : AbortSignal.timeout(65_000),
    });
  } catch (error) { throw new Error(`Dev server request failed: ${error.message}`); }
  const result = await response.json();
  if (!response.ok) throw new Error(`${result.error ?? `HTTP ${response.status}`}${result.revision ? ` (revision ${result.revision})` : ''}`);
  return result;
}
export function createMcp(root) {
  const server = new McpServer({ name: 'chronicle-module', version: '0.1.0' });
  const controllers = new Set();
  for (const name of ['preview', 'status']) {
    server.registerTool(name, {
      description: name === 'preview' ? 'Preview the current local module in the explicitly paired Chronicle browser. Returns its pinned source revision and browser result.' : 'Get local module revision, validation errors, and paired browser encounter context.',
      inputSchema: {},
    }, async (_args, extra) => {
      const controller = new AbortController();
      controllers.add(controller);
      try {
        const result = await sessionRequest(root, name, { signal: AbortSignal.any([controller.signal, extra.signal]) });
        return { content: [{ type: 'text', text: JSON.stringify(result) }] };
      } catch (error) {
        return { isError: true, content: [{ type: 'text', text: error.message }] };
      } finally { controllers.delete(controller); }
    });
  }
  // The SDK owns the transport callbacks. Wrap its close hook so disconnected
  // clients cancel outstanding HTTP previews rather than leaving commands queued.
  const onclose = server.server.onclose;
  server.server.onclose = () => { for (const controller of controllers) controller.abort(); onclose?.(); };
  return server;
}
export async function runMcp(root) {
  const server = createMcp(root);
  await server.connect(new StdioServerTransport());
  return server;
}
