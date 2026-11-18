import { parseArgs } from 'node:util';

export function parseCliArgs(argv = process.argv.slice(2)) {
  const args = [...argv];
  // pnpm forwards the npm-style separator after the script's dev/mcp command.
  // Remove only that separator, preserving normal option values and strict parsing.
  if (args[1] === '--') args.splice(1, 1);
  return parseArgs({ args, allowPositionals: true, options: {
    root: { type: 'string', default: process.cwd() },
    origin: { type: 'string' },
    url: { type: 'string' },
    port: { type: 'string' },
  } });
}
