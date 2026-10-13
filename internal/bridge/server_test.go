package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aosfather.drawio-mcp/internal/diagram"
	"aosfather.drawio-mcp/internal/store"
)

func listenLocal(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func postJSON(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Host = "localhost:6002"
	rec := httptest.NewRecorder()
	routeRequest(rec, req)
	return rec
}

func getSessionState(t *testing.T, sessionID string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/state?sessionId="+sessionID, nil)
	req.Host = "localhost:6002"
	rec := httptest.NewRecorder()
	routeRequest(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET state = %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const cellXML = `<mxfile><diagram name="Page-1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"/></root></mxGraphModel></diagram></mxfile>`

func TestSetStateVersionAndStateID(t *testing.T) {
	id := "mcp-teststate-1"
	defer func() { stateMu.Lock(); delete(stateSto, id); stateMu.Unlock(); store.ClearHistory(id) }()

	v1 := SetState(id, cellXML, "", false, true)
	v2 := SetState(id, cellXML, "", false, true)
	if v1 != 1 || v2 != 2 {
		t.Fatalf("versions = %d %d", v1, v2)
	}
	s1, s2 := GetState(id), GetState(id)
	if s1.StateID != s2.StateID {
		t.Fatal("stateId changed on write")
	}
	// a server write resets userEdited and records the server version
	s1.UserEdited = true
	SetState(id, cellXML, "", false, true)
	s2 = GetState(id)
	if s2.UserEdited || s2.ServerVersion != v2+1 || s2.ServerXML != cellXML {
		t.Fatalf("state = %+v", s2)
	}
}

func TestStateAPIPushAndConflicts(t *testing.T) {
	id := "mcp-apitest-1"
	defer func() { stateMu.Lock(); delete(stateSto, id); stateMu.Unlock(); store.ClearHistory(id) }()

	// first poll creates the session
	st := getSessionState(t, id)
	stateID := st["stateId"].(string)
	if st["blank"] != true {
		t.Fatalf("blank = %v", st["blank"])
	}

	// a browser push based on the current state is accepted
	rec := postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "stateId": stateID, "xml": cellXML, "svg": "S",
	})
	if rec.Code != 200 {
		t.Fatalf("push = %d %s", rec.Code, rec.Body.String())
	}
	st = getSessionState(t, id)
	if st["blank"] != false || st["version"].(float64) != 2 {
		t.Fatalf("state = %v", st)
	}
	if s := GetState(id); !s.UserEdited || s.SVG != "S" {
		t.Fatalf("push not recorded as user edit: %+v", s)
	}

	// a push naming another state is refused
	rec = postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "stateId": "other", "xml": cellXML,
	})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Session was recreated") {
		t.Fatalf("conflict = %d %s", rec.Code, rec.Body.String())
	}

	// a stale base version keeps the server write and saves to history
	// (AI write bumps serverVersion)
	SetState(id, "<server-write/>", "", false, true)
	rec = postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "xml": cellXML, "baseVersion": 1,
	})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "Diagram changed on the server") {
		t.Fatalf("stale push = %d %s", rec.Code, rec.Body.String())
	}
	if s := GetState(id); s.XML != "<server-write/>" {
		t.Fatalf("stale push overwrote the server write: %s", s.XML)
	}
	if len(store.GetHistory(id)) == 0 {
		t.Fatal("stale push not kept in history")
	}

	// a sync reply older than the latest server write only clears the
	// sync request, without touching history
	if !RequestSync(id) {
		t.Fatal("sync request refused")
	}
	historyLen := len(store.GetHistory(id))
	SetState(id, "<second-write/>", "", false, true)
	rec = postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "xml": cellXML, "baseVersion": 2, "source": "sync",
	})
	if rec.Code != 409 {
		t.Fatalf("stale sync = %d", rec.Code)
	}
	if s := GetState(id); s.SyncRequested != 0 || s.XML != "<second-write/>" {
		t.Fatalf("stale sync mishandled: %+v", s)
	}
	if len(store.GetHistory(id)) != historyLen {
		t.Fatal("a sync reply was kept in history")
	}

	// export data is stored when the id matches the pending request
	if !RequestExport(id, ExportPNG, "", &ExportOptions{Width: intPtr(800)}) {
		t.Fatal("export request refused")
	}
	firstExportID := GetState(id).ExportID
	rec = postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "exportData": "PNGDATA", "exportId": firstExportID,
	})
	if rec.Code != 200 {
		t.Fatalf("export return = %d", rec.Code)
	}
	if s := GetState(id); s.ExportData != "PNGDATA" || s.ExportFormat != "" {
		t.Fatalf("export data = %+v", s)
	}
	// a late export result of another export number is ignored
	RequestExport(id, ExportSVG, "", nil)
	rec = postJSON(t, "/api/state", map[string]any{
		"sessionId": id, "exportData": "LATE", "exportId": firstExportID,
	})
	if s := GetState(id); s.ExportData == "LATE" {
		t.Fatal("late export result accepted")
	}
}

