# Tool routing index

Generated from `internal/capabilities/capabilities.json` by `go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md`. Do not edit by hand.

## Workflow

1. `start_session` once, then follow the `next_step` it returns.
2. `get_guide(sections=[...])` with only the sections this task needs.
3. `create_new_diagram` / `load_diagram`, then `edit_diagram` for refinements.
4. `screenshot_diagram` to check the result, `export_diagram` to save a file.

Guide sections: core, xml, layout, edges, styles, editing, pages, export, libraries.

## Tools (routing index)

| Tool | Use when | Use instead |
|---|---|---|
| `add_page` | Append a new tab WITHOUT touching existing pages or unsaved user edits. Returns the new page id, name and index. | replacing the whole document -> create_new_diagram |
| `create_new_diagram` | Create a brand-new document from XML, REPLACING every page and any unsaved user edits. The previous state stays in History. | adding another tab to an existing diagram -> add_page; changing only a few cells -> edit_diagram; opening an existing .drawio file from disk -> load_diagram |
| `delete_page` | Delete an entire tab. Refuses to delete the last remaining page. | removing a single shape -> edit_diagram |
| `edit_diagram` | Add, update or delete individual cells of an existing page by cell id. Operations are all-or-nothing. | recreating the whole document -> create_new_diagram; adding a tab -> add_page; you only want to read the current state -> get_diagram |
| `export_diagram` | Save the current diagram to a .drawio, .png, .svg or .drawio.svg file. Format is detected from the file extension. | only previewing the result on screen -> screenshot_diagram |
| `get_diagram` | Read the current diagram XML, including the user's manual browser edits, when you need cell ids, page structure or an exact copy of the state. | you already know the exact ids you just wrote |
| `get_drawing_guide` | Legacy alias. Returns the complete drawing guide in one call, including sections you do not need. | only part of the guide is needed -> get_guide |
| `get_guide` | Load only the guide sections needed for the current task. Call after start_session and before generating any XML. | you need the whole guide verbatim in one call -> get_drawing_guide |
| `get_shape_library` | Get exact style names before drawing vendor icon shapes. Call with no library to list every available library. | the diagram uses only plain shapes |
| `list_pages` | List every page's id, name, 0-based index and cell count before targeting one with a page selector. | you need the cells inside a page -> get_diagram |
| `load_diagram` | Open a .drawio file from disk into the session, REPLACING the whole document. The server reads the file itself; handles plain XML and draw.io compressed format. | you already have the XML in context and want to redraw -> create_new_diagram; you only want to inspect the in-memory diagram -> get_diagram |
| `rename_page` | Change the visible tab label of an existing page. | changing the cells on a page -> edit_diagram |
| `screenshot_diagram` | Render the current diagram to a PNG so you can check overlapping shapes and edges crossing shapes after drawing or a heavy edit. | saving a file to disk -> export_diagram |
| `start_session` | Start a diagramming session and open the live browser preview. Call once before any other draw.io tool in a conversation. | a session is already active in this conversation |
