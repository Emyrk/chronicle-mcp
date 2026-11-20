<p align="center">
  <a href="https://chronicleclassic.com/?utm_source=github&amp;utm_medium=referral&amp;utm_campaign=readme&amp;utm_id=chronicle_mcp&amp;utm_content=logo">
    <img src="https://raw.githubusercontent.com/Emyrk/chronicle/main/frontend/chronicle/public/c/chronicle/ChronicleLogoCenter.svg" alt="Chronicle" width="320" />
  </a>
</p>

<h3 align="center">Chronicle MCP — your agent, connected to your browser</h3>

## Quick start

The Claude plugin uses a shared, headless Go daemon over authenticated loopback
HTTP MCP at `http://127.0.0.1:17643/mcp`. The plugin runtime does **not** require
Node.js. The legacy standalone Node relay described below still requires Node.js
22+ and `npm ci`; do not run it alongside the daemon on the same port.

### 1. Load the Claude Code plugin

```sh
git clone https://github.com/Emyrk/chronicle-mcp.git
cd /absolute/path/to/your/project
claude --plugin-dir /absolute/path/chronicle-mcp
```

No marketplace is required. Use a Claude Code version supporting
[plugin path variables](https://code.claude.com/docs/en/plugins-reference#environment-variables)
and HTTP MCP `headersHelper`. The plugin bundles three thin skills, an explicit
installer, and MCP configuration, with no hooks or agents. Loading it does not
install or start anything automatically.

### 2. Set up the shared daemon

Run `/chronicle:setup` and approve the setup command. The installer downloads only
the version pinned in `scripts/release.tsv`, verifies its bundled SHA256, and
stores the executable in `${CLAUDE_PLUGIN_DATA}/bin/<version>/chronicle`. It checks
the hash again before every invocation. A checksum fetched alongside an artifact
is not trusted. Existing compatible services are reused by the CLI; setup never
silently stops or upgrades a running daemon. Resolve an occupied port or version
conflict explicitly rather than killing the listener.

**Release status: unreleased.** No artifacts or checksums have been published or
invented. Setup fails closed until maintainers ship reviewed release metadata.
For local development, build the Go CLI and explicitly set `CHRONICLE_DEV_BINARY`
to its absolute path in the environment used to launch Claude Code. This is an
opt-in, unverified local executable, not an integrity-verified release install.

The installer supports **Linux and macOS, amd64 and arm64**, using POSIX `sh`,
`curl`, and `sha256sum` or `shasum`. Native Windows installation is not implemented;
build/run the Go CLI manually and configure an HTTP MCP client with its
`chronicle auth headers` command as headers helper. Do not run the POSIX plugin
installer in a Windows environment and assume it installed a native binary.

After setup reports status, reconnect the server in `/mcp` or restart Claude
Code. Its headers helper reads credentials privately through the installed CLI;
it does not install or start a service. Never paste tokens or private state into
chat. The daemon's per-user state is independent of plugin binary storage. Run
`/chronicle:chronicle` to load live browser context and skills.

Use `/chronicle:teardown` for graceful shared shutdown. It checks status and asks
for approval; active clients/work require an additional explicit confirmation
before `stop --force`. Stopping affects every connected agent, not just this
plugin. Uninstalling the plugin is not a daemon shutdown operation.

Do not enable a second Chronicle MCP entry alongside the plugin, or start the
legacy `npm run dev` listener on port 17643. When opening this checkout itself,
do not also approve its `.mcp.json` as a project server: the headers helper uses
plugin-specific path variables.

### Alternative: standalone MCP client (including Xum)

Use this instead of the plugin; the same npm dependency installation is required.

Replace `/absolute/path/chronicle-mcp` with your checkout path:

```json
{
  "mcpServers": {
    "chronicle-mcp": {
      "command": "node",
      "args": [
        "/absolute/path/chronicle-mcp/tools/cli.mjs",
        "mcp",
        "--root",
        "/absolute/path/chronicle-mcp"
      ]
    }
  }
}
```

In Xum, add the command and arguments under **Settings → MCP**, then connect.
Use `node` directly, not `npm run mcp`: npm writes extra text to the stdio channel.
The MCP process starts its own loopback server—**do not start a separate dev server**.

**Using Vite on `http://localhost:5173`?** Append `"--origin", "http://localhost:5173"` to `args`, then restart the MCP connection. For another deployment, pass its exact origin instead. The defaults allow `https://chronicleclassic.com` and `http://localhost:4000`.

### 3. Connect your browser tab

Open a module edit page and click **Connect to local MCP**. Approve browser local-network access if prompted. Your account needs Chronicle's inference-chat permission; a model-provider connection is not required.

Ask your agent to call `whereami`, then read or edit the module and run a preview. Select an instance and encounters in the Preview pane first. Changes stay in the browser working copy; saving requires browser approval and does not publish a release.

| Tool | What it does |
| --- | --- |
| `whereami` | Reports the connected page URL and connection state. |
| `read_lua_files` | Reads the browser working copy and revision. |
| `edit_lua_file` | Edits a module file with a revision check. |
| `run_startup_check` | Checks Lua startup in the browser worker. |
| `run_preview` | Replays the module against selected encounters. |
| `save_lua_files` | Requests approval to save the working copy to main. |

The page supplies its own tools; only `whereami` is available before a tab connects.

The MCP initialization instructions guide the agent to call `whereami`, then
`gen_page_context` when advertised, followed by `list_skill` and `read_skill`
for relevant skills before acting. This server/browser-supplied guidance is
page-scoped; refresh it after navigation. These are dynamic browser tools, not
locally bundled skill content or fixed relay schemas.

> [!NOTE]
> One MCP process and one browser tab can use the relay at a time. Port **17643** must be free. Shared use across multiple MCP processes is not implemented yet.

## How it works

The browser owns all source files and edits. MCP does not read module files,
package ZIPs, hash sources, or watch the filesystem. The browser exposes shared
Chronicle-defined tool contracts and validates inputs through Chronicle's tool
endpoint. Mutations and confirmation policy remain browser responsibilities.
The relay cannot override a tool's effect or confirmation policy.

Only one tab owns the lease, renewed by heartbeats for 10 seconds. Navigation
changes the page generation or session, immediately invalidating pending calls,
even if the URL stays the same. Closing the tab expires the lease and fails
outstanding calls. A late result is ignored without disconnecting the tab.

## Configuration and lifecycle

`--root` optionally chooses the private session-file directory, defaulting to
cwd. No `project.json`, Lua source, or other module files are required.
`--origin https://example.com` approves an additional exact browser origin.
`--port 0` selects an ephemeral port for integration tests; the normal browser
integration uses port 17643. Both npm and pnpm option separators are supported.

MCP owns the listener. It fails clearly if the port is occupied, never attaches
to another process, and never stops the existing listener. Client disconnect,
stdin closure, SIGINT, and SIGTERM cancel pending calls, release the listener,
and remove this process's session file.

`tools/list` returns `whereami` plus the connected browser's tool names,
descriptions, and JSON input schemas unchanged. Without a browser it returns
only `whereami`. Catalog, page generation/session changes, and lease expiry
emit `notifications/tools/list_changed`.

`whereami` returns `{page:null|{url,generation,sessionId},connected}` using the
latest heartbeat URL. URL fragments are stripped. Browser tool calls return the
browser's JSON continuation unchanged in MCP text content. A continuation whose
`status` is not `success` (including rejection, stale state, and client errors)
is an MCP `isError` result. HTTP failures also become `isError`; `stale_page`
becomes `{status:"stale_state",result:{message:"Page changed"}}`.

Tool calls also pin the client's last explicit observation. `tools/list` and
`whereami` record the page generation/session and each capability. Notifications
do not refresh this observation. A delayed call after navigation or capability
revision changes fails stale rather than silently targeting the new page. Call
`whereami` or list tools again to observe the current page and resume. Clients
must first observe connected tools before calling them. Canonical tool input
schemas remain unchanged; observation metadata travels only inside the relay.
Repeated calls with unchanged page/capability revisions remain valid.

One call may be pending. It times out after 60 seconds (the local HTTP client
allows 65 seconds). Canceling an MCP call removes its pending browser command.

## Deployment and security

Chronicle deployment integration is required. The site must implement protocol 2
and allow `http://127.0.0.1:17643` in its `connect-src` CSP. CORS and Private
Network Access preflight responses do not override browser or enterprise policy.
There is no remote tunnel or public listener.

The listener binds only IPv4 loopback. `Host` must exactly match
`127.0.0.1:<port>`. Approved origins always include
`https://chronicleclassic.com` and `http://localhost:4000`, plus `--origin`.
Scheme and port must match exactly; wildcards and trailing slashes are rejected.
All routes reject unapproved Origins. All routes require bearer authentication
except approved-origin bootstrap and OPTIONS. Origin-less local requests still
require bearer authentication.

The private `.chronicle/dev.json` contains `{port,token}`, with mode 0600. It is
internal relay state, not a user setup step. Restarting rotates the token. Keep
it private. Logs go to stderr; stdout belongs exclusively to official MCP stdio.
Approved sites can bootstrap the token and are trusted.

## HTTP protocol 2

- `POST /v1/connect`: `{protocol:2}` returns `{protocol:2,token}`. Requires an
  approved Origin, not a bearer token. Does not claim the tab lease.
- `POST /v1/browser`: `{clientId,page:{url,generation,sessionId},tools,result?}`.
  Claims/renews the single-tab lease. A competing client receives 409. The page
  URL must be HTTP(S), have an approved exact origin, and match the request's
  Origin when present. Send origin + pathname + search, without hash.
  Each tool is `{name,description,inputSchema,capability,effect,confirmation}`.
  Capability is `{id,version,scopeType?,scopeId?,stateRevision?}`; version is a
  positive integer, stateRevision a nonnegative integer. `effect` is `read` or
  `view_mutation`; `confirmation` is `never` or `unless_yolo`.
  Schemas are JSON objects with `type:"object"`. Names are unique, at most 128
  characters using letters, digits, `_`, `.`, `-`; `whereami` and `status` are
  reserved. Maximum 64 tools, 128 KiB complete heartbeat/catalog body, 64 KiB
  serialized result value. Optional result is `{id,generation,result:<JSON>}`.
  Unknown IDs or stale generations acknowledge `{ok:true,resultAccepted:false}`.
- `GET /v1/state`: `{protocol:2,commands:[{id,type:"tool",generation,toolName,
  arguments,capability}]}`. No ZIP or source revision. Browser requests require
  `X-Chronicle-Client` matching the active lease. Commands pin the tool capability
  and page generation/session at call time. The browser must deduplicate command
  IDs and recheck the pinned capability before execution.
- `POST /v1/call`: `{name,arguments:<object>,page:{generation,sessionId},capability}`
  includes the last explicitly observed page and capability. Missing or mismatched
  observation returns 409 `stale_page` before enqueueing. Holds HTTP until the browser returns
  `{id,generation,result}`. Unknown tool: 404; no browser or concurrent call: 409;
  page generation/session change: 409 `stale_page`; timeout: 504; shutdown: 503.
  Complete request bodies are bounded to 128 KiB. Inputs receive full validation
  in the browser, not via a lossy reconstruction of schemas in the relay.
- `GET /v1/status`: `{protocol:2,page:null|page,tools:[],connected,pending}`.

Preflight permits Authorization, Content-Type, X-Chronicle-Client, and Private
Network Access. Continue heartbeats while a tool executes, before polling state.

## Deprecated standalone dev command

`npm run dev` starts only the same browser-tool relay without MCP. It does not
serve a local package. Do not run it alongside MCP on the same port. For legacy
pairing links, `--url <editor-url>` infers its origin unless `--origin` is supplied;
the two must match. The printed fragment token is private. Normal bootstrap
requires no copied token. Stop the standalone server with Ctrl+C.

The Lua starter, `project.json`, `lua-types/`, and legacy package helper remain
for compatibility, but are not part of the MCP flow.

## Tests

Run `npm test`. Tests use ephemeral loopback ports and temporary directories,
including a directory with no module files. They cover authentication, schemas,
bounds, real official MCP stdio, dynamic notifications, navigation invalidation,
late results, cancellation, lifecycle cleanup, plugin packaging, and legacy packaging helpers.
They never use or stop an existing listener on port 17643.
