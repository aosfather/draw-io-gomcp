// Embedded HTTP server for MCP.
// Serves the draw.io embed with state sync and a history UI. The browser
// preview page polls /api/state and pushes the user's diagram back; MCP
// tools read and write the same in-memory session store.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"aosfather.drawio-mcp/internal/assets"
	"aosfather.drawio-mcp/internal/diagram"
	"aosfather.drawio-mcp/internal/mcplog"
	"aosfather.drawio-mcp/internal/store"

	"github.com/google/uuid"
)

const maxBodyBytes = 10 * 1024 * 1024 // 10 MiB

// ExportFormat is a draw.io export format; xmlsvg is an SVG with the
// diagram embedded.
type ExportFormat string

const (
	ExportPNG   ExportFormat = "png"
	ExportSVG   ExportFormat = "svg"
	ExportXMLSVG ExportFormat = "xmlsvg"
)

// ExportOptions are extra draw.io export parameters (PNG only): width caps
// the image size (never upscales), pageID renders a page other than the one
// on screen.
type ExportOptions struct {
	Width  *int   `json:"width,omitempty"`
	PageID string `json:"pageId,omitempty"`
}

// SessionState is one browser session's diagram and export bookkeeping.
type SessionState struct {
	XML         string
	Version     int64
	// Made when the state is created (first use, or again after it expired
	// or the MCP process restarted) and kept by every write. A tab tells by
	// it that the server lost what it knew, and every push names the state
	// it was based on, so one based on a lost state is refused.
	StateID string
	// Created blank because nothing was saved; cleared by the first write.
	Blank bool
	// Version of the last write the browser did not make itself (AI edit,
	// restore). A browser push based on an older version is rejected.
	ServerVersion int64
	// The XML of that write: what a thumbnail taken after loading it shows.
	ServerXML string
	// The browser saved a change of the user's since that write (a sync
	// reply is no change).
	UserEdited bool
	LastUpdated time.Time
	LastPolled  int64 // Last browser poll; an open tab keeps the session alive
	SVG         string // Cached SVG from last browser save
	SyncRequested int64 // Timestamp when sync requested, cleared when browser responds
	ExportFormat  ExportFormat   // Set by MCP tool to request browser export
	ExportXML     string         // Single-page projection to load before a page-targeted export
	ExportOptions *ExportOptions // Extra draw.io export parameters (PNG only)
	ExportID      int64          // Number of the pending export, echoed with its result
	ExportData    string         // Base64/SVG data returned by browser after export
}

const (
	defaultPort = 6002
	maxPort     = 6020
	sessionTTL  = 60 * time.Minute
)

var (
	stateMu  sync.Mutex
	stateSto = map[string]*SessionState{}
	// Called after every state change (AI write, browser push, restore)
	stateListener func(sessionID, xml string)
	// Reads a session's saved diagram when its state is created again (it
	// expired, or the MCP process restarted)
	savedStateLoader func(sessionID string) string

	lastExportID int64

	serverPort  int
	httpServer  *http.Server
	cleanupStop chan struct{}
)

func cleanupLoop(stop chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cleanupExpiredSessions()
		case <-stop:
			return
		}
	}
}

// DrawioBaseURL is the configurable draw.io embed URL for private
// deployments.
var DrawioBaseURL = func() string {
	if v := os.Getenv("DRAWIO_BASE_URL"); v != "" {
		return v
	}
	return "https://embed.diagrams.net"
}()

// drawioOrigin is the scheme+host of the embed URL, for the page's
// postMessage security check.
var drawioOrigin = getOrigin(DrawioBaseURL)

func getOrigin(rawURL string) string {
	if i := strings.Index(rawURL, "//"); i >= 0 {
		return rawURL[:i+2] + strings.SplitN(rawURL[i+2:], "/", 2)[0]
	}
	return rawURL // Fallback if parsing fails
}

// GetState returns the session's state, or nil.
func GetState(sessionID string) *SessionState {
	stateMu.Lock()
	defer stateMu.Unlock()
	return stateSto[sessionID]
}

