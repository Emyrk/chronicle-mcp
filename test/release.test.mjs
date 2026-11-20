import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

const script = resolve('scripts/build-release.sh');
const version = readFileSync('internal/relay/relay.go', 'utf8').match(/^const Version = "([^"]+)"$/m)[1];

test('release builder emits only the four supported raw executables', t => {
  const dir = mkdtempSync(join(tmpdir(), 'chronicle-release-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, 'bin');
  mkdirSync(bin);
  const log = join(dir, 'calls');
  writeFileSync(join(bin, 'go'), '#!/bin/sh\nprintf "%s %s %s %s\\n" "$GOOS" "$GOARCH" "$CGO_ENABLED" "$*" >> "$BUILD_LOG"\n', { mode: 0o755 });
  const output = join(dir, 'output with spaces');
  const result = spawnSync('sh', [script, `v${version}`, output], {
    encoding: 'utf8', env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, BUILD_LOG: log },
  });
  assert.equal(result.status, 0, result.stderr);
  const calls = readFileSync(log, 'utf8').trim().split('\n');
  assert.equal(calls.length, 4);
  for (const [i, platform] of ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64'].entries()) {
    const [os, arch] = platform.split('-');
    assert.ok(calls[i].startsWith(`${os} ${arch} 0 build -trimpath -buildvcs=true `));
    assert.ok(calls[i].includes(`-o ${output}/chronicle-mcp-${platform} ./cmd/chronicle-mcp`));
  }
});

test('release builder refuses malformed and mismatched versions before building', t => {
  const dir = mkdtempSync(join(tmpdir(), 'chronicle-release-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  for (const version of ['latest', 'v1.2.3-beta', 'v999999.0.0', 'v1.2.3\nmalicious']) {
    const result = spawnSync('sh', [script, version, join(dir, 'out')], { encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /release (version|tag) must/);
  }
});
