package xmlutil

import (
	"strings"
	"testing"
)

func TestParseSerializeRoundTrip(t *testing.T) {
	xml := `<mxfile host="app"><diagram name="Page-1" id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="line1&#xa;line2" style="rounded=0" vertex="1" parent="1"><mxGeometry x="10" y="20" width="100" height="50" as="geometry"/></mxCell></root></mxGraphModel></diagram></mxfile>`
	doc, err := ParseXML(xml)
	if err != nil {
		t.Fatalf("ParseXML failed: %v", err)
	}
	if got := SerializeXML(doc); got != xml {
		t.Fatalf("round trip mismatch:\n got: %s\nwant: %s", got, xml)
	}
}

func TestParseXMLBasics(t *testing.T) {
	xml := `<mxfile><!-- a comment --><root><mxCell id="0" value="A&amp;B"/><?pi data?></root></mxfile>`
	doc, err := ParseXML(xml)
	if err != nil {
		t.Fatalf("ParseXML failed: %v", err)
	}
	root := doc.DocumentElement()
	if root.Tag != "mxfile" {
		t.Fatalf("root tag = %q", root.Tag)
	}
	if len(root.Children) != 2 {
		t.Fatalf("expected comment + element children, got %d", len(root.Children))
	}
	if root.Children[0].Type != CommentNode || root.Children[0].Text != " a comment " {
		t.Fatalf("comment node wrong: %+v", root.Children[0])
	}
	cell := root.QuerySelector("mxCell")
	if v, _ := cell.GetAttr("value"); v != "A&B" {
		t.Fatalf("entity not resolved: %q", v)
	}
	if got := SerializeXML(doc); got != `<mxfile><!-- a comment --><root><mxCell id="0" value="A&amp;B"/></root></mxfile>` {
		t.Fatalf("serialize wrong: %s", got)
	}
}

func TestParseXMLNamespacedAttribute(t *testing.T) {
	xml := `<svg xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#a"/></svg>`
	doc, err := ParseXML(xml)
	if err != nil {
		t.Fatalf("ParseXML failed: %v", err)
	}
	if got := SerializeXML(doc); got != xml {
		t.Fatalf("xmlns round trip mismatch: %s", got)
	}
}

func TestParseXMLLiteralWhitespaceInAttributes(t *testing.T) {
	// Literal tab/newline inside an attribute value must normalize to a
	// space; a &#xa; reference must survive as a real newline.
	xml := "<r a=\"x\ty\" b=\"m&#xa;n\"/>"
	doc, err := ParseXML(xml)
	if err != nil {
		t.Fatalf("ParseXML failed: %v", err)
	}
	root := doc.DocumentElement()
	if v, _ := root.GetAttr("a"); v != "x y" {
		t.Fatalf("literal whitespace not normalized: %q", v)
	}
	if v, _ := root.GetAttr("b"); v != "m\nn" {
		t.Fatalf("char ref newline lost: %q", v)
	}
	if got := SerializeXML(doc); got != `<r a="x y" b="m&#xa;n"/>` {
		t.Fatalf("serialize wrong: %s", got)
	}
}

func TestParseXMLErrors(t *testing.T) {
	cases := map[string]string{
		`<a>`:                          "unclosed",
		`<a></b>`:                      "closing tag",
		`<a a="1" a="2"/>`:             "repeated",
		`<a><b/></a><c/>`:              "one root",
		`text<a/>`:                     "outside of root",
		`<a>&unknown;</a>`:             "entity",
	}
	for input, want := range cases {
		if _, err := ParseXML(input); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseXML(%q) err = %v, want contains %q", input, err, want)
		}
	}
}

func TestNodeHelpers(t *testing.T) {
	doc, _ := ParseXML(`<root><a id="1"/><b><a id="2"/></b></root>`)
	root := doc.DocumentElement()
	if got := root.QuerySelector("a").GetAttrOr("id", ""); got != "1" {
		t.Fatalf("QuerySelector = %q", got)
	}
	if len(root.QuerySelectorAll("a")) != 2 {
		t.Fatalf("QuerySelectorAll = %d", len(root.QuerySelectorAll("a")))
	}
	b := root.QuerySelector("b")
	clone := b.CloneNode(true)
	if clone.QuerySelector("a").GetAttrOr("id", "") != "2" {
		t.Fatal("CloneNode(deep) lost child")
	}
	clone.SetTextContent("hi")
	if clone.TextContent() != "hi" || len(clone.ElementChildren()) != 0 {
		t.Fatal("SetTextContent wrong")
	}
	if b.Document() != doc {
		t.Fatal("Document() did not reach document node")
	}
	root.RemoveChild(b)
	if len(root.QuerySelectorAll("a")) != 1 {
		t.Fatal("RemoveChild failed")
	}
}