// StateXML returns the session's XML and cached SVG (ok=false when the
// session has no state).
func StateXML(sessionID string) (xml, svg string, ok bool) {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		return "", "", false
	}
	return state.XML, state.SVG, true
}

// LastPolledAt returns the timestamp of the session's last browser poll.
func LastPolledAt(sessionID string) int64 {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		return 0
	}
	return state.LastPolled
}

// ExportDataOf returns the browser's latest export result.
func ExportDataOf(sessionID string) string {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		return ""
	}
	return state.ExportData
}

// ClearExportRequest drops a finished or timed-out export request.
func ClearExportRequest(sessionID string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		return
	}
	state.ExportData = ""
	state.ExportFormat = ""
	state.ExportXML = ""
	state.ExportOptions = nil
	state.ExportID = 0
}

// OnStateChange registers the callback invoked after every state change.
func OnStateChange(listener func(sessionID, xml string)) {
	stateMu.Lock()
	defer stateMu.Unlock()
	stateListener = listener
}

// OnSessionRecreate registers the loader that reads a session's saved
// diagram when its state is created again.
func OnSessionRecreate(loader func(sessionID string) string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	savedStateLoader = loader
}

// SetState writes the session's diagram and bumps the version. Returns the
// new version.
func SetState(sessionID, xml, svg string, fromBrowser bool, notify bool) int64 {
	stateMu.Lock()
	existing := stateSto[sessionID]
	newVersion := existingVersion(existing) + 1
	stateID := existingStateID(existing)
	stateSto[sessionID] = &SessionState{
		XML:           xml,
		Version:       newVersion,
		StateID:       stateID,
		ServerVersion: ternaryInt64(!fromBrowser, newVersion, existingServerVersion(existing)),
		ServerXML:     ternaryString(!fromBrowser, xml, existingServerXML(existing)),
		UserEdited:    ternaryBool(!fromBrowser, false, existingUserEdited(existing)),
		LastUpdated:   time.Now(),
		LastPolled:    existingLastPolled(existing),
		// The image of this XML, never an older one's: a write without an
		// image (AI write, sync reply) leaves none until the browser sends
		// it, unless it is the same XML
		SVG:           svg,
		ExportFormat:  existingExportFormat(existing),
		ExportXML:     existingExportXML(existing),
		ExportOptions: existingExportOptions(existing),
		ExportID:      existingExportID(existing),
		ExportData:    existingExportData(existing),
	}
	if svg == "" && existing != nil && existing.XML == xml {
		stateSto[sessionID].SVG = existing.SVG
	}
	listener := stateListener
	stateMu.Unlock()

	mcplog.Debug("State updated: session=%s, version=%d", sessionID, newVersion)
	if notify && listener != nil {
		listener(sessionID, xml)
	}
	return newVersion
}

func existingVersion(s *SessionState) int64 {
	if s == nil {
		return 0
	}
	return s.Version
}

func existingStateID(s *SessionState) string {
	if s == nil {
		return uuid.NewString()
	}
	return s.StateID
}

func existingServerVersion(s *SessionState) int64 {
	if s == nil {
		return 0
	}
	return s.ServerVersion
}

func existingServerXML(s *SessionState) string {
	if s == nil {
		return ""
	}
	return s.ServerXML
}

func existingUserEdited(s *SessionState) bool {
	if s == nil {
		return false
	}
	return s.UserEdited
}

func existingLastPolled(s *SessionState) int64 {
	if s == nil {
		return 0
	}
	return s.LastPolled
}

func existingExportFormat(s *SessionState) ExportFormat {
	if s == nil {
		return ""
	}
	return s.ExportFormat
}

func existingExportXML(s *SessionState) string {
	if s == nil {
		return ""
	}
	return s.ExportXML
}

func existingExportOptions(s *SessionState) *ExportOptions {
	if s == nil {
		return nil
	}
	return s.ExportOptions
}

func existingExportID(s *SessionState) int64 {
	if s == nil {
		return 0
	}
	return s.ExportID
}

