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

# draw-io-gomcp — Draw.io MCP Server Installer & User Guide (DSH)

`draw-io-gomcp` is a local, zero-dependency Draw.io (diagrams.net) MCP server written in Go. It exposes 14 MCP tools that let an AI agent generate, edit, preview, and export draw.io diagrams via natural language. This skill is the DSH-compatible bundle.

All processing is **local** — no cloud, no accounts, no API keys; diagrams never leave the machine. The server runs over **stdio** as a single static binary with no Node.js / Python / browser dependency.

## Step 1 — Install the server

Run the bundled installer:

```bash
bash scripts/install.sh
```

The script will:
- Detect OS (macOS / Linux / Windows) and architecture (amd64 / arm64).
- Fetch the latest release asset from the [GitHub releases page](https://github.com/aosfather/draw-io-gomcp/releases) (or build from source when Go is present and no prebuilt binary matches).
- Place the binary at `~/.local/bin/draw-io-gomcp`.
- Register it as an MCP server in DSH's config or fall back to other common clients.

## Step 2 — Use the tools

Once connected, ask in natural language:

- "Draw a sequence diagram of a login flow."
- "Create a class diagram for a blog system with User, Post, Comment."
- "Make an architecture diagram: frontend → API gateway → microservices → database."

Recommended workflow:
1. `start_session` once, then follow the `next_step` in its result.
2. `get_guide(sections=[...])` with only the sections this task needs.
3. Before vendor icons, call `get_shape_library` — never guess icon style names.
4. `create_new_diagram` or `load_diagram`, then `edit_diagram` for refinements.
5. `screenshot_diagram` to check the result, or `export_diagram` to save a file.
6. `list_pages` / `add_page` / `rename_page` / `delete_page` for multi-page docs.

See `references/routing-index.md` for the authoritative `when_to_use` / `when_not_to_use` table, and `references/drawio-tools.md` for the full tool list.

## DSH-specific install paths

This skill is discovered automatically when placed at either:
- Project scope: `<repo>/.dsh/skills/draw-io-gomcp/SKILL.md`
- User scope: `$DSH_HOME/skills/draw-io-gomcp/SKILL.md` (default `~/.dsh/skills/`)

It also works in the shared `.agents/skills/draw-io-gomcp/SKILL.md` root that dsh scans alongside other agents.

---

Source: https://github.com/aosfather/draw-io-gomcp (Go port of next-ai-draw-io v0.5, maintained independently).
