import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { ListToolsRequestSchema, CallToolRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { PORT, startServer } from './server.mjs';

export async function sessionRequest(root, route, { signal, data } = {}) {
  let session;
  try { session = JSON.parse(await readFile(path.join(root, '.chronicle/dev.json'), 'utf8')); }
  catch { throw new Error('No dev session. Start the Chronicle module MCP process first.'); }
  if (!Number.isInteger(session.port) || session.port < 1 || session.port > 65535 || !/^[a-f0-9]{64}$/.test(session.token)) throw new Error('Invalid dev session file');
  let response;
  try {
    response = await fetch(`http://127.0.0.1:${session.port}/v1/${route}`, {
      method: route === 'call' ? 'POST' : 'GET',
      headers: { Authorization: `Bearer ${session.token}`, 'Content-Type': 'application/json' },
      body: route === 'call' ? JSON.stringify(data) : undefined,
      signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(65_000)]) : AbortSignal.timeout(65_000),
    });
  } catch (error) { throw new Error(`Dev server request failed: ${error.message}`); }
  const result = await response.json();
  if (!response.ok) throw new Error(result.error ?? `HTTP ${response.status}`);
  return result;
}
export function createMcp(root) {
  const server = new Server({ name: 'chronicle-module', version: '0.1.0' }, { capabilities: { tools: { listChanged: true } } });
  const controllers = new Set();
  // Only explicit client observations authorize calls, not background notifications.
  let observed;
  const observe = state => { observed = state.connected ? state : undefined; return state; };
  server.setRequestHandler(ListToolsRequestSchema, async () => {
    const state = observe(await sessionRequest(root, 'status'));
    return { tools: [
      { name: 'whereami', description: 'Get the connected Chronicle page URL, generation, session, and connection status.', inputSchema: { type: 'object', properties: {}, additionalProperties: false } },
      ...state.tools.map(({ name, description, inputSchema }) => ({ name, description, inputSchema })),
    ] };
  });
  server.setRequestHandler(CallToolRequestSchema, async (request, extra) => {
    const controller = new AbortController();
    controllers.add(controller);
    try {
      const signal = AbortSignal.any([controller.signal, extra.signal]);
      if (request.params.name === 'whereami') {
        const { page, connected } = observe(await sessionRequest(root, 'status', { signal }));
        return { content: [{ type: 'text', text: JSON.stringify({ page, connected }) }] };
      }
      const tool = observed?.tools.find(tool => tool.name === request.params.name);
      if (!tool) throw new Error('Observe connected browser tools with whereami or tools/list before calling this tool');
      const response = await sessionRequest(root, 'call', { signal, data: { name: request.params.name, arguments: request.params.arguments ?? {}, page: { generation: observed.page.generation, sessionId: observed.page.sessionId }, capability: tool.capability } });
      const result = response.result;
      const isError = result?.isError === true || (typeof result?.status === 'string' && result.status !== 'success');
      return { ...(isError ? { isError: true } : {}), content: [{ type: 'text', text: JSON.stringify(result) }] };
    } catch (error) {
      const text = error.message === 'stale_page' ? JSON.stringify({ status: 'stale_state', result: { message: 'Page changed' } }) : error.message;
      return { isError: true, content: [{ type: 'text', text }] };
    } finally { controllers.delete(controller); }
  });
  const onclose = server.onclose;
  server.onclose = () => { for (const controller of controllers) controller.abort(); onclose?.(); };
  return server;
}
export async function runMcp(root = process.cwd(), { origin, port = PORT, transport = new StdioServerTransport(), input = process.stdin } = {}) {
  // Bind first. Never attach to a server that may belong to another module.
  const server = createMcp(root);
  const local = await startServer({ root, origin, port, onChange: () => server.sendToolListChanged() });
  let closing;
  const onclose = server.onclose;
  const close = () => {
    if (!closing) closing = Promise.resolve().then(async () => {
      input.off('end', stop);
      input.off('close', stop);
      input.off('error', stop);
      process.off('SIGINT', stop);
      process.off('SIGTERM', stop);
      try { await server.close(); } finally { await local.close(); }
    });
    return closing;
  };
  const stop = () => { void close().catch(error => { console.error(error.message); process.exitCode = 1; }); };
  server.onclose = () => { onclose?.(); stop(); };
  input.once('end', stop);
  input.once('close', stop);
  input.once('error', stop);
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  try { await server.connect(transport); }
  catch (error) { await close(); throw error; }
  // Also handle an input stream that ended while the listener was starting.
  if (input.readableEnded || input.destroyed) await close();
  return { server, port: local.port, close };
}
