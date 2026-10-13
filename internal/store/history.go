// Simple diagram history - matches the web app pattern.
// Stores {xml, svg} entries in a circular buffer.
package store

import (
	"sync"

	"aosfather.drawio-mcp/internal/mcplog"
)

const maxHistory = 20

// HistoryEntry is one diagram version. ID is stable across shifts of the
// circular buffer.
type HistoryEntry struct {
	ID  int64
	XML string
	SVG string
}

var (
	historyMu    sync.Mutex
	historyStore = map[string][]*HistoryEntry{}
	nextEntryID  int64
)

// AddHistory appends an entry, deduplicating against the last one. Returns
// the new entry's index.
//
// Dedupe: skip if same as last entry (any other change, also of page
// settings only, is a new version; keepInHistory leaves out draw.io's
// copy of a server write)
func AddHistory(sessionID, xml, svg string) int {
	historyMu.Lock()
	defer historyMu.Unlock()
	history := historyStore[sessionID]

	// The empty svg of a previous entry is filled in when the browser
	// sends a thumbnail later
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.XML == xml {
			if svg != "" && last.SVG == "" {
				last.SVG = svg
			}
			return len(history) - 1
		}
	}

	history = append(history, &HistoryEntry{ID: nextEntryID, XML: xml, SVG: svg})
	nextEntryID++

	// Circular buffer
	if len(history) > maxHistory {
		history = history[1:]
	}
	historyStore[sessionID] = history

	mcplog.Debug("History: session=%s, entries=%d", sessionID, len(history))
	return len(history) - 1
}

// GetHistory returns a copy of the session's entries, oldest first.
func GetHistory(sessionID string) []HistoryEntry {
	historyMu.Lock()
	defer historyMu.Unlock()
	entries := historyStore[sessionID]
	out := make([]HistoryEntry, len(entries))
	for i, e := range entries {
		out[i] = *e
	}
	return out
}

// GetHistoryEntry looks up an entry by its id; the array index shifts as
// old entries drop.
func GetHistoryEntry(sessionID string, id int64) (HistoryEntry, bool) {
	historyMu.Lock()
	defer historyMu.Unlock()
	for _, e := range historyStore[sessionID] {
		if e.ID == id {
			return *e, true
		}
	}
	return HistoryEntry{}, false
}

// ClearHistory drops the session's history.
func ClearHistory(sessionID string) {
	historyMu.Lock()
	defer historyMu.Unlock()
	delete(historyStore, sessionID)
}

// UpdateLastHistorySVG gives the last entry the image the browser took of
// shownXml, the diagram it just loaded, when that entry is this diagram.
func UpdateLastHistorySVG(sessionID, svg, shownXML string) bool {
	historyMu.Lock()
	defer historyMu.Unlock()
	history := historyStore[sessionID]
	if len(history) == 0 {
		return false
	}
	last := history[len(history)-1]
	if last.SVG == "" && last.XML == shownXML {
		last.SVG = svg
		return true
	}
	return false
}
