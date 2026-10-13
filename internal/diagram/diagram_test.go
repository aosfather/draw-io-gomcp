package diagram

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"strings"
	"testing"
)

const sampleMxfile = `<mxfile host="app.diagrams.net"><diagram name="Page-1" id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"><mxGeometry x="1" y="2" width="3" height="4" as="geometry"/></mxCell></root></mxGraphModel></diagram><diagram name="Page-2" id="p2"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>`

func TestWrapCellsInModel(t *testing.T) {
	// bare cells get the wrapper and root cells
	got := WrapCellsInModel(`<mxCell id="2" value="hi" vertex="1"/>`)
	if got != `<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="hi" vertex="1"/></root></mxGraphModel>` {
		t.Fatalf("got %s", got)
	}
	// root cells the model wrote anyway are replaced
	got = WrapCellsInModel(`<mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2"/>`)
	if strings.Contains(got, `<mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="0"`) {
		t.Fatalf("root cells duplicated: %s", got)
	}
	if strings.Count(got, `id="0"`) != 1 {
		t.Fatalf("got %s", got)
	}
	// trailing closing tags some providers append are dropped
	got = WrapCellsInModel(`<mxCell id="2"/></root></mxGraphModel>`)
	if !strings.HasSuffix(got, `<mxCell id="2"/></root></mxGraphModel>`) {
		t.Fatalf("got %s", got)
	}
	if strings.Count(got, "</mxGraphModel>") != 1 {
		t.Fatalf("got %s", got)
	}
	// a label holding id='1' is not an id
	got = WrapCellsInModel(`<mxCell id="2" value="parent id='1'" vertex="1"/>`)
	if strings.Count(got, `id="`) != 3 {
		t.Fatalf("got %s", got)
	}
	// <mxfile>, <mxGraphModel> and anything else are returned unchanged
	if got := WrapCellsInModel(sampleMxfile); got != sampleMxfile {
		t.Fatalf("mxfile changed: %s", got)
	}
	if got := WrapCellsInModel("no cells here"); got != "no cells here" {
		t.Fatalf("plain text changed: %s", got)
	}
	// leading comments are allowed
	got = WrapCellsInModel("<!-- c --><mxCell id=\"2\"/>")
	if !strings.HasPrefix(got, "<mxGraphModel>") {
		t.Fatalf("got %s", got)
	}
	// xml declaration is stripped
	got = WrapCellsInModel(`<?xml version="1.0"?><mxCell id="2"/>`)
	if strings.Contains(got, "?xml") {
		t.Fatalf("got %s", got)
	}
}

func TestNormalizeToMxfile(t *testing.T) {
	if got := NormalizeToMxfile(sampleMxfile, "", ""); got != sampleMxfile {
		t.Fatal("mxfile changed")
	}
	got := NormalizeToMxfile(`<mxGraphModel><root><mxCell id="0"/></root></mxGraphModel>`, "pid", "My Page")
	if got != `<mxfile host="app.diagrams.net"><diagram id="pid" name="My Page"><mxGraphModel><root><mxCell id="0"/></root></mxGraphModel></diagram></mxfile>` {
		t.Fatalf("got %s", got)
	}
	// auto id/name (note: a self-closing <mxGraphModel/> is not recognized,
	// matching the TypeScript regex which requires whitespace or >)
	got = NormalizeToMxfile(`<mxGraphModel></mxGraphModel>`, "", "")
	if !strings.Contains(got, `name="Page-1"`) || !strings.Contains(got, `id="`) {
		t.Fatalf("got %s", got)
	}
	if got := NormalizeToMxfile("<div/>", "", ""); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
	if got := NormalizeToMxfile("   ", "", ""); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
}

func TestHasCells(t *testing.T) {
	if HasCells(BlankMxfile) {
		t.Fatal("blank mxfile has cells")
	}
	if !HasCells(`<mxCell id="2"/>`) {
		t.Fatal("cell id=2 not detected")
	}
	if HasCells(`<mxCell id="1" parent="0"/>`) {
		t.Fatal("root cell detected")
	}
	if !HasCells(`<UserObject id="u"><mxCell/></UserObject>`) {
		t.Fatal("UserObject not detected")
	}
	// compressed page text
	if !HasCells(`<mxfile><diagram name="P" id="x">zVPLc5swEP41vofI</diagram></mxfile>`) {
		t.Fatal("compressed page not detected")
	}
}