func existingExportData(s *SessionState) string {
	if s == nil {
		return ""
	}
	return s.ExportData
}

func ternaryInt64(cond bool, a, b int64) int64 {
	if cond {
		return a
	}
	return b
}

func ternaryString(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func ternaryBool(cond bool, a, b bool) bool {
	if cond {
		return a
	}
	return b
}

// KeepInHistory keeps the session's diagram in History before a write
// replaces it. Nothing to keep when the browser saved no change of the
// user's since the last server write and History ends with that write: the
// state is that write, or draw.io's own copy of it from a sync (other text,
// same diagram).
func KeepInHistory(sessionID, xml, svg string) {
	state := GetState(sessionID)
	history := store.GetHistory(sessionID)
	if state != nil && !state.UserEdited && len(history) > 0 &&
		history[len(history)-1].XML == state.ServerXML {
		return
	}
	store.AddHistory(sessionID, xml, svg)
}

// RequestExport asks the browser bridge to export the current diagram as
// png/svg. When projectionXML is given (a single-page <mxfile>), the bridge
// loads it first, waits for draw.io's own load event, exports, then reloads
// the session's real document — so a page-targeted export never mutates the
// canonical session state. Returns false when the session is unknown.
func RequestExport(sessionID string, format ExportFormat, projectionXML string, options *ExportOptions) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		return false
	}
	state.ExportData = ""
	state.ExportXML = projectionXML
	state.ExportOptions = options
	state.ExportFormat = format
	// The browser sends this back with the result, so a late result of an
	// export that timed out is not taken for this one
	lastExportID++
	state.ExportID = lastExportID
	return true
}

// RequestSync asks the browser to push its current diagram.
func RequestSync(sessionID string) bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		mcplog.Debug("Sync requested for non-existent session=%s", sessionID)
		return false
	}
	state.SyncRequested = time.Now().UnixMilli()
	mcplog.Debug("Sync requested for session=%s", sessionID)
	return true
}

// WaitForSync waits until the browser answered the sync request (or it
// timed out).
func WaitForSync(sessionID string, timeoutMS int64) bool {
	if timeoutMS <= 0 {
		timeoutMS = 3000
	}
	start := time.Now()
	for time.Since(start).Milliseconds() < timeoutMS {
		state := GetState(sessionID)
		if state == nil || state.SyncRequested == 0 {
			return true // Sync completed
		}
		time.Sleep(100 * time.Millisecond)
	}
	mcplog.Warn("Sync timeout for session=%s", sessionID)
	return false // Timeout
}

// session ids look like "mcp-<base36 time>-<base36 random>" (start_session).
// Only this charset is accepted, because ids are written into the page's
// HTML and script and into the redirect Location header.
var sessionIDRe = regexp.MustCompile(`^mcp-[a-z0-9-]{1,64}$`)

func isValidSessionID(sessionID string) bool {
	return sessionIDRe.MatchString(sessionID)
}

// getMostRecentSessionId finds the most recent active session (for
// auto-redirect when no sessionId provided).
func getMostRecentSessionID() string {
	stateMu.Lock()
	defer stateMu.Unlock()
	var mostRecentID string
	var mostRecent time.Time
	for id, state := range stateSto {
		if mostRecentID == "" || state.LastUpdated.After(mostRecent) {
			mostRecentID = id
			mostRecent = state.LastUpdated
		}
	}
	return mostRecentID
}

// RestoreSavedSession gives a session whose state is gone (it expired, or
// the MCP process restarted) its auto-saved diagram back. The MCP tools
// call this before they read the state, so they never build on an older
// copy and then overwrite the file. Not a change worth saving again.
func RestoreSavedSession(sessionID string) {
	stateMu.Lock()
	if _, ok := stateSto[sessionID]; ok || !isValidSessionID(sessionID) {
		stateMu.Unlock()
		return
	}
	loader := savedStateLoader
	stateMu.Unlock()
	if loader == nil {
		return
	}
	saved := loader(sessionID)
	if saved != "" {
		SetState(sessionID, saved, "", false, false)
	}
}

