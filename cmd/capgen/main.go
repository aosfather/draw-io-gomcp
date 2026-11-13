// Command capgen runs the routing lint and exports the capabilities catalogue.
//
// Usage:
//
//	go run ./cmd/capgen -check
//	go run ./cmd/capgen -out build/capabilities.index.json
//	go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md
//
// The catalogue is validated at package init, so any lint failure aborts every
// command before a file is written. -skill is opt-in: generated documentation
// is never written implicitly by a build. It renders a routing index for the
// bundled skill package, not a second SKILL.md (a duplicate frontmatter block
// inside references/ would be misread by skill packaging scanners).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aosfather.drawio-mcp/internal/capabilities"
)

type indexDoc struct {
	Server      string                    `json:"server"`
	Version     int                       `json:"version"`
	GeneratedBy string                    `json:"generated_by"`
	SearchHint  string                    `json:"search_hint"`
	Guide       []string                  `json:"guide_sections"`
	Tools       []capabilities.IndexEntry `json:"tools"`
}

// skillDescription is the single source of truth for the bundled skill's
// frontmatter trigger text. TestSkillFrontmatterMatchesCatalogue keeps
// skills/draw-io-gomcp/SKILL.md in sync with it.
const skillDescription = `Installs and configures the draw-io-gomcp MCP server, and teaches the agent ` +
	`to generate, edit and preview Draw.io (.drawio / diagrams.net) diagrams with ` +
	`a live preview. Use when the user asks to draw a flowchart, sequence, class, ` +
	`ER, architecture, network, UML or any other diagram, or wants to create, ` +
	`open, edit or export a .drawio file. Not for Mermaid-only or ASCII-only ` +
	`output, nor for raster image generation.`

func main() {
	check := flag.Bool("check", false, "print a lint report and write nothing")
	out := flag.String("out", "", "path for the registry index JSON")
	skill := flag.String("skill", "", "optional path for the generated skill routing index")
	flag.Parse()

	if *check || (*out == "" && *skill == "") {
		fmt.Printf("capabilities: server=%s version=%d tools=%d guide_sections=%d - lint OK\n",
			capabilities.Server(), capabilities.Version(),
			len(capabilities.All()), len(capabilities.GuideSections()))
		for _, note := range capabilities.MissingReciprocals() {
			fmt.Println("  note:", note)
		}
		if *out == "" && *skill == "" {
			return
		}
	}

	if *out != "" {
		doc := indexDoc{
			Server:      capabilities.Server(),
			Version:     capabilities.Version(),
			GeneratedBy: "capgen",
			SearchHint:  "Match when_to_use and when_not_to_use against the task; return top-k names, then describe_tool(name) for the full schema.",
			Guide:       capabilities.GuideSections(),
			Tools:       capabilities.Index(),
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fail(err)
		}
		writeFile(*out, append(data, '\n'))
		fmt.Printf("wrote registry index: %s (%d tools)\n", *out, len(doc.Tools))
	}

	if *skill != "" {
		writeFile(*skill, []byte(renderRoutingIndex()))
		fmt.Printf("wrote skill routing index: %s\n", *skill)
	}
}

func renderRoutingIndex() string {
	var b strings.Builder
	b.WriteString("# Tool routing index\n\n")
	b.WriteString("Generated from `internal/capabilities/capabilities.json` by `go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md`. Do not edit by hand.\n\n")
	b.WriteString("## Workflow\n\n")
	b.WriteString("1. `start_session` once, then follow the `next_step` it returns.\n")
	b.WriteString("2. `get_guide(sections=[...])` with only the sections this task needs.\n")
	b.WriteString("3. `create_new_diagram` / `load_diagram`, then `edit_diagram` for refinements.\n")
	b.WriteString("4. `screenshot_diagram` to check the result, `export_diagram` to save a file.\n\n")
	b.WriteString("Guide sections: " + strings.Join(capabilities.GuideSections(), ", ") + ".\n\n")
	b.WriteString("## Tools (routing index)\n\n")
	b.WriteString("| Tool | Use when | Use instead |\n|---|---|---|\n")
	for _, e := range capabilities.Index() {
		instead := make([]string, 0, len(e.WhenNotToUse))
		for _, x := range e.WhenNotToUse {
			if x.Use != "" {
				instead = append(instead, x.Case+" -> "+x.Use)
			} else {
				instead = append(instead, x.Case)
			}
		}
		b.WriteString("| `" + e.Name + "` | " + e.WhenToUse + " | " + strings.Join(instead, "; ") + " |\n")
	}
	return b.String()
}

func writeFile(path string, data []byte) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "capgen:", err)
	os.Exit(1)
}
