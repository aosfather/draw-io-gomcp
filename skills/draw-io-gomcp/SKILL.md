---
name: draw-io-gomcp
description: >-
  Installs and configures the draw-io-gomcp MCP server, and teaches the agent
  to generate, edit and preview Draw.io (.drawio / diagrams.net) diagrams with
  a live preview. Use when the user asks to draw a flowchart, sequence, class,
  ER, architecture, network, UML or any other diagram, or wants to create,
  open, edit or export a .drawio file. Not for Mermaid-only or ASCII-only
  output, nor for raster image generation.
---

# draw-io-gomcp — Draw.io MCP Server Installer & User Guide

`draw-io-gomcp` is a local, zero-dependency Draw.io (diagrams.net) MCP server written in Go. It exposes 14 MCP tools that let an AI agent generate, edit, preview, and export draw.io diagrams via natural language. Tool descriptions and behaviour hints are generated from one catalogue (`internal/capabilities/capabilities.json`); drawing rules are loaded on demand with `get_guide` instead of being injected at session start. This skill installs the server into the user's MCP client and teaches the agent how to drive it.

All processing is **local** — no cloud, no accounts, no API keys; diagrams never leave the machine. The server runs over **stdio** as a single static binary with no Node.js / Python / browser dependency.

## What this skill does

1. **Installs** the `draw-io-gomcp` binary (downloads the right build for the OS/architecture, or builds from source if Go is available).
2. **Registers** it as an MCP server in the active client's config (`~/.workbuddy/mcp.json`, Claude Desktop, Cursor, etc.).
3. **Verifies** the connection so the agent can start drawing.

## Step 1 — Install the server

Run the bundled installer:

```bash
bash scripts/install.sh
```

The script will:
- Detect OS (macOS / Linux / Windows) and architecture (amd64 / arm64).
- Fetch the latest release asset from the [GitHub releases page](https://github.com/aosfather/draw-io-gomcp/releases) (or build from source when Go is present and no prebuilt binary matches).
- Place the binary at `~/.local/bin/draw-io-gomcp`.
- Detect the MCP client and append the `draw-io` server entry to its config.
- Print the exact config block it wrote and tell the user to trust/enable the server.

**Prebuilt binaries** are named `draw-io-gomcp-{version}-{os}-{arch}.tar.gz` / `.zip` — e.g.
`draw-io-gomcp-0.5.0-darwin-arm64.tar.gz`. All binaries are stripped (`-s -w`) and
static (`CGO_ENABLED=0`), no runtime dependencies.

Manual download: go to [https://github.com/aosfather/draw-io-gomcp/releases](https://github.com/aosfather/draw-io-gomcp/releases), pick the asset matching your OS/arch, extract it, and place the `draw-io-gomcp` binary at `~/.local/bin/`.

If you prefer a manual build from source:

```bash
git clone https://github.com/aosfather/draw-io-gomcp.git
cd draw-io-gomcp
bash build.sh            # build all platforms into dist/
# or
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o draw-io-gomcp ./cmd/server
```

Then add to `~/.workbuddy/mcp.json`:

```json
{
  "mcpServers": {
    "draw-io": {
      "command": "/absolute/path/to/draw-io-gomcp",
      "args": [],
      "env": {}
    }
  }
}
```

> After install, **trust/enable** the server in the client and start a new session. The 14 draw.io tools will appear automatically. A stdio server is read once at connect time, so restarting the session is required after any description change.

## Step 2 — Use the tools

Once connected, simply ask in natural language. See `references/routing-index.md` for the authoritative `when_to_use` / `when_not_to_use` table, and `references/drawio-tools.md` for the full tool list and prompting patterns. Quick examples:

- "Draw a sequence diagram of a login flow."
- "Create a class diagram for a blog system with User, Post, Comment."
- "Make an architecture diagram: frontend → API gateway → microservices → database."
- "Load my existing diagram at ~/diagrams/sys.drawio and add an error path."

Recommended workflow the agent should follow:
1. `start_session` once, then follow the `next_step` in its result.
2. `get_guide(sections=[...])` with only the sections this task needs:
   `core`, `xml`, `layout`, `edges`, `styles`, `editing`, `pages`, `export`, `libraries`.
   Do NOT call `get_drawing_guide` by default — it returns every section at once.
3. Before vendor icons, call `get_shape_library` (with no argument to list all
   libraries). Never guess icon style names.
4. `create_new_diagram` or `load_diagram`, then `edit_diagram` for refinements.
5. `screenshot_diagram` to check the result, or `export_diagram` to save a file.
6. `list_pages` / `add_page` / `rename_page` / `delete_page` for multi-page docs.

Error contract: if `edit_diagram` fails with `code: stale_state`, rebuild the
operations from the returned `current_xml` and retry — no extra guide call and
no `get_diagram` are needed. `code: unseen_state` means call `get_diagram` first.

## Keeping this package in sync

This package is bundled in the server repository at `skills/draw-io-gomcp/`, so
it is versioned and released together with the binary. The catalogue
`internal/capabilities/capabilities.json` is the single source of truth. From
the repository root:

```bash
go run ./cmd/capgen -check                                            # lint the catalogue
go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md
go test ./...                                                         # fails on drift
```

That test also compares the `description` frontmatter above with the catalogue,
so do not edit the frontmatter without updating `cmd/capgen/main.go`.
See `PUBLISH.md` for the packaging / release checklist.

## Notes

- When developing inside the server repository, build locally
  (`go build -o ~/.local/bin/draw-io-gomcp ./cmd/server`) instead of running
  `scripts/install.sh`, which fetches the latest published release.
- This skill packages the **installer and usage guide** for `draw-io-gomcp`. The server binary itself is fetched at install time, not bundled, so the skill stays small and always ships the latest stable build.
- Source: https://github.com/aosfather/draw-io-gomcp (Go port of next-ai-draw-io v0.5, maintained independently).
