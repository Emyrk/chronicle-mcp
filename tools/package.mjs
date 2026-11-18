import { lstat, readdir, readFile } from 'node:fs/promises';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { zipSync } from 'fflate';

export const LIMITS = { files: 128, bytes: 4 * 1024 * 1024, fileBytes: 1024 * 1024 };
const ignored = new Set(['node_modules', 'tools', 'test', 'tests', 'dist', 'build', 'coverage', 'vendor', 'lua-types']);
const raster = /\.(png|jpe?g|webp)$/i;
export const digest = data => createHash('sha256').update(data).digest('hex');
function safeName(name) {
  return typeof name === 'string' && name.length > 0 && !name.includes('\\') && !name.includes('\0') && !path.posix.isAbsolute(name) && name.split('/').every(p => p && p !== '.' && p !== '..' && !p.startsWith('.') && !ignored.has(p));
}
export async function snapshot(root, limits = LIMITS) {
  try {
    const files = new Map();
    let total = 0;
    async function add(name) {
      if (files.has(name)) return;
      if (!safeName(name)) throw new Error(`Unsafe package path: ${name}`);
      // Check every component, not only the final entry, to reject symlink parents.
      let current = root;
      for (const part of name.split('/')) {
        current = path.join(current, part);
        if ((await lstat(current)).isSymbolicLink()) throw new Error(`Symlink not allowed: ${name}`);
      }
      const stat = await lstat(current);
      if (!stat.isFile()) throw new Error(`Not a regular file: ${name}`);
      if (stat.size > limits.fileBytes) throw new Error(`File too large: ${name}`);
      if (files.size >= limits.files || total + stat.size > limits.bytes) throw new Error('Package exceeds file or byte limit');
      const data = await readFile(current);
      if (data.length > limits.fileBytes || total + data.length > limits.bytes) throw new Error('Package exceeds byte limit');
      total += data.length;
      files.set(name, data);
    }
    await add('project.json');
    const manifest = JSON.parse(files.get('project.json').toString('utf8'));
    // README is optional; other failures (including dangling symlinks) remain errors.
    let hasReadme = true;
    try { await lstat(path.join(root, 'README.md')); }
    catch (error) { if (error.code === 'ENOENT') hasReadme = false; else throw error; }
    if (hasReadme) await add('README.md');
    let visited = 0;
    async function walk(dir = '') {
      for (const entry of await readdir(path.join(root, dir), { withFileTypes: true })) {
        if (++visited > 4096) throw new Error('Project directory scan limit exceeded');
        if (entry.name.startsWith('.') || ignored.has(entry.name)) continue;
        const name = dir ? `${dir}/${entry.name}` : entry.name;
        if (entry.isSymbolicLink()) throw new Error(`Symlink not allowed: ${name}`);
        if (entry.isDirectory()) await walk(name);
        else if (entry.name.endsWith('.lua')) await add(name);
      }
    }
    await walk();
    if (manifest.manifest_version !== 1 || manifest.kind !== 'panel' || !safeName(manifest.entrypoint) || !manifest.entrypoint.endsWith('.lua') || !files.has(manifest.entrypoint)) throw new Error('Invalid panel manifest or missing Lua entrypoint');
    const assets = manifest.assets ?? [];
    if (!Array.isArray(assets) || assets.length > limits.files) throw new Error('assets must be a bounded array of raster paths');
    for (const asset of assets) {
      if (typeof asset !== 'string' || !raster.test(asset)) throw new Error('Only declared raster assets are supported');
      await add(asset);
    }
    const entries = Object.create(null);
    for (const name of [...files.keys()].sort()) entries[name] = [files.get(name), { level: 0, mtime: new Date(1980, 0, 1) }];
    const zip = zipSync(entries, { level: 0 });
    return { protocol: 1, revision: digest(zip), archive: Buffer.from(zip).toString('base64') };
  } catch (error) {
    const message = error.message;
    return { protocol: 1, revision: digest(`error:${message}`), archive: '', error: message };
  }
}