func TestProjectAndFindPages(t *testing.T) {
	proj := ProjectPage(sampleMxfile, &PageSelector{PageName: "Page-2"})
	if !proj.OK || proj.Index != 1 || proj.Name != "Page-2" {
		t.Fatalf("proj = %+v", proj)
	}
	if !strings.HasPrefix(proj.XML, `<mxfile host="app.diagrams.net"><diagram name="Page-2"`) {
		t.Fatalf("xml = %s", proj.XML)
	}
	idx := 0
	if found, i := FindPageElement(ParseMxfile(sampleMxfile), &PageSelector{PageIndex: &idx}); found == nil || i != 0 {
		t.Fatal("index selector failed")
	}
	// default: first page
	if found, i := FindPageElement(ParseMxfile(sampleMxfile), nil); found == nil || i != 0 {
		t.Fatal("default selector failed")
	}
	if proj := ProjectPage("not xml", nil); proj.OK || proj.Reason != "parse" {
		t.Fatalf("proj = %+v", proj)
	}
	if proj := ProjectPage(sampleMxfile, &PageSelector{PageID: "nope"}); proj.OK || proj.Reason != "notfound" {
		t.Fatalf("proj = %+v", proj)
	}
	pages := ListPagesFromDoc(ParseMxfile(sampleMxfile))
	if len(pages) != 2 || pages[0].Name != "Page-1" || pages[1].ID != "p2" {
		t.Fatalf("pages = %+v", pages)
	}
	if pages[0].CellCount != 3 || pages[1].CellCount != 2 {
		t.Fatalf("cellCounts = %+v", pages)
	}
}

func TestAddRenameDeletePage(t *testing.T) {
	doc := ParseMxfile(sampleMxfile)
	info, err := AddPageToDoc(doc, "", "", "")
	if err != nil || info.Name != "Page-3" || info.Index != 2 {
		t.Fatalf("info = %+v err = %v", info, err)
	}
	if _, dupErr := AddPageToDoc(doc, "p1", "", ""); dupErr == nil {
		t.Fatal("duplicate id accepted")
	}
	_, err = AddPageToDoc(doc, "", "", sampleMxfile)
	if err == nil || !strings.Contains(err.Error(), "must be a bare <mxGraphModel>") {
		t.Fatalf("mxfile accepted: %v", err)
	}
	if _, err := AddPageToDoc(doc, "", "Custom", `<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2"/></root></mxGraphModel>`); err != nil {
		t.Fatalf("custom xml rejected: %v", err)
	}
	serialized := SerializeMxfile(doc)
	if !strings.Contains(serialized, `name="Custom"`) || strings.Count(serialized, "<diagram") != 4 {
		t.Fatalf("serialized = %s", serialized)
	}

	if !RenamePageInDoc(doc, &PageSelector{PageName: "Page-3"}, "Renamed") {
		t.Fatal("rename failed")
	}
	if RenamePageInDoc(doc, &PageSelector{PageID: "zzz"}, "X") {
		t.Fatal("renamed missing page")
	}

	res := DeletePageFromDoc(doc, &PageSelector{PageName: "Renamed"})
	if !res.OK || res.DeletedID == "" || res.DeletedIndex != 2 {
		t.Fatalf("res = %+v", res)
	}
	// delete down to one page, then refuse
	DeletePageFromDoc(doc, &PageSelector{PageID: "p1"})
	DeletePageFromDoc(doc, &PageSelector{PageID: "p2"})
	// "Custom" is the page name; its id was generated
	res = DeletePageFromDoc(doc, &PageSelector{PageName: "Custom"})
	if res.OK || res.Reason != "Cannot delete the only remaining page" {
		t.Fatalf("res = %+v", res)
	}
	res = DeletePageFromDoc(doc, &PageSelector{PageID: "zzz"})
	if res.OK || res.Reason != "Page not found" {
		t.Fatalf("res = %+v", res)
	}
}

