# Multi-browser connections: proposed protocol 3

Status: agreed with the Chronicle frontend agent; implementation pending.
This is a design contract, not a description of the current protocol 2 daemon.

## Invariants

- The shared daemon accepts multiple explicitly connected browser tabs.
- Each live connection has an opaque ID and a human-readable, editable name.
- A connection belongs to at most one MCP agent session. An agent selects at
  most one connection. Neither discovery nor `whereami` acquires ownership.
- A tab connection ID, a page generation/session, and an MCP agent session are
  separate identities. Navigation never routes an operation to another tab.
- Browser validation, execution, previews, and save approval remain authoritative.
- Disconnect, release, revocation, or expiry cancel pending operations and
  invalidate observations. They never save, discard edits, or close the tab.

## Agent tools

These four relay-owned tools are always advertised, including before selection:

| Tool | Input | Behavior |
| --- | --- | --- |
| `list_connections` | `{}` | List live tabs with ID, name, page URL, availability, and safe owner identity; no acquisition or page observation |
| `select_connection` | `{connection_id}` | Acquire an available tab for this MCP session; explicit selection only |
| `release_connection` | `{connection_id}` | Release the caller's selected tab, cancel its pending work, and clear selection/observation |
| `whereami` | `{}` | Observe the selected tab and its current page/capabilities; never acquire or choose a tab |

Selecting the already-owned connection is idempotent. Selecting a busy or absent
connection fails without changing the caller's selection. If an agent already
owns a different connection, selection fails with `already_selected`: explicitly
release the old connection first. There is no agent-side force/takeover option.
The ID on release guards against a delayed release acting on a newer selection.

After selecting, call `whereami`, rediscover the selected page's tools, then
fetch `gen_page_context` and applicable remote skills. Successful selection alone
does not authorize browser tool execution; explicit observation is required.
With no selection, `whereami` returns `selectedConnection: null`, `page: null`,
and `connected: false`, with instructions to list/select. It does not auto-select,
even when exactly one tab exists.

Only the selected tab's dynamic tools are advertised to that agent. An unselected
agent sees only the four relay tools. All four names are reserved against browser
catalog collisions. Selection/release, navigation, catalog changes, revocation,
and disconnect notify affected agents that their tool list changed. Notifications
never refresh the explicit observation used to authorize an operation.

Owner display data is `{id,name,version}`: `id` is a daemon-generated public
opaque identifier, NOT an MCP transport session ID or bearer token. Client names
and versions are self-reported, bounded, untrusted display text, not identities
for authorization. Owner id/name/version are nonempty and at most 128 Unicode
code points each; the public owner ID uses opaque ASCII. Availability is one of `available`, `owned_by_you`, or `busy`.
No browser bearer credentials, tool arguments, or results appear in listings.

## Browser identity and authentication

Protocol 3 retains the existing `/v1/` route namespace but changes bootstrap and
ownership payloads. Protocol 2 clients receive an explicit upgrade error; there
is no implicit single-browser fallback that could route a call incorrectly.

A tab generates a random opaque `clientId` for one connection lifecycle. It stays
stable across heartbeats, SPA navigation, and transient retries. Explicit
reconnection after disconnect or a full document reload uses a new ID and
requires agent selection again. Do not persist a credential/connection ID in
storage that a duplicated tab inherits. A display name may be persisted.

`POST /v1/connect` with an approved Origin accepts `{protocol:3,clientId}` and
returns `{protocol:3,clientId,token}`, echoing the requested clientId. The token
is exactly 64 lowercase hexadecimal characters. The credential is unique to that connection
and grants no agent/control authority. Active duplicate IDs return 409 rather
than replacing an existing tab. Unused bootstrap allocations expire after the
browser heartbeat lease. Only connections with a valid heartbeat appear in
agent discovery. Retrying a lost bootstrap response uses a fresh ID.

The daemon admits at most 64 total bootstrap allocations/live connections.
Bootstrap returns HTTP 503 when capacity is exhausted; expiry or explicit
disconnect frees capacity. Management responses are bounded to 8 MiB, including
all connection metadata, and non-MCP response writes have a five-second deadline.
These limits do not change MCP SSE stream lifetime.

All browser operations require this token. Header `X-Chronicle-Client` and body
`clientId`, where present, must match its bound connection. Exact request Origin
and page-origin matching remain enforced; bind the connection to its bootstrap
Origin. A token for tab A cannot poll, rename, submit results, revoke, or
disconnect tab B. Approved Chronicle subdomains remain trusted bootstrap origins.

## Browser messages

### Heartbeat: `POST /v1/browser`

Keep the current payload plus the required display name:

```json
{
  "clientId": "opaque-tab-id",
  "name": "summarize-consumes",
  "page": {"url": "https://octo.chronicleclassic.com/...", "generation": "g1", "sessionId": "p1"},
  "tools": [],
  "result": {"id": "call-id", "generation": "g1", "ownershipRevision": 3, "result": {}}
}
```

`result` remains optional. Names are trimmed, nonempty, at most 80 Unicode code
points, and rendered as text. Duplicate names are allowed; IDs disambiguate them.
The frontend supplies a useful default from the module/page context. Rename via
the next heartbeat; renaming does not release ownership or invalidate a page.
Existing schema, capability, body/result-size, and continuation rules remain.

Response:

```json
{
  "ok": true,
  "connection": {
    "id": "opaque-tab-id",
    "name": "summarize-consumes",
    "page": {"url": "https://octo.chronicleclassic.com/...", "generation": "g1", "sessionId": "p1"},
    "owner": {"id": "public-owner-id", "name": "Claude Code", "version": "..."},
    "ownershipRevision": 3
  },
  "resultAccepted": true
}
```

