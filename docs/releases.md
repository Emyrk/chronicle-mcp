# Publishing executable releases

The release workflow publishes raw executables for Linux and macOS, each on
amd64 and arm64. Windows is not a supported installer/daemon lifecycle target.
No Go or Node runtime is required by the downloaded daemon.

## Trust and dependencies

GitHub Actions generates build-provenance attestations for each executable using
its OIDC identity and Sigstore infrastructure. Maintainers do not provision a
long-lived signing key. The installer uses GitHub CLI (`gh attestation verify`)
to verify the downloaded bytes against `Emyrk/chronicle-mcp`,
`.github/workflows/release.yml`, and the exact release tag, and refuses
self-hosted-runner provenance. HTTPS transport or a checksum downloaded beside
the executable is not a substitute for this check.

Install GitHub CLI through a trusted package manager or its official documented
installation route. Setup agents must explain its secure-download purpose and
ask the user before installing or upgrading it. Never download an unverified
verifier to verify itself. GitHub API access is required by this method; network,
authentication, rate-limit, or verification failures must stop installation,
not trigger a weaker fallback. Do not share tokens in chat.

## Release procedure

1. Merge the release changes into `main`, including passing CI. Set the daemon
   `internal/relay.Version`, `plugins/chronicle/.claude-plugin/plugin.json` version, and the
   installer's pinned release metadata consistently. Use a new stable version
   for each release; never reuse a published tag for different executable bytes.
2. Create and push a `vX.Y.Z` tag at that reviewed commit. This is an explicit
   maintainer action, not something setup performs.
3. The release workflow runs Linux/macOS regression checks, verifies the tag
   matches the source/plugin version and is on `main`, then cross-builds:
   - `chronicle-mcp-linux-amd64`
   - `chronicle-mcp-linux-arm64`
   - `chronicle-mcp-darwin-amd64`
   - `chronicle-mcp-darwin-arm64`
4. It attests and verifies all four executables before uploading a draft GitHub
   Release, then publishes the release. Existing releases/assets are not
   overwritten. If publication fails after draft creation, inspect and recover
   the draft explicitly; a rerun intentionally does not clobber it.
5. Verify setup from the published plugin/release on a clean machine. Until the
   tag workflow succeeds, the pinned release may not exist and setup will fail.

For a build-only check, run:

```sh
sh scripts/build-release.sh v0.2.3 /path/to/temporary-output
```

Use the version matching the source. The build script does not publish or sign
anything; locally built files do not acquire GitHub provenance.

Protect `main`, release tags, and workflow changes with repository rules and
review. Attestations establish which workflow built bytes, not whether that
workflow's source is safe. Enable GitHub immutable releases where available.
CI runs with read-only permissions; only the publish job receives release,
OIDC, and attestation write permissions. Third-party actions are SHA-pinned.

## Upgrades

This PR does not introduce automatic daemon replacement or a self-updater.
The plugin pins a version rather than installing mutable `latest`. Before
updating a plugin while its daemon is running, explicitly stop the daemon using
its current matching CLI (or the current plugin's teardown skill). Active
clients require separate approval for forced shutdown. Then update the plugin,
run setup, and reconnect browser tabs and agent clients. Preserve any nondefault
origin/state configuration when using the CLI directly.

The new CLI intentionally refuses incompatible running versions. If the plugin
was already updated, use the retained old matching binary to stop the old
service; do not kill an unknown listener or bypass verification. Browser working
copies remain in Chronicle, but pending calls are canceled on shutdown. Rollback
is an explicit stop-and-install of a prior trusted version, not automatic.
