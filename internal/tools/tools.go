// The 13 MCP tools and the diagram-workflow prompt, ported from index.ts.
// Every handler returns (*toolResult, error); the mapping to
// *mcp.CallToolResult lives in toMCPResult.
package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aosfather.drawio-mcp/internal/bridge"
	"aosfather.drawio-mcp/internal/diagram"
	"aosfather.drawio-mcp/internal/mcplog"
	"aosfather.drawio-mcp/internal/store"
	"aosfather.drawio-mcp/internal/xmlutil"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// toolResult is the tool layer's response before conversion to MCP types.
type toolResult struct {
	text      string
	image     bool
	imageData string
	isError   bool
}

func textResult(t string) *toolResult { return &toolResult{text: t} }

func errorResult(t string) *toolResult { return &toolResult{text: t, isError: true} }

// imageResult keeps the TS content order: the image first, then the text.
func imageResult(text, base64Data string) *toolResult {
	return &toolResult{text: text, image: true, imageData: base64Data}
}

func (r *toolResult) toMCPResult() *mcp.CallToolResult {
	if r.image {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewImageContent(r.imageData, "image/png"),
				mcp.NewTextContent(r.text),
			},
			IsError: r.isError,
		}
	}
	if r.isError {
		return mcp.NewToolResultError(r.text)
	}
	return mcp.NewToolResultText(r.text)
}

// Deps carries the process-wide collaborators of the tools.
type Deps struct {
	Autosaver *store.Autosaver
	Version   string
}

// toolHandler is the uniform handler signature before registration.
type toolHandler func(ctx context.Context, args map[string]any) (*toolResult, error)

// pageSelectorSchema mirrors the shared Zod fragment: every multi-page-aware
// tool reuses these three optional fields so the LLM learns one consistent
// interface.
func pageSelectorSchema() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString("page_id",
			mcp.Description("Target a page by its id (as returned by list_pages or add_page). Wins over page_name and page_index when multiple are set.")),
		mcp.WithString("page_name",
			mcp.Description(`Target a page by its display name (e.g. "CNN"). Used only when page_id is not set.`)),
		mcp.WithNumber("page_index",
			mcp.Description("Target a page by its 0-based tab index. Used only when page_id and page_name are not set.")),
	}
}

// operationsItemSchema is the JSON Schema of one operation.
var operationsItemSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"operation": map[string]any{
			"type":        "string",
			"enum":        []string{"update", "add", "delete"},
			"description": "Operation to perform: add, update, or delete",
		},
		"cell_id": map[string]any{
			"type":        "string",
			"description": "The id of the mxCell. Must match the id attribute in new_xml.",
		},
		"new_xml": map[string]any{
			"type":        "string",
			"description": "Complete mxCell XML element (required for update/add)",
		},
	},
	"required": []string{"operation", "cell_id"},
}

// parseOperations converts the raw JSON array into diagram operations.
func parseOperations(args map[string]any) []diagram.Operation {
	raw, _ := args["operations"].([]any)
	ops := make([]diagram.Operation, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		opType, _ := m["operation"].(string)
		cellID, _ := m["cell_id"].(string)
		newXML, _ := m["new_xml"].(string)
		ops = append(ops, diagram.Operation{Operation: opType, CellID: cellID, NewXML: newXML})
	}
	return ops
}

// mutationLoad is the result of loadMxfileForMutation.
type mutationLoad struct {
	ok      bool
	message string
	doc     *xmlutil.Node
	sess    *diagramSession
	// The browser state this mutation is built on ("" when the session had
	// no state).
	browserXML string
	browserSVG string
}

func failMutation(message string) mutationLoad {
	return mutationLoad{ok: false, message: message}
}

// writeBack persists the mutated document: history, state store, and the
// model's seen-state marker.
func (m mutationLoad) writeBack(newDoc *xmlutil.Node) {
	newXML := diagram.SerializeMxfile(newDoc)
	// The store may hold user edits the model has not seen yet.
	sawLatest := diagram.CheckEditGate(m.sess.LastSeenXML(), m.browserXML).OK
	// Save history before overwriting so the user can undo.
	bridge.KeepInHistory(m.sess.ID(), m.sess.XML(), m.browserSVG)
	m.sess.SetXML(newXML)
	m.sess.IncVersion()
	bridge.SetState(m.sess.ID(), newXML, "", false, true)
	// The model just wrote this exact state. If it had seen the state it
	// built on, mark the result as seen so edit_diagram needs no extra
	// get_diagram; otherwise edit_diagram must ask for one.
	if sawLatest {
		m.sess.SetLastSeenXML(newXML)
	} else {
		m.sess.SetLastSeenXML("")
	}
	store.AddHistory(m.sess.ID(), newXML, "")
}

// loadMxfileForMutation loads the latest session XML, normalises to mxfile
// if needed, and returns a parsed document the caller can mutate, plus the
// writeBack persister.
func loadMxfileForMutation() mutationLoad {
	sess := currentSessionSnapshot()
	if sess == nil {
		return failMutation("No active session. Please call start_session first.")
	}
	// Pull latest from browser so we don't clobber autosaved changes.
	browserXML, browserSVG, hasState := sessionState(sess.ID())
	if hasState && browserXML != "" {
		sess.SetXML(browserXML)
	}
	if sess.XML() == "" {
		return failMutation("No diagram exists yet. Use create_new_diagram first, then page tools.")
	}
	// Make sure the in-memory shape is canonical mxfile before any CRUD.
	normalized := diagram.NormalizeToMxfile(sess.XML(), "", "")
	if normalized == "" {
		return failMutation("Current session XML is neither <mxGraphModel> nor <mxfile>; cannot perform page operations.")
	}
	sess.SetXML(normalized)

	doc := diagram.ParseMxfile(normalized)
	if doc == nil {
		return failMutation("Failed to parse current session XML as <mxfile>.")
	}
	return mutationLoad{ok: true, doc: doc, sess: sess, browserXML: browserXML, browserSVG: browserSVG}
}