// ensureSessionStateInitialized lazily creates a session's state.
func ensureSessionStateInitialized(sessionID string) {
	if sessionID == "" || !isValidSessionID(sessionID) {
		return
	}
	RestoreSavedSession(sessionID)
	stateMu.Lock()
	if _, ok := stateSto[sessionID]; ok {
		stateMu.Unlock()
		return
	}
	stateMu.Unlock()

	// Not a change worth saving: the browser fills it on its next push.
	// A blank diagram keeps the draw.io spinner (spin=1) from waiting
	// forever when no load(xml) is ever sent
	SetState(sessionID, diagram.BlankMxfile, "", false, false)
	// Nothing is known about this session: a tab that still shows it keeps
	// its diagram
	stateMu.Lock()
	if state, ok := stateSto[sessionID]; ok {
		state.Blank = true
	}
	stateMu.Unlock()
}

// StartHTTPServer starts the embedded server on 127.0.0.1, probing ports
// upward when one is in use. Returns the actual port.
func StartHTTPServer(port int) (int, error) {
	stateMu.Lock()
	if httpServer != nil {
		p := serverPort
		stateMu.Unlock()
		return p, nil
	}
	stateMu.Unlock()

	if port == 0 {
		port = defaultPort
	}
	for {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return serve(ln, port)
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return 0, err
		}
		if port >= maxPort {
			return 0, fmt.Errorf("no available ports in range %d-%d", defaultPort, maxPort)
		}
		mcplog.Info("Port %d in use, trying %d", port, port+1)
		port++
	}
}

func serve(ln net.Listener, port int) (int, error) {
	stateMu.Lock()
	serverPort = port
	srv := &http.Server{Handler: http.HandlerFunc(handleRequest)}
	httpServer = srv
	// Sessions expire after an hour of inactivity
	if cleanupStop == nil {
		cleanupStop = make(chan struct{})
		go cleanupLoop(cleanupStop)
	}
	stateMu.Unlock()

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			mcplog.Error("HTTP server failed: %v", err)
		}
	}()

	mcplog.Info("HTTP server running on http://localhost:%d", port)
	return port, nil
}

// StopHTTPServer stops the embedded server.
func StopHTTPServer() {
	stateMu.Lock()
	srv := httpServer
	httpServer = nil
	serverPort = 0
	stateMu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
}

// Shutdown stops the server and the cleanup timer.
func Shutdown() {
	stateMu.Lock()
	if cleanupStop != nil {
		close(cleanupStop)
		cleanupStop = nil
	}
	stateMu.Unlock()
	StopHTTPServer()
}

// GetServerPort returns the port the server is running on.
func GetServerPort() int {
	stateMu.Lock()
	defer stateMu.Unlock()
	return serverPort
}

func cleanupExpiredSessions() {
	now := time.Now().UnixMilli()
	stateMu.Lock()
	var expired []string
	for id, state := range stateSto {
		lastActive := state.LastUpdated.UnixMilli()
		if state.LastPolled > lastActive {
			lastActive = state.LastPolled
		}
		if now-lastActive > sessionTTL.Milliseconds() {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		delete(stateSto, id)
	}
	stateMu.Unlock()
	for _, id := range expired {
		store.ClearHistory(id)
		mcplog.Info("Cleaned up expired session: %s", id)
	}
}

// handleRequest recovers from panics: a bad request must never take down
// the MCP process.
func handleRequest(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			mcplog.Error("HTTP request failed: %v", rec)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}()
	routeRequest(w, r)
}

// isLocalRequest serves only requests addressed to localhost, sent by the
// preview page itself (Origin is the address it was opened at, the Host) or
// by a non-browser client (no Origin header). This blocks DNS rebinding,
// other websites, and pages on other localhost ports, whose plain text
// POSTs need no CORS preflight.
func isLocalRequest(r *http.Request) bool {
	host := r.Host
	origin := r.Header.Get("Origin")
	if !localHostRe.MatchString(host) {
		return false
	}
	return origin == "" || origin == "http://"+host
}

