// Shape and icon library docs (embedded docs/shape-libraries/*.md), the
// same files the web app's get_shape_library tool reads. Ported from
// shape-library.ts.
package tools

import (
	"fmt"
	"strings"

	"aosfather.drawio-mcp/internal/assets"
)

// ShapeLibraryGroups lists the libraries by group.
var ShapeLibraryGroups = map[string][]string{
	"Cloud":       {"aws4", "azure2", "gcp2", "alibaba_cloud", "openstack", "salesforce"},
	"Networking":  {"cisco19", "network", "kubernetes", "vvd", "rack"},
	"Business":    {"bpmn", "lean_mapping"},
	"General":     {"flowchart", "basic", "arrows2", "infographic", "sitemap"},
	"UI/Mockups":  {"android", "material_design"},
	"Enterprise":  {"citrix", "sap", "mscae", "atlassian"},
	"Engineering": {"fluidpower", "electrical", "pid", "cabinets", "floorplan"},
	"Icons":       {"webicons"},
}

// The groups in display order (Go maps are unordered).
var shapeLibraryGroupOrder = []string{
	"Cloud", "Networking", "Business", "General", "UI/Mockups", "Enterprise", "Engineering", "Icons",
}

// ShapeLibraryList is the libraries by group, one "- Group: a, b" line
// each, for tool descriptions.
var ShapeLibraryList = func() string {
	var lines []string
	for _, group := range shapeLibraryGroupOrder {
		lines = append(lines, fmt.Sprintf("- %s: %s", group, strings.Join(ShapeLibraryGroups[group], ", ")))
	}
	return strings.Join(lines, "\n")
}()

// shapeLibraries is the set of valid library names.
var shapeLibraries = func() map[string]bool {
	set := map[string]bool{}
	for _, names := range ShapeLibraryGroups {
		for _, n := range names {
			set[n] = true
		}
	}
	return set
}()

// GetShapeLibrary reads the docs of one shape library from the embedded
// files.
func GetShapeLibrary(name string) (string, error) {
	library := strings.ToLower(strings.TrimSpace(name))
	if !shapeLibraries[library] {
		var names []string
		for _, group := range shapeLibraryGroupOrder {
			names = append(names, ShapeLibraryGroups[group]...)
		}
		return "", fmt.Errorf("Library %q not found. Available: %s", name, strings.Join(names, ", "))
	}
	data, err := assets.ShapeLibrariesFS.ReadFile("shape-libraries/" + library + ".md")
	if err != nil {
		return "", fmt.Errorf("Library %q is missing from this installation.", library)
	}
	return string(data), nil
}