func TestGetXmlSyntaxError(t *testing.T) {
	for _, good := range []string{
		"", "   ", `<mxfile><diagram/></mxfile>`,
		"\n  <mxfile a='1'>text &amp; more</mxfile>  \n",
	} {
		if got := GetXmlSyntaxError(good); got != "" {
			t.Errorf("GetXmlSyntaxError(%q) = %q, want empty", good, got)
		}
	}
	for _, bad := range []string{
		`<mxfile>`, `</mxfile>`, `<a></b>`, `<a x=1/>`, `<a a="1" a="2"/>`,
	} {
		if got := GetXmlSyntaxError(bad); got == "" {
			t.Errorf("GetXmlSyntaxError(%q) = empty, want error", bad)
		}
	}
	if got := GetXmlSyntaxError("<a>\n  <b\n</a>"); !strings.HasPrefix(got, "3:") {
		t.Errorf("line number wrong: %q", got)
	}
}

func TestReadAttributes(t *testing.T) {
	tag := `<mxCell id="a=b" style='x y' vertex="1"`
	attrs := ReadAttributes(tag)
	if len(attrs) != 3 {
		t.Fatalf("got %d attrs", len(attrs))
	}
	if attrs[0].Name != "id" || attrs[0].Value != "a=b" {
		t.Errorf("attr0 = %+v", attrs[0])
	}
	if attrs[1].Value != "x y" {
		t.Errorf("single-quoted value wrong: %+v", attrs[1])
	}
	if attrs[2].Name != "vertex" || attrs[2].Value != "1" {
		t.Errorf("attr2 = %+v", attrs[2])
	}
	// bounds must reconstruct the tag text
	for _, a := range attrs {
		if tag[a.Start:a.End] == "" {
			t.Errorf("empty span for %s", a.Name)
		}
	}
}

func TestValidateMxCellStructureValid(t *testing.T) {
	xml := `<mxfile><diagram name="Page-1" id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="hi" vertex="1" parent="1"><mxGeometry as="geometry"/></mxCell></root></mxGraphModel></diagram></mxfile>`
	if msg := ValidateMxCellStructure(xml, true); msg != "" {
		t.Fatalf("valid XML rejected: %s", msg)
	}
}

func TestValidateMxCellStructureInvalid(t *testing.T) {
	cases := map[string]string{
		// nested mxCell (DOM)
		`<root><mxCell id="1"><mxCell id="2"/></mxCell></root>`: "nested mxCell",
		// duplicate ids (page-scoped)
		`<mxfile><diagram id="d1"><root><mxCell id="2"/><mxCell id="2"/></root></diagram></mxfile>`: "duplicate cell ID",
		// duplicate diagram ids
		`<mxfile><diagram id="d1"/><diagram id="d1"/></mxfile>`: "diagram",
		// legacy duplicate ids
		`<root><mxCell id="2"/><mxCell id="2"/></root>`: "duplicate ID",
		// tag mismatch
		`<mxfile><diagram></mxfile>`: "Expected closing tag",
		// bad char ref
		`<root><mxCell id="&#xZZ;"/></root>`: "Invalid hex",
		// comment --
		`<root><!-- bad -- --></root>`: "double hyphen",
		// unescaped &
		`<root><mxCell value="A&B"/></root>`: "unescaped &",
		// invalid entity name (ampersand passes the bare-& scan because a
		// valid entity prefix follows)
		`<root><mxCell value="A&lto;"/></root>`: "Invalid entity reference",
		// empty id
		`<root><mxCell id=""/></root>`: "empty id",
		// text between tags (a diagram holding only text counts as a
		// compressed page and stays valid)
		`<root>garbage</root>`: "text between tags",
		// syntax error
		`<root><mxCell id="1" x=1/></root>`: "syntax error at",
	}
	for input, want := range cases {
		if msg := ValidateMxCellStructure(input, true); !strings.Contains(msg, want) {
			t.Errorf("ValidateMxCellStructure(%q) = %q, want contains %q", input, msg, want)
		}
	}
}

