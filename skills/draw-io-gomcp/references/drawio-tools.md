# draw-io-gomcp — Tool Reference

`draw-io-gomcp` exposes **14 MCP tools** over stdio. Tool descriptions and all four behaviour hints are generated from `internal/capabilities/capabilities.json`; this file is a human-readable mirror. The agent discovers the tools automatically once the server is connected.

## Tools

| # | Tool | Purpose |
|---|------|---------|
| 1 | `start_session` | Starts a session, opens live preview, returns `next_step`. |
| 2 | `get_guide` | Loads **only** the requested guide sections. The progressive-disclosure entry point. |
| 3 | `get_drawing_guide` | Legacy alias: returns every guide section at once. Prefer `get_guide`. |
| 4 | `get_shape_library` | Exact style names for one icon library. No argument returns the full catalogue. |
| 5 | `create_new_diagram` | Creates a new document, replacing all pages (destructive). |
| 6 | `load_diagram` | Loads a `.drawio` file from disk, replacing all pages (destructive). |
| 7 | `edit_diagram` | Add / update / delete mxCells by id. All-or-nothing. |
| 8 | `get_diagram` | Current mxfile XML, including the user's manual browser edits. |
| 9 | `screenshot_diagram` | Renders to PNG for visual checking (not for saving files). |
| 10 | `export_diagram` | Saves to `.drawio` / `.png` / `.svg` / `.drawio.svg`. |
| 11 | `list_pages` | Lists page ids, names, indices and cell counts. |
| 12 | `add_page` | Adds a tab without touching existing pages. |
| 13 | `rename_page` | Renames a page (by id / name / index). |
| 14 | `delete_page` | Deletes a page (destructive; refuses the last page). |

## Recommended agent workflow

1. `start_session` once, then follow the `next_step` in its result.
2. `get_guide(sections=[...])` with only the sections this task needs. Do NOT
   call `get_drawing_guide` by default.
3. Before vendor icons, `get_shape_library` (no argument lists all libraries).
4. `create_new_diagram` or `load_diagram`.
5. `edit_diagram` with structured operations for refinements.
6. `screenshot_diagram` to check the result, or `export_diagram` to save.
7. `list_pages` / `add_page` / `rename_page` / `delete_page` for multi-page docs.

Guide sections: `core`, `xml`, `layout`, `edges`, `styles`, `editing`, `pages`,
`export`, `libraries`. Typical loads:

| Task | Sections |
|---|---|
| New simple flowchart | `core`, `xml`, `layout` |
| Diagram with connectors | + `edges` |
| Minimal / black-and-white | + `styles` |
| AWS / Kubernetes icons | + `libraries`, then `get_shape_library` |
| Editing an existing file | `editing`, `pages` |
| Export or multi-page work | `export`, `pages` |

`edit_diagram` errors are contracts, not prose: `code: stale_state` returns
`current_xml`, so rebuild operations from it and retry; `code: unseen_state`
means call `get_diagram` first.

## Prompting patterns (what to say to the agent)

- "Draw a sequence diagram of a login flow with success and error paths."
- "Create a class diagram for a blog: User, Post, Comment, with relationships."
- "Make an architecture diagram: browser → CDN → API gateway → auth service → Postgres."
- "Show an AWS architecture using the AWS shape library: Lambda, API Gateway, DynamoDB."
- "Load ~/diagrams/sys.drawio and add a caching layer (Redis) between API and DB."
- "Export the current diagram as PNG to ~/exports/arch.png."
- "Add a second page titled 'Data Model' with an ER diagram."

## Notes

- Every multi-page-aware tool accepts consistent targeting fields: `page_id`, `page_name`, or `page_index` (0-based). `page_id` wins when several are set.
- `edit_diagram` operations use the shape `{ "operation": "add|update|delete", "cell_id": "...", "new_xml": "<mxCell .../>" }`.
- Diagrams are rendered locally; the preview opens in the system browser via `start_session`.
- `references/routing-index.md` is the machine-generated `when_to_use` / `when_not_to_use` table — the authoritative routing reference, regenerated with `go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md`.
- Routing metadata (`when_to_use` / `when_not_to_use` / tier / annotations) lives in `internal/capabilities/capabilities.json`. Run `go run ./cmd/capgen -check` to lint it, or `-out build/capabilities.index.json` to export a registry index. Restart the MCP session after any change: `tools/list` is fetched once and cached.
