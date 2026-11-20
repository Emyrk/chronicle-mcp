# Release installer metadata

`chronicle.sh` supports POSIX Linux/macOS on amd64/arm64. It installs raw executable
artifacts, not archives (no extraction or archive path traversal). Native Windows
installation is intentionally unsupported; use the Go CLI manually.

`release.tsv` is bundled trust metadata reviewed and shipped with the plugin.
The current `unreleased unreleased` sentinel deliberately prevents a download.
To prepare an actual release, replace it with:

- First line: `<pinned-version> released`
- Following lines: `<os>-<arch> <https-artifact-url> <lowercase-sha256>`

Fields are whitespace-separated. Supported platforms are `linux-amd64`,
`linux-arm64`, `darwin-amd64`, and `darwin-arm64`. Include exactly one row for
each supported published artifact. Compute SHA256 from the reviewed build output
and commit the hashes with the plugin; never trust adjacent downloaded checksum
files or a mutable `latest` endpoint. Do not mark unreleased builds released or
insert placeholder digests. There is no release publishing automation here.

Changing plugin metadata selects a new versioned install directory, but does not
permit replacing a running daemon. The CLI owns compatible reuse and refusal of
incompatible listeners. The installer never calls stop during setup, nor deletes
or replaces an existing binary whose hash mismatches. An interrupted install
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
the installed executable's hash is verified again before use.
