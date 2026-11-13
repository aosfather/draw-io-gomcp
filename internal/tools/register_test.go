package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// TestRegisteredToolSurface inspects the real tools/list response an agent
// receives, rather than the catalogue in isolation. It is the closest thing
// to "does the agent actually see the new routing layer" without running a
// full MCP client process.
func TestRegisteredToolSurface(t *testing.T) {
	s := server.NewMCPServer("draw-io-test", "test", server.WithToolCapabilities(false))
	Register(s, Deps{}) // handlers are not invoked; a nil Autosaver is safe here

	c, err := client.NewInProcessClient(s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = "2025-06-18"
	initReq.Params.ClientInfo = mcp.Implementation{Name: "capgen-test", Version: "1.0"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatal(err)
	}
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(res.Tools), 14; got != want {
		t.Fatalf("tools/list returned %d tools, want %d", got, want)
	}

	byName := map[string]mcp.Tool{}
	totalDescChars := 0
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
		totalDescChars += len(tool.Description)
		if len(tool.Description) > 600 {
			t.Errorf("%s: description is %d chars (limit 600)", tool.Name, len(tool.Description))
		}
		if !strings.Contains(tool.Description, "Not for:") {
			t.Errorf("%s: description has no exclusion cases", tool.Name)
		}
		a := tool.Annotations
		if a.ReadOnlyHint == nil || a.DestructiveHint == nil || a.IdempotentHint == nil || a.OpenWorldHint == nil {
			t.Errorf("%s: a behaviour hint is unset", tool.Name)
		}
	}
	t.Logf("tools/list total description size: %d chars across %d tools", totalDescChars, len(res.Tools))

	if _, ok := byName["get_guide"]; !ok {
		t.Fatal("get_guide is not exposed")
	}
	for _, name := range []string{"create_new_diagram", "load_diagram", "delete_page"} {
		a := byName[name].Annotations
		if a.DestructiveHint == nil || !*a.DestructiveHint {
			t.Errorf("%s should be marked destructive", name)
		}
	}
	if d := byName["get_shape_library"].Description; strings.Contains(d, "cisco19") || strings.Contains(d, "Cloud:") {
		t.Error("the shape library catalogue leaked into tools/list")
	}
	if d := byName["start_session"].Description; !strings.Contains(d, "get_guide") {
		t.Logf("start_session description does not mention get_guide (the next_step in its result does)")
	}

	// The get_guide schema must advertise the section enum so the model knows
	// which values are legal without a failed call.
	raw, err := json.Marshal(byName["get_guide"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range GuideSections() {
		if !strings.Contains(string(raw), section) {
			t.Errorf("get_guide schema is missing section %q", section)
		}
	}
}
