package capabilities

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestCatalogueValidates(t *testing.T) {
	if err := catalog.Validate(); err != nil {
		t.Fatalf("capabilities.json failed lint: %v", err)
	}
	if got, want := len(catalog.Tools), 14; got != want {
		t.Fatalf("tool count = %d, want %d (13 original + get_guide)", got, want)
	}
}

func TestDescriptionsAreShortAndStructured(t *testing.T) {
	for _, c := range All() {
		d := c.Description()
		if len(d) > maxDescriptionChars {
			t.Errorf("%s: description is %d chars", c.Name, len(d))
		}
		for _, want := range []string{"Not for:", "Requires:"} {
			if !strings.Contains(d, want) {
				t.Errorf("%s: description missing %q", c.Name, want)
			}
		}
	}
}

func TestCriticalExclusionsAreReciprocal(t *testing.T) {
	// These are the pairs the model most often confuses. Both sides must
	// point at each other so the disambiguation survives either entry point.
	pairs := [][2]string{
		{"create_new_diagram", "add_page"},
		{"create_new_diagram", "edit_diagram"},
		{"screenshot_diagram", "export_diagram"},
		{"get_guide", "get_drawing_guide"},
	}
	missing := MissingReciprocals()
	for _, p := range pairs {
		for _, m := range missing {
			if strings.HasPrefix(m, p[0]+" -> "+p[1]+" ") || strings.HasPrefix(m, p[1]+" -> "+p[0]+" ") {
				t.Errorf("critical exclusion not reciprocal: %s", m)
			}
		}
	}
	t.Logf("informational: %d intentionally asymmetric exclusions", len(missing))
}

func TestIndexIsCompleteAndSorted(t *testing.T) {
	idx := Index()
	if len(idx) != len(All()) {
		t.Fatalf("index has %d entries, catalogue has %d", len(idx), len(All()))
	}
	for i := 1; i < len(idx); i++ {
		if idx[i-1].Name > idx[i].Name {
			t.Fatalf("index not sorted at %d: %s > %s", i, idx[i-1].Name, idx[i].Name)
		}
	}
	for _, e := range idx {
		if e.WhenToUse == "" || e.Tier == "" {
			t.Errorf("%s: incomplete index entry", e.Name)
		}
	}
}

// regressionQueries is the human-facing regression suite. Each row is a real
// user request and the tool chain the agent is expected to follow. Run these
// through an actual MCP client after any description change; this test keeps
// the expected tool names alive so renames cannot silently break the list.
var regressionQueries = []struct {
	query  string
	expect []string
}{
	{"画一个登录时序图", []string{"start_session", "get_guide", "create_new_diagram", "screenshot_diagram"}},
	{"加载 ~/a.drawio 并加一个 Redis 节点", []string{"start_session", "load_diagram", "get_diagram", "edit_diagram"}},
	{"再加一页数据模型", []string{"list_pages", "add_page"}},
	{"把数据库框改成红色", []string{"edit_diagram"}},
	{"导出 PNG 到 ~/arch.png", []string{"export_diagram"}},
	{"看看现在画成什么样", []string{"screenshot_diagram"}},
	{"用 AWS 图标画架构图", []string{"get_shape_library", "create_new_diagram"}},
	{"黑白极简风格重画", []string{"get_guide", "create_new_diagram"}},
	{"删掉第二页", []string{"list_pages", "delete_page"}},
	{"存成还能编辑的 svg", []string{"export_diagram"}},
	{"当前有哪些页", []string{"list_pages"}},
	{"这个图里有哪些 cell id", []string{"get_diagram"}},
}

func TestRegressionQueriesReferenceKnownTools(t *testing.T) {
	known := map[string]bool{}
	for _, c := range All() {
		known[c.Name] = true
	}
	for _, q := range regressionQueries {
		for _, name := range q.expect {
			if !known[name] {
				t.Errorf("query %q expects unknown tool %q", q.query, name)
			}
		}
	}
	if got, want := len(regressionQueries), 12; got != want {
		t.Fatalf("regression suite has %d cases, want %d", got, want)
	}
}

// TestToolOptionsMatchCatalogue verifies what actually lands in tools/list:
// the compiled short description and all four behaviour hints.
func TestToolOptionsMatchCatalogue(t *testing.T) {
	for _, c := range All() {
		tool := mcp.NewTool(c.Name, ToolOptions(c.Name)...)
		if tool.Description != c.Description() {
			t.Errorf("%s: description does not match catalogue", c.Name)
		}
		a := tool.Annotations
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Fatalf("%s: a behaviour hint is unset; clients assume the worst case", c.Name)
		}
		if *a.ReadOnlyHint != c.Annotations.ReadOnly ||
			*a.DestructiveHint != c.Annotations.Destructive ||
			*a.IdempotentHint != c.Annotations.Idempotent ||
			*a.OpenWorldHint != c.Annotations.OpenWorld {
			t.Errorf("%s: annotations do not match catalogue", c.Name)
		}
	}
}

func TestDestructiveToolsAreMarked(t *testing.T) {
	for _, name := range []string{"create_new_diagram", "load_diagram", "delete_page"} {
		tool := mcp.NewTool(name, ToolOptions(name)...)
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("%s replaces or removes content but is not marked destructive", name)
		}
	}
	for _, name := range []string{"get_diagram", "screenshot_diagram", "list_pages"} {
		tool := mcp.NewTool(name, ToolOptions(name)...)
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is read-only but lacks readOnlyHint", name)
		}
	}
}