func TestApplyDiagramOperations(t *testing.T) {
	xml := `<mxfile><diagram name="P1" id="d1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"/><mxCell id="3" value="B" vertex="1" parent="2"/><mxCell id="e1" edge="1" source="2" target="3" parent="1"/><mxCell id="lbl" value="L" vertex="1" parent="e1"/></root></mxGraphModel></diagram></mxfile>`

	// add
	res := ApplyDiagramOperations(xml, []Operation{{Operation: "add", CellID: "9", NewXML: `<mxCell id="9" value="N" vertex="1" parent="1"/>`}}, nil)
	if len(res.Errors) != 0 || !strings.Contains(res.Result, `id="9"`) {
		t.Fatalf("res = %+v", res)
	}
	// update
	res = ApplyDiagramOperations(xml, []Operation{{Operation: "update", CellID: "2", NewXML: `<mxCell id="2" value="A2" vertex="1" parent="1"/>`}}, nil)
	if len(res.Errors) != 0 || !strings.Contains(res.Result, `value="A2"`) {
		t.Fatalf("res = %+v", res)
	}
	// id mismatch
	res = ApplyDiagramOperations(xml, []Operation{{Operation: "update", CellID: "2", NewXML: `<mxCell id="5" value="X"/>`}}, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "ID mismatch") {
		t.Fatalf("res = %+v", res)
	}
	// delete cascades to children and edges and edge labels
	res = ApplyDiagramOperations(xml, []Operation{{Operation: "delete", CellID: "2"}}, nil)
	if len(res.Errors) != 0 {
		t.Fatalf("res = %+v", res)
	}
	for _, gone := range []string{`id="2"`, `id="3"`, `id="e1"`, `id="lbl"`} {
		if strings.Contains(res.Result, gone) {
			t.Fatalf("%s survived: %s", gone, res.Result)
		}
	}
	// root cells are protected
	res = ApplyDiagramOperations(xml, []Operation{{Operation: "delete", CellID: "0"}}, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "root cell") {
		t.Fatalf("res = %+v", res)
	}
	// redundant delete is a no-op
	res = ApplyDiagramOperations(xml, []Operation{
		{Operation: "delete", CellID: "3"},
		{Operation: "delete", CellID: "3"},
	}, nil)
	if len(res.Errors) != 0 {
		t.Fatalf("res = %+v", res)
	}
	// unknown cell
	res = ApplyDiagramOperations(xml, []Operation{{Operation: "delete", CellID: "zz"}}, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "not found") {
		t.Fatalf("res = %+v", res)
	}
	// wrapped cells: the wrapper is the cell
	wrapped := `<root><mxCell id="0"/><mxCell id="1" parent="0"/><UserObject id="u1" label="U"><mxCell id="u1" style="x" vertex="1" parent="1"/></UserObject></root>`
	res = ApplyDiagramOperations(wrapped, []Operation{{Operation: "update", CellID: "u1", NewXML: `<UserObject id="u1" label="U2"><mxCell id="u1" style="y" vertex="1" parent="1"/></UserObject>`}}, nil)
	if len(res.Errors) != 0 || !strings.Contains(res.Result, `label="U2"`) {
		t.Fatalf("res = %+v", res)
	}
	// page selector
	res = ApplyDiagramOperations(sampleMxfile, []Operation{{Operation: "add", CellID: "5", NewXML: `<mxCell id="5" vertex="1" parent="1"/>`}}, &PageSelector{PageName: "Page-2"})
	if len(res.Errors) != 0 {
		t.Fatalf("res = %+v", res)
	}
	// selector on a bare model is rejected
	res = ApplyDiagramOperations(`<mxGraphModel><root><mxCell id="0"/></root></mxGraphModel>`, nil, &PageSelector{PageID: "x"})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "not multi-page") {
		t.Fatalf("res = %+v", res)
	}
	// page not found
	res = ApplyDiagramOperations(sampleMxfile, nil, &PageSelector{PageID: "zz"})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "not found in <mxfile>") {
		t.Fatalf("res = %+v", res)
	}
	// syntax error upfront
	res = ApplyDiagramOperations(`<a>`, nil, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "XML parse error") {
		t.Fatalf("res = %+v", res)
	}
}

func TestEditDiagramAllOrNothing(t *testing.T) {
	xml := `<mxfile><diagram name="P1" id="d1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"/></root></mxGraphModel></diagram></mxfile>`

	out := EditDiagram(xml, []Operation{
		{Operation: "add", CellID: "3", NewXML: `<mxCell id="3" vertex="1" parent="1"/>`},
		{Operation: "add", CellID: "4", NewXML: `<mxCell id="5" vertex="1" parent="1"/>`},
	}, nil)
	if out.OK || len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "ID mismatch") {
		t.Fatalf("out = %+v", out)
	}
	// nothing was applied
	if strings.Contains(xml, `id="3"`) {
		t.Fatal("input mutated")
	}
	out = EditDiagram(xml, []Operation{
		{Operation: "add", CellID: "3", NewXML: `<mxCell id="3" vertex="1" parent="1"/><mxCell id="4" vertex="1" parent="1"/>`},
	}, nil)
	if out.OK || !strings.Contains(out.Errors[0], "exactly one cell") {
		t.Fatalf("out = %+v", out)
	}
	out = EditDiagram(xml, []Operation{
		{Operation: "add", CellID: "3", NewXML: `<mxCell id="3"vertex="1"vertex="1"/>`},
	}, nil)
	if !out.OK || out.Applied != 1 || len(out.Fixes) == 0 {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(out.XML, `id="3" vertex="1"`) {
		t.Fatalf("xml = %s", out.XML)
	}
}