var localHostRe = regexp.MustCompile(`^(localhost|127\.0\.0\.1)(:\d+)?$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func routeRequest(w http.ResponseWriter, r *http.Request) {
	u, err := urlFromRequest(r)
	if err != nil {
		// e.g. "//" is not a valid URL path
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request"))
		return
	}

	if !isLocalRequest(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden"))
		return
	}

	requestOrigin := r.Header.Get("Origin")
	if requestOrigin == fmt.Sprintf("http://localhost:%d", GetServerPort()) {
		w.Header().Set("Access-Control-Allow-Origin", requestOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	}

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch u.Path {
	case "/", "/index.html":
		handleIndex(w, u.Query().Get("mcp"))
	case "/api/state":
		handleStateAPI(w, r, u.Query().Get("sessionId"))
	case "/api/history":
		handleHistoryAPI(w, r, u.Query().Get("sessionId"))
	case "/api/restore":
		handleRestoreAPI(w, r)
	case "/api/history-svg":
		handleHistorySVGAPI(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}
}

// urlFromRequest parses the request path like `new URL(req.url, base)`; a
// path starting with "//" would be a protocol-relative URL, which the
// browser-based version rejects.
func urlFromRequest(r *http.Request) (*url.URL, error) {
	raw := r.URL.RequestURI()
	if strings.HasPrefix(raw, "//") {
		return nil, errors.New("invalid URL path")
	}
	return url.Parse(raw)
}

func handleIndex(w http.ResponseWriter, sessionID string) {
	if sessionID != "" && !isValidSessionID(sessionID) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Invalid session id"))
		return
	}

	// Auto-redirect to most recent session if no sessionId provided
	if sessionID == "" {
		if recent := getMostRecentSessionID(); recent != "" {
			w.Header().Set("Location", "/?mcp="+url.QueryEscape(recent))
			w.WriteHeader(http.StatusFound)
			return
		}
	}

	ensureSessionStateInitialized(sessionID)

	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(getHTMLPage(sessionID)))
}

func handleStateAPI(w http.ResponseWriter, r *http.Request, sessionID string) {
	switch r.Method {
	case http.MethodGet:
		if sessionID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sessionId required"})
			return
		}
		ensureSessionStateInitialized(sessionID)
		stateMu.Lock()
		state := stateSto[sessionID]
		// Polling counts as activity, so a session stays alive while its
		// tab is open
		if state != nil {
			state.LastPolled = time.Now().UnixMilli()
		}
		var resp map[string]any
		if state != nil {
			var exportFormat any
			if state.ExportFormat != "" {
				exportFormat = state.ExportFormat
			}
			var exportOptions any
			if state.ExportOptions != nil {
				exportOptions = state.ExportOptions
			}
			var exportID any
			if state.ExportID != 0 {
				exportID = state.ExportID
			}
			resp = map[string]any{
				"xml":           state.XML,
				"version":       state.Version,
				"stateId":       state.StateID,
				"blank":         state.Blank,
				"syncRequested": state.SyncRequested != 0,
				"exportFormat":  exportFormat,
				"exportXml":     state.ExportXML,
				"exportOptions": exportOptions,
				"exportId":      exportID,
			}
		} else {
			resp = map[string]any{
				"xml":           nil,
				"version":       0,
				"stateId":       nil,
				"blank":         false,
				"syncRequested": false,
				"exportFormat":  nil,
				"exportXml":     nil,
				"exportOptions": nil,
				"exportId":      nil,
			}
		}
		stateMu.Unlock()
		writeJSON(w, http.StatusOK, resp)

	case http.MethodPost:
		handleStatePost(w, r)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("Method Not Allowed"))
	}
}

// readBody reads the request body up to maxBodyBytes, answering 413 when
// it is larger.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "Payload too large"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid body"})
		}
		return nil, false
	}
	return body, true
}

func handleStatePost(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON"})
		return
	}

	sessionID, _ := data["sessionId"].(string)
	if sessionID == "" || !isValidSessionID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valid sessionId required"})
		return
	}

	// Browser is returning export data (png/svg)
	if exportData, hasExport := data["exportData"]; hasExport {
		stateMu.Lock()
		state := stateSto[sessionID]
		if state != nil {
			exportID, _ := data["exportId"].(float64)
			if exportID != 0 && int64(exportID) == state.ExportID {
				s, _ := exportData.(string)
				state.ExportData = s
				state.ExportFormat = ""
				state.ExportXML = ""
				state.ExportOptions = nil
				state.ExportID = 0
				mcplog.Debug("Export data received for session=%s", sessionID)
			} else {
				mcplog.Debug("Ignored a late export result for session=%s", sessionID)
			}
		}
		stateMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}

	// A push can come before the tab's first poll after a restart: recover
	// the saved file first, so it is compared with that and never
	// overwrites it unseen
	ensureSessionStateInitialized(sessionID)

	stateMu.Lock()
	current := stateSto[sessionID]

	// A tab of this version names the state its push is based on. Another
	// state (the server lost the one it knew, or the tab has not polled
	// yet): refused, and the tab's next poll decides whose diagram wins.
	if current != nil {
		if dataStateID, hasStateID := data["stateId"]; hasStateID {
			stateID, _ := dataStateID.(string)
			if stateID != current.StateID {
				version := current.Version
				stateMu.Unlock()
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":       "Session was recreated",
					"stateChanged": true,
					"version":     version,
				})
				return
			}
			// What a recovering tab showed: kept in history only
			if source, _ := data["source"].(string); source == "recover" {
				xml, xmlOK := data["xml"].(string)
				saved := xmlOK && xml != "" && xml != current.XML
				if saved {
					svg, _ := data["svg"].(string)
					store.AddHistory(sessionID, xml, svg)
				}
				version := current.Version
				stateMu.Unlock()
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":          "Diagram changed on the server",
					"version":        version,
					"savedToHistory": saved,
				})
				return
			}
		}
	}

	// The browser edited a version older than the latest AI write (it has
	// not loaded that write yet). Keep the AI write; the browser loads it
	// on its next poll. A sync reply is also stale after a newer write of
	// the browser's own (a user edit saved while the export ran).
	if baseVersion, isNum := data["baseVersion"].(float64); isNum {
		var serverVersion, version int64
		if current != nil {
			serverVersion, version = current.ServerVersion, current.Version
		}
		source, _ := data["source"].(string)
		if baseVersion < float64(serverVersion) ||
			(source == "sync" && baseVersion < float64(version)) {
			savedToHistory := false
			if source == "sync" {
				// A stale sync reply: the store already holds the newer AI
				// write, so the sync is done.
				if current != nil {
					current.SyncRequested = 0
				}
			} else if xml, xmlOK := data["xml"].(string); xmlOK && xml != "" {
				// A user edit lost the race with an AI write. Keep it in
				// history so the user can restore it.
				svg, _ := data["svg"].(string)
				store.AddHistory(sessionID, xml, svg)
				savedToHistory = true
			}
			var v any
			if current != nil {
				v = current.Version
			}
			stateMu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":          "Diagram changed on the server",
				"version":        v,
				"savedToHistory": savedToHistory,
			})
			return
		}
	}

	xml, xmlOK := data["xml"].(string)
	stateMu.Unlock()
	if !xmlOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "xml must be a string"})
		return
	}
	svg, _ := data["svg"].(string)
	source, _ := data["source"].(string)
	newVersion := SetState(sessionID, xml, svg, true, true)
	// The browser saved a change of the user's (a sync reply is no change)
	if source != "sync" {
		if saved := GetState(sessionID); saved != nil {
			saved.UserEdited = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "version": newVersion})
}

func handleHistoryAPI(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("Method Not Allowed"))
		return
	}
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sessionId required"})
		return
	}
	history := store.GetHistory(sessionID)
	entries := make([]map[string]any, len(history))
	for i, e := range history {
		entries[i] = map[string]any{"index": i, "id": e.ID, "svg": e.SVG}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(history)})
}

func handleRestoreAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("Method Not Allowed"))
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON"})
		return
	}
	sessionID, _ := data["sessionId"].(string)
	idNum, idOK := data["id"].(float64)
	if sessionID == "" || !idOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sessionId and id required"})
		return
	}

	entry, found := store.GetHistoryEntry(sessionID, int64(idNum))
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Entry not found"})
		return
	}

	// Edits in the browser since the last entry are not in history yet:
	// keep them, so the restore can be undone (any state besides a blank
	// page; a cleared document with its own pages counts)
	if current := GetState(sessionID); current != nil &&
		diagram.ContentFingerprint(current.XML, true) != diagram.ContentFingerprint(diagram.BlankMxfile, true) {
		KeepInHistory(sessionID, current.XML, current.SVG)
	}
	newVersion := SetState(sessionID, entry.XML, "", false, true)
	store.AddHistory(sessionID, entry.XML, entry.SVG)

	mcplog.Info("Restored session %s to history entry %d", sessionID, int64(idNum))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "newVersion": newVersion})
}

func handleHistorySVGAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte("Method Not Allowed"))
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON"})
		return
	}
	sessionID, _ := data["sessionId"].(string)
	svg, _ := data["svg"].(string)
	if sessionID == "" || svg == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sessionId and svg required"})
		return
	}

	// The browser took it of the server write it loaded, named by the
	// state and version. One that arrives after the next server write, or
	// for a state since lost, is dropped; a browser write since (a sync
	// reply) leaves that write's image valid.
	stateMu.Lock()
	defer stateMu.Unlock()
	state := stateSto[sessionID]
	if state == nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	stateID, _ := data["stateId"].(string)
	version, _ := data["version"].(float64)
	if state.StateID == stateID && state.ServerVersion == int64(version) && state.ServerXML != "" {
		store.UpdateLastHistorySVG(sessionID, svg, state.ServerXML)
		if state.XML == state.ServerXML {
			state.SVG = svg
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// --- Preview page -------------------------------------------------------

// The preview page is embedded in the binary.
var baseTemplate = func() string {
	index, err := assets.PreviewFS.ReadFile("preview/index.html")
	if err != nil {
		mcplog.Error("Missing embedded preview template: %v", err)
		return ""
	}
	css, err := assets.PreviewFS.ReadFile("preview/preview.css")
	if err != nil {
		mcplog.Error("Missing embedded preview css: %v", err)
		css = nil
	}
	script, err := assets.PreviewFS.ReadFile("preview/preview.js")
	if err != nil {
		mcplog.Error("Missing embedded preview script: %v", err)
		script = nil
	}
	html := strings.Replace(string(index), "{{CSS}}", string(css), 1)
	return strings.Replace(html, "{{SCRIPT}}", string(script), 1)
}()

// scriptJSON builds a JSON string literal that is safe inside a <script>
// element: `<` becomes the escape sequence, so "</script>" can never end
// the element early.
func scriptJSON(value string) string {
	b, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return strings.Replace(string(b), "<", `\u003c`, -1)
}

// normalizeURL removes the trailing slash to avoid double slashes in the
// iframe src.
func normalizeURL(u string) string {
	return strings.TrimSuffix(u, "/")
}

func getHTMLPage(sessionID string) string {
	badge := ""
	if sessionID != "" {
		last := sessionID
		if len(last) > 8 {
			last = last[len(last)-8:]
		}
		badge = fmt.Sprintf(`<span class="session">%s</span>`, last)
	}
	disabled := ""
	if sessionID == "" {
		disabled = "disabled"
	}
	page := strings.Replace(baseTemplate, "{{SESSION_BADGE}}", badge, 1)
	page = strings.ReplaceAll(page, "{{DISABLED}}", disabled)
	page = strings.Replace(page, "{{DRAWIO_URL}}", normalizeURL(DrawioBaseURL), 1)
	page = strings.Replace(page, "{{SESSION_JSON}}", scriptJSON(sessionID), 1)
	page = strings.Replace(page, "{{ORIGIN_JSON}}", scriptJSON(drawioOrigin), 1)
	return page
}
