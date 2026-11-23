# Release installer metadata

`chronicle.sh` supports POSIX Linux/macOS on amd64/arm64. It installs raw executable
artifacts, not archives (no extraction or archive path traversal). Native Windows
installation is intentionally unsupported; use the Go CLI manually.

`release.tsv` pins `v0.2.4 attested`, not a mutable latest release. This selects
raw assets named `chronicle-mcp-<os>-<arch>` from
`https://github.com/Emyrk/chronicle-mcp/releases/download/v0.2.4/` for
`linux-amd64`, `linux-arm64`, `darwin-amd64`, and `darwin-arm64`.
The pin is not evidence that the release exists: downloads fail closed until the
tag's release workflow publishes the artifacts and their attestations.

Before execution, `gh attestation verify` authenticates the artifact against:

- `--repo Emyrk/chronicle-mcp`
- `--signer-workflow Emyrk/chronicle-mcp/.github/workflows/release.yml`
- `--source-ref refs/tags/v0.2.4` (derived from the pinned version)
- `--deny-self-hosted-runners`

The release workflow uses `actions/attest-build-provenance` on raw executables
built on GitHub-hosted runners. The installer verifies the temporary download
before atomic installation, and verifies the installed file before every
invocation, including credential helpers. This requires network access and may
require GitHub CLI authentication. If authentication is needed, have the user
complete the normal `gh auth login` flow privately; never request or expose tokens
in chat. Network or authentication failures fail closed. Verification
failure (including unsupported verifier flags) blocks execution, not a fallback
to adjacent checksums or unauthenticated bytes. Verifier diagnostics go to stderr.

Legacy `<version> released` metadata with platform URL/SHA256 rows remains only
for bundled-hash regression fixtures. Production metadata uses attested mode.

Changing plugin metadata selects a new versioned install directory, but does not
permit replacing a running daemon. The CLI owns compatible reuse and refusal of
incompatible listeners. The installer never calls stop during setup, nor deletes
or replaces an existing binary whose verification fails. An interrupted install
cleans its temporary download and lock where possible; inspect a leftover lock
manually after an uncatchable process termination.

`CLAUDE_PLUGIN_DATA` must be an absolute private path with no traversal or symlink
components. `CHRONICLE_DEV_BINARY` explicitly chooses an absolute local executable
and bypasses artifact verification (with a stderr warning). It is intended only
for user-approved development. `CHRONICLE_TEST_ALLOW_HTTP=1` permits loopback HTTP
fixture downloads; production metadata must contain only HTTPS URLs. Tests copy
the script and supply their own metadata in a temporary directory, never rewrite
production metadata, and never start or stop the user's service.

`start`, `status`, and `stop [--force]` forward to the CLI. `headers` forwards to
`auth headers`, which prints secret JSON for MCP consumption, not chat. No command
except `start` downloads an executable. The helper runs on each MCP connection;
the installed executable's attestation is verified again before use.

## GitHub-attested download dependency

Release installation requires GitHub CLI (`gh`) with `gh attestation verify`
support, including all flags above. The installer never installs or upgrades it.
If missing or unsupported, the calling agent must ask the user for consent to
install or upgrade through a trusted package manager or https://cli.github.com/.
Explain the security purpose, proposed command, and any required privileges.
Refusal blocks release setup. Approved development binaries need no verifier.

## Before upgrading the plugin

While the old plugin/CLI is still available, use `/chronicle:teardown` or that
old CLI's `status` and `stop` with the same state directory. Active work requires
renewed explicit confirmation for `stop --force`. Then upgrade the plugin and
run setup. A new CLI cannot stop an incompatible old daemon. If already upgraded,
use the retained old executable explicitly, not the new plugin's stop helper.
There is no upgrade engine or automatic shutdown in this installer.