func TestContentFingerprintAndGate(t *testing.T) {
	fp := ContentFingerprint(sampleMxfile, true)
	// same content re-serialized with different attributes/ids/order must
	// match (cell order stays, like the TypeScript canonicalizer)
	rewritten := `<mxfile host="other"><diagram id="changed" name="Page-1"><mxGraphModel dx="1" dy="2"><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" vertex="1" value="A" parent="1"><mxGeometry height="4" as="geometry" width="3" x="1" y="2"/></mxCell></root></mxGraphModel></diagram><diagram id="x2" name="Page-2"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel></diagram></mxfile>`
	if ContentFingerprint(rewritten, true) != fp {
		t.Fatalf("fingerprints differ:\n%s\nvs\n%s", fp, ContentFingerprint(rewritten, true))
	}
	// a real user edit must be detected
	edited := strings.Replace(sampleMxfile, `value="A"`, `value="B"`, 1)
	if ContentFingerprint(edited, true) == fp {
		t.Fatal("edit not detected")
	}
	// a page rename is detected with names included, not without
	renamed := strings.Replace(sampleMxfile, "Page-2", "Renamed", 1)
	if ContentFingerprint(renamed, true) == fp {
		t.Fatal("rename not detected")
	}
	if ContentFingerprint(renamed, false) != ContentFingerprint(sampleMxfile, false) {
		t.Fatal("rename detected without names")
	}
	// bare model fingerprints like its wrapping
	bare := `<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"/></root></mxGraphModel>`
	if ContentFingerprint(bare, false) != ContentFingerprint(singlePageMxfile(), false) {
		t.Fatal("bare model fingerprint differs from wrapped")
	}

	// gate
	if r := CheckEditGate("", live()); r.OK || r.Reason != "no-context" {
		t.Fatalf("r = %+v", r)
	}
	if r := CheckEditGate("x", ""); !r.OK {
		t.Fatalf("r = %+v", r)
	}
	if r := CheckEditGate(sampleMxfile, rewritten); !r.OK {
		t.Fatalf("rewritten state flagged stale: %+v", r)
	}
	if r := CheckEditGate(sampleMxfile, edited); r.OK || r.Reason != "stale" {
		t.Fatalf("r = %+v", r)
	}
	// bare model on both sides: same cells, different attribute order
	if r := CheckEditGate(bare, `<mxGraphModel dx="9"><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" vertex="1" parent="1" value="A"/></root></mxGraphModel>`); !r.OK {
		t.Fatalf("r = %+v", r)
	}
}

func singlePageMxfile() string {
	return `<mxfile host="app.diagrams.net"><diagram name="Page-1" id="p1"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="A" vertex="1" parent="1"/></root></mxGraphModel></diagram></mxfile>`
}

func live() string { return sampleMxfile }

func TestMarkPageSeen(t *testing.T) {
	sel := &PageSelector{PageName: "Page-1"}
	// saw page 1 of a two-page doc; other pages unchanged → seen all
	if got := MarkPageSeen(sampleMxfile, rewrittenPage1(), sel); got != rewrittenPage1() {
		t.Fatal("expected liveXml to be fully seen")
	}
	// other pages changed → still lastSeenXml
	if got := MarkPageSeen(sampleMxfile, changedPage2(), sel); got != sampleMxfile {
		t.Fatal("expected lastSeenXml unchanged")
	}
	// empty record: one-page doc is fully seen
	if got := MarkPageSeen("", singlePageMxfile(), sel); got != singlePageMxfile() {
		t.Fatal("expected liveXml for one-page doc")
	}
	// empty record with multi-page doc stays empty
	if got := MarkPageSeen("", sampleMxfile, sel); got != "" {
		t.Fatal("expected empty lastSeenXml")
	}
}

func rewrittenPage1() string {
	return strings.Replace(sampleMxfile, `id="p1"`, `id="regen"`, 1)
}

func changedPage2() string {
	return strings.Replace(sampleMxfile, `name="Page-2"`, `name="Page-2b"`, 1)
}

