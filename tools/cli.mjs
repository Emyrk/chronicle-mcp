#!/usr/bin/env node
import path from 'node:path';
import { parseArgs } from 'node:util';
import { startServer } from './server.mjs';
import { runMcp } from './mcp.mjs';

try {
  const { values, positionals } = parseArgs({ allowPositionals: true, options: {
    root: { type: 'string', default: process.cwd() },
    origin: { type: 'string' },
    url: { type: 'string' },
  } });
  const root = path.resolve(values.root);
  if (positionals.length !== 1 || !['dev', 'mcp'].includes(positionals[0])) throw new Error('Usage: node tools/cli.mjs dev|mcp [--root path] [--origin exact-origin] [--url workshop-url]');
  if (positionals[0] === 'mcp') await runMcp(root);
  else {
    const url = values.url ? new URL(values.url) : undefined;
    const origin = values.origin ?? url?.origin ?? 'https://chronicleclassic.com';
    if (url && url.origin !== origin) throw new Error('--url must have the exact --origin');
    const server = await startServer({ root, origin });
    console.error(`Chronicle module dev: http://127.0.0.1:${server.port}`);
    if (url) {
      url.hash = `module-dev=${server.token}`;
      console.error(`Pair in your browser (explicit approval required):\n${url.href}\nKeep this token private.`);
    } else {
      console.error('To print a usable pairing link, restart with the full module editor URL:\nnpm run dev -- --url https://<site>/modules/<author>/<slug>/edit\nOr use https://<site>/modules/id/<uuid>/edit');
    }
    console.error('Stop with Ctrl+C.');
    let stopping = false;
    const stop = async () => { if (stopping) return; stopping = true; await server.close(); };
    process.on('SIGINT', stop);
    process.on('SIGTERM', stop);
  }
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
