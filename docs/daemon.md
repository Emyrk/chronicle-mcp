# Go daemon

The Go daemon replaces the per-client Node listener with one headless loopback
service. Existing Node sources and tests remain as regression references. It
implements browser protocol 3 as daemon version 0.2.0. It uses the official Go MCP SDK v1.8.0, verified against the module registry, and
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
See [publishing executable releases](releases.md) for the four-platform build,
GitHub attestation verification, and explicit upgrade procedure. A configured
release pin is usable only after its tag workflow publishes the artifacts.

## Flags and state

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port` | `17643` | IPv4 loopback port; `0` selects an ephemeral port for tests |
| `--state-dir` | OS user config directory + `/chronicle-mcp` | Private shared state directory |
| `--origin` | `https://chronicleclassic.com` | One additional exact browser origin |
| `--idle-timeout` | `6h` | Agent inactivity shutdown; `0` disables |
| `--agent-lease` | `5m` | Inactive agent session retention, positive and at most `24h` |
| `stop --force` | false | Explicitly disconnect active agents and cancel pending calls |

The defaults allow HTTPS `chronicleclassic.com` and all its subdomains on the
default HTTPS port, plus `http://localhost:4000` and `http://localhost:5175`. Each allowed Origin is echoed
exactly in CORS responses; page URLs must still match the requesting Origin.
All these subdomains are trusted to bootstrap browser credentials. `--origin`
adds one exact origin; wildcard flag values, paths, and trailing slashes are invalid.
Use the same `--state-dir` on all commands when overriding it. On Linux, the
default respects `XDG_CONFIG_HOME`, otherwise `$HOME/.config/chronicle-mcp`. On macOS
it is `$HOME/Library/Application Support/chronicle-mcp`.

The directory is mode 0700 and `dev.json` is mode 0600. It contains the agent bearer token, a private state-cleanup marker, listener port,
version, and configuration. Browser credentials are separate per-tab tokens held
only in daemon memory and returned to that tab during bootstrap.
Never share or commit it. A daemon lock prevents competing writers. State is
atomically written, symlink paths are refused, and shutdown removes only the
state belonging to that daemon. `status` emits health JSON without credentials:
`connections` contains safe IDs, names, page URLs, ownership revisions, availability,
pending-work indicators, and public owner identity. It also includes aggregate
agent/pending counts, last agent activity, and idle deadline/remaining duration.
Raw MCP/browser page session identifiers, tool arguments, and results are omitted.
A disabled idle timer has `disabled: true` and no deadline. `pendingCall` explains
why an elapsed idle deadline cannot yet stop the daemon. Owner IDs are independent
public opaque IDs, not MCP session IDs; names and versions are bounded untrusted
client display text, not authenticated identities.
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

Four relay tools are always advertised, even without a connected browser:

1. `list_connections {}` lists live tabs without selecting or observing a page.
2. `select_connection {"connection_id":"..."}` explicitly acquires an available
   tab. Ask the user to choose when unspecified; duplicate names need an ID.
3. `whereami {}` observes only the selected tab, never acquiring a tab.
4. `release_connection {"connection_id":"..."}` releases only that selection.

There is no automatic selection, even with exactly one tab. Selecting a busy or
absent connection cannot change an existing selection; switching requires explicit
release first. Re-selecting the same owned connection is idempotent. Selection
alone does not authorize browser tools: explicitly observe with `whereami` or
`tools/list`, then fetch current page guidance and skills. Each agent sees only
its selected tab's dynamic catalog. Tool-list notifications target affected
agents and do not update their observations.

An agent can select one tab, and a tab has at most one owner. Different tabs have
independent pending calls. Release, revoke, disconnect, lease expiry, and shutdown
cancel only the corresponding work and invalidate its observation. Browser
ownership revisions increase on every acquisition/release to reject stale results
and approvals even after the same agent reacquires the same tab. Cancellation is
not rollback of a browser mutation that already ran.

Clients should DELETE their MCP session on exit and send pings more frequently
than `--agent-lease` while retaining it. A silent TCP/SSE connection is not an
indefinite ownership lease. The relay owns one session inactivity clock for both
GET and POST and closes the SDK session when it expires. Agent expiry releases
the tab but does not disconnect its browser.

All authenticated activity from a known connected agent, including pings and
HTTP keepalive requests, renews its activity. Browser heartbeats, status/health
polling, and housekeeping do not renew global idle activity, including when no
agents remain. Pending tool calls pin their agent ownership and prevent idle
exit, but retain the existing 60-second call deadline. The browser still has a
10-second heartbeat lease. The daemon checks idle/lease expiry once per second.