func TestReservedIDErrorAndPrepareNewDiagram(t *testing.T) {
	if ReservedIDError(`<mxfile/>`) != "" {
		t.Fatal("mxfile should skip the check")
	}
	if got := ReservedIDError(`<mxCell id="0" vertex="1"/>`); !strings.Contains(got, "root cells") {
		t.Fatalf("got %q", got)
	}
	if got := ReservedIDError(`<UserObject id="1" label="x"><mxCell id="1"/></UserObject>`); !strings.Contains(got, "root cells") {
		t.Fatalf("got %q", got)
	}
	// plain edge/shape ids are fine
	if got := ReservedIDError(`<mxCell id="e" edge="1" source="0" target="1"/>`); got != "" {
		t.Fatalf("got %q", got)
	}
	// a label holding id='1' is not an id
	if got := ReservedIDError(`<mxCell id="2" value="id='1'" vertex="1"/>`); got != "" {
		t.Fatalf("got %q", got)
	}

	out := PrepareNewDiagram(`<mxCell id="2" value="hi" vertex="1"/>`, "pid", "Name")
	if !out.OK || out.XML != `<mxfile host="app.diagrams.net"><diagram id="pid" name="Name"><mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/><mxCell id="2" value="hi" vertex="1"/></root></mxGraphModel></diagram></mxfile>` {
		t.Fatalf("out = %+v", out)
	}
	out = PrepareNewDiagram(`<mxCell id="0" vertex="1"/>`, "", "")
	if out.OK || !strings.Contains(out.Error, "root cells") {
		t.Fatalf("out = %+v", out)
	}
	out = PrepareNewDiagram("<div/>", "", "")
	if out.OK || !strings.Contains(out.Error, "XML must be") {
		t.Fatalf("out = %+v", out)
	}
}

// percentEncode mirrors encodeURIComponent.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '!' || c == '~' || c == '*' || c == '\'' || c == '(' || c == ')' {
			b.WriteByte(c)
		} else {
			b.WriteString(strings.ToUpper(("%" + string(hexDigits(c>>4)) + string(hexDigits(c&0x0f)))))
		}
	}
	return b.String()
}

func hexDigits(v byte) byte {
	if v < 10 {
		return '0' + v
	}
	return 'A' + v - 10
}

// compressPage mirrors draw.io's save format: encodeURIComponent → raw
// deflate → base64.
func compressPage(xml string) string {
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	fw.Write([]byte(percentEncode(xml)))
	fw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDecompressPageContent(t *testing.T) {
	xml := `<mxGraphModel><root><mxCell id="0"/></root></mxGraphModel>`
	if got := DecompressPageContent(compressPage(xml)); got != xml {
		t.Fatalf("got %q", got)
	}
	// older files: inflated text is the XML, no URI encoding
	var buf bytes.Buffer
	fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	fw.Write([]byte(xml))
	fw.Close()
	if got := DecompressPageContent(base64.StdEncoding.EncodeToString(buf.Bytes())); got != xml {
		t.Fatalf("got %q", got)
	}
	if got := DecompressPageContent("!!!not-base64!!!"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestParseDrawioFileContent(t *testing.T) {
	// compressed page
	compressed := compressPage(`<mxGraphModel><root><mxCell id="0"/><mxCell id="1" parent="0"/></root></mxGraphModel>`)
	file := `<mxfile><diagram name="P1" id="d1">` + compressed + `</diagram></mxfile>`
	res := ParseDrawioFileContent(file)
	if !res.OK {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.XML, "<mxGraphModel>") {
		t.Fatalf("not decompressed: %s", res.XML)
	}
	// plain mxfile passes through unchanged
	res = ParseDrawioFileContent(sampleMxfile)
	if !res.OK || res.XML != sampleMxfile {
		t.Fatalf("res = %+v", res)
	}
	// bare model is wrapped
	res = ParseDrawioFileContent(`<mxGraphModel><root><mxCell id="0"/></root></mxGraphModel>`)
	if !res.OK || !strings.HasPrefix(res.XML, "<mxfile") {
		t.Fatalf("res = %+v", res)
	}
	// errors
	if res := ParseDrawioFileContent("   "); res.OK || res.Error != "File is empty." {
		t.Fatalf("res = %+v", res)
	}
	if res := ParseDrawioFileContent("<div/>"); res.OK || !strings.Contains(res.Error, "Not a draw.io file") {
		t.Fatalf("res = %+v", res)
	}
	bad := `<mxfile><diagram name="P" id="d">!!!garbage!!!</diagram></mxfile>`
	if res := ParseDrawioFileContent(bad); res.OK || !strings.Contains(res.Error, `Page "P"`) {
		t.Fatalf("res = %+v", res)
	}
}
