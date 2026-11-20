<p align="center">
  <a href="https://chronicleclassic.com/?utm_source=github&amp;utm_medium=referral&amp;utm_campaign=readme&amp;utm_id=chronicle_mcp&amp;utm_content=logo">
    <img src="https://raw.githubusercontent.com/Emyrk/chronicle/main/frontend/chronicle/public/c/chronicle/ChronicleLogoCenter.svg" alt="Chronicle" width="320" />
  </a>
</p>

<h3 align="center">Chronicle MCP — your agent, connected to your browser</h3>

## Quick start

The Claude plugin uses a shared, headless Go daemon over authenticated loopback
HTTP MCP at `http://127.0.0.1:17643/mcp`. The plugin runtime does **not** require
Node.js. The old Node relay remains only as a compatibility reference; do not run it
alongside the daemon on the same port.

### 1. Install the Claude Code plugin

Add our marketplace and install the plugin from your terminal:

```sh
claude plugin marketplace add Emyrk/chronicle-mcp
claude plugin install chronicle@chronicle-plugins
```

Then start Claude Code in your project and run `/chronicle:setup`.
This is Chronicle's own marketplace, not an Anthropic directory listing.

For local development instead, clone the repository and load it directly:

```sh
git clone https://github.com/Emyrk/chronicle-mcp.git
cd /absolute/path/to/your/project
claude --plugin-dir /absolute/path/chronicle-mcp
```

