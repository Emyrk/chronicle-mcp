---
name: teardown
description: Stops the shared Chronicle daemon gracefully. Use when the user explicitly requests Chronicle shutdown or plugin teardown.
disable-model-invocation: true
---

# Stop Chronicle

This stops a shared per-user daemon, not just this Claude session. Warn that
other agents and browser work may be interrupted.

Resolve the plugin root and the same persistent data directory used during setup
from Claude's plugin context; do not assume plugin variables are exported into
the shell. If the data path is unavailable, ask the user to confirm it rather
than guessing from directory names. Pass it explicitly to every command,
including `stop --force`. Substitute the resolved absolute paths into the
placeholders below; do not execute the placeholders literally.

With normal command approval, inspect status first:

```sh
CLAUDE_PLUGIN_DATA="/resolved/plugin/data" sh "/resolved/plugin/root/scripts/chronicle.sh" status
```

Ask for explicit shutdown confirmation after explaining the impact. Then run:

```sh
CLAUDE_PLUGIN_DATA="/resolved/plugin/data" sh "/resolved/plugin/root/scripts/chronicle.sh" stop
```

If stop refuses because clients or work are active, report that refusal and ask
for renewed explicit confirmation to interrupt all shared work. Only after that
confirmation, use `stop --force` through the same script. A connected MCP client
can itself count as active. Never kill a PID, use a broad process-name kill, or
silently retry with force. Report the CLI result; do not claim a failed stop
succeeded. Keep installed binaries and private state management with the daemon;
teardown does not uninstall the plugin or delete browser source.
