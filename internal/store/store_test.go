package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aosfather.drawio-mcp/internal/diagram"
)

func TestHistoryCircularBufferAndStableIDs(t *testing.T) {
	id := "test-session-history"
	defer ClearHistory(id)

	first := AddHistory(id, "<a/>", "")
	for i := 0; i < 25; i++ {
		AddHistory(id, strings.Repeat("x", i+1), "")
	}
	h := GetHistory(id)
	if len(h) != 20 {
		t.Fatalf("len = %d", len(h))
	}
	if h[0].XML != strings.Repeat("x", 6) {
		t.Fatalf("oldest kept entry = %q", h[0].XML)
	}
	// ids are stable across shifts
	if int64(h[0].ID) <= int64(first) {
		t.Fatalf("ids not increasing: first=%d oldest=%d", first, h[0].ID)
	}
	// dedupe fills in the svg of the last entry
	idx := AddHistory(id, strings.Repeat("x", 25), "svg-data")
	if idx != len(h)-1 {
		t.Fatalf("dedupe returned index %d", idx)
	}
	entry, ok := GetHistoryEntry(id, h[len(h)-1].ID)
	if !ok || entry.SVG != "svg-data" {
		t.Fatalf("svg not filled: %+v", entry)
	}
}

func TestUpdateLastHistorySVG(t *testing.T) {
	id := "test-session-svg"
	defer ClearHistory(id)
	AddHistory(id, "<dia/>", "")
	if !UpdateLastHistorySVG(id, "S", "<dia/>") {
		t.Fatal("update refused")
	}
	if UpdateLastHistorySVG(id, "S2", "<other/>") {
		t.Fatal("updated a non-matching entry")
	}
	if UpdateLastHistorySVG(id, "S3", "<dia/>") {
		t.Fatal("overwrote an existing svg")
	}
}

func TestExpandHomeAndDefaultDataDir(t *testing.T) {
	if p := ExpandHome("~"); p == "~" || strings.HasPrefix(p, "~") {
		t.Fatalf("home not expanded: %q", p)
	}
	if p := ExpandHome("~/sub"); !strings.HasSuffix(p, "sub") || strings.HasPrefix(p, "~") {
		t.Fatalf("home not expanded: %q", p)
	}
	if p := ExpandHome("/tmp/x"); p != "/tmp/x" {
		t.Fatalf("path changed: %q", p)
	}
	t.Setenv("DRAWIO_DATA_DIR", "off")
	if DefaultDataDir() != "" {
		t.Fatal("off not honored")
	}
	t.Setenv("DRAWIO_DATA_DIR", "~/custom")
	if p := DefaultDataDir(); !strings.HasSuffix(p, "custom") || strings.HasPrefix(p, "~") {
		t.Fatalf("dir = %q", p)
	}
}

func TestAutosaver(t *testing.T) {
	dir := t.TempDir()
	a := NewAutosaver(dir, 20, 2)

	// no file before the delay
	a.Schedule("mcp-aaaaaaaa-1", "<mxfile><diagram/></mxfile>")
	if _, err := os.Stat(filepath.Join(dir, "mcp-aaaaaaaa-1.drawio")); !os.IsNotExist(err) {
		t.Fatal("written before delay")
	}
	time.Sleep(80 * time.Millisecond)
	data, err := os.ReadFile(filepath.Join(dir, "mcp-aaaaaaaa-1.drawio"))
	if err != nil || string(data) != "<mxfile><diagram/></mxfile>" {
		t.Fatalf("data = %q err = %v", data, err)
	}

	// the newest save wins
	a.Schedule("mcp-bbbbbbbb-2", "<v1/>")
	a.Schedule("mcp-bbbbbbbb-2", "<v2/>")
	time.Sleep(80 * time.Millisecond)
	if data, err := os.ReadFile(filepath.Join(dir, "mcp-bbbbbbbb-2.drawio")); err != nil || string(data) != "<v2/>" {
		t.Fatalf("data = %q err = %v", data, err)
	}

	// a blank page is not written as a new file
	a.Schedule("mcp-cccccccc-3", diagram.BlankMxfile)
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "mcp-cccccccc-3.drawio")); !os.IsNotExist(err) {
		t.Fatal("blank page written")
	}

	// only the 2 newest mcp-*.drawio files are kept
	for i := 0; i < 4; i++ {
		a.Schedule("mcp-dddddd0"+string(rune('0'+i))+"-x", "<d/>")
		time.Sleep(30 * time.Millisecond)
	}
	a.Flush()
	time.Sleep(120 * time.Millisecond)
	entries, _ := os.ReadDir(dir)
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".drawio") {
			count++
		}
	}
	if count > 2 {
		t.Fatalf("%d files kept", count)
	}

	// a file that could not be read back is never overwritten
	a2 := NewAutosaver(dir, 10, 50)
	locked := filepath.Join(dir, "mcp-eeeeeeee-9.drawio")
	if err := os.WriteFile(locked, []byte("<locked/>"), 0o000); err != nil {
		t.Fatal(err)
	}
	if got := a2.Load("mcp-eeeeeeee-9"); got != "" {
		t.Fatalf("load = %q", got)
	}
	a2.Schedule("mcp-eeeeeeee-9", "<new/>")
	a2.Flush()
	time.Sleep(60 * time.Millisecond)
	// make it readable again to verify the content is unchanged
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(locked)
	if err != nil || string(data) != "<locked/>" {
		t.Fatalf("protected file changed: %q %v", data, err)
	}
}
