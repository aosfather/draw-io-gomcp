package tools

import (
	"strings"
	"testing"

	"aosfather.drawio-mcp/internal/capabilities"
)

func TestGetGuideReturnsOnlyRequestedSections(t *testing.T) {
	text, err := GetGuide([]string{"core", "xml"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# guide://core", "# guide://xml", "next_step:", `get_guide(sections=["edges"])`} {
		if !strings.Contains(text, want) {
			t.Errorf("result missing %q", want)
		}
	}
	if strings.Contains(text, "# guide://edges") || strings.Contains(text, "# guide://export") {
		t.Error("unrequested sections leaked into the result")
	}
}

func TestGetGuideIsDeterministicAndFullyExpanded(t *testing.T) {
	a, err := GetGuide([]string{"edges", "core"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := GetGuide([]string{"core", "edges"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("section order depends on request order; output must be stable for prompt caching")
	}
	if strings.Index(a, "# guide://core") > strings.Index(a, "# guide://edges") {
		t.Error("sections are not in canonical order")
	}
	for name, body := range guideSectionText {
		if strings.Contains(body, "{{") {
			t.Errorf("section %q still has an unexpanded placeholder", name)
		}
	}
}

func TestGetGuideRejectsBadInput(t *testing.T) {
	if _, err := GetGuide(nil); err == nil || !strings.Contains(err.Error(), "Available:") {
		t.Errorf("empty sections should list available sections, got %v", err)
	}
	if _, err := GetGuide([]string{"core", "nope"}); err == nil || !strings.Contains(err.Error(), "unknown section") {
		t.Errorf("unknown section should be rejected, got %v", err)
	}
}

func TestFullGuideContainsEverySection(t *testing.T) {
	for _, name := range GuideSections() {
		if !strings.Contains(DrawingGuide, "# guide://"+name) {
			t.Errorf("DrawingGuide is missing section %q", name)
		}
	}
}

func TestShapeLibraryCatalogueIsNotInToolsList(t *testing.T) {
	d := capabilities.Must("get_shape_library").Description()
	for _, leaked := range []string{"cisco19", "alibaba_cloud", "fluidpower", "Cloud:"} {
		if strings.Contains(d, leaked) {
			t.Errorf("shape library catalogue leaked into tools/list via %q", leaked)
		}
	}
	if len(d) > 600 {
		t.Errorf("get_shape_library description is %d chars", len(d))
	}
}