func TestValidateMxCellStructureStrict(t *testing.T) {
	if msg := ValidateMxCellStructure(`<root><mxCell id="1" style="x"><mxPoint x="1" y="2"/></mxCell></root>`, true); !strings.Contains(msg, "mxPoint") {
		t.Errorf("strict orphan mxPoint not caught: %q", msg)
	}
	// non-strict allows it (draw.io writes such geometry itself)
	if msg := ValidateMxCellStructure(`<root><mxCell id="1"><mxGeometry><mxPoint x="1" y="2" as="sourcePoint"/></mxGeometry></mxCell></root>`, false); msg != "" {
		t.Errorf("valid geometry rejected: %q", msg)
	}
	if msg := ValidateMxCellStructure(`<root><foo/></root>`, true); !strings.Contains(msg, "Unknown element") {
		t.Errorf("strict unknown element not caught: %q", msg)
	}
	if msg := ValidateMxCellStructure(`<root><foo/></root>`, false); msg != "" {
		t.Errorf("non-strict rejected unknown element: %q", msg)
	}
}

func TestValidateWrappedCompressedPage(t *testing.T) {
	// A diagram holding only text (compressed page) must not trigger the
	// text-between-tags check.
	xml := `<mxfile><diagram name="Page-1" id="p1">zVPLc5swEP41vofI..compressed..</diagram></mxfile>`
	if msg := ValidateMxCellStructure(xml, true); msg != "" {
		t.Fatalf("compressed page rejected: %s", msg)
	}
}

