# draw-io-gomcp [中文](README-CN.md)

A local, zero-dependency [Draw.io](https://www.draw.io/) (diagrams.net) MCP server written in Go.

`draw-io-gomcp` is a [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that exposes
draw.io diagramming capabilities to any MCP-compatible AI client. It is implemented in pure Go as a **local
CLI application** that communicates over the **stdio** transport, so it runs on **Windows, macOS, and Linux**
with no runtime dependencies beyond a single binary.

This project is a **Go port** of [next-ai-draw-io](https://github.com/next-ai/next-ai-draw-io), converted
from its **v0.5** release. It keeps the same clean tool surface but is rebuilt from the ground up in Go and
will **evolve independently** going forward.

## Why draw-io-gomcp?

| Feature | Benefit |
| --- | --- |
| 🖥️ **Runs locally** | No cloud, no servers, no accounts. Your diagrams never leave your machine. |
| 🔌 **Works with any MCP client** | Connect it to WorkBuddy, Claude, Cursor, or any other MCP-capable client. |
| 📦 **No extra environment** | Only one small CLI binary. No Node.js, no Python, no browser automation required. |
| ⚡ **Small & efficient** | A single static binary with a tiny footprint and fast startup. |
| 👁️ **Live preview & re-edit** | Built-in preview lets you view the rendered diagram in real time and edit it again. |

## Features

- **MCP stdio server** — speaks the MCP protocol over standard input/output, so it plugs into any client.
- **Diagram generation** — let your AI assistant create draw.io diagrams from natural language.
- **Live preview** — results can be previewed immediately, and you can refine the diagram with follow-up edits.
- **Cross-platform** — prebuilt binaries for Windows, macOS (Intel & Apple Silicon), and Linux.
- **Minimal configuration** — a tiny config is enough to get started; no complex setup.

## Installation

Download the prebuilt binary for your platform from the [Releases](../../releases) page, or build it yourself:

```bash
# Build from source (requires Go 1.22+)
git clone https://github.com/aosfather/draw-io-gomcp.git
cd draw-io-gomcp
go build -o draw-io-gomcp ./cmd/server

# Run
./draw-io-gomcp
```

## Configuration

`draw-io-gomcp` needs only a small amount of configuration to work. The recommended way is to register it
as an MCP server in your client's config file and point `command` at the binary.

### Example: WorkBuddy

Add the following entry to your WorkBuddy MCP configuration (`~/.workbuddy/mcp.json`):

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

> Replace `/absolute/path/to/draw-io-gomcp` with the actual path to the binary on your machine.

### Example: Other MCP clients (Claude Desktop, Cursor, etc.)

The same shape applies to any client that supports the `stdio` transport:

```json
{
  "mcpServers": {
    "draw-io": {
      "command": "/absolute/path/to/draw-io-gomcp"
    }
  }
}
```

After adding the configuration, **trust / enable** the server in your client, then start a new session.
The draw.io tools will appear automatically.

## Usage

Once connected, simply ask your AI assistant to draw something, for example:

- *"Draw a sequence diagram of a login flow."*
- *"Create a class diagram for a blog system."*
- *"Make an architecture diagram showing the frontend, API, and database."*

The assistant generates the diagram, and you can **preview it live** and ask for refinements
("make the database box red", "add an error path", etc.).

## Capability routing

Tool descriptions and all four MCP behaviour hints are generated from
`internal/capabilities/capabilities.json`, which also drives the registry
index and the bundled skill's routing table. Long-form drawing rules are split
into nine guide sections and loaded on demand with `get_guide(sections=[...])`;
`start_session` returns a `next_step` instead of injecting the whole guide.
`get_drawing_guide` remains as a legacy full-text alias.

```bash
go run ./cmd/capgen -check                                            # lint the catalogue
go run ./cmd/capgen -out build/capabilities.index.json                 # registry / router index
go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md
```

`go test ./...` fails if the generated routing table or the skill's frontmatter
description drifts from the catalogue, so regenerate both before committing.

Restart the MCP session after changing the catalogue: `tools/list` is fetched
once per connection and cached.

## Bundled skill package

`skills/draw-io-gomcp/` is a self-contained Agent Skill package (frontmatter,
installer and references) that lives in this repository. Releasing the server
no longer requires tracking down a separate copy of the docs. To publish, zip
that directory with `SKILL.md` at the archive root; the `PUBLISH.md` inside it
has the full checklist.

## How it works

```
┌──────────────┐   stdio (MCP)   ┌────────────────────┐   draw.io   ┌──────────────┐
│ AI Client    │ ───────────────▶│ draw-io-gomcp     │ ───────────▶│ Diagram /    │
│ (WorkBuddy)  │ ◀───────────────│ (Go, local CLI)    │ ◀───────────│ Preview      │
└──────────────┘   JSON-RPC      └────────────────────┘   .drawio    └──────────────┘
```

1. The AI client sends a tool call over the MCP stdio transport.
2. `draw-io-gomcp` processes the request locally and produces the draw.io diagram source.
3. The result is returned to the client and rendered in the live preview for viewing and re-editing.

## Porting & Roadmap

This is a Go port of [`next-ai-draw-io`](https://github.com/next-ai/next-ai-draw-io), converted from its
**v0.5** release. The Go version shares the same tool surface but is maintained separately and will
**diverge and evolve independently** with improvements such as:

- Richer diagram types and templates
- A better preview / editing experience
- Additional MCP capabilities and tooling
