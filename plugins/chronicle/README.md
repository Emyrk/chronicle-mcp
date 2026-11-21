# Chronicle for Claude Code

Connect Claude Code to a Chronicle module editor through an authenticated local
MCP daemon. The browser owns module source, execution, and save approval.

This directory is the installable plugin. The repository root holds the
marketplace catalog, daemon source, release workflow, and tests; those are not
part of this plugin directory. Licensed under MIT; see [LICENSE](LICENSE).

Use the Chronicle skills to set up the daemon, select a browser connection, and
tear down explicitly. The release-download method requires GitHub CLI (`gh`)
for attestation verification. Setup must ask permission before installing or
upgrading dependencies; it never silently replaces a running daemon.

See the [project documentation](https://github.com/Emyrk/chronicle-mcp#readme)
for installation, platform support, secure downloads, and upgrades.

## Directory eligibility

This is the functional local-relay plugin, not a restricted skills-only package.
It includes an installer script, an MCP credential helper, and a loopback MCP
endpoint. These require eligibility under the directory's executable, credential,
and endpoint policies; moving the plugin into this folder alone does not grant
that eligibility or imply directory approval.
