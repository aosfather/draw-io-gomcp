package main

import (
	"os"
	"strings"
	"testing"

	"aosfather.drawio-mcp/internal/capabilities"
)

const skillRoot = "../../skills/draw-io-gomcp"

// TestSkillFrontmatterMatchesCatalogue guards the only field the host actually
// parses when deciding whether to load the skill. A drift here means the skill
// silently stops matching the queries it was written for.
func TestSkillFrontmatterMatchesCatalogue(t *testing.T) {
	data, err := os.ReadFile(skillRoot + "/SKILL.md")
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || lines[0] != "---" {
		t.Fatal("SKILL.md has no frontmatter opening delimiter")
	}
	var block []string
	inDescription := false
	for _, line := range lines[1:] {
		if line == "---" {
			break
		}
		if strings.HasPrefix(line, "description:") {
			if !strings.Contains(line, ">-") {
				t.Fatalf("description must be a folded scalar so it can be compared: %q", line)
			}
			inDescription = true
			continue
		}
		if inDescription && strings.HasPrefix(line, "  ") {
			block = append(block, strings.TrimSpace(line))
		}
	}
	got := strings.Join(block, " ")
	if got != skillDescription {
		t.Errorf("SKILL.md frontmatter description is out of sync with capgen.\n got: %q\nwant: %q", got, skillDescription)
	}
}

// TestRoutingIndexIsCurrent makes a stale generated file fail the build instead
// of shipping an outdated route table inside the skill package.
func TestRoutingIndexIsCurrent(t *testing.T) {
	path := skillRoot + "/references/routing-index.md"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run capgen -skill to create it)", path, err)
	}
	if want := renderRoutingIndex(); string(data) != want {
		t.Errorf("%s is stale; regenerate with:\n  go run ./cmd/capgen -skill %s", path, path)
	}
}

func TestRoutingIndexCoversEveryTool(t *testing.T) {
	got := renderRoutingIndex()
	for _, c := range capabilities.All() {
		if !strings.Contains(got, "`"+c.Name+"`") {
			t.Errorf("routing index is missing tool %q", c.Name)
		}
	}
	if n := len(capabilities.All()); n != 14 {
		t.Errorf("catalogue has %d tools, expected 14; update the skill package docs too", n)
	}
}

func TestSkillPackageIsComplete(t *testing.T) {
	for _, rel := range []string{
		"SKILL.md",
		"PUBLISH.md",
		"scripts/install.sh",
		"references/drawio-tools.md",
		"references/routing-index.md",
	} {
		info, err := os.Stat(skillRoot + "/" + rel)
		if err != nil {
			t.Errorf("missing skill package file %s: %v", rel, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("skill package file %s is empty", rel)
		}
	}
}