// Register wires every tool and the prompt into the MCP server.
func Register(s *server.MCPServer, deps Deps) {
	register := func(name string, opts []mcp.ToolOption, h toolHandler) {
		s.AddTool(mcp.NewTool(name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res, err := h(ctx, req.GetArguments())
			if err != nil {
				return nil, err
			}
			return res.toMCPResult(), nil
		})
	}
	registerWrite := func(name string, opts []mcp.ToolOption, h toolHandler) {
		register(name, opts, writeQueue.wrap(h))
	}

	// The same guide as start_session, for hosts that show prompts to the user
	s.AddPrompt(
		mcp.NewPrompt("diagram-workflow",
			mcp.WithPromptDescription("Guidelines for creating and editing draw.io diagrams")),
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return mcp.NewGetPromptResult(
				"",
				[]mcp.PromptMessage{mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(DrawingGuide))},
			), nil
		},
	)

	// Tool: get_drawing_guide
	register("get_drawing_guide", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Get drawing guide"),
		mcp.WithDescription("Return the drawing guide: XML format, layout, edge routing, style and editing rules. "+
			"start_session already returns it; call this only if the guide is no longer in your context."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		return textResult(DrawingGuide), nil
	})

	// Tool: get_shape_library
	register("get_shape_library", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Get shape library"),
		mcp.WithDescription("Get the style syntax and shape names of a draw.io icon library. Call this BEFORE drawing with "+
			"cloud, network or other icon shapes, and use the exact names it returns; never guess them.\n\n"+
			"Libraries:\n"+ShapeLibraryList),
		mcp.WithString("library",
			mcp.Required(),
			mcp.Description("Library name, e.g. aws4, kubernetes, flowchart")),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		library, _ := args["library"].(string)
		found, err := GetShapeLibrary(library)
		if err != nil {
			return errorResult("Error: " + err.Error()), nil
		}
		return textResult(found), nil
	})

	// Tool: start_session
	registerWrite("start_session", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Start session"),
		mcp.WithDescription("Start a new diagram session and open the browser for real-time preview. " +
			"Starts an embedded server and opens a browser window with draw.io. " +
			"The browser will show diagram updates as they happen. " +
			"The result includes the drawing guide; follow it when drawing."),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		port, err := bridge.StartHTTPServer(ServerPort)
		if err != nil {
			mcplog.Error("start_session failed: %v", err)
			return errorResult("Error: " + err.Error()), nil
		}

		// Create session
		sessionID := generateSessionID()
		setCurrentSession(&diagramSession{id: sessionID})

		// Open browser
		browserURL := fmt.Sprintf("http://localhost:%d?mcp=%s", port, sessionID)
		if err := openBrowser(browserURL); err != nil {
			mcplog.Warn("Could not open the browser: %v", err)
		}

		savePath := deps.Autosaver.PathFor(sessionID)
		saveNote := ""
		if savePath != "" {
			saveNote = fmt.Sprintf("\n\nAuto-save: after every change the diagram is saved to %s. To continue it in a later conversation, call start_session, then load_diagram with this path.", savePath)
		}

		mcplog.Info("Started session %s, browser at %s", sessionID, browserURL)

		return textResult(fmt.Sprintf(
			"Session started successfully!\n\nSession ID: %s\nBrowser URL: %s\n\nThe browser will now show real-time diagram updates.%s\n\n%s",
			sessionID, browserURL, saveNote, DrawingGuide)), nil
	})

	// Tool: create_new_diagram
	registerWrite("create_new_diagram", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Create new diagram"),
		mcp.WithDescription(`Create a NEW diagram, REPLACING the whole document: every page and any unsaved user changes (the previous state stays in History). To add a tab use add_page; to change cells use edit_diagram.

Before using icon shapes (AWS, Azure, GCP, Kubernetes, Cisco...), call get_shape_library first. Follow the drawing guide returned by start_session (call get_drawing_guide if it is no longer in your context).

Accepted xml:
1) Only the mxCell elements of one page (recommended). The server adds <mxfile>, <mxGraphModel>, <root> and the root cells "0" and "1":
<mxCell id="2" value="Shape" style="rounded=1;whiteSpace=wrap;html=1;" vertex="1" parent="1"><mxGeometry x="40" y="40" width="120" height="60" as="geometry"/></mxCell>
2) A bare <mxGraphModel> with <root> (one page).
3) A full <mxfile> with one or more <diagram> pages. Every page's <root> must start with <mxCell id="0"/><mxCell id="1" parent="0"/>.

Rules: cells are siblings (never nested), ids are unique per page and start from "2", parent="1" for top-level shapes, no XML comments, and shapes stay within x 0 to 800 and y 0 to 600.`),
		mcp.WithString("xml",
			mcp.Required(),
			mcp.Description("REQUIRED: the mxCell elements of one page, a bare <mxGraphModel>, or a full <mxfile> with one or more <diagram> pages.")),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}
		inputXML, _ := args["xml"].(string)

		prepared := diagram.PrepareNewDiagram(inputXML, "", "")
		if !prepared.OK {
			mcplog.Error("%s", prepared.Error)
			return errorResult("Error: " + prepared.Error), nil
		}
		if len(prepared.Fixes) > 0 {
			mcplog.Info("XML auto-fixed: %s", strings.Join(prepared.Fixes, ", "))
		}
		// Every later tool can assume session.xml is an mxfile
		xml := prepared.XML

		mcplog.Info("Setting diagram content, %d chars", len(xml))

		// Sync from browser state first
		browserXML, browserSVG, hasState := sessionState(sess.ID())
		if hasState && browserXML != "" {
			sess.SetXML(browserXML)
		}

		// Save user's state before AI overwrites (with cached SVG)
		if sess.XML() != "" {
			bridge.KeepInHistory(sess.ID(), sess.XML(), browserSVG)
		}

		// Update session state
		sess.SetXML(xml)
		sess.IncVersion()

		// Push to embedded server state. The model just authored this
		// exact XML, so record it as seen — edit_diagram may follow
		// without a redundant get_diagram round-trip.
		bridge.SetState(sess.ID(), xml, "", false, true)
		sess.SetLastSeenXML(xml)

		// Save AI result (no SVG yet - will be captured by browser)
		store.AddHistory(sess.ID(), xml, "")

		// Report page count back to the caller so the LLM learns whether
		// multi-page worked or fell back to single.
		var pageSummary string
		doc := diagram.ParseMxfile(xml)
		if doc == nil {
			pageSummary = "no pages parsed"
		} else {
			pages := diagram.ListPagesFromDoc(doc)
			switch {
			case len(pages) > 1:
				parts := make([]string, len(pages))
				for i, p := range pages {
					parts[i] = fmt.Sprintf("%d:%s", p.Index, p.Name)
				}
				pageSummary = fmt.Sprintf("%d pages: %s", len(pages), strings.Join(parts, ", "))
			case len(pages) == 1:
				pageSummary = fmt.Sprintf("1 page: %s", pages[0].Name)
			default:
				pageSummary = "no pages parsed"
			}
		}

		mcplog.Info("Diagram content set successfully (%s)", pageSummary)

		return textResult(fmt.Sprintf(
			"Diagram content set successfully!\n\nThe diagram is now visible in your browser.\n\nXML length: %d characters\n%s",
			len(xml), pageSummary)), nil
	})

	// Tool: load_diagram
	registerWrite("load_diagram", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Load .drawio file"),
		mcp.WithDescription("Load a .drawio file from disk into the current session, REPLACING the entire diagram (all pages). " +
			"The server reads the file directly — you do NOT need to read the file yourself or pass its XML through create_new_diagram. " +
			"Handles both plain-XML and draw.io's compressed save format.\n\n" +
			"After loading, call get_diagram before edit_diagram — you haven't seen the file's cell IDs or structure yet."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Absolute path to the .drawio file to load (e.g. /Users/me/diagram.drawio or ~/diagram.drawio). Relative paths resolve against the MCP server's working directory, which is often not your project.")),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}
		rawPath, _ := args["path"].(string)
		absolutePath, err := filepath.Abs(store.ExpandHome(rawPath))
		if err != nil {
			absolutePath = rawPath
		}

		var content []byte
		if info, statErr := os.Stat(absolutePath); statErr != nil || !info.Mode().IsRegular() {
			// A pipe or device could be read forever, and the other write
			// tools wait for this one
			msg := "not a regular file"
			if statErr != nil {
				msg = statErr.Error()
			}
			return errorResult(fmt.Sprintf("Error: Cannot read file %s: %s", absolutePath, msg)), nil
		}
		content, err = os.ReadFile(absolutePath)
		if err != nil {
			return errorResult(fmt.Sprintf("Error: Cannot read file %s: %v", absolutePath, err)), nil
		}

		loaded := diagram.ParseDrawioFileContent(string(content))
		if !loaded.OK {
			return errorResult("Error: " + loaded.Error), nil
		}
		xml := loaded.XML

		mcplog.Info("Loading diagram from %s (%d chars)", absolutePath, len(xml))

		// Save the user's current state before replacing (same flow as
		// create_new_diagram).
		browserXML, browserSVG, hasState := sessionState(sess.ID())
		if hasState && browserXML != "" {
			sess.SetXML(browserXML)
		}
		if sess.XML() != "" {
			bridge.KeepInHistory(sess.ID(), sess.XML(), browserSVG)
		}

		sess.SetXML(xml)
		sess.IncVersion()
		bridge.SetState(sess.ID(), xml, "", false, true)
		// Deliberately NOT marking the loaded XML as seen: the model only
		// supplied a path, so it doesn't know the file's cell IDs. The
		// edit gate will require one get_diagram before edits.
		sess.SetLastSeenXML("")

		store.AddHistory(sess.ID(), xml, "")

		pageSummary := "no pages parsed"
		if doc := diagram.ParseMxfile(xml); doc != nil {
			if pages := diagram.ListPagesFromDoc(doc); len(pages) > 0 {
				parts := make([]string, len(pages))
				for i, p := range pages {
					parts[i] = fmt.Sprintf("[%d] id=%s name=%q cells=%d", p.Index, p.ID, p.Name, p.CellCount)
				}
				pageSummary = fmt.Sprintf("Pages (%d): %s", len(pages), strings.Join(parts, " | "))
			}
		}

		mcplog.Info("Diagram loaded from file (%s)", pageSummary)

		return textResult(fmt.Sprintf(
			"Diagram loaded from %s!\n\nThe diagram is now visible in your browser.\n\n%s\n\nCall get_diagram before edit_diagram — you haven't seen this file's cell IDs yet.",
			absolutePath, pageSummary)), nil
	})

	// Tool: edit_diagram
	registerWrite("edit_diagram", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Edit diagram"),
		mcp.WithDescription("Edit a specific page in the current diagram by ID-based operations (update/add/delete cells).\n\n" +
			"All-or-nothing: if any operation fails, nothing is applied and every failure is listed.\n\n" +
			"Freshness: the server remembers the last diagram state you have seen, and rejects this call " +
			"only if the user edited the diagram in the browser since then. You do NOT need to call " +
			"get_diagram before every edit: a rejected call changes nothing and includes the current XML " +
			"of the page, so you can rebuild your operations and retry.\n\n" +
			"Call get_diagram first only when you don't know the current diagram content (cell IDs, " +
			"structure) — e.g. the diagram wasn't created in this conversation, or you're unsure your " +
			"memory of it is accurate.\n\n" +
			"Multi-page targeting:\n" +
			"- page_id / page_name / page_index are optional; when all omitted, the FIRST page is targeted\n" +
			"- Use list_pages to discover what pages exist\n\n" +
			"Operations:\n" +
			"- add: Add a new cell. Provide cell_id (new unique id within the page) and new_xml. One cell per operation.\n" +
			"- update: Replace an existing cell by its id. Provide cell_id and complete new_xml.\n" +
			"- delete: Remove a cell by its id. Only cell_id is needed. Its children and connected edges are deleted too, so give only a container's id.\n\n" +
			"For add/update, new_xml must be a complete mxCell element including mxGeometry. No XML comments. " +
			"Every \" inside new_xml must be escaped as \\\" in the JSON.\n\n" +
			"Example - Add a rectangle on the default (first) page:\n" +
			"{\"operations\": [{\"operation\": \"add\", \"cell_id\": \"rect-1\", \"new_xml\": \"<mxCell id=\\\"rect-1\\\" value=\\\"Hello\\\" style=\\\"rounded=0;\\\" vertex=\\\"1\\\" parent=\\\"1\\\"><mxGeometry x=\\\"100\\\" y=\\\"100\\\" width=\\\"120\\\" height=\\\"60\\\" as=\\\"geometry\\\"/></mxCell>\"}]}\n\n" +
			"Example - Delete a cell on the default page:\n" +
			"{\"operations\": [{\"operation\": \"delete\", \"cell_id\": \"rect-1\"}]}"),
		mcp.WithArray("operations",
			mcp.Required(),
			mcp.Description("Array of operations to apply"),
			mcp.Items(operationsItemSchema)),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}
		operations := parseOperations(args)

		// Fetch latest state from browser. Re-normalise to mxfile: the
		// embed/sync path can hand back a bare <mxGraphModel>, and adopting
		// it verbatim would silently strip a multi-page document down to
		// one page on the next write.
		browserXML, browserSVG, hasState := sessionState(sess.ID())
		if hasState && browserXML != "" {
			if normalized := diagram.NormalizeToMxfile(browserXML, "", ""); normalized != "" {
				sess.SetXML(normalized)
			} else {
				sess.SetXML(browserXML)
			}
			mcplog.Info("Fetched latest diagram state from browser")
		}

		if sess.XML() == "" {
			return errorResult("Error: No diagram to edit. Please create a diagram first with create_new_diagram."), nil
		}

		pageSelector := pickPageSelector(args)

		// Enforce workflow: the model must have seen the current diagram
		// state. Content comparison instead of a wall-clock timeout —
		// slow reasoning between get_diagram and edit_diagram is fine as
		// long as nothing changed in the browser meanwhile (#885).
		gate := diagram.CheckEditGate(sess.LastSeenXML(), browserXML)
		if !gate.OK {
			if gate.Reason == "stale" {
				mcplog.Warn("edit_diagram rejected: the browser has changes the model has not seen")
			} else {
				mcplog.Warn("edit_diagram rejected: the model has not seen the diagram yet")
			}
			// The error carries the current page, so the model has now
			// seen it and can retry without a get_diagram round-trip,
			// unless other pages changed too.
			liveXML := browserXML
			if liveXML == "" {
				liveXML = sess.XML()
			}
			sess.SetLastSeenXML(diagram.MarkPageSeen(sess.LastSeenXML(), liveXML, pageSelector))
			reason := "You have not seen this diagram yet, so no changes were made."
			if gate.Reason == "stale" {
				reason = "The diagram changed in the browser since you last saw it (e.g. manual user edits). No changes were made."
			}
			next := fmt.Sprintf("%s Call get_diagram without a page selector, then retry.", OTHER_PAGES_UNSEEN)
			if sess.LastSeenXML() == liveXML {
				next = "Build your operations on this XML and retry."
			}
			return errorResult(fmt.Sprintf(
				"Error: %s\n\nCurrent XML of %s:\n\n%s\n\n%s",
				reason, describeSelector(pageSelector),
				diagram.TargetPageXML(sess.XML(), pageSelector), next)), nil
		}

		mcplog.Info("Editing diagram with %d operation(s) on %s", len(operations), describeSelector(pageSelector))

		outcome := diagram.EditDiagram(sess.XML(), operations, pageSelector)
		if !outcome.OK {
			mcplog.Warn("Edit rejected: %s", strings.Join(outcome.Errors, "; "))
			if outcome.PageError {
				return errorResult("Error: " + outcome.Errors[0]), nil
			}
			lines := make([]string, len(outcome.Errors))
			for i, e := range outcome.Errors {
				lines[i] = "- " + e
			}
			return errorResult(fmt.Sprintf(
				"Error: No changes were made because %d operation(s) failed:\n%s\n\nCurrent XML of %s:\n\n%s\n\nFix the operations against this XML and retry.",
				len(outcome.Errors), strings.Join(lines, "\n"),
				describeSelector(pageSelector),
				diagram.TargetPageXML(sess.XML(), pageSelector))), nil
		}
		if len(outcome.Fixes) > 0 {
			mcplog.Info("new_xml auto-fixed: %s", strings.Join(outcome.Fixes, "; "))
		}
		result := outcome.XML

		// Save the pre-edit state for undo (with cached SVG from browser).
		// Done only once the edit applied: a rejected edit returns above
		// without leaving a phantom history entry.
		bridge.KeepInHistory(sess.ID(), sess.XML(), browserSVG)

		// Update state
		sess.SetXML(result)
		sess.IncVersion()

		// Push to embedded server; the pushed XML is now the latest
		// state the model has seen.
		bridge.SetState(sess.ID(), result, "", false, true)
		sess.SetLastSeenXML(result)

		// Save AI result (no SVG yet - will be captured by browser)
		store.AddHistory(sess.ID(), result, "")

		mcplog.Info("Diagram edited successfully")

		return textResult(fmt.Sprintf(
			"Diagram edited successfully!\n\nApplied %d operation(s) on %s.",
			outcome.Applied, describeSelector(pageSelector))), nil
	})

	// Tool: get_diagram
	register("get_diagram", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Get diagram"),
		mcp.WithDescription("Get the current diagram XML (fetches latest from browser, including user's manual edits). " +
			"Call this when you don't know the current diagram content (cell IDs, pages, structure) — " +
			"e.g. before editing a diagram you didn't create in this conversation, or after edit_diagram " +
			"was rejected because the user changed the diagram in the browser.\n\n" +
			"Returns the full <mxfile> by default. If a page selector is provided, returns just that page's <mxGraphModel> embedded in a one-page <mxfile> wrapper."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}

		// Request browser to push fresh state and wait for it (an
		// expired session first gets its saved file back to sync)
		staleNote := ""
		bridge.RestoreSavedSession(sess.ID())
		if bridge.RequestSync(sess.ID()) {
			if !bridge.WaitForSync(sess.ID(), 3000) {
				mcplog.Warn("get_diagram: sync timeout - state may be stale")
				staleNote = "\n\nNote: the browser did not respond, so this XML may not include the user's latest manual edits (is the preview tab open?)."
			}
		}

		// Fetch latest state from browser, re-normalising to mxfile so a
		// bare <mxGraphModel> pushed back by the embed/sync path doesn't
		// strip page structure (see edit_diagram for the same guard).
		browserXML, _, hasState := sessionState(sess.ID())
		if hasState && browserXML != "" {
			if normalized := diagram.NormalizeToMxfile(browserXML, "", ""); normalized != "" {
				sess.SetXML(normalized)
			} else {
				sess.SetXML(browserXML)
			}
		}

		if sess.XML() == "" {
			return textResult("No diagram exists yet. Use create_new_diagram to create one."), nil
		}

		pageSelector := pickPageSelector(args)

		// The model is now looking at the current state. Record the raw
		// store value — the gate's fast path is plain string equality
		// against the store, with a structural comparison as fallback.
		liveXML := browserXML
		if liveXML == "" {
			liveXML = sess.XML()
		}
		pageList := "No <mxfile> wrapper detected (legacy single-page session)."
		if doc := diagram.ParseMxfile(sess.XML()); doc != nil {
			if pages := diagram.ListPagesFromDoc(doc); len(pages) > 0 {
				parts := make([]string, len(pages))
				for i, p := range pages {
					parts[i] = fmt.Sprintf("[%d] id=%s name=%q cells=%d", p.Index, p.ID, p.Name, p.CellCount)
				}
				pageList = fmt.Sprintf("Pages (%d): %s", len(pages), strings.Join(parts, " | "))
			}
		}

		// No selector → return full mxfile
		if !diagram.HasPageSelector(pageSelector) {
			sess.SetLastSeenXML(liveXML)
			return textResult(fmt.Sprintf(
				"Current diagram XML:\n\n%s\n\n%s%s",
				sess.XML(), pageList, staleNote)), nil
		}

		// Selector → return a single-page projection
		projection := diagram.ProjectPage(sess.XML(), pageSelector)
		if !projection.OK {
			if projection.Reason == "parse" {
				return errorResult(fmt.Sprintf(
					"Error: a page selector was given but the current session XML could not be parsed as a multi-page <mxfile> (it may be a legacy single-page document or malformed), so it has no addressable pages.\n\n%s",
					pageList)), nil
			}
			return errorResult(fmt.Sprintf(
				"Error: Page %s not found.\n\n%s",
				describeSelector(pageSelector), pageList)), nil
		}
		// One page shown counts for all only if the others are as the
		// model saw them last
		sess.SetLastSeenXML(diagram.MarkPageSeen(sess.LastSeenXML(), liveXML, pageSelector))
		otherPagesNote := ""
		if sess.LastSeenXML() != liveXML {
			otherPagesNote = fmt.Sprintf("\n\nNote: %s Call get_diagram without a page selector before editing.", OTHER_PAGES_UNSEEN)
		}
		return textResult(fmt.Sprintf(
			"Page %d (%q):\n\n%s\n\n%s%s%s",
			projection.Index, projection.Name, projection.XML, pageList, staleNote, otherPagesNote)), nil
	})

	// Tool: screenshot_diagram
	register("screenshot_diagram", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Screenshot diagram"),
		mcp.WithDescription("Render the diagram in the preview and return it as a PNG image, so you can see your own result. " +
			"Call this once after drawing or heavily editing a complex diagram, then fix overlaps and edges that cross shapes. " +
			"Without a page selector it shows the page on screen. Needs the preview tab to be open."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}
		if previewStalled(sess.ID()) {
			return previewStalledError(sess.ID()), nil
		}
		browserXML, _, hasState := sessionState(sess.ID())
		xml := sess.XML()
		if hasState && browserXML != "" {
			xml = browserXML
		}
		if !diagram.HasCells(xml) {
			return textResult("The diagram is empty."), nil
		}

		pageSelector := pickPageSelector(args)
		pageID := ""
		projectionXML := ""
		if diagram.HasPageSelector(pageSelector) {
			doc := diagram.NormalizeToMxfile(xml, "", "")
			if doc == "" {
				doc = xml
			}
			pageID = pageIdFor(doc, pageSelector)
			// A page without an id: load just that page and capture
			// it, as export_diagram does
			if pageID == "" {
				projection := diagram.ProjectPage(doc, pageSelector)
				if !projection.OK {
					return errorResult(fmt.Sprintf(
						"Error: Page %s not found.", describeSelector(pageSelector))), nil
				}
				projectionXML = projection.XML
			}
		}

		var data string
		var options *bridge.ExportOptions
		if pageID != "" {
			options = &bridge.ExportOptions{PageID: pageID}
		}
		for _, width := range screenshotWidths {
			w := width
			if options == nil {
				options = &bridge.ExportOptions{}
			}
			options.Width = &w
			data = exportViaBrowser(sess.ID(), bridge.ExportPNG, projectionXML, options)
			if data == "" || len(data) <= maxScreenshotChars {
				break
			}
		}
		if data == "" {
			return errorResult("Error: Screenshot timed out. Make sure the preview tab is open and in front."), nil
		}
		stripped := strings.TrimPrefix(data, "data:image/png;base64,")
		subject := "the page on screen"
		if diagram.HasPageSelector(pageSelector) {
			subject = "page " + describeSelector(pageSelector)
		}
		return imageResult(
			fmt.Sprintf("Screenshot of %s.\n\n%s", subject, screenshotChecklist),
			stripped), nil
	})

	// Tool: export_diagram
	register("export_diagram", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Export diagram"),
		mcp.WithDescription("Export the current diagram to a file. Supports .drawio (XML), .png, .svg, and .drawio.svg (an SVG with the diagram embedded, which draw.io can open and edit again). " +
			"The format is auto-detected from the file extension, or can be specified explicitly.\n\n" +
			"Multi-page behaviour:\n" +
			"- .drawio with NO page selector: writes the full <mxfile> (all pages).\n" +
			"- .drawio with a page selector: writes a single-page <mxfile> containing only that page.\n" +
			"- .png / .svg with NO page selector: exports the currently active page in the browser.\n" +
			"- .png with a page selector: renders that page without changing what the user sees.\n" +
			"- .svg / .drawio.svg with a page selector: temporarily loads that page into the browser, captures it, then restores the full document (the user sees a brief flicker)."),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("Absolute file path to save to (e.g. /Users/me/diagram.drawio, ~/diagram.png). Relative paths resolve against the MCP server's working directory, which is often not your project.")),
		mcp.WithString("format",
			mcp.Description("Export format. If omitted, detected from file extension. Defaults to drawio."),
			mcp.Enum([]string{"drawio", "png", "svg", "drawio.svg"}...)),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		sess := currentSessionSnapshot()
		if sess == nil {
			return errorResult(noSessionResult), nil
		}
		rawPath, _ := args["path"].(string)
		path := store.ExpandHome(rawPath)
		formatArg, _ := args["format"].(string)

		// Detect format from extension if not specified
		lowerPath := strings.ToLower(path)
		detectedFormat := formatArg
		if detectedFormat == "" {
			switch {
			case strings.HasSuffix(lowerPath, ".drawio.svg"):
				detectedFormat = "drawio.svg"
			case strings.HasSuffix(lowerPath, ".png"):
				detectedFormat = "png"
			case strings.HasSuffix(lowerPath, ".svg"):
				detectedFormat = "svg"
			default:
				detectedFormat = "drawio"
			}
		}

		// The .drawio file is written from the state, so get the
		// user's latest edits into it first, as get_diagram does (the
		// images are made by the browser from its canvas)
		syncNote := ""
		if detectedFormat == "drawio" {
			bridge.RestoreSavedSession(sess.ID())
			if !bridge.RequestSync(sess.ID()) {
				syncNote = "\n\nNote: the preview was not reachable, so the file may not include the user's latest manual edits."
			} else if !bridge.WaitForSync(sess.ID(), 3000) {
				mcplog.Warn("export_diagram: sync timeout - state may be stale")
				syncNote = "\n\nNote: the browser did not respond, so the file may not include the user's latest manual edits (is the preview tab open?)."
			}
		}

		// Fetch latest state, re-normalised to mxfile so a page
		// selector works on a bare <mxGraphModel> pushed by the browser
		browserXML, _, hasState := sessionState(sess.ID())
		if hasState && browserXML != "" {
			if normalized := diagram.NormalizeToMxfile(browserXML, "", ""); normalized != "" {
				sess.SetXML(normalized)
			} else {
				sess.SetXML(browserXML)
			}
		}

		if sess.XML() == "" {
			return errorResult("Error: No diagram to export. Please create a diagram first."), nil
		}

		pageSelector := pickPageSelector(args)

		// .drawio path - write XML directly (no browser round-trip).
		if detectedFormat == "drawio" {
			filePath := path
			if !strings.HasSuffix(strings.ToLower(filePath), ".drawio") {
				filePath += ".drawio"
			}
			absolutePath, err := filepath.Abs(filePath)
			if err != nil {
				absolutePath = filePath
			}

			outXML := sess.XML()
			if diagram.HasPageSelector(pageSelector) {
				projection := diagram.ProjectPage(sess.XML(), pageSelector)
				if !projection.OK {
					if projection.Reason == "parse" {
						return errorResult("Error: Cannot parse current session XML as <mxfile>; cannot project a single page."), nil
					}
					return errorResult(fmt.Sprintf(
						"Error: Page %s not found for export.", describeSelector(pageSelector))), nil
				}
				outXML = projection.XML
			}

			if err := os.WriteFile(absolutePath, []byte(outXML), 0o644); err != nil {
				return errorResult(fmt.Sprintf("Error: %v", err)), nil
			}
			mcplog.Info("Diagram exported to %s", absolutePath)
			return textResult(fmt.Sprintf(
				"Diagram exported successfully!\n\nFile: %s\nSize: %d characters%s",
				absolutePath, len(outXML), syncNote)), nil
		}

		// PNG or SVG: request browser to export via iframe. Replace a
		// known extension that does not match the format.
		filePath := path
		suffix := "." + detectedFormat
		if !strings.HasSuffix(lowerPath, suffix) {
			for _, known := range []string{".drawio.svg", ".drawio", ".png", ".svg"} {
				if strings.HasSuffix(lowerPath, known) {
					filePath = filePath[:len(filePath)-len(known)]
					break
				}
			}
			filePath += suffix
		}
		absolutePath, err := filepath.Abs(filePath)
		if err != nil {
			absolutePath = filePath
		}

		// draw.io's name for an SVG with the diagram embedded
		browserFormat := bridge.ExportFormat(detectedFormat)
		if detectedFormat == "drawio.svg" {
			browserFormat = bridge.ExportXMLSVG
		}

		if _, _, stateOK := sessionState(sess.ID()); !stateOK {
			return errorResult("Error: Session state not found. Is the browser open?"), nil
		}
		if previewStalled(sess.ID()) {
			return previewStalledError(sess.ID()), nil
		}

		// -----------------------------------------------------------------
		// Page-targeted PNG/SVG export.
		//
		// drawio's JSON embed protocol has no working `selectPage` action,
		// so to export a specific page we build a single-page <mxfile>
		// projection and hand it to the browser bridge alongside the export
		// request. The bridge loads the projection, waits for draw.io's own
		// render, exports, then reloads the user's real document — entirely
		// browser-side. The canonical session state is never mutated here,
		// so there is no restore race and no concurrent-edit clobbering.
		// -----------------------------------------------------------------
		// PNG: draw.io renders any page by id, without touching the
		// page on screen. SVG export has no page option, so it still
		// needs the projection below.
		projectionXML := ""
		pngPageID := ""
		if diagram.HasPageSelector(pageSelector) && detectedFormat == "png" {
			pngPageID = pageIdFor(sess.XML(), pageSelector)
		}
		if diagram.HasPageSelector(pageSelector) && pngPageID == "" {
			projection := diagram.ProjectPage(sess.XML(), pageSelector)
			if !projection.OK {
				if projection.Reason == "parse" {
					return errorResult("Error: Cannot parse current session XML as <mxfile>; cannot target page for export."), nil
				}
				return errorResult(fmt.Sprintf(
					"Error: Page %s not found for export.", describeSelector(pageSelector))), nil
			}
			projectionXML = projection.XML
		}

		var options *bridge.ExportOptions
		if pngPageID != "" {
			options = &bridge.ExportOptions{PageID: pngPageID}
		}
		exportData := exportViaBrowser(sess.ID(), browserFormat, projectionXML, options)

		if exportData == "" {
			if projectionXML != "" {
				return errorResult("Error: Export timed out after loading the single-page projection. The browser may be closed or unresponsive."), nil
			}
			return errorResult("Error: Export timed out. Make sure the browser tab is open and the diagram is loaded."), nil
		}

		// Decode and write
		if detectedFormat == "png" {
			base64Data := strings.TrimPrefix(exportData, "data:image/png;base64,")
			binary, err := base64.StdEncoding.DecodeString(base64Data)
			if err != nil {
				return errorResult(fmt.Sprintf("Error: %v", err)), nil
			}
			if err := os.WriteFile(absolutePath, binary, 0o644); err != nil {
				return errorResult(fmt.Sprintf("Error: %v", err)), nil
			}
		} else {
			svgContent := exportData
			if strings.HasPrefix(svgContent, "data:image/svg+xml;base64,") {
				base64Data := strings.TrimPrefix(svgContent, "data:image/svg+xml;base64,")
				decoded, err := base64.StdEncoding.DecodeString(base64Data)
				if err != nil {
					return errorResult(fmt.Sprintf("Error: %v", err)), nil
				}
				svgContent = string(decoded)
			}
			if err := os.WriteFile(absolutePath, []byte(svgContent), 0o644); err != nil {
				return errorResult(fmt.Sprintf("Error: %v", err)), nil
			}
		}

		size := int64(0)
		if info, err := os.Stat(absolutePath); err == nil {
			size = info.Size()
		}
		mcplog.Info("Diagram exported to %s (%s, %d bytes)", absolutePath, detectedFormat, size)
		return textResult(fmt.Sprintf(
			"Diagram exported successfully!\n\nFile: %s\nFormat: %s\nSize: %d bytes",
			absolutePath, detectedFormat, size)), nil
	})

	// Tool: list_pages
	register("list_pages", []mcp.ToolOption{
		mcp.WithTitleAnnotation("List pages"),
		mcp.WithDescription("List every page (tab) in the current diagram. Returns each page's id, name, 0-based index, and cell count. Use this to discover what pages exist before targeting one with edit_diagram, get_diagram, export_diagram, rename_page, or delete_page."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		loaded := loadMxfileForMutation()
		if !loaded.ok {
			return errorResult("Error: " + loaded.message), nil
		}
		pages := diagram.ListPagesFromDoc(loaded.doc)
		if len(pages) == 0 {
			return textResult("No pages in document."), nil
		}
		lines := make([]string, len(pages))
		for i, p := range pages {
			lines[i] = fmt.Sprintf(`  [%d] id=%q name=%q cells=%d`, p.Index, p.ID, p.Name, p.CellCount)
		}
		return textResult(fmt.Sprintf("Pages (%d):\n%s", len(pages), strings.Join(lines, "\n"))), nil
	})

	// Tool: add_page
	registerWrite("add_page", []mcp.ToolOption{
		mcp.WithTitleAnnotation("Add page"),
		mcp.WithDescription("Append a new page (tab) to the current diagram WITHOUT touching existing pages or unsaved user changes. Use this when the user wants \"another diagram alongside\" — e.g. \"add a CNN page\" — instead of create_new_diagram which wipes everything.\n\n" +
			"Inputs:\n" +
			"- name: optional display name for the tab (defaults to Page-N where N = existing-page-count + 1)\n" +
			"- id: optional explicit page id; if omitted the server generates a short alphanumeric id\n" +
			"- xml: optional starting content: the mxCell elements of the page (root cells \"0\" and \"1\" are added), or a bare <mxGraphModel>. If omitted, the page starts blank.\n\n" +
			"Returns the new page's id, name, and index so the caller can immediately target it with edit_diagram."),
		mcp.WithString("name",
			mcp.Description(`Optional display name for the new tab (e.g. "CNN"). Defaults to "Page-N".`)),
		mcp.WithString("id",
			mcp.Description("Optional explicit page id. If omitted the server generates one. Must be unique across pages.")),
		mcp.WithString("xml",
			mcp.Description("Optional starting content: the mxCell elements of the page, or a bare <mxGraphModel>. If omitted the page starts blank.")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	}, func(ctx context.Context, args map[string]any) (*toolResult, error) {
		loaded := loadMxfileForMutation()
		if !loaded.ok {
			return errorResult("Error: " + loaded.message), nil
		}
		name, _ := args["name"].(string)
		id, _ := args["id"].(string)
		xml, hasXML := args["xml"].(string)

		// If caller provided XML, validate it before splicing it in so we
		// never get a half-broken mxfile written to the session.
		cleanXML := ""
		if hasXML && xml != "" {
			if reserved := diagram.ReservedIDError(xml); reserved != "" {
				return errorResult("Error: " + reserved), nil
			}
			cleanXML = diagram.WrapCellsInModel(xml)
			valid, errMsg, fixed, fixes := xmlutil.ValidateAndFixXML(cleanXML, true)
			if fixed != "" {
				cleanXML = fixed
				mcplog.Info("add_page: starting XML auto-fixed: %s", strings.Join(fixes, ", "))
			}
			if !valid {
				return errorResult(fmt.Sprintf("Error: starting xml validation failed - %s", errMsg)), nil
			}
		}

		info, err := diagram.AddPageToDoc(loaded.doc, id, name, cleanXML)
		if err != nil {
			return errorResult("Error: " + err.Error()), nil
		}

		loaded.writeBack(loaded.doc)
		mcplog.Info("Added page id=%s name=%q index=%d", info.ID, info.Name, info.Index)
		return textResult(fmt.Sprintf(
			"Page added.\n\nid=%s\nname=%s\nindex=%d\ncells=%d",
			info.ID, info.Name, info.Index, info.CellCount)), nil
	})

	// Tool: rename_page
	registerWrite("rename_page", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Rename page"),
		mcp.WithDescription("Rename an existing page (tab). At least one of page_id / page_name / page_index is required to identify which page to rename. The new_name becomes the visible tab label in the editor."),
		mcp.WithString("new_name",
			mcp.Required(),
			mcp.Description("The new display name for the page tab.")),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		loaded := loadMxfileForMutation()
		if !loaded.ok {
			return errorResult("Error: " + loaded.message), nil
		}
		newName, _ := args["new_name"].(string)
		pageSelector := pickPageSelector(args)
		if !diagram.HasPageSelector(pageSelector) {
			return errorResult("Error: rename_page requires one of page_id, page_name, or page_index to identify the page."), nil
		}

		if !diagram.RenamePageInDoc(loaded.doc, pageSelector, newName) {
			return errorResult(fmt.Sprintf(
				"Error: Page %s not found.", describeSelector(pageSelector))), nil
		}

		loaded.writeBack(loaded.doc)
		mcplog.Info("Renamed page %s → %q", describeSelector(pageSelector), newName)
		return textResult(fmt.Sprintf(
			"Page %s renamed to %q.", describeSelector(pageSelector), newName)), nil
	})

	// Tool: delete_page
	registerWrite("delete_page", append(pageSelectorSchema(), []mcp.ToolOption{
		mcp.WithTitleAnnotation("Delete page"),
		mcp.WithDescription("Delete a page (tab) from the current diagram. At least one of page_id / page_name / page_index is required. Refuses to delete the last remaining page — the editor needs at least one tab."),
		mcp.WithOpenWorldHintAnnotation(false),
	}...), func(ctx context.Context, args map[string]any) (*toolResult, error) {
		loaded := loadMxfileForMutation()
		if !loaded.ok {
			return errorResult("Error: " + loaded.message), nil
		}

		pageSelector := pickPageSelector(args)
		if !diagram.HasPageSelector(pageSelector) {
			return errorResult("Error: delete_page requires one of page_id, page_name, or page_index to identify the page."), nil
		}

		outcome := diagram.DeletePageFromDoc(loaded.doc, pageSelector)
		if !outcome.OK {
			return errorResult(fmt.Sprintf("Error: %s.", outcome.Reason)), nil
		}

		loaded.writeBack(loaded.doc)
		mcplog.Info("Deleted page id=%s index=%d", outcome.DeletedID, outcome.DeletedIndex)
		return textResult(fmt.Sprintf(
			"Page deleted (id=%s, was at index %d).", outcome.DeletedID, outcome.DeletedIndex)), nil
	})
}
