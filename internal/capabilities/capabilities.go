// Package capabilities is the single source of truth for the draw.io MCP
// tool surface. It compiles structured route metadata into the short
// descriptions and annotations exposed by tools/list, and exports a compact
// index that a registry or tool-search gateway can use for retrieval.
//
// There are deliberately no new dependencies: the catalogue is embedded JSON
// parsed with encoding/json. Descriptions are generated, never hand-written
// twice, so tools/list, the skill file and the registry index cannot drift.
package capabilities

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

//go:embed capabilities.json
var raw []byte

// Exclusion is one "when NOT to use this tool" case. Use is the name of the
// tool to call instead, or empty when the case simply should not happen.
type Exclusion struct {
	Case string `json:"case"`
	Use  string `json:"use"`
}

// Annotations are the four MCP tool behaviour hints. They are hints, not a
// trust boundary: clients MUST map them to their own approval tiers.
type Annotations struct {
	ReadOnly    bool `json:"read_only"`
	Destructive bool `json:"destructive"`
	Idempotent  bool `json:"idempotent"`
	OpenWorld   bool `json:"open_world"`
}

// Capability is one tool's routing metadata.
type Capability struct {
	Name         string      `json:"name"`
	Title        string      `json:"title"`
	WhenToUse    string      `json:"when_to_use"`
	WhenNotToUse []Exclusion `json:"when_not_to_use"`
	Requires     string      `json:"requires"`
	Errors       []string    `json:"errors"`
	Docs         []string    `json:"docs"`
	Tier         string      `json:"tier"`
	Annotations  Annotations `json:"annotations"`
}

// Catalogue is the embedded document.
type Catalogue struct {
	Version       int          `json:"version"`
	Server        string       `json:"server"`
	GuideSections []string     `json:"guide_sections"`
	Tools         []Capability `json:"tools"`
}

var catalog = func() Catalogue {
	var c Catalogue
	if err := json.Unmarshal(raw, &c); err != nil {
		panic("capabilities.json: " + err.Error())
	}
	if err := c.Validate(); err != nil {
		panic("capabilities.json: " + err.Error())
	}
	return c
}()

const maxDescriptionChars = 600

// tierHints is the required annotation shape for each tier. A tool whose
// annotations disagree with its tier is a lint failure.
var tierHints = map[string]Annotations{
	"read":              {ReadOnly: true, Destructive: false, Idempotent: true, OpenWorld: false},
	"write":             {ReadOnly: false, Destructive: false, Idempotent: false, OpenWorld: false},
	"write_idempotent":  {ReadOnly: false, Destructive: false, Idempotent: true, OpenWorld: false},
	"write_destructive": {ReadOnly: false, Destructive: true, Idempotent: false, OpenWorld: false},
}

var vagueWords = []string{"help with", "process ", "handle ", "general ", "various "}

// Validate enforces the routing contract at process start.
func (c Catalogue) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server is empty")
	}
	if len(c.GuideSections) == 0 {
		return fmt.Errorf("guide_sections is empty")
	}
	sections := map[string]bool{}
	for _, s := range c.GuideSections {
		if sections[s] {
			return fmt.Errorf("duplicate guide section %q", s)
		}
		sections[s] = true
	}

	names := map[string]bool{}
	for _, t := range c.Tools {
		if t.Name == "" || names[t.Name] {
			return fmt.Errorf("missing or duplicate tool name %q", t.Name)
		}
		names[t.Name] = true
		if err := t.validate(sections); err != nil {
			return fmt.Errorf("tool %s: %w", t.Name, err)
		}
	}
	for _, t := range c.Tools {
		for _, x := range t.WhenNotToUse {
			if x.Use != "" && !names[x.Use] {
				return fmt.Errorf("tool %s: when_not_to_use points at unknown tool %q", t.Name, x.Use)
			}
		}
	}
	return nil
}

func (t Capability) validate(sections map[string]bool) error {
	if t.Title == "" {
		return fmt.Errorf("title is empty")
	}
	hints, ok := tierHints[t.Tier]
	if !ok {
		return fmt.Errorf("unknown tier %q", t.Tier)
	}
	if t.Annotations != hints {
		return fmt.Errorf("annotations %+v do not match tier %q (%+v)", t.Annotations, t.Tier, hints)
	}
	w := strings.TrimSpace(t.WhenToUse)
	if w == "" {
		return fmt.Errorf("when_to_use is empty")
	}
	lower := strings.ToLower(w)
	for _, v := range vagueWords {
		if strings.Contains(lower, v) {
			return fmt.Errorf("when_to_use contains vague wording %q", strings.TrimSpace(v))
		}
	}
	if strings.Count(w, ". ") > 1 {
		return fmt.Errorf("when_to_use should be at most two sentences")
	}
	if len(t.WhenNotToUse) == 0 {
		return fmt.Errorf("when_not_to_use needs at least one case")
	}
	for _, x := range t.WhenNotToUse {
		if strings.TrimSpace(x.Case) == "" {
			return fmt.Errorf("when_not_to_use has an empty case")
		}
	}
	for _, d := range t.Docs {
		if !strings.HasPrefix(d, "guide://") || !sections[strings.TrimPrefix(d, "guide://")] {
			return fmt.Errorf("doc %q is not a declared guide section", d)
		}
	}
	if n := len(t.Description()); n > maxDescriptionChars {
		return fmt.Errorf("compiled description is %d chars (limit %d)", n, maxDescriptionChars)
	}
	return nil
}

