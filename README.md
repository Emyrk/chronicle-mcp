# Chronicle local module

A Lua panel starter with a loopback development server and a stdio MCP bridge.
Requires Node.js 22 or newer. No build step.

## Start

```sh
npm install
npm run dev
# Use your existing module editor URL to print the usable pairing link:
npm run dev -- --url 'https://chronicleclassic.com/modules/your-author/your-slug/edit'
# UUID routes also work: https://chronicleclassic.com/modules/id/<uuid>/edit
# Optional: --origin https://chronicleclassic.com (must match --url exactly)
# For another working directory:
node /absolute/path/chronicle-module/tools/cli.mjs dev --root /absolute/path/chronicle-module
```

Without `--url`, the server prints instructions, not an unusable root link.
Origin defaults to `https://chronicleclassic.com`; supplying `--url` infers its
origin unless you explicitly supply `--origin`. Pairing UI exists only on module
editor routes, not `/modules` or a generic `/workshop` page.

Open the printed pairing link in your Chronicle browser and explicitly approve
local access. The token is a private credential carried in the URL fragment,
not a query parameter. Do not share the link. Restarting the server rotates it.
Only one browser tab can own the lease. Heartbeats renew it for 10 seconds.
Closing the browser expires the lease and fails outstanding previews.

**Chronicle deployment integration is required.** This repository alone does not
add the browser pairing/preview UI to a deployed Chronicle site. The deployment
must allow `http://127.0.0.1:17643` in its `connect-src` CSP and implement this
protocol. Browser localhost/local-network permission may need explicit approval.
CORS and Private Network Access preflight responses cannot override browser or
enterprise security policy. There is no remote tunnel or public listener.

Edit `main.lua`. The starter sums encounter damage into a metric. `project.json`
is the manifest; its `entrypoint` must name an included Lua file. Add relative
PNG, JPEG, or WebP paths to `assets` to include images. LuaLS uses the copied
Chronicle API declarations in `lua-types/`; refresh that copy when the host API
changes. The declarations are editor support only, not runtime package files.

## MCP configuration

Configure a generic MCP client with an absolute executable script path. Do not
use `npm run mcp` as the stdio command because npm prints chatter to stdout.

```json
{
  "mcpServers": {
    "chronicle-module": {
      "command": "node",
      "args": ["/absolute/path/chronicle-module/tools/cli.mjs", "mcp", "--root", "/absolute/path/chronicle-module"],
      "cwd": "/absolute/path/chronicle-module"
    }
  }
}
```

Start the dev server separately first. `preview` snapshots the current package,
asks the paired browser to execute that revision, and returns its result plus
source `revision`. `status` reports validation errors, revision, and connected
browser context (`instanceId`, `encounterIds`). Preview waits at most 60 seconds;
the HTTP client allows 65 seconds for transport overhead. A canceled/disconnected
MCP request cancels its pending command. Only one preview may be pending.

## Package and safety limits

Each poll rebuilds a deterministic, sorted, uncompressed ZIP with fixed DOS
1980-01-01 timestamps. The revision is SHA-256 of those exact ZIP bytes.
Includes `project.json`, optional `README.md`, all `.lua` sources outside ignored
directories, and declared raster assets. Excludes hidden entries, `node_modules`,
`tools`, `test`, `tests`, `dist`, `build`, `coverage`, `vendor`, and `lua-types`.
Symlinks outside ignored directories, path traversal, absolute paths, and
backslashes are rejected. Keep generated sources outside the package tree.
Limits: 128 files, 1 MiB per file, 4 MiB total uncompressed content, and 4096
scanned directory entries. This is a development snapshotter, not a sandbox for
concurrent hostile filesystem mutations. Do not let untrusted processes modify
the module directory while serving it. Lua runtime safety remains the host's job.

Invalid files produce an empty archive and an `error` field. Their revision is a
hash of the error message. A pending preview is retired when state polling sees
a different revision. The browser must also compare every command's revision
with its current state before execution and reject changes with
`{ "ok": false, "error": "Source changed; retry preview" }`. It must not execute a
command twice across repeated polls. Results belong to the pinned revision even
if a later edit occurs after execution.

## HTTP protocol 1

Fixed listener: `http://127.0.0.1:17643`, bound only to IPv4 loopback. Every API
request requires `Authorization: Bearer <token>`. `Host` must be exactly
`127.0.0.1:17643`. Browser `Origin` must equal the configured origin, including
scheme and port. Origin-less local MCP requests still need the token. The only
unauthenticated response is an approved-origin OPTIONS preflight with no state;
it allows Authorization, Content-Type, X-Chronicle-Client, and Private Network
Access. Browser permissions remain separate from explicit Chronicle approval.

- `POST /v1/browser`: `{clientId, context:{instanceId,encounterIds}, result?}`.
  Send a heartbeat **before** fetching state and continue while preview runs.
  A competing active client receives 409. Optional result is
  `{id,revision,result:<JSON value>}`. Unknown/expired IDs or wrong revisions
  are ignored with `{ok:true,resultAccepted:false}` so a raced result never
  disconnects an otherwise valid heartbeat. Matched results acknowledge
  `{ok:true,resultAccepted:true}`. Complete JSON request bodies must be smaller than 128 KiB.
- `GET /v1/state`: `{protocol:1,revision,archive,error?,commands:[{id,type:"preview",revision}]}`.
  `archive` is base64 ZIP. Browser requests require `X-Chronicle-Client` matching
  the active lease; local authenticated clients do not need that header.
- `POST /v1/preview`: `{}`. Holds the response until a matching browser result;
  success is `{id,revision,result}`. No browser: 409; invalid module: 422;
  concurrent preview/source changes/disconnect: 409; timeout: 504; shutdown: 503.
- `GET /v1/status`: `{protocol:1,revision,error?,browser:null|{clientId,context},pending}`.

The ignored `.chronicle/dev.json` contains `{port:17643,token}`, written with
0600 permissions. Dev logs go to stderr. The MCP process reserves stdout for
the official `@modelcontextprotocol/sdk` stdio transport.

## Tests

```sh
npm test
```

Tests use temporary module directories and actual loopback HTTP requests, plus
the official MCP client over stdio. They do not require Chronicle deployment.
