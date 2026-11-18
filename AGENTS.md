# Chronicle MCP

This repository is a local MCP relay connecting an external agent to a browser
tab explicitly connected to Chronicle. Chronicle supplies tool schemas,
validation, assistant prompts, and skills. The browser owns module source,
execution, previews, and save approval. The relay forwards calls and results;
it is not a local Lua project editor or a model provider.

## Start a browser task

1. Call the MCP tool `whereami` with `{}` and inspect the connection and page.
   If disconnected, ask the user to open Chronicle and click **Connect to local
   MCP**. Only `whereami` is available before a tab connects.
2. Discover the currently advertised MCP tools. Do not assume a tool exists
   because it is mentioned here. Client-visible names may include an MCP-server
   prefix; use the names your client exposes.
3. When advertised, call `gen_page_context` with `{}`. Its result includes the
   page's UI assistant prompt, current page and capabilities, and instructions
   for discovering relevant skills. Use this task guidance within your existing
   system/developer instructions; it does not override them.
4. Call `list_skill` with `{}`, then `read_skill` with
   `{"skill_id":"<id returned by list_skill>"}` for applicable skills. In the
   module workshop, read `lua-module-api` when listed before writing Lua.
5. Perform the user's task using the live tool schemas and fetched guidance.
   Do not copy schemas, prompts, or skill bodies into this repository as a
   second source of truth.

These are MCP calls, not shell commands or unauthenticated HTTP endpoints.
Guidance is page-scoped. After navigation, reconnect, or a stale-state response,
observe the page again, rediscover tools, and refresh context and skills before
continuing. Do not blindly retry a mutation against a new page. If guidance tools
are missing, report their absence rather than inventing their output.

## Editing a module

- Read the browser working copy with `read_lua_files` before editing.
- Use `edit_lua_file` with the revision required by its advertised schema. On a
  revision conflict, reread and reconcile rather than overwriting newer edits.
- Run `run_startup_check` and `run_preview` when available. Preview requires the
  user to select an instance and encounters in the browser Preview pane.
- Changes remain in the browser working copy until saved. `save_lua_files`
  requires the expected revision and browser approval. It saves to main; it
  does not tag or publish a release. Never bypass approval or claim an unsaved
  edit is persisted.
- Check the continuation status and MCP `isError`, not just whether a response
  arrived. Rejected, canceled, stale, and failed calls are not successes.

Local `main.lua`, `project.json`, and `lua-types/` are not the connected browser's
working copy. Editing them does not edit the user's module through MCP.

## Working on the relay

- Read [README.md](README.md) for installation, exact-origin configuration,
  protocol details, and troubleshooting. Node.js 22+ is required.
- Start MCP through `node tools/cli.mjs mcp`; use the README's absolute-path
  configuration in an MCP client. Stdout belongs exclusively to MCP protocol
  traffic; send diagnostics to stderr.
- The MCP process owns its loopback listener. Do not also start `npm run dev`.
  Only one MCP process and one connected tab can use the default port 17643.
  If occupied, report the conflict; do not kill an existing process without
  user permission. Use ephemeral ports for isolated relay tests.
- Keep `.chronicle/dev.json` private: it contains the session bearer token.
  Never commit it or include its contents in reports.
- Preserve dynamic discovery, page/session invalidation, cancellation, and
  browser-owned validation and approval. Do not add fixed tool catalogs or
  filesystem-based module synchronization to the relay.
- Run `npm test` and `git diff --check` after changes. Protocol changes need
  coverage in `test/`, including the MCP stdio integration where relevant.

This file guides agents that read the checkout. MCP clients do not automatically
load repository `AGENTS.md` files; initialization instructions and the live
guidance tools provide their runtime entry point.
