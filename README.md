# Chronicle local module

A generic stdio MCP relay for Chronicle's browser-owned working copy. Requires
Node.js 22 or newer. No build step. The project name is unchanged.

## Quick start

1. Run `npm install` in this directory.
2. Configure your MCP client as below. The MCP process starts its own loopback
   webserver. Do not start a separate dev server.
3. Open your normal Chronicle module editor and explicitly connect the local
   browser tools. Approve browser localhost/local-network access if prompted.
4. Ask your MCP client for `whereami`, then use the tools the connected page
   advertises, such as `read_lua_files`, `save_lua_files`, or `run_preview`.

The browser owns all source files and edits. MCP does not read module files,
package ZIPs, hash sources, or watch the filesystem. The browser exposes shared
Chronicle-defined tool contracts and validates inputs through Chronicle's tool
endpoint. Mutations and confirmation policy remain browser responsibilities.
The relay cannot override a tool's effect or confirmation policy.

Only one tab owns the lease, renewed by heartbeats for 10 seconds. Navigation
changes the page generation or session, immediately invalidating pending calls,
even if the URL stays the same. Closing the tab expires the lease and fails
outstanding calls. A late result is ignored without disconnecting the tab.

## MCP configuration

Use an absolute script path. Do not use `npm run mcp` as the stdio command because
npm prints chatter to stdout.

```json
{
  "mcpServers": {
    "chronicle-module": {
      "command": "node",
      "args": ["/absolute/path/chronicle-module/tools/cli.mjs", "mcp"],
      "cwd": "/any/writable/directory"
    }
  }
}
```

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
late results, cancellation, lifecycle cleanup, and legacy packaging helpers.
They never use or stop an existing listener on port 17643.