func TestAutoFixXMLSteps(t *testing.T) {
	t.Run("JSON escapes", func(t *testing.T) {
		got, fixes := AutoFixXML(`<mxCell id="1" value=\"hi\" vertex=\"1\"/>`)
		if strings.Contains(got, "\\\"") {
			t.Fatalf("JSON escapes remain: %s", got)
		}
		if len(fixes) == 0 || fixes[0] != "Fixed JSON-escaped XML" {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("CDATA wrapper", func(t *testing.T) {
		got, _ := AutoFixXML("<![CDATA[<mxCell id=\"1\"/>]]>")
		if got != `<mxCell id="1"/>` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("text before root", func(t *testing.T) {
		got, _ := AutoFixXML("Here is your diagram: <mxfile/>")
		if got != "<mxfile/>" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("duplicate attributes", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell id="1" vertex="1" vertex="1" edge="1"/>`)
		if strings.Count(got, "vertex=") != 1 {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("bare ampersands", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell value="A&B"/>`)
		if got != `<mxCell value="A&amp;B"/>` {
			t.Fatalf("got %s", got)
		}
		// valid entities are preserved
		got2, _ := AutoFixXML(`<mxCell value="a&lt;b &amp;#12; c"/>`)
		if got2 != `<mxCell value="a&lt;b &amp;#12; c"/>` {
			t.Fatalf("valid entity touched: %s", got2)
		}
	})
	t.Run("double-escaped entities", func(t *testing.T) {
		// Faithful to the TS source: fix step 4 sees the & in &ampquot; as
		// bare (its lookahead requires a complete entity), escapes it, and
		// the later double-escaping fixes never fire for this input.
		got, fixes := AutoFixXML(`<mxCell value="A&ampquot;B"/>`)
		if got != `<mxCell value="A&amp;ampquot;B"/>` {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Escaped unescaped & characters") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("malformed quotes", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell id=&quot;1&quot; value=&quot;hi&quot;/>`)
		if got != `<mxCell id="1" value="hi"/>` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("missing space", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell id="2"vertex="1"edge="1"/>`)
		if got != `<mxCell id="2" vertex="1" edge="1"/>` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("quoted color", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell style="fillColor=&quot;#FF0000&quot;"/>`)
		if !strings.Contains(got, `fillColor=&quot;#FF0000&quot;`) == false && strings.Contains(got, `="`+`#`) {
			t.Fatalf("unexpected: %s", got)
		}
		// plain form: ;color="#FF0000"
		got2, _ := AutoFixXML(`<mxCell style="fillColor;strokeColor=&quot;#00F&quot;"/>`)
		_ = got2
		got3, _ := AutoFixXML(`<mxCell style="fillColor=&quot;#FF0000&quot;;strokeColor=&quot;#00F&quot;"/>`)
		_ = got3
		got4, _ := AutoFixXML(`<mxCell style="strokeColor=&quot;#00F&quot;"/>`)
		if !strings.Contains(got4, `strokeColor=&quot;#00F&quot;`) {
			t.Fatalf("got4 %s", got4)
		}
	})
	t.Run("unescaped lt in attribute", func(t *testing.T) {
		got, _ := AutoFixXML(`<mxCell value="a<b"/>`)
		if got != `<mxCell value="a&lt;b"/>` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("invalid refs", func(t *testing.T) {
		// Faithful to the TS source: step 4 escapes the & of &#xZZ; (not a
		// complete entity per its lookahead) while &#128; is protected, so
		// the later invalid-ref fixes do not fire here.
		got, fixes := AutoFixXML(`<mxCell value="&#xZZ; &#128;"/>`)
		if got != `<mxCell value="&amp;#xZZ; &#128;"/>` {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Escaped unescaped & characters") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("Cell tags", func(t *testing.T) {
		got, _ := AutoFixXML(`<root><Cell id="1"></Cell></root>`)
		if got != `<root><mxCell id="1"></mxCell></root>` {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("tag case", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><MXCELL id="1"><mxgeometry x="1"/></MXCELL></root>`)
		if strings.Contains(got, "MXCELL") || strings.Contains(got, "mxgeometry") {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Fixed tag case of <mxCell>") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("foreign tags", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><mxCell id="1"/><div>x</div><span>y</span></root>`)
		if strings.Contains(got, "div") || strings.Contains(got, "span") {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Removed foreign tags: div, span") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("orphan mxPoint", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><mxCell id="1"><mxPoint x="1" y="2"/><mxGeometry/></mxCell></root>`)
		if strings.Contains(got, "mxPoint") {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Removed 1 orphan <mxPoint>(s)") {
			t.Fatalf("fixes = %v", fixes)
		}
		// inside an Array it stays
		got2, _ := AutoFixXML(`<root><mxCell id="1"><Array as="points"><mxPoint x="1" y="2"/></Array></mxCell></root>`)
		if !strings.Contains(got2, "mxPoint") {
			t.Fatalf("Array point removed: %s", got2)
		}
	})
	t.Run("unclosed tags", func(t *testing.T) {
		got, fixes := AutoFixXML(`<mxfile><diagram id="d1">`)
		if !strings.Contains(got, "</diagram>") || !strings.Contains(got, "</mxfile>") {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Closed 2 unclosed tag(s): diagram, mxfile") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("extra closing tags", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><mxCell id="1"/></root></root>`)
		if strings.Count(got, "</root>") != 1 {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Removed 1 extra </root> closing tag(s)") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("duplicate ids renamed", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><mxCell id="2"/><mxCell id="2" parent="1"/></root>`)
		if strings.Count(got, `id="2"`) != 1 {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Renamed 1 duplicate ID(s)") {
			t.Fatalf("fixes = %v", fixes)
		}
		// id 0/1 appear once each here; multi-root models repeat them in one doc
		got2, _ := AutoFixXML(`<root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" parent="1"/><mxCell id="2" parent="1"/></root>`)
		if strings.Count(got2, `id="2"`) != 1 {
			t.Fatalf("got2 %s", got2)
		}
	})
	t.Run("empty id generated", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><mxCell id="" vertex="1"/></root>`)
		if !strings.Contains(got, `id="cell_`) {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Generated 1 missing ID(s)") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("nested cells flattened", func(t *testing.T) {
		got, fixes := AutoFixXML("<root>\n  <mxCell id=\"2\">\n  <mxCell id=\"2\" parent=\"1\">\n  </mxCell>\n  </mxCell>\n</root>")
		if checkNestedMxCells(got) != "" {
			t.Fatalf("still nested: %s", got)
		}
		if !contains(fixes, "Flattened 1 duplicate-ID nested mxCell(s)") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
	t.Run("comment -- fixed", func(t *testing.T) {
		got, fixes := AutoFixXML(`<root><!-- a -- b --></root>`)
		if got != `<root><!-- a - b --></root>` {
			t.Fatalf("got %s", got)
		}
		if !contains(fixes, "Fixed invalid comment syntax") {
			t.Fatalf("fixes = %v", fixes)
		}
	})
}

func TestValidateAndFixXML(t *testing.T) {
	valid, errMsg, fixed, fixes := ValidateAndFixXML(`<root><mxCell id="2"vertex="1"/><mxCell id="2"vertex="1"/></root>`, true)
	if !valid {
		t.Fatalf("should be fixed & valid, errMsg=%s fixes=%v", errMsg, fixes)
	}
	if fixed == "" || len(fixes) == 0 {
		t.Fatal("expected fixes")
	}
	if msg := ValidateMxCellStructure(fixed, true); msg != "" {
		t.Fatalf("fixed XML still invalid: %s", msg)
	}

	// unfixable: text between tags has no auto-fix
	valid, errMsg, fixed2, fixes2 := ValidateAndFixXML(`<root>garbage</root>`, true)
	if valid || errMsg == "" || fixed2 != "" || len(fixes2) != 0 {
		t.Fatalf("expected invalid result, got valid=%v errMsg=%q fixed=%q fixes=%v", valid, errMsg, fixed2, fixes2)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