// Description compiles the routing metadata into the four-sentence form the
// model sees in tools/list. Long-form rules live in guide sections instead.
func (t Capability) Description() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(t.WhenToUse))
	if !strings.HasSuffix(strings.TrimSpace(t.WhenToUse), ".") {
		b.WriteString(".")
	}
	if len(t.WhenNotToUse) > 0 {
		parts := make([]string, 0, len(t.WhenNotToUse))
		for _, x := range t.WhenNotToUse {
			if x.Use != "" {
				parts = append(parts, x.Case+" -> "+x.Use)
			} else {
				parts = append(parts, x.Case)
			}
		}
		b.WriteString("\nNot for: " + strings.Join(parts, "; ") + ".")
	}
	if t.Requires != "" {
		b.WriteString("\nRequires: " + t.Requires + ".")
	}
	if len(t.Errors) > 0 {
		b.WriteString("\nErrors: " + strings.Join(t.Errors, ", ") + ".")
	}
	if len(t.Docs) > 0 {
		b.WriteString("\nDetails: " + strings.Join(t.Docs, ", ") + ". Load with get_guide.")
	}
	return b.String()
}

// All returns the catalogue in declaration order.
func All() []Capability { return catalog.Tools }

// Server returns the server identifier used in registry indexes.
func Server() string { return catalog.Server }

// Version returns the catalogue version, to be pinned alongside a release.
func Version() int { return catalog.Version }

// GuideSections returns the declared guide section names.
func GuideSections() []string {
	out := make([]string, len(catalog.GuideSections))
	copy(out, catalog.GuideSections)
	return out
}

// Must returns one capability and panics on an unknown name. Registering a
// tool without a catalogue entry is a programming error, caught at startup.
func Must(name string) Capability {
	for _, t := range catalog.Tools {
		if t.Name == name {
			return t
		}
	}
	panic("capabilities: unregistered tool " + name)
}

// ToolOptions returns the MCP options that tools/list should expose for a
// tool: title, compiled short description and all four behaviour hints.
func ToolOptions(name string) []mcp.ToolOption {
	c := Must(name)
	a := c.Annotations
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(c.Title),
		mcp.WithDescription(c.Description()),
		mcp.WithReadOnlyHintAnnotation(a.ReadOnly),
		mcp.WithDestructiveHintAnnotation(a.Destructive),
		mcp.WithIdempotentHintAnnotation(a.Idempotent),
		mcp.WithOpenWorldHintAnnotation(a.OpenWorld),
	}
}

// IndexEntry is the compact, retrievable record for a registry or tool search.
type IndexEntry struct {
	Name         string      `json:"name"`
	WhenToUse    string      `json:"when_to_use"`
	WhenNotToUse []Exclusion `json:"when_not_to_use"`
	Requires     string      `json:"requires"`
	Tier         string      `json:"tier"`
	Docs         []string    `json:"docs"`
}

// Index returns the retrieval index, sorted by name for stable output.
func Index() []IndexEntry {
	out := make([]IndexEntry, 0, len(catalog.Tools))
	for _, t := range catalog.Tools {
		out = append(out, IndexEntry{
			Name: t.Name, WhenToUse: t.WhenToUse,
			WhenNotToUse: t.WhenNotToUse, Requires: t.Requires,
			Tier: t.Tier, Docs: t.Docs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// MissingReciprocals reports "A says use B instead" pairs where B does not
// mention A back. These are review hints, not hard failures: an asymmetric
// exclusion is often intentional.
func MissingReciprocals() []string {
	mentions := map[string]map[string]bool{}
	for _, t := range catalog.Tools {
		mentions[t.Name] = map[string]bool{}
		for _, x := range t.WhenNotToUse {
			if x.Use != "" {
				mentions[t.Name][x.Use] = true
			}
		}
	}
	var out []string
	for from, to := range mentions {
		for target := range to {
			if !mentions[target][from] {
				out = append(out, fmt.Sprintf("%s -> %s has no reciprocal", from, target))
			}
		}
	}
	sort.Strings(out)
	return out
}
