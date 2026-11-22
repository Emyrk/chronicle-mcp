<p align="center">
  <a href="https://chronicleclassic.com/?utm_source=github&amp;utm_medium=referral&amp;utm_campaign=readme&amp;utm_id=chronicle_mcp&amp;utm_content=logo">
    <img src="https://raw.githubusercontent.com/Emyrk/chronicle/main/frontend/chronicle/public/c/chronicle/ChronicleLogoCenter.svg" alt="Chronicle" width="320" />
  </a>
</p>

<h3 align="center">Chronicle MCP — your agent, connected to your browser</h3>

Connect Claude Code or another MCP agent to a Chronicle browser tab. The local
relay forwards tools and results; module files, execution, previews, and save
approval stay in the browser.

## Quick start

**Requires:** Linux or macOS (amd64/arm64), Claude Code, `curl`, and
[GitHub CLI](https://cli.github.com/) with `gh attestation verify` support.
No Go or Node runtime is needed for release installs. Native Windows is unsupported.

1. Install from Chronicle's own marketplace:

   ```sh
   claude plugin marketplace add Emyrk/chronicle-mcp
   claude plugin install chronicle@chronicle-plugins
   ```

2. Run **`/chronicle:setup`** in Claude Code and approve setup. Then reconnect
   the server in `/mcp` or restart Claude Code. Setup verifies the pinned release;
   missing releases or failed verification block installation.
3. Open [Chronicle Modules](https://choose.chronicleclassic.com/modules), open a
   module editor, and click **Connect agent** (or **Connect to local MCP**).
   Allow browser local-network access if prompted.
4. Run **`/chronicle:connect`** and tell the agent which tab name or ID to use.
   It loads the selected page's tools and guidance before editing.

**You stay in control:** tabs are never selected automatically. Edits remain in
the browser working copy until you approve saving; saving does not publish a
release. Use the browser controls to revoke access or disconnect.

Run **`/chronicle:teardown`** to stop the shared daemon. This affects all connected
agents. Use only one Chronicle MCP installation at a time, and never share
credentials or private daemon state in chat.

## Dashboard and updates

With the daemon running, open **http://127.0.0.1:17643/** to inspect connections,
revoke control, or shut down. Keep this unauthenticated dashboard local; do not
expose it through a proxy or port forward.

Before updating, tear down with the current plugin, then run:

```sh
claude plugin marketplace update chronicle-plugins
claude plugin update chronicle@chronicle-plugins
```

Restart Claude Code and run setup again. Plugin updates do not replace a running
daemon. See the [upgrade procedure](docs/releases.md#upgrades) for version conflicts.

## Headless service and other MCP clients

Other clients use authenticated, stateful Streamable HTTP at
`http://127.0.0.1:17643/mcp`. See [client setup and daemon configuration](docs/daemon.md#agent-connection)
for authentication, CLI commands, origins, and lifecycle details.

## Development

Requires Go 1.25+. From this checkout:

```sh
mkdir -p .build
go build -o .build/chronicle-mcp ./cmd/chronicle-mcp
export CHRONICLE_DEV_BINARY="$PWD/.build/chronicle-mcp"
"$CHRONICLE_DEV_BINARY" start
claude --plugin-dir "$PWD/plugins/chronicle"
```

This explicitly uses an unverified local build instead of a downloaded release.
Connect your browser and run `/chronicle:connect` as above.

Checks (Node 22+ is only needed for tests):

```sh
go test -race ./...
go vet ./...
node --test test/*.test.mjs
git diff --check
```

The installable plugin lives in `plugins/chronicle/`; development files stay outside it.
Bump the plugin version when distributing changes to cached marketplace installs.
See [release publishing](docs/releases.md), [installer verification](plugins/chronicle/scripts/README.md),
and the [browser protocol](docs/multi-connections.md) for details.
