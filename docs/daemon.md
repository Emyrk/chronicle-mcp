# Go daemon

The Go daemon replaces the per-client Node listener with one headless loopback
service. Existing Node sources and tests remain as regression references. It
uses the official Go MCP SDK v1.8.0, verified against the module registry, and
requires Go 1.25 or newer to build:

```sh
go build -o chronicle-mcp ./cmd/chronicle-mcp
./chronicle-mcp start
./chronicle-mcp status
./chronicle-mcp stop
```

`serve` runs in the foreground. `start` detaches and waits for authenticated
readiness; it reuses an existing daemon only when identity, version, protocol,
and configuration match. An occupied port, incompatible version, different
configuration, invalid credentials, or unexpected service produces an error,
not an automatic kill or upgrade. Detached startup logs are discarded; use
`serve` for diagnostic errors. A startup timeout does not kill an unverified
process. Linux, macOS, and BSD are supported. Windows builds, but daemon state
locking and detached startup are currently unsupported and fail explicitly.
No release binaries are provided by this change.

## Flags and state

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port` | `17643` | IPv4 loopback port; `0` selects an ephemeral port for tests |
| `--state-dir` | OS user config directory + `/chronicle-mcp` | Private shared state directory |
| `--origin` | `https://chronicleclassic.com` | One additional exact browser origin |
| `--idle-timeout` | `6h` | Agent inactivity shutdown; `0` disables |
| `--agent-lease` | `5m` | Inactive agent session retention, positive and at most `24h` |
| `stop --force` | false | Explicitly disconnect active agents and cancel pending calls |

The defaults always allow browser origins `https://chronicleclassic.com` and
`http://localhost:4000`. Wildcards, paths, and trailing slashes are invalid.
Use the same `--state-dir` on all commands when overriding it. On Linux, the
default respects `XDG_CONFIG_HOME`, otherwise `$HOME/.config/chronicle-mcp`. On macOS
it is `$HOME/Library/Application Support/chronicle-mcp`.

The directory is mode 0700 and `dev.json` is mode 0600. It contains separate
browser and agent bearer tokens, listener port, version, and configuration.
Never share or commit it. A daemon lock prevents competing writers. State is
atomically written, symlink paths are refused, and shutdown removes only the
state belonging to that daemon. `status` emits health JSON without credentials. It includes the connected browser
page URL, ownership busy state and owner client name/version, last agent activity,
and the idle deadline and remaining duration. Raw MCP/browser session identifiers
are omitted. A disabled idle timer has `disabled: true` and no deadline. A
`pendingCall` flag explains why an elapsed idle deadline cannot yet stop the daemon.
Client names are self-reported, not authenticated identities.
`stop` uses authenticated HTTP, not a recorded PID. Active sessions or calls
require `--force`; shutdown never invokes a save or erases browser working files.

## Agent connection

MCP Streamable HTTP is served at `http://127.0.0.1:17643/mcp`. Agent and management
endpoints require the agent token and reject browser Origin headers. The command
below emits **only** the JSON request headers to stdout, for a client's private
header helper. It reads existing state and never installs or starts anything:

```sh
chronicle-mcp auth headers
```

Configure the client's headers helper to execute that command. Do not paste its
output into committed MCP configuration. Start the daemon explicitly before
connecting. A restart rotates credentials and clients must reconnect. Clients
must support stateful Streamable HTTP and send `Mcp-Session-Id` as required by
MCP. Multiple clients can initialize and discover tools concurrently.

`tools/list` observes the current browser catalog but does **not** claim it.
An explicit `whereami` claims an unowned connected browser for that agent session
and reports `ownership.owned` and `ownership.busy`. Browser tool calls require
that claim; another session receives an error, never implicit takeover. Ownership
ends on MCP DELETE/disconnect, bounded inactivity lease expiry, browser lease
expiry, or daemon shutdown. A disconnected TCP request alone is not a reliable
session-close signal. Clients should DELETE their MCP session on exit and send
MCP pings more frequently than `--agent-lease` while they want to retain it.
A silent SSE connection is not an indefinite ownership lease. The relay owns the
single session inactivity clock and closes the MCP SDK session when its lease
expires; SDK timers cannot expire a GET-renewed session behind that clock.

All authenticated activity from a known connected agent, including pings and
HTTP keepalive requests, renews its activity. Browser heartbeats, status/health
polling, and housekeeping do not renew global idle activity, including when no
agents remain. Pending tool calls pin their agent ownership and prevent idle
exit, but retain the existing 60-second call deadline. The browser still has a
10-second heartbeat lease. The daemon checks idle/lease expiry once per second.

Tools and their JSON schemas come from the browser, not a local catalog. Only
`whereami` exists while disconnected. Tool lists and explicit `whereami` capture
page/session/capability observations. Notifications never refresh observations.
Navigation invalidates pending calls, and delayed calls against changed state
fail stale until the client explicitly observes again. Approval/rejection and
other browser continuation JSON are returned unchanged, with MCP `isError` for
any non-success status.

## Browser compatibility and management endpoints

Browser protocol 2 preserves `/v1/connect`, `/v1/browser`, `/v1/state`, and
`/v1/status`, including original request/response fields, body bounds, exact
origins, Host validation, private-network preflights, capability pinning, and
late-result handling. The bootstrap route returns only the browser credential.
It cannot grant MCP or management access. The old internal Node `/v1/call` route
is intentionally not exposed: calls now enter through authenticated MCP session
ownership, not a sessionless bypass. Browser mutations, validation, previews,
and save approval still execute entirely in Chronicle.

Authenticated agent-only `GET /health` returns service `chronicle`, version,
protocol, number of agents/pending calls, and effective timing/origin config.
`POST /shutdown` accepts `{ "force": false }`, returning 409 if sessions or calls
are active. These endpoints do not extend daemon idle lifetime.

## Validation

```sh
go test -race ./...
go vet ./...
npm ci
npm test
git diff --check
```

Go integration tests use the official MCP client over real ephemeral HTTP
listeners. They cover dynamic discovery, exclusive ownership, cancellation,
continuations, stale state, browser authentication, management isolation,
private state, detached startup/reuse/stop, and deterministic lease/idle clocks.
No test uses or stops an existing default-port daemon.