Dynamic tools and their JSON schemas come from the browser, not a local catalog.
The four relay tools exist while disconnected. Tool lists and explicit `whereami` capture
page/session/capability observations. Notifications never refresh observations.
Navigation invalidates pending calls, and delayed calls against changed state
fail stale until the client explicitly observes again. Approval/rejection and
other browser continuation JSON are returned unchanged, with MCP `isError` for
any non-success status.

## Browser compatibility and management endpoints

[The protocol 3 contract](multi-connections.md) defines the exact browser wire
format. Version 0.2.0 does not silently downgrade to protocol 2; bootstrap returns
an actionable upgrade error. CLI compatibility rejects protocol 2 daemons without
stopping them. Explicitly stop/upgrade an old daemon with its matching CLI.

- `POST /v1/connect` requires approved Origin and `{protocol:3,clientId}`. It
  returns a fresh 64-hex token bound to that client ID and exact Origin. Duplicate
  active IDs return 409; unused allocations expire after ten seconds. The registry
  permits at most 64 allocated/live connections. Bootstrap returns 503 at capacity;
  disconnect a tab or wait for unused/expired leases before retrying with a fresh ID.
- All browser routes require that token, matching `X-Chronicle-Client`, and exact
  bootstrap Origin. Body client IDs must also match. Tokens cannot operate on
  another tab, MCP, or management endpoints.
- `POST /v1/browser` adds a trimmed, nonempty name of at most 80 Unicode code
  points. Heartbeat responses return connection/owner/revision metadata. Results
  must echo the command's original ownership revision, not the current one.
- `GET /v1/state` returns only that connection and at most one pending command.
  Different tabs can execute independently. Browser `/v1/status` is scoped too.
- `POST /v1/browser/revoke` accepts `expectedOwnershipRevision`. A stale revision
  returns 409 with fresh top-level `connection` metadata; do not retry automatically.
- `POST /v1/browser/disconnect` removes only that tab and invalidates its token.
  Reconnect with a fresh client ID; ownership is never restored automatically.

The 128-KiB request and 64-KiB result bounds, exact Origin/Host validation,
private-network preflights, capability pinning, and late-result rules remain.
Unknown or stale results return `resultAccepted:false` without unpairing a healthy
tab. Browser validation, previews, mutations, and save approval remain entirely
in Chronicle. The legacy sessionless Node `/v1/call` route is not exposed.

Authenticated agent-only `GET /health` returns service `chronicle`, version,
protocol, number of agents/pending calls, and effective timing/origin config.
`POST /shutdown` accepts `{ "force": false }`, returning 409 if sessions or calls
are active. These endpoints do not extend daemon idle lifetime. Control/browser
responses are buffered snapshots capped at 8 MiB and written outside registry
locks with a five-second write deadline. This accommodates worst-case escaped
metadata for all 64 connections. CLI health decoding uses the same bound. MCP SSE
streams do not inherit that deadline.

## Frontend discovery

`GET http://127.0.0.1:17643/healthz` is a credential-free liveness and
compatibility probe. It returns only `{"service":"chronicle","version":"0.2.1","protocol":3}`
with `Cache-Control: no-store`. Check the service and protocol, not just HTTP 200.
It does not connect a tab, acquire ownership, or renew agent/daemon idle timers.
The existing `/health` remains an authenticated management endpoint.

The probe retains exact `127.0.0.1:<port>` Host validation (`localhost` is not an
alias) and allows CORS only for approved Chronicle origins, including the exact
configured `--origin`. Approved-origin OPTIONS preflights are supported. No
Authorization or `X-Chronicle-Client` header is needed:

```js
const response = await fetch("http://127.0.0.1:17643/healthz", {
  credentials: "omit",
  signal: AbortSignal.timeout(2000),
});
if (!response.ok) throw new Error("Relay unavailable");
const relay = await response.json();
const compatible = relay.service === "chronicle" && relay.protocol === 3;
```

A failed browser fetch can also mean local-network permission or browser policy
blocked access; it does not prove the daemon is not installed. This endpoint
cannot start a stopped daemon. Browser bootstrap still requires explicit connect.

## Local dashboard

`GET /` serves an embedded, unauthenticated dashboard on this same loopback
listener. Use `http://127.0.0.1:<port>/`; `localhost` is not an alias for the
exact Host policy. It displays safe inspection data plus uptime and provides
revision-pinned control revocation and confirmed shared shutdown. The dashboard
is local administration, not browser workshop execution; it never saves or
modifies module source.

Dashboard routes have their own same-origin policy, independent of Chronicle
browser CORS. Remote/null Origins and cross-site Fetch Metadata are refused.
Mutations require POST, exact loopback Origin, JSON, and a dashboard-specific
header. These checks prevent cross-origin websites from invoking local controls;
they do not restrict other local processes or local users. Do not expose the
listener through a reverse proxy or port forward. Existing authenticated MCP,
health, shutdown, and browser APIs retain their credential requirements.
Dashboard polling never extends agent leases or daemon idle lifetime.

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