Use one installation method at a time to avoid loading duplicate Chronicle
plugins. When moving from `--plugin-dir` to the marketplace installation, start
a new session without `--plugin-dir`. Do not assume the inline plugin and the
marketplace plugin share a data directory; run setup for the installed plugin.
Use a Claude Code version supporting
[plugin path variables](https://code.claude.com/docs/en/plugins-reference#environment-variables)
and HTTP MCP `headersHelper`. The plugin bundles three thin skills, an explicit
installer, and MCP configuration, with no hooks or agents. Loading it does not
install or start anything automatically.

### 2. Set up the shared daemon

**Secure-download prerequisite:** GitHub CLI (`gh`) with `gh attestation verify`
support is required for the GitHub-attested release download method.
The setup agent must ask your permission before installing or upgrading it,
explain that it verifies release authenticity for security, and use a trusted
package-manager installation. Declining leaves release setup blocked; it must
not fall back to unverified downloads. See [GitHub CLI installation](https://cli.github.com/).
Explicit local development builds do not require `gh`. Verification requires
network access and a CLI supporting all required attestation policy flags.

Run `/chronicle:setup` and approve the setup command. The installer downloads only
the version pinned in `scripts/release.tsv`, verifies GitHub build provenance, and
stores the executable in `${CLAUDE_PLUGIN_DATA}/bin/<version>/chronicle-mcp`. It checks
provenance again before every invocation, pinning the repository, release workflow,
exact source tag, and GitHub-hosted runners. A downloaded checksum alone is not trusted. Existing compatible services are reused by the CLI; setup never
silently stops or upgrades a running daemon. Resolve an occupied port or version
conflict explicitly rather than killing the listener.

**Release pin: v0.2.1.** This pin does not claim an existing published release.
Setup fails closed until the tagged release publishes binaries and attestations.
For local development, build the Go CLI and explicitly set `CHRONICLE_DEV_BINARY`
to its absolute path in the environment used to launch Claude Code. This is an
opt-in, unverified local executable, not an integrity-verified release install.

The installer supports **Linux and macOS, amd64 and arm64**, using POSIX `sh`,
`curl`, and GitHub CLI (`gh`). Native Windows installation is not implemented;
the Go binary cross-builds, but Windows daemon locking/startup are also
currently unsupported. Do not run the POSIX plugin
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

**Before upgrading the plugin**, tear down with the old plugin/CLI first, then
upgrade and run setup. The new CLI cannot stop an incompatible old daemon. If
already upgraded, use the retained old executable explicitly with the same state
directory and user approval. There is no automatic upgrade engine.

Do not enable a second Chronicle MCP entry alongside the plugin, or start the
legacy `npm run dev` listener on port 17643. MCP configuration lives in
`plugin.mcp.json`, explicitly referenced by the plugin manifest, so opening this
checkout does not also register a project MCP server. If you previously enabled
a separate project/user `chronicle-mcp` entry, disable it and use the plugin
entry `plugin:chronicle:chronicle-mcp` instead.

### 3. Build and test locally before the first release

Requires Go 1.25+. Build into an ignored local directory:

```sh
mkdir -p .build
go build -o .build/chronicle-mcp ./cmd/chronicle-mcp
export CHRONICLE_DEV_BINARY="$PWD/.build/chronicle-mcp"
"$CHRONICLE_DEV_BINARY" start
"$CHRONICLE_DEV_BINARY" status
claude --plugin-dir "$PWD"
```

Then run `/chronicle:chronicle`. The explicit development override is inherited
by the plugin's setup and authentication helpers. To inspect startup errors,
use `serve` in the foreground rather than `start`.

### 4. Connect a browser

Open https://choose.chronicleclassic.com/modules, open a module editor, and click
**Connect to local MCP**. Approve browser local-network
access if prompted. Each tab registers a named connection with its own ID. Tell
the agent which connection to use; it lists connections, explicitly selects the
ID, then calls `whereami` and fetches live page context and skills.

`whereami` only observes the agent's selection. It never acquires a tab, even if
only one is connected. Each agent can own one tab and each tab has at most one
owner. Release the current connection before selecting another. Only the four
connection-management tools exist before selection; page tools come from the
selected tab, never from a combined catalog.

Browser UI can rename a connection, show its owner, revoke control, or disconnect
without closing the module. Navigation invalidates page observations but does
not select another tab. Full reload/reconnect creates a new connection and
requires explicit selection again. Agent sessions expire after five minutes
without activity by default; clients should send MCP pings to retain control.

Edits stay in the browser working copy. Saving requires browser approval and
never implicitly publishes a release. Stopping the daemon does not close the
browser or discard those edits. Tray UI is a follow-up.

## Headless service and other MCP clients

```sh
chronicle-mcp --help   # list commands; use help <command> for flags
chronicle-mcp serve    # foreground, suitable for a service manager
chronicle-mcp start    # detached; reuse only a compatible existing daemon
chronicle-mcp status   # inspect state without exposing credentials
chronicle-mcp stop    # graceful shared shutdown; refuses active clients/work
```

After explicit approval to disconnect everyone, `chronicle-mcp stop --force` stops
an active service. It never kills an unrelated listener by PID or port.

Other clients can connect to `http://127.0.0.1:17643/mcp` using stateful
Streamable HTTP and the private Authorization header provided by
`chronicle-mcp auth headers`. Configure a header helper where supported; otherwise
use the client's secure credential configuration. Do not paste credentials into
chat or commit them. MCP sessions share the service, not browser ownership.

The default idle exit is **six hours**. Any authenticated connected-agent
activity counts, including pings. Browser heartbeats, status polling, and
housekeeping never keep the service alive when no agents remain. Zero agents
means inactivity, not immediate exit. Use `--idle-timeout 0` to disable idle exit.
After exit, run setup/start again; an HTTP connection does not start the daemon.

See [daemon configuration, security, protocol, and lifecycle](docs/daemon.md)
for flags, session leases, state paths, and exact-origin browser configuration.
The service defaults to loopback only and separates browser credentials from
agent/control credentials. `--origin http://localhost:5173` permits a local Vite
deployment; production defaults allow HTTPS `chronicleclassic.com` and all its
subdomains, including `https://octo.chronicleclassic.com`.

## Local dashboard

With the daemon running, open **http://127.0.0.1:17643/**. The dashboard is
embedded in the same Go binary and uses the existing loopback listener. There is
no login, separate web server, or Node runtime requirement. For a custom port,
use `http://127.0.0.1:<port>/`.

The dashboard shows server version, uptime, idle shutdown timing, connected
browser tabs, owners, and pending work. It can revoke a tab's agent control or
request graceful shared shutdown, with confirmation before disruptive actions.
Revocation does not save, discard edits, disconnect the browser, or close tabs.

The dashboard deliberately grants control to local users/processes that can
reach the loopback port. It must not be exposed through a reverse proxy or port
forward. Browser-origin checks and security headers prevent other websites from
using dashboard controls; the existing MCP/browser endpoints still require their
own credentials. Dashboard responses never expose bearer tokens, raw MCP session
IDs, tool arguments, or results. Dashboard polling does not reset the six-hour
agent-inactivity timeout. After shutdown or idle exit, start the daemon through
the CLI or `/chronicle:setup`; a stopped daemon cannot serve its own dashboard.

Frontend availability checks can use credential-free `GET /healthz` on the same
`127.0.0.1` listener; see [frontend discovery](docs/daemon.md#frontend-discovery).

## Browser protocol compatibility

Multi-tab selection uses **browser protocol 3** and requires the matching
Chronicle frontend. Protocol 2 clients receive an upgrade error; the daemon
never falls back to routing calls to an arbitrary tab. See the
[multi-connection contract](docs/multi-connections.md) for ownership, revocation,
and stale-operation guarantees.

## Marketplace updates

The marketplace catalog is `.claude-plugin/marketplace.json`; the plugin version
comes from `.claude-plugin/plugin.json`. Maintainers must bump the plugin version
to distribute changed files to cached marketplace installs. Pushing commits alone
with the same version does not update those installs.

To update manually from a terminal:

```sh
claude plugin marketplace update chronicle-plugins
claude plugin update chronicle@chronicle-plugins
```

Reload plugins or start a new Claude Code session after updating. Third-party
marketplace auto-update is off by default. Users can opt in under **`/plugin` →
Marketplaces → chronicle-plugins → auto-update**; the catalog cannot enable it
for them.

**Plugin updates do not upgrade the running daemon.** If a plugin update changes
the pinned executable version, explicitly stop the old daemon with its matching
CLI before upgrading, then run setup and reconnect. With auto-update enabled,
you may need the retained old CLI if the plugin has already changed. Never
silently force shutdown or bypass release verification. See the
[upgrade procedure](docs/releases.md#upgrades).

## Release packaging

`scripts/release.tsv` pins v0.2.1 and GitHub-attested raw executables for
Linux/macOS amd64/arm64. The installer verifies provenance before execution,
rejects unsafe paths, and stores binaries outside the replaceable plugin
installation. See [release metadata and verification policy](scripts/README.md)
and the [release publishing procedure](docs/releases.md).
No hook or background auto-installer runs when loading the plugin.

## Development and validation

```sh
go test -race ./...
go vet ./...
npm ci
npm test
sh -n scripts/chronicle.sh
git diff --check
```

Go tests exercise the real HTTP MCP client, browser protocol, ownership,
cancellation, idle expiry, and detached CLI lifecycle. Node tests retain legacy
protocol regression coverage and exercise the installer with local fixture
artifacts; Node is a test dependency, not a Go daemon runtime dependency.

The [legacy Node relay documentation](docs/legacy-node-relay.md), `tools/`, Lua
starter, `project.json`, and `lua-types/` remain as references. They do not edit
the connected browser's working copy. No tray UI is
included yet.
