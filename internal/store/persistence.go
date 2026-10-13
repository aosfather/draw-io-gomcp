// Auto-save of each session's latest diagram as a plain .drawio file, so a
// diagram survives the MCP process (hosts start a new one when a
// conversation is resumed). Like the web app's saved sessions: written one
// second after the last change, at most 50 kept. History is not saved.
package store

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"aosfather.drawio-mcp/internal/diagram"
	"aosfather.drawio-mcp/internal/mcplog"
)

const (
	autosaveDelayMS = 1000
	autosaveMaxFile = 50
)

// isBlank reports whether the XML is the blank page the browser shows
// before any drawing (page names count: empty pages the user named or
// added are kept).
func isBlank(xml string) bool {
	return !diagram.HasCells(xml) &&
		diagram.ContentFingerprint(xml, true) == diagram.ContentFingerprint(diagram.BlankMxfile, true)
}

// ExpandHome expands a leading ~ to the home directory (shells do this,
// MCP hosts don't).
func ExpandHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		return home + p[1:]
	}
	return p
}

// DefaultDataDir reads DRAWIO_DATA_DIR, default ~/.next-ai-drawio.
// Returns nil when saving is disabled ("off").
func DefaultDataDir() string {
	dir := os.Getenv("DRAWIO_DATA_DIR")
	if dir == "off" {
		return ""
	}
	if dir != "" {
		return ExpandHome(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".next-ai-drawio"
	}
	return filepath.Join(home, ".next-ai-drawio")
}

// isGone reports whether the file surely does not exist (not merely out of
// reach).
func isGone(path string) bool {
	_, err := os.Stat(path)
	return err != nil && os.IsNotExist(err)
}

// Session file names this tool created: mcp-<time in base 36>-<random>,
// made by start_session. DRAWIO_DATA_DIR may be a folder that also holds
// the user's diagrams.
var sessionFileRe = regexp.MustCompile(`^mcp-[0-9a-z]{8}-[0-9a-z]{1,6}\.drawio$`)

type pendingSave struct {
	xml   string
	timer *time.Timer
}

// Autosaver writes each session's latest diagram after a short delay,
// atomically (tmp file + rename).
type Autosaver struct {
	mu       sync.Mutex
	dir      string
	delayMS  int
	maxFiles int
	pending  map[string]*pendingSave
	// Saved files that could not be read back: never written over, since
	// the session then shows something else than what they hold. Cleared
	// once the file is read, or is surely gone (a folder without permission
	// also makes a file look missing).
	unreadable map[string]bool
}

// NewAutosaver creates an autosaver writing into dir ("" disables saving).
// delayMS or maxFiles <= 0 selects the default.
func NewAutosaver(dir string, delayMS, maxFiles int) *Autosaver {
	if delayMS <= 0 {
		delayMS = autosaveDelayMS
	}
	if maxFiles <= 0 {
		maxFiles = autosaveMaxFile
	}
	return &Autosaver{
		dir:        dir,
		delayMS:    delayMS,
		maxFiles:   maxFiles,
		pending:    map[string]*pendingSave{},
		unreadable: map[string]bool{},
	}
}

// PathFor returns the path of a session's file, or "" when saving is off.
func (a *Autosaver) PathFor(sessionID string) string {
	if a.dir == "" {
		return ""
	}
	return filepath.Join(a.dir, sessionID+".drawio")
}

// Load returns the session's saved diagram, or "".
func (a *Autosaver) Load(sessionID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	path := a.PathFor(sessionID)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			delete(a.unreadable, path)
			return ""
		}
		mcplog.Warn("Could not read the saved diagram %s: %v", path, err)
		a.unreadable[path] = true
		return ""
	}
	delete(a.unreadable, path)
	return string(data)
}

// Schedule stores the XML and (re)starts the write timer.
func (a *Autosaver) Schedule(sessionID, xml string) {
	if a.dir == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.pending[sessionID]; p != nil && p.timer != nil {
		p.timer.Stop()
	}
	a.pending[sessionID] = &pendingSave{
		xml:   xml,
		timer: time.AfterFunc(time.Duration(a.delayMS)*time.Millisecond, func() { a.write(sessionID) }),
	}
}

// Flush writes every pending save now (on shutdown).
func (a *Autosaver) Flush() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.pending))
	for id, p := range a.pending {
		if p.timer != nil {
			p.timer.Stop()
		}
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.write(id)
	}
}

func (a *Autosaver) write(sessionID string) {
	a.mu.Lock()
	entry := a.pending[sessionID]
	delete(a.pending, sessionID)
	path := a.PathFor(sessionID)
	if entry == nil || path == "" {
		a.mu.Unlock()
		return
	}
	if a.unreadable[path] {
		// Deleted meanwhile: nothing left to protect
		if isGone(path) {
			delete(a.unreadable, path)
		} else {
			mcplog.Warn("Not saving %s: it could not be read, so it may hold work this session does not show", path)
			a.mu.Unlock()
			return
		}
	}
	a.mu.Unlock()

	_, err := os.Stat(path)
	isNew := os.IsNotExist(err)
	// A blank page the browser shows before any drawing: nothing to keep
	if isNew && isBlank(entry.xml) {
		return
	}
	if err := os.MkdirAll(a.dir, 0o755); err != nil {
		mcplog.Warn("Auto-save failed for %s: %v", path, err)
		return
	}
	// Write to a temporary file first so a crash never leaves half a file
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(entry.xml), 0o644); err != nil {
		mcplog.Warn("Auto-save failed for %s: %v", path, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		mcplog.Warn("Auto-save failed for %s: %v", path, err)
		return
	}
	if isNew {
		a.removeOldest()
	}
}

func (a *Autosaver) removeOldest() {
	if a.dir == "" {
		return
	}
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	type fileMTime struct {
		name  string
		mtime int64
	}
	var files []fileMTime
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !sessionFileRe.MatchString(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileMTime{name, info.ModTime().UnixMilli()})
	}
	// Newest first
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })
	for _, f := range files[min(a.maxFiles, len(files)):] {
		_ = os.Remove(filepath.Join(a.dir, f.name))
	}
}
