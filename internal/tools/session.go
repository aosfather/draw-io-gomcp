// Session bookkeeping and shared helpers for the MCP tools. Ported from
// the tool-layer parts of index.ts (session state, exclusive queue, export
// queue) and exclusive.ts.
package tools

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"

	"aosfather.drawio-mcp/internal/bridge"
	"aosfather.drawio-mcp/internal/diagram"
)

// ServerPort is the port the embedded HTTP server prefers (PORT env).
var ServerPort = func() int {
	if v := os.Getenv("PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			return p
		}
	}
	return 6002
}()

// diagramSession is the tool layer's view of the current session.
type diagramSession struct {
	mu sync.Mutex
	id string
	// The exact state-store XML the model last saw (get_diagram) or wrote
	// itself (create/edit/page CRUD). The store only changes on server
	// writes or browser pushes (user autosave / sync), so edit_diagram can
	// detect unseen user edits by comparing the live store against this.
	// Empty = no diagram context established yet.
	lastSeenXML string
	xml         string
	version     int
}

func (s *diagramSession) ID() string { return s.id }

func (s *diagramSession) XML() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.xml
}

func (s *diagramSession) SetXML(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.xml = v
}

func (s *diagramSession) LastSeenXML() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeenXML
}

func (s *diagramSession) SetLastSeenXML(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeenXML = v
}

func (s *diagramSession) IncVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version++
	return s.version
}

var (
	sessionMu      sync.Mutex
	currentSession *diagramSession
)

func setCurrentSession(s *diagramSession) {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	currentSession = s
}

// currentSessionSnapshot returns the current session, or nil. A handler
// keeps working on the returned session even when start_session replaces
// the global afterwards.
func currentSessionSnapshot() *diagramSession {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	return currentSession
}

// sessionState restores the session's saved diagram after an expiry or
// process restart, then returns the browser's state. After the restore the
// state exists again, so a tool never builds on an older copy and then
// overwrites the file. Call it before requestSync or requestExport, which
// need the state.
func sessionState(sessionID string) (xml, svg string, ok bool) {
	bridge.RestoreSavedSession(sessionID)
	return bridge.StateXML(sessionID)
}

// noSessionResult is the shared "call start_session first" error.
const noSessionResult = "Error: No active session. Please call start_session first."

// pickPageSelector pulls a clean PageSelector out of a tool's parsed input.
// Returns nil when none of the page_* fields are set, so callers can simply
// pass it through to the lower layers (they treat nil as "first page" by
// convention).
func pickPageSelector(args map[string]any) *diagram.PageSelector {
	selector := &diagram.PageSelector{}
	if v, ok := args["page_id"].(string); ok && v != "" {
		selector.PageID = v
	}
	if v, ok := args["page_name"].(string); ok && v != "" {
		selector.PageName = v
	}
	if v, ok := args["page_index"].(float64); ok {
		idx := int(v)
		selector.PageIndex = &idx
	}
	if !diagram.HasPageSelector(selector) {
		return nil
	}
	return selector
}

// describeSelector formats a selector for human-readable error messages.
func describeSelector(s *diagram.PageSelector) string {
	if s == nil {
		return "first page"
	}
	if s.PageID != "" {
		return fmt.Sprintf("id=%q", s.PageID)
	}
	if s.PageName != "" {
		return fmt.Sprintf("name=%q", s.PageName)
	}
	if s.PageIndex != nil {
		return fmt.Sprintf("index=%d", *s.PageIndex)
	}
	return "first page"
}

// exclusive serialises the tools that write the diagram, and start_session:
// two writes at once would both build on the same document, and the second
// would drop the first one's change. start_session in the queue keeps a
// session switch from landing in the middle of a write.
type exclusiveQueue struct {
	mu sync.Mutex
}

func (q *exclusiveQueue) wrap(h func(ctx context.Context, args map[string]any) (*toolResult, error)) func(ctx context.Context, args map[string]any) (*toolResult, error) {
	return func(ctx context.Context, args map[string]any) (*toolResult, error) {
		q.mu.Lock()
		defer q.mu.Unlock()
		return h(ctx, args)
	}
}

var writeQueue exclusiveQueue

// exportQueueMu has one export slot per session, so export requests run one
// at a time: a concurrent call waits for the previous one.
var exportQueueMu sync.Mutex

