# Chronicle MCP

This repository is a local MCP relay connecting an external agent to a browser
tab explicitly connected to Chronicle. Chronicle supplies tool schemas,
validation, assistant prompts, and skills. The browser owns module source,
execution, previews, and save approval. The relay forwards calls and results;
it is not a local Lua project editor or a model provider.

## Start a browser task

1. Call `whereami` with `{}` to observe the selected connection; it never acquires
   a tab. If unselected, call `list_connections` and ask the user which connection
   to use unless they already identified its ID or unique name. Explicitly call
   `select_connection` with `connection_id`, then `whereami` again. Do not auto-select
   even a single tab. If no tabs exist, ask the user to click **Connect to local
   MCP** in Chronicle. Only the four relay connection tools exist before selection.
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

- Read [README.md](README.md) and [docs/daemon.md](docs/daemon.md) for the Go
  daemon, CLI, exact-origin configuration, and protocol. Go 1.25+ builds it;
  Node.js 22+ is only needed for legacy regression and installer tests.
- Start the shared service with `chronicle-mcp start` or foreground `chronicle-mcp serve`.
  Agents connect over authenticated HTTP at `http://127.0.0.1:17643/mcp`.
  `chronicle-mcp auth headers` is a private credential helper, not a chat command:
  never capture or display its output in reports.
- Do not also start the legacy Node relay on the same port. Reuse only a verified
  compatible daemon; report unrelated port conflicts and incompatible versions.
  Never silently kill or upgrade another service. Use ephemeral ports and
  temporary state directories in tests.
- Keep daemon `dev.json` in its per-user state directory private, along with
  legacy `.chronicle/dev.json`. They contain bearer credentials.
- Only `select_connection` claims browser ownership; `whereami` and tools/list
  are observation-only. Release the old connection before explicitly selecting
  another.
  If another agent owns it, report the conflict rather than taking over.
  Browser ownership UI belongs in the Chronicle frontend; tray support is a follow-up.
- Preserve dynamic discovery, page/session invalidation, cancellation, and
  browser-owned validation and approval. Do not add fixed tool catalogs or
  filesystem-based module synchronization to the relay.
- Run `go test -race ./...`, `go vet ./...`, `npm test`, and `git diff --check`
  after changes. Cover Go protocol/lifecycle changes in `internal/` tests;
  retain legacy Node and installer coverage in `test/`. Do not publish fabricated
  release artifacts or checksums: use an explicit local development binary until
  a real pinned release exists.

This file guides agents that read the checkout. MCP clients do not automatically
load repository `AGENTS.md` files; initialization instructions and the live
guidance tools provide their runtime entry point.