func intPtr(i int) *int { return &i }

func TestRestoreAPI(t *testing.T) {
	id := "mcp-restore-1"
	defer func() { stateMu.Lock(); delete(stateSto, id); stateMu.Unlock(); store.ClearHistory(id) }()

	SetState(id, cellXML, "", false, true)
	idx := store.AddHistory(id, "<old-diagram/>", "svg1")
	entryID := int64(store.GetHistory(id)[idx].ID)

	rec := postJSON(t, "/api/restore", map[string]any{"sessionId": id, "id": entryID})
	if rec.Code != 200 {
		t.Fatalf("restore = %d %s", rec.Code, rec.Body.String())
	}
	s := GetState(id)
	if s.XML != "<old-diagram/>" {
		t.Fatalf("xml = %s", s.XML)
	}
	// the replaced state went to history, and the restored entry is last
	h := store.GetHistory(id)
	if h[len(h)-1].XML != "<old-diagram/>" || h[len(h)-2].XML != cellXML {
		t.Fatalf("history = %+v", h)
	}
	// unknown entry
	rec = postJSON(t, "/api/restore", map[string]any{"sessionId": id, "id": 99999})
	if rec.Code != 404 {
		t.Fatalf("missing entry = %d", rec.Code)
	}
}

func TestLocalRequestGuard(t *testing.T) {
	id := "mcp-local-1"
	defer func() { stateMu.Lock(); delete(stateSto, id); stateMu.Unlock() }()

	req := httptest.NewRequest(http.MethodGet, "/api/state?sessionId="+id, nil)
	req.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	routeRequest(rec, req)
	if rec.Code != 403 {
		t.Fatalf("foreign host = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/state", strings.NewReader("{}"))
	req.Host = "localhost:6002"
	req.Header.Set("Origin", "http://other-site.example")
	rec = httptest.NewRecorder()
	routeRequest(rec, req)
	if rec.Code != 403 {
		t.Fatalf("foreign origin = %d", rec.Code)
	}
}

func TestIndexPage(t *testing.T) {
	defer func() { stateMu.Lock(); delete(stateSto, "mcp-index-1"); stateMu.Unlock() }()

	req := httptest.NewRequest(http.MethodGet, "/?mcp=mcp-index-1", nil)
	req.Host = "localhost:6002"
	rec := httptest.NewRecorder()
	routeRequest(rec, req)
	if rec.Code != 200 {
		t.Fatalf("index = %d", rec.Code)
	}
	html := rec.Body.String()
	if strings.Contains(html, "{{") {
		t.Fatal("unfilled placeholder")
	}
	if !strings.Contains(html, "mcp-index-1") || !strings.Contains(html, "https://embed.diagrams.net") {
		t.Fatal("session or drawio url missing")
	}

	// invalid session id
	req = httptest.NewRequest(http.MethodGet, "/?mcp=BAD_ID", nil)
	req.Host = "localhost:6002"
	rec = httptest.NewRecorder()
	routeRequest(rec, req)
	if rec.Code != 400 {
		t.Fatalf("bad id = %d", rec.Code)
	}
}

func TestStartHTTPServerPortProbe(t *testing.T) {
	// occupy a port, then start there: the server must move to the next one
	ln, err := listenLocal(defaultPort)
	if err != nil {
		t.Skipf("cannot occupy port %d: %v", defaultPort, err)
	}
	defer ln.Close()

	port, err := StartHTTPServer(defaultPort)
	if err != nil {
		t.Fatalf("start = %v", err)
	}
	defer Shutdown()
	if port != defaultPort+1 {
		t.Fatalf("port = %d", port)
	}
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<html") && !strings.Contains(string(body), "<!DOCTYPE") {
		t.Fatalf("body = %.80s", body)
	}
}

func TestEnsureSessionRestoreFromLoader(t *testing.T) {
	id := "mcp-restore2-1"
	defer func() { stateMu.Lock(); delete(stateSto, id); stateMu.Unlock(); store.ClearHistory(id) }()

	loaded := false
	OnSessionRecreate(func(sid string) string {
		loaded = true
		return diagram.BlankMxfile
	})
	defer OnSessionRecreate(nil)

	ensureSessionStateInitialized(id)
	if !loaded {
		t.Fatal("loader not called")
	}
	// the restored state is not marked blank (it holds real content)
	if s := GetState(id); s == nil || s.Blank {
		t.Fatalf("state = %+v", s)
	}
}