// exportViaBrowser asks the browser to export (optionally via a page
// projection) and polls for the resulting image data. Returns "" on
// timeout.
func exportViaBrowser(sessionID string, format bridge.ExportFormat, projectionXML string, options *bridge.ExportOptions) string {
	exportQueueMu.Lock()
	defer exportQueueMu.Unlock()

	bridge.RequestExport(sessionID, format, projectionXML, options)

	// A projection export does an extra load + render round-trip in the
	// browser, so give it a longer window.
	timeout := 10 * time.Second
	if projectionXML != "" {
		timeout = 15 * time.Second
	}
	start := time.Now()
	exportData := ""
	for time.Since(start) < timeout {
		exportData = bridge.ExportDataOf(sessionID)
		if exportData != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	bridge.ClearExportRequest(sessionID)
	return exportData
}

// previewStalled is true when the preview tab polled before but has gone
// quiet. Browsers slow down timers in background tabs (Chrome: about once a
// minute after 5 minutes hidden), so an export would just time out.
func previewStalled(sessionID string) bool {
	lastPolled := bridge.LastPolledAt(sessionID)
	return lastPolled != 0 && time.Now().UnixMilli()-lastPolled > 10_000
}

func previewStalledError(sessionID string) *toolResult {
	port := bridge.GetServerPort()
	return errorResult(fmt.Sprintf(
		"Error: The preview tab is not responding (browsers pause background tabs). Ask the user to bring the preview tab to the front (http://localhost:%d?mcp=%s), then retry.",
		port, sessionID))
}

// pageIdFor returns the <diagram id> of the page a selector targets, for
// draw.io's pageId option.
func pageIdFor(xml string, selector *diagram.PageSelector) string {
	doc := diagram.ParseMxfile(xml)
	if doc == nil {
		return ""
	}
	el, _ := diagram.FindPageElement(doc, selector)
	if el == nil {
		return ""
	}
	id, _ := el.GetAttr("id")
	return id
}

// Screenshot size: Claude Desktop caps a tool result at about 150,000
// characters, so retry smaller above 140,000 base64 characters. (Claude
// Code 2.1 accepted a 240,000 character image in testing.)
var screenshotWidths = []int{1000, 700}

const maxScreenshotChars = 140_000

// Adapted from the web app's vision check (lib/validation-prompts.ts)
const screenshotChecklist = `Check this rendering of the diagram for:
1. Overlapping shapes that cover each other or their labels (critical)
2. Edges crossing shapes that are not their source or target (critical)
3. Text that is cut off, overlapping or too small to read (warning)
4. Layout problems: cramped shapes, poor spacing or misalignment (warning)
5. Rendering errors: missing, incomplete or broken elements, such as an icon that did not load (critical)
If there are critical issues, fix them with edit_diagram and take one more screenshot. Do at most two rounds of fixes. Minor cosmetic issues are fine, and diagrams with only 1 or 2 shapes pass unless something is clearly broken.`

// generateSessionID creates ids like "mcp-<base36 time>-<base36 random>",
// matching the pattern the autosaver and the HTML page validate.
func generateSessionID() string {
	now := strconv.FormatInt(time.Now().UnixMilli(), 36)
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	rnd := make([]byte, 6)
	for i := range rnd {
		rnd[i] = digits[rand.IntN(36)]
	}
	return "mcp-" + now + "-" + string(rnd)
}

// openBrowser opens the URL in the default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// OTHER_PAGES_UNSEEN is the note for a one-page view that does not count
// for the whole document (edit-gate).
const OTHER_PAGES_UNSEEN = "You have not seen the other pages in their current state."

// INSTRUCTIONS is the server instruction block. Hosts truncate instructions
// (Claude Code at 2,048 characters) and may show only the first 512, so the
// essentials come first. The full rules are in DrawingGuide, returned by
// start_session.
const INSTRUCTIONS = `next-ai-drawio creates and edits draw.io diagrams and shows them live in a browser preview, where the user can also edit them by hand.

Start with start_session: it opens the preview and its result contains the drawing guide (layout, edge routing and style rules). Follow the guide when drawing; call get_drawing_guide if it is no longer in your context.

Before using cloud or icon shapes (AWS, Azure, GCP, Kubernetes, Cisco, BPMN...), call get_shape_library and use the exact style names it returns. Never guess icon style names.

After drawing a complex diagram, call screenshot_diagram once to see it, and fix overlapping shapes or edges that cross shapes.

Tools:
- create_new_diagram: draw a new diagram, replacing the whole document. Send only the mxCell elements of one page (the server adds the wrapper and root cells), or a full <mxfile> for several pages.
- edit_diagram: add, update or delete cells of an existing page by id. All-or-nothing; a rejected call includes the current XML so you can retry.
- get_diagram: read the current XML, including the user's manual edits.
- screenshot_diagram: see the rendered diagram as an image.
- load_diagram, export_diagram: open or save .drawio files and export .png or .svg. Use absolute paths.
- list_pages, add_page, rename_page, delete_page: manage pages (tabs).`