`owner` is null when available. `resultAccepted` is present only for submitted
results. Unknown/late calls or mismatched ownership revisions return
`resultAccepted:false`, never unpairing a healthy tab. Ownership revision starts
at zero, is always a nonnegative JavaScript-safe integer, and increases on every
acquisition or release/revocation, including
release followed by reacquisition by the same agent.

### Poll: `GET /v1/state`

Require the token-bound `X-Chronicle-Client`. Return `{protocol:3,connection,
commands:[...]}` for that connection only. Commands retain existing fields and
add `ownershipRevision`. Never return another tab's work. At most one operation
is pending per connection; different tabs can execute independently. Frontend
parsing rejects more than one command, a command without a non-null metadata
owner, or a command revision that differs from the connection metadata.

Heartbeat and poll responses expose the same connection metadata. When ownership
revision changes, the frontend cancels stale approvals/queued work and ignores
late worker results. An already-running browser mutation may not be reversible;
revocation is not rollback. Results must echo the command's original revision,
never whichever revision is current when the worker finishes.

### Revoke: `POST /v1/browser/revoke`

Require the connection-bound credential/header and
`{expectedOwnershipRevision}`. A stale revision returns 409 with fresh connection
metadata in the top-level `connection` field so the UI does not revoke a new
owner accidentally. A failed or stale revoke immediately cancels local work;
the frontend never retries automatically against a newer revision. Otherwise revoke
the current owner, cancel pending work, invalidate the agent's selection and
observation, and return `{ok:true,connection}`. An already-unowned connection with
a matching revision is an idempotent success. The tab stays connected.

### Disconnect: `POST /v1/browser/disconnect`

Require the connection-bound credential/header; an empty JSON body `{}` is
accepted. Stop frontend polling first, then use an independent short timeout for
the best-effort disconnect request. Remove that connection, release
its owner, cancel its pending operations, and invalidate the token. Return
`{ok:true}`. The frontend clears credentials and local control state even if the
request fails; heartbeat expiry is the fallback. Never close or clear the module.

## Lifecycle and frontend UI

- Preserve the current 10-second browser heartbeat lease. Expiry removes that
  connection, its credential, ownership, and pending work.
- Preserve the agent inactivity lease and six-hour daemon idle policy. Browser
  heartbeats and UI polling do not count as agent activity.
- Agent DELETE/expiry releases its selected tab, not the tab's connection.
- Navigation changes only that tab's page generation/session and cancels its
  pending calls. Ownership survives navigation within the connection Origin;
  the agent must observe and reload guidance before resuming.
- A transient heartbeat/poll network failure immediately cancels local pending
  work. Retry the same credential/ID only within the lease; never silently
  bootstrap a replacement connection. HTTP 401/403/404 is terminal and requires
  explicit reconnect. Keep started/aborted command IDs deduplicated for the
  whole connection lifetime so recovery cannot reexecute work the daemon still
  considers pending. Cancellation is not rollback of a completed mutation.
- Daemon restart invalidates credentials and selections. Reconnecting a browser
  never restores agent ownership automatically.
- UI shows connection name, copyable ID, connected/reconnecting/disconnected
  state, available/owned status, owner display name, and a revoke-control action.
- Derive the initial name from module/page context. A user-edited name survives
  SPA navigation; owner names are rendered as plain text.
- Rename, revoke, and disconnect are explicit user actions. Use confirmation for
  revocation/disconnection when work or approval is pending. Do not expose tokens.
- Show actionable unsupported-protocol, expired-session, and connection-conflict
  errors rather than silently downgrading or reconnecting into another session.

## Daemon/plugin changes and rollout

The daemon's compatibility identity must advertise protocol 3, and CLI reuse
checks must reject a running protocol 2 service with an explicit restart/upgrade
instruction. Do not silently stop it. CLI status should list safe per-connection
metadata and aggregate pending/agent counts, replacing its singleton browser
view. Browser-scoped `/v1/status`, if retained, must be token/header-scoped just
like `/v1/state`; it must not expose a global catalog.

The plugin bootstrap skill must list connections and request an explicit choice
when the user has not identified a connection. Selecting by a user-specified
unique name is a client-side lookup to an ID; ambiguous names require a choice.
The relay always selects by ID. The skill then selects, observes, and fetches
live guidance. Its local instructions must not copy remote task guidance.

Frontend and daemon changes ship together; neither side silently downgrades to
protocol 2. A clear upgrade-required message is preferable to unsafe routing.

## Acceptance coverage

1. Two tabs connect concurrently and appear with distinct IDs; duplicate names
   do not collapse them. Neither listing nor `whereami` acquires a tab.
2. Two agents select different tabs; each sees only its selected dynamic tools
   and each browser receives only its own commands.
3. Two agents race for one connection: exactly one succeeds. Failed selection
   cannot release or steal ownership. Cross-tab tokens/headers are rejected.
4. Release/switch, revoke/reacquire, navigation, expiry, and daemon restart reject
   stale observations, approvals, and results without altering the browser copy.
5. Browser revocation updates the UI and clears the agent's selection/catalog.
   A delayed revoke with an old revision cannot evict a newly acquired owner.
6. Pending operations on separate tabs are independent; an agent disconnect
   cancels only that agent's work. Global teardown still disconnects everyone.
7. Protocol 2 gets an actionable upgrade error. Existing protocol 3 body limits,
   exact page-origin checks, MCP cancellation, and browser approval survive.

Frontend work is coordinated with the Chronicle workspace `MCP To Chronicle`;
daemon, CLI inspection, plugin guidance, and relay tests belong in this repo.
