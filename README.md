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
stores the executable in `${CLAUDE_PLUGIN_DATA}/bin/<version>/chronicle-mcp`. It checks
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

Open Chronicle and click **Connect to local MCP**. Approve browser local-network
access if prompted. Ask the agent to call `whereami`, then fetch the live page
context and relevant skills. Only `whereami` exists before a browser connects.

`whereami` claims an unowned browser for that agent session. Automatic tool
listing does not claim ownership. Another agent can connect to the daemon, but
cannot operate an owned tab. Agent sessions expire after five minutes without
activity by default; clients should send MCP pings to retain their session.

Edits stay in the browser working copy. Saving requires browser approval and
never implicitly publishes a release. Stopping the daemon does not close the
browser or discard those edits. Tray and browser ownership UI are follow-ups.

## Headless service and other MCP clients

```sh
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

## Release packaging

`scripts/release.tsv` pins a release version and raw executable URLs/SHA256s for
Linux/macOS amd64/arm64. It deliberately remains `unreleased` until real binaries
are published. The installer verifies hashes before execution, rejects unsafe
paths, and stores binaries outside the replaceable plugin installation.
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
the connected browser's working copy. No tray UI or release publication is
included yet.
