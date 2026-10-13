// Package diagram ports the multi-page helpers (pages.ts), the ID-based
// operations (diagram-operations.ts), the edit core (edit-diagram.ts), the
// edit gate (edit-gate.ts), new-diagram preparation (new-diagram.ts) and the
// .drawio file loading (load-diagram.ts).
package diagram

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"aosfather.drawio-mcp/internal/xmlutil"
)

// PageInfo summarises one <diagram> page.
type PageInfo struct {
	ID        string
	Name      string
	Index     int
	CellCount int
}

// PageSelector is used by all multi-page-aware tools. nil or all-empty means
// "the first page".
type PageSelector struct {
	PageID    string
	PageName  string
	PageIndex *int
}

// HasPageSelector reports whether the selector targets a specific page.
func HasPageSelector(s *PageSelector) bool {
	if s == nil {
		return false
	}
	return s.PageID != "" || s.PageName != "" || s.PageIndex != nil
}

// DescribeSelector renders the selector for error messages, like
// JSON.stringify(selector) in the TypeScript sources.
func DescribeSelector(s *PageSelector) string {
	if s == nil {
		return "null"
	}
	var fields []string
	if s.PageID != "" {
		fields = append(fields, fmt.Sprintf(`"page_id":%q`, s.PageID))
	}
	if s.PageName != "" {
		fields = append(fields, fmt.Sprintf(`"page_name":%q`, s.PageName))
	}
	if s.PageIndex != nil {
		fields = append(fields, fmt.Sprintf(`"page_index":%d`, *s.PageIndex))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

const base36Digits = "0123456789abcdefghijklmnopqrstuvwxyz"

// randomBase36 returns n random base-36 characters. No crypto needed: page
// ids only have to be unique within a session, exactly like Math.random in
// the TypeScript source.
func randomBase36(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = base36Digits[rand.IntN(36)]
	}
	return string(out)
}

// GeneratePageID produces a short page id similar in shape to drawio's
// auto-assigned ids: 12 alphanumeric chars with a single dash.
func GeneratePageID() string {
	return randomBase36(8) + "-" + randomBase36(4)
}

var (
	mxCellWithIDRe = regexp.MustCompile(`<mxCell\b[^>]*\bid\s*=\s*["']([^"']*)["']`)
	userObjectRe   = regexp.MustCompile(`<UserObject\b|<object\b`)
	diagramTextRe  = regexp.MustCompile(`<diagram\b[^>]*>\s*[^\s<]`)
)

// HasCells reports whether there is any cell besides the root cells "0" and
// "1", or a page in draw.io's compressed format (text instead of a model),
// which is not checked further.
func HasCells(xml string) bool {
	for _, m := range mxCellWithIDRe.FindAllStringSubmatch(xml, -1) {
		if m[1] != "0" && m[1] != "1" {
			return true
		}
	}
	return userObjectRe.MatchString(xml) || diagramTextRe.MatchString(xml)
}

var xmlDeclPrefixRe = regexp.MustCompile(`(?i)^\s*<\?xml[^>]*\?>\s*`)
var mxFileStartRe = regexp.MustCompile(`(?i)^\s*(<\?xml[^>]*\?>\s*)?<mxfile[\s>]`)
var mxGraphModelStartRe = regexp.MustCompile(`(?i)^\s*(<\?xml[^>]*\?>\s*)?<mxGraphModel[\s>]`)

// IsMxFile cheaply checks whether the XML starts with an <mxfile> root.
func IsMxFile(xml string) bool { return mxFileStartRe.MatchString(xml) }

// IsMxGraphModel cheaply checks whether the XML starts with a bare
// <mxGraphModel>.
func IsMxGraphModel(xml string) bool { return mxGraphModelStartRe.MatchString(xml) }

// StripXMLDeclaration removes a leading <?xml ... ?> declaration. It is only
// valid at the very start of a document, so it must be stripped before
// splicing a fragment into a wrapper.
func StripXMLDeclaration(xml string) string {
	return xmlDeclPrefixRe.ReplaceAllString(xml, "")
}

func escapeAttr(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

const ROOT_CELLS = `<mxCell id="0"/><mxCell id="1" parent="0"/>`

// BlankMxfile is a one-page document with only the root cells.
const BlankMxfile = `<mxfile><diagram name="Page-1" id="page-1"><mxGraphModel><root>` + ROOT_CELLS + `</root></mxGraphModel></diagram></mxfile>`

var firstCellRe = regexp.MustCompile(`<(mxCell|UserObject|object|root)[\s/>]`)
var onlyCommentsRe = regexp.MustCompile(`^(?:<!--[\s\S]*?-->|[^<])*$`)
var rootTagRe = regexp.MustCompile(`</?root>`)
var trailingClosesRe = regexp.MustCompile(`^(\s*</[^>]+>)*\s*$`)
var rootCellRe = regexp.MustCompile(`<mxCell\b[^>]*?(?:/>|>\s*</mxCell>)`)

// WrapCellsInModel turns a list of bare cells (optionally inside <root>) into
// a one-page <mxGraphModel>, adding the "0" and "1" root cells. The model
// then only writes its own cells, as in the web app. Root cells the model
// wrote anyway are replaced, and comments or text before the first cell and
// trailing closing tags some providers append are dropped. <mxfile>,
// <mxGraphModel> and anything else are returned unchanged.
func WrapCellsInModel(xml string) string {
	content := StripXMLDeclaration(strings.TrimSpace(xml))
	loc := firstCellRe.FindStringIndex(content)
	if loc == nil {
		return xml
	}
	// Only comments and plain text may come before the first cell
	if !onlyCommentsRe.MatchString(content[:loc[0]]) {
		return xml
	}

	content = strings.TrimSpace(rootTagRe.ReplaceAllString(content[loc[0]:], ""))
	// End of the last cell, counting wrapped cells (</UserObject>, </object>)
	end := -1
	for _, close := range []string{"/>", "</mxCell>", "</UserObject>", "</object>"} {
		if at := strings.LastIndex(content, close); at != -1 {
			if at+len(close) > end {
				end = at + len(close)
			}
		}
	}
	if end != -1 && trailingClosesRe.MatchString(content[end:]) {
		content = content[:end]
	}
	// The root cells come with the wrapper (a label holding id='1' is not
	// an id)
	content = strings.TrimSpace(rootCellRe.ReplaceAllStringFunc(content, func(cell string) string {
		for _, a := range xmlutil.ReadAttributes(cell) {
			if a.Name == "id" && (a.Value == "0" || a.Value == "1") {
				return ""
			}
		}
		return cell
	}))
	return `<mxGraphModel><root>` + ROOT_CELLS + content + `</root></mxGraphModel>`
}

// NormalizeToMxfile wraps a bare <mxGraphModel> in an
// <mxfile><diagram>...</diagram></mxfile>. An already-mxfile input returns
// unchanged; anything else returns "" so the caller can surface a clear
// error.
func NormalizeToMxfile(xml string, pageID, pageName string) string {
	trimmed := strings.TrimSpace(xml)
	if trimmed == "" {
		return ""
	}
	if IsMxFile(trimmed) {
		return trimmed
	}
	if !IsMxGraphModel(trimmed) {
		return ""
	}

	if pageID == "" {
		pageID = GeneratePageID()
	}
	if pageName == "" {
		pageName = "Page-1"
	}
	host := "app.diagrams.net"
	inner := StripXMLDeclaration(trimmed)
	return `<mxfile host="` + escapeAttr(host) + `"><diagram id="` + escapeAttr(pageID) + `" name="` + escapeAttr(pageName) + `">` + inner + `</diagram></mxfile>`
}

// ParseMxfile parses an mxfile XML string. Returns nil on parse error or if
// the root is not <mxfile> — callers are expected to have run
// NormalizeToMxfile first.
func ParseMxfile(xml string) *xmlutil.Node {
	if xmlutil.GetXmlSyntaxError(xml) != "" {
		return nil
	}
	doc, err := xmlutil.ParseXML(xml)
	if err != nil {
		return nil
	}
	if root := doc.DocumentElement(); root == nil || root.Tag != "mxfile" {
		return nil
	}
	return doc
}

// SerializeMxfile serialises an mxfile doc back to a string.
func SerializeMxfile(doc *xmlutil.Node) string {
	return xmlutil.SerializeXML(doc)
}

// PageProjection is the result of ProjectPage.
type PageProjection struct {
	OK     bool
	Reason string // "parse" | "notfound"
	XML    string
	Index  int
	Name   string
}

// ProjectPage projects a single page out of an mxfile string into a
// standalone one-page <mxfile>. Used by get_diagram and export_diagram.
func ProjectPage(xml string, selector *PageSelector) PageProjection {
	doc := ParseMxfile(xml)
	if doc == nil {
		return PageProjection{Reason: "parse"}
	}
	found, index := FindPageElement(doc, selector)
	if found == nil {
		return PageProjection{Reason: "notfound"}
	}
	return PageProjection{
		OK:    true,
		XML:   `<mxfile host="app.diagrams.net">` + xmlutil.SerializeXML(found) + `</mxfile>`,
		Index: index,
		Name:  found.GetAttrOr("name", ""),
	}
}

// ListPagesFromDoc walks every <diagram> child of <mxfile> and returns
// summary info.
func ListPagesFromDoc(doc *xmlutil.Node) []PageInfo {
	diagrams := doc.QuerySelectorAll("diagram")
	result := make([]PageInfo, 0, len(diagrams))
	for idx, d := range diagrams {
		cellCount := 0
		if root := d.QuerySelector("root"); root != nil {
			cellCount = len(root.QuerySelectorAll("mxCell"))
		}
		result = append(result, PageInfo{
			ID:        d.GetAttrOr("id", ""),
			Name:      d.GetAttrOr("name", fmt.Sprintf("Page-%d", idx+1)),
			Index:     idx,
			CellCount: cellCount,
		})
	}
	return result
}

// FindPageElement resolves a page selector to its <diagram> element.
// Resolution order: page_id → page_name → page_index → default (first page).
// Returns (nil, 0) when nothing matches.
func FindPageElement(doc *xmlutil.Node, selector *PageSelector) (*xmlutil.Node, int) {
	diagrams := doc.QuerySelectorAll("diagram")
	if len(diagrams) == 0 {
		return nil, 0
	}
	if !HasPageSelector(selector) {
		return diagrams[0], 0
	}
	if selector.PageID != "" {
		for i, d := range diagrams {
			if d.GetAttrOr("id", "") == selector.PageID {
				return d, i
			}
		}
		return nil, 0
	}
	if selector.PageName != "" {
		for i, d := range diagrams {
			if d.GetAttrOr("name", "") == selector.PageName {
				return d, i
			}
		}
		return nil, 0
	}
	if selector.PageIndex != nil {
		idx := *selector.PageIndex
		if idx >= 0 && idx < len(diagrams) {
			return diagrams[idx], idx
		}
		return nil, 0
	}
	return nil, 0
}

// AddPageToDoc appends a new <diagram> to the mxfile doc. The new page's
// model defaults to an empty model with the "0" and "1" root cells. pageXML
// must be a BARE <mxGraphModel> — a full <mxfile> is rejected explicitly.
func AddPageToDoc(doc *xmlutil.Node, id, name, pageXML string) (PageInfo, error) {
	existing := ListPagesFromDoc(doc)
	if id == "" {
		id = GeneratePageID()
	}
	for _, p := range existing {
		if p.ID == id {
			return PageInfo{}, fmt.Errorf("Page id %q already exists", id)
		}
	}
	if name == "" {
		name = fmt.Sprintf("Page-%d", len(existing)+1)
	}

	inner := `<mxGraphModel><root>` + ROOT_CELLS + `</root></mxGraphModel>`
	if strings.TrimSpace(pageXML) != "" {
		trimmed := StripXMLDeclaration(strings.TrimSpace(pageXML))
		if IsMxFile(trimmed) {
			return PageInfo{}, fmt.Errorf(
				"addPageToDoc: opts.xml must be a bare <mxGraphModel>; received a full <mxfile>. Extract the target diagram's <mxGraphModel> first.")
		}
		if !IsMxGraphModel(trimmed) {
			return PageInfo{}, fmt.Errorf("addPageToDoc: opts.xml must be a bare <mxGraphModel>.")
		}
		inner = trimmed
	}

	snippet := `<wrapper><diagram id="` + escapeAttr(id) + `" name="` + escapeAttr(name) + `">` + inner + `</diagram></wrapper>`
	if xmlutil.GetXmlSyntaxError(snippet) != "" {
		return PageInfo{}, fmt.Errorf(
			"Failed to parse new page xml — make sure it is a valid <mxGraphModel>")
	}
	tempDoc, err := xmlutil.ParseXML(snippet)
	if err != nil {
		return PageInfo{}, fmt.Errorf(
			"Failed to parse new page xml — make sure it is a valid <mxGraphModel>")
	}
	newDiagram := tempDoc.QuerySelector("diagram")
	if newDiagram == nil {
		return PageInfo{}, fmt.Errorf("Failed to construct <diagram> element for new page")
	}

	imported := newDiagram.CloneNode(true)
	doc.DocumentElement().AppendChild(imported)

	return PageInfo{
		ID:        id,
		Name:      name,
		Index:     len(existing),
		CellCount: len(imported.QuerySelectorAll("mxCell")),
	}, nil
}

// RenamePageInDoc renames the page matched by selector. Returns true on
// success.
func RenamePageInDoc(doc *xmlutil.Node, selector *PageSelector, newName string) bool {
	found, _ := FindPageElement(doc, selector)
	if found == nil {
		return false
	}
	found.SetAttr("name", newName)
	return true
}

// DeletePageResult is the outcome of DeletePageFromDoc.
type DeletePageResult struct {
	OK           bool
	Reason       string
	DeletedID    string
	DeletedIndex int
}

// DeletePageFromDoc deletes a page. Refuses to delete the last remaining
// page — the embed needs at least one diagram to render anything, and
// silently recreating one would be surprising behaviour for an MCP caller.
func DeletePageFromDoc(doc *xmlutil.Node, selector *PageSelector) DeletePageResult {
	// Match first, so a wrong selector reports "not found" even on a
	// one-page document
	found, index := FindPageElement(doc, selector)
	if found == nil {
		return DeletePageResult{Reason: "Page not found"}
	}
	if len(ListPagesFromDoc(doc)) <= 1 {
		return DeletePageResult{Reason: "Cannot delete the only remaining page"}
	}
	id := found.GetAttrOr("id", "")
	found.Parent.RemoveChild(found)
	return DeletePageResult{OK: true, DeletedID: id, DeletedIndex: index}
}

var reservedIDInputRe = regexp.MustCompile(`<(mxGraphModel|mxfile)\b`)
var reservedIDTagRe = regexp.MustCompile(`<(mxCell|UserObject|object)\b((?:\s+[\w:.-]+\s*=\s*(?:"[^"]*"|'[^']*'))*)\s*\/?>`)

// ReservedIDError checks for shapes or edges with the reserved root cell ids
// "0"/"1" — they would be renamed as duplicates, breaking their edges. The
// id may sit on a <UserObject> or <object> wrapper. Returns the error for
// the model, or "".
func ReservedIDError(input string) string {
	if reservedIDInputRe.MatchString(input) {
		return ""
	}
	// Each opening tag with its attributes; quoted values are read as a
	// whole, so text such as label="id='1'" is not an attribute
	for _, m := range reservedIDTagRe.FindAllStringSubmatch(input, -1) {
		tag, attrText := m[1], m[2]
		attrs := map[string]string{}
		for _, a := range xmlutil.ReadAttributes(attrText) {
			attrs[a.Name] = a.Value
		}
		id := attrs["id"]
		if id != "0" && id != "1" {
			continue
		}
		// A wrapper's id is its cell's; an mxCell counts as a shape or edge
		if tag != "mxCell" || attrs["vertex"] == "1" || attrs["edge"] == "1" {
			return `Cell ids "0" and "1" are the root cells, which are added automatically. Give shapes and edges ids starting at "2".`
		}
	}
	return ""
}

// NewDiagram is the outcome of PrepareNewDiagram.
type NewDiagram struct {
	OK    bool
	XML   string
	Fixes []string
	Error string
}

// PrepareNewDiagram prepares a whole new diagram written by the model, for
// the create_new_diagram tool and the web app's display_diagram tool.
// Bare cells get the wrapper and root cells first, since the strict parser
// rejects several top-level elements. Then the XML is validated and
// auto-fixed while it is still a bare model, where duplicate ids are
// renamed, and finally turned into an <mxfile>.
func PrepareNewDiagram(input, pageID, pageName string) NewDiagram {
	if reserved := ReservedIDError(input); reserved != "" {
		return NewDiagram{Error: reserved}
	}
	xml := WrapCellsInModel(input)
	valid, errMsg, fixed, fixes := xmlutil.ValidateAndFixXML(xml, true)
	if fixed != "" {
		xml = fixed
	}
	if !valid {
		return NewDiagram{Error: "XML validation failed - " + errMsg}
	}
	normalized := NormalizeToMxfile(xml, pageID, pageName)
	if normalized == "" {
		return NewDiagram{Error: "XML must be the mxCell elements of one page, a <mxGraphModel>, or an <mxfile> with one or more <diagram> children."}
	}
	return NewDiagram{OK: true, XML: normalized, Fixes: fixes}
}

// canonicalizeElement serialises an element subtree canonically: tag +
// attributes sorted by name + child elements in order + non-whitespace text.
// Whitespace-only text nodes (pretty-printing) are dropped.
func canonicalizeElement(el *xmlutil.Node) string {
	attrs := make([]string, 0, len(el.Attrs))
	for _, a := range el.Attrs {
		attrs = append(attrs, a.Name+"="+strconv.Quote(a.Value))
	}
	sort.Strings(attrs)
	var children strings.Builder
	for _, child := range el.Children {
		if child.Type == xmlutil.ElementNode {
			children.WriteString(canonicalizeElement(child))
		} else if child.Type == xmlutil.TextNode {
			if text := strings.TrimSpace(child.Text); text != "" {
				children.WriteString(strconv.Quote(text))
			}
		}
	}
	return `<` + el.Tag + ` ` + strings.Join(attrs, " ") + `>` + children.String() + `</` + el.Tag + `>`
}

// ContentFingerprint builds a structural fingerprint of a diagram document:
// page names + each page's <root> subtree, ignoring everything draw.io
// rewrites on re-serialisation (mxfile/mxGraphModel attributes, diagram ids,
// formatting). A bare <mxGraphModel> fingerprints identically to its
// single-page mxfile wrapping. Unparseable input falls back to the trimmed
// raw string.
//
// With includeNames=false page names are dropped — used when the other side
// of a comparison is a bare <mxGraphModel>, which carries no page name at
// all.
func ContentFingerprint(xml string, includeNames bool) string {
	normalized := NormalizeToMxfile(xml, "", "")
	var doc *xmlutil.Node
	if normalized != "" {
		doc = ParseMxfile(normalized)
	}
	if doc == nil {
		return strings.TrimSpace(xml)
	}
	var pages []string
	for _, d := range doc.QuerySelectorAll("diagram") {
		name := ""
		if includeNames {
			name = d.GetAttrOr("name", "")
		}
		root := d.QuerySelector("root")
		// No <root> means the page content is not plain XML (e.g. draw.io's
		// compressed format) — fingerprint the raw text instead.
		body := ""
		if root != nil {
			body = canonicalizeElement(root)
		} else {
			body = strings.TrimSpace(d.TextContent())
		}
		pages = append(pages, name+"="+body)
	}
	return strings.Join(pages, "\n")
}

// EditGateResult is the outcome of CheckEditGate.
type EditGateResult struct {
	OK     bool
	Reason string // "no-context" | "stale"
}

// CheckEditGate compares the store XML the model last saw with the live
// store XML. Instead of a wall-clock timeout we compare content: the store
// only changes on server writes or browser pushes, so if the live store
// still matches lastSeenXml, nothing happened that the model hasn't seen.
func CheckEditGate(lastSeenXML, liveXML string) EditGateResult {
	// Model never fetched or produced any diagram state in this session.
	if lastSeenXML == "" {
		return EditGateResult{Reason: "no-context"}
	}
	// Browser state moved since the model last looked (e.g. manual user
	// edits): force a re-fetch. An empty liveXml means the store has no
	// entry to compare against, so there is nothing newer to have missed.
	if liveXML != "" && liveXML != lastSeenXML {
		// A bare <mxGraphModel> on either side carries no page name, so
		// comparing names would mismatch against anything not called
		// "Page-1". Compare cell trees only in that case.
		includeNames := !IsMxGraphModel(liveXML) && !IsMxGraphModel(lastSeenXML)
		if ContentFingerprint(liveXML, includeNames) != ContentFingerprint(lastSeenXML, includeNames) {
			return EditGateResult{Reason: "stale"}
		}
	}
	return EditGateResult{OK: true}
}

// MarkPageSeen returns the new lastSeenXml after the model was shown only
// the selected page of liveXml. It has seen the whole document if the other
// pages are as it last saw them, or if it saw nothing before.
func MarkPageSeen(lastSeenXML, liveXML string, selector *PageSelector) string {
	parse := func(xml string) *xmlutil.Node {
		normalized := NormalizeToMxfile(xml, "", "")
		if normalized == "" {
			return nil
		}
		return ParseMxfile(normalized)
	}
	// An empty record also follows load_diagram and page tools that wrote
	// over unseen changes, when the model may remember older pages: only
	// a one-page document is then fully seen
	if lastSeenXML == "" {
		pages := 1
		if doc := parse(liveXML); doc != nil {
			pages = len(doc.QuerySelectorAll("diagram"))
		}
		if pages <= 1 {
			return liveXML
		}
		return lastSeenXML
	}
	otherPages := func(xml string) string {
		doc := parse(xml)
		if doc == nil {
			return "\x00null"
		}
		if found, _ := FindPageElement(doc, selector); found != nil {
			found.Parent.RemoveChild(found)
		}
		return ContentFingerprint(SerializeMxfile(doc), true)
	}
	before := otherPages(lastSeenXML)
	return boolString(before != "\x00null" && before == otherPages(liveXML), liveXML, lastSeenXML)
}

func boolString(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
