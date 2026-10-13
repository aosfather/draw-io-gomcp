package xmlutil

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Port of xml-validation.ts: validation and auto-fix for draw.io XML.
// Error messages are kept verbatim so the model sees identical feedback.

// maxXMLSize is the maximum XML size to process (1MB) - larger XMLs may
// cause performance issues.
const maxXMLSize = 1_000_000

// structuralAttrs are attributes that should not be duplicated in draw.io.
var structuralAttrs = []string{"edge", "parent", "source", "target", "vertex", "connectable"}

var validEntities = map[string]bool{"lt": true, "gt": true, "amp": true, "quot": true, "apos": true}

// validDrawioTagList keeps the declaration order: it is interpolated into
// error messages exactly like the TypeScript Set iteration order.
var validDrawioTagList = []string{
	"mxfile", "diagram", "mxGraphModel", "root", "mxCell", "mxGeometry",
	"mxPoint", "Array", "Object", "mxRectangle", "UserObject", "object",
}

var validDrawioTags = func() map[string]bool {
	m := make(map[string]bool, len(validDrawioTagList))
	for _, t := range validDrawioTagList {
		m[t] = true
	}
	return m
}()

var stderrLog = log.New(os.Stderr, "", 0)

// ============================================================================
// XML parsing helpers
// ============================================================================

type parsedTag struct {
	tag           string
	tagName       string
	isClosing     bool
	isSelfClosing bool
	startIndex    int
	endIndex      int
}

var tagHeadRe = regexp.MustCompile(`^<(\/?)([a-zA-Z][a-zA-Z0-9:_-]*)`)

// parseXmlTags parses XML tags while properly handling quoted strings.
func parseXmlTags(x string) []parsedTag {
	var tags []parsedTag
	i := 0
	for i < len(x) {
		rel := strings.IndexByte(x[i:], '<')
		if rel == -1 {
			break
		}
		tagStart := i + rel

		tagEnd := tagStart + 1
		inQuote := false
		quoteChar := byte(0)
		for tagEnd < len(x) {
			c := x[tagEnd]
			if inQuote {
				if c == quoteChar {
					inQuote = false
				}
			} else {
				if c == '"' || c == '\'' {
					inQuote = true
					quoteChar = c
				} else if c == '>' {
					break
				}
			}
			tagEnd++
		}
		if tagEnd >= len(x) {
			break
		}
		tag := x[tagStart : tagEnd+1]
		i = tagEnd + 1

		m := tagHeadRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		tags = append(tags, parsedTag{
			tag:           tag,
			tagName:       m[2],
			isClosing:     m[1] == "/",
			isSelfClosing: strings.HasSuffix(tag, "/>"),
			startIndex:    tagStart,
			endIndex:      tagEnd,
		})
	}
	return tags
}

var commentRe = regexp.MustCompile(`(?s)<!--[\s\S]*?-->`)

func stripComments(x string) string {
	return commentRe.ReplaceAllString(x, "")
}

func isSpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	}
	return false
}

// quoteTracker tells whether a position lies inside a quoted attribute
// value. Positions must be queried in increasing order.
type quoteTracker struct {
	s         string
	i         int
	inQuote   bool
	quoteChar byte
}

func newQuoteTracker(s string) *quoteTracker { return &quoteTracker{s: s} }

func (q *quoteTracker) inside(pos int) bool {
	for ; q.i < pos && q.i < len(q.s); q.i++ {
		c := q.s[q.i]
		if q.inQuote {
			if c == q.quoteChar {
				q.inQuote = false
			}
		} else if c == '"' || c == '\'' {
			// Only quotes that follow "=" open an attribute value
			j := q.i - 1
			for j >= 0 && isSpaceByte(q.s[j]) {
				j--
			}
			if j >= 0 && q.s[j] == '=' {
				q.inQuote = true
				q.quoteChar = c
			}
		}
	}
	return q.inQuote
}

// replaceInOpeningTags rewrites every opening tag with fn, leaving text and
// closing tags as is.
func replaceInOpeningTags(x string, fn func(tag string) string) string {
	var out strings.Builder
	last := 0
	for _, t := range parseXmlTags(x) {
		if t.isClosing {
			continue
		}
		out.WriteString(x[last:t.startIndex])
		out.WriteString(fn(t.tag))
		last = t.endIndex + 1
	}
	out.WriteString(x[last:])
	return out.String()
}

type span struct{ start, end int }

// ============================================================================
// Validation helper functions
// ============================================================================

var anyTagRe = regexp.MustCompile(`<[^>]+>`)

// checkDuplicateAttributes checks for duplicate structural attributes in a tag.
func checkDuplicateAttributes(x string) string {
	for _, loc := range anyTagRe.FindAllStringIndex(x, -1) {
		tag := x[loc[0]:loc[1]]
		attrs := ReadAttributes(tag)
		counts := make(map[string]int, len(attrs))
		var order []string
		for _, a := range attrs {
			if counts[a.Name] == 0 {
				order = append(order, a.Name)
			}
			counts[a.Name]++
		}
		var dups []string
		for _, name := range order {
			if counts[name] > 1 && isStructural(name) {
				dups = append(dups, name)
			}
		}
		if len(dups) > 0 {
			return fmt.Sprintf("Invalid XML: Duplicate structural attribute(s): %s. Remove duplicate attributes.", strings.Join(dups, ", "))
		}
	}
	return ""
}

func isStructural(name string) bool {
	for _, s := range structuralAttrs {
		if s == name {
			return true
		}
	}
	return false
}

var mxfileRe = regexp.MustCompile(`(?i)<mxfile[\s>]`)
var legacyIDRe = regexp.MustCompile(`(?i)\bid\s*=\s*["']([^"']+)["']`)

// checkDuplicateIds checks for duplicate IDs in XML. Cell IDs are unique
// within a page, not across a multi-page document, so the DOM-aware check is
// page-scoped; the legacy regex check is kept for bare models.
func checkDuplicateIds(x string) string {
	if mxfileRe.MatchString(x) {
		doc, err := ParseXML(x)
		if err == nil {
			root := doc.DocumentElement()
			if root != nil && root.Tag == "mxfile" {
				diagrams := root.QuerySelectorAll("diagram")

				// 1) <diagram> ids must be unique across the file.
				diagramIDs := map[string]int{}
				var diagramOrder []string
				for _, d := range diagrams {
					id := d.GetAttrOr("id", "")
					if id != "" {
						if diagramIDs[id] == 0 {
							diagramOrder = append(diagramOrder, id)
						}
						diagramIDs[id]++
					}
				}
				var dupDiagrams []string
				for _, id := range diagramOrder {
					if diagramIDs[id] > 1 {
						dupDiagrams = append(dupDiagrams, "'"+id+"'")
					}
				}
				if len(dupDiagrams) > 0 {
					if len(dupDiagrams) > 3 {
						dupDiagrams = dupDiagrams[:3]
					}
					return fmt.Sprintf("Invalid XML: Found duplicate <diagram> id(s): %s. Each page must have a unique id.", strings.Join(dupDiagrams, ", "))
				}

				// 2) Within each page, cell ids must be unique. A cell with
				// a link or custom data is a UserObject/object holding the
				// id, around an mxCell whose own id does not count.
				for i, d := range diagrams {
					pageID := d.GetAttrOr("id", fmt.Sprintf("(index %d)", i))
					cells := d.QuerySelectorAll("mxCell", "UserObject", "object")
					cellIDs := map[string]int{}
					var cellOrder []string
					for _, c := range cells {
						wrapped := c.Tag == "mxCell" && c.Parent != nil &&
							(c.Parent.Tag == "UserObject" || c.Parent.Tag == "object")
						id := c.GetAttrOr("id", "")
						if id != "" && !wrapped {
							if cellIDs[id] == 0 {
								cellOrder = append(cellOrder, id)
							}
							cellIDs[id]++
						}
					}
					var dups []string
					for _, id := range cellOrder {
						if cellIDs[id] > 1 {
							dups = append(dups, fmt.Sprintf("'%s' (%dx)", id, cellIDs[id]))
						}
					}
					if len(dups) > 0 {
						if len(dups) > 3 {
							dups = dups[:3]
						}
						return fmt.Sprintf("Invalid XML: Found duplicate cell ID(s) in page \"%s\": %s. All mxCell ids must be unique within a page.", pageID, strings.Join(dups, ", "))
					}
				}
				return ""
			}
		}
		// fall through to regex on parse error
	}

	ids := map[string]int{}
	var order []string
	for _, m := range legacyIDRe.FindAllStringSubmatch(x, -1) {
		if ids[m[1]] == 0 {
			order = append(order, m[1])
		}
		ids[m[1]]++
	}
	var duplicateIDs []string
	for _, id := range order {
		if ids[id] > 1 {
			duplicateIDs = append(duplicateIDs, fmt.Sprintf("'%s' (%dx)", id, ids[id]))
		}
	}
	if len(duplicateIDs) > 0 {
		if len(duplicateIDs) > 3 {
			duplicateIDs = duplicateIDs[:3]
		}
		return fmt.Sprintf("Invalid XML: Found duplicate ID(s): %s. All id attributes must be unique.", strings.Join(duplicateIDs, ", "))
	}
	return ""
}

// checkTagMismatches checks for tag mismatches using parsed tags.
func checkTagMismatches(x string) string {
	tags := parseXmlTags(stripComments(x))
	var stack []string
	for _, t := range tags {
		if t.isClosing {
			if len(stack) == 0 {
				return fmt.Sprintf("Invalid XML: Closing tag </%s> without matching opening tag", t.tagName)
			}
			expected := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !strings.EqualFold(expected, t.tagName) {
				return fmt.Sprintf("Invalid XML: Expected closing tag </%s> but found </%s>", expected, t.tagName)
			}
		} else if !t.isSelfClosing {
			stack = append(stack, t.tagName)
		}
	}
	if len(stack) > 0 {
		return fmt.Sprintf("Invalid XML: Document has %d unclosed tag(s): %s", len(stack), strings.Join(stack, ", "))
	}
	return ""
}

var charRefRe = regexp.MustCompile(`&#x?[^;]+;?`)
var hexDigitsRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)
var decDigitsRe = regexp.MustCompile(`^[0-9]+$`)

// checkCharacterReferences checks for invalid character references.
func checkCharacterReferences(x string) string {
	for _, loc := range charRefRe.FindAllStringIndex(x, -1) {
		ref := x[loc[0]:loc[1]]
		if strings.HasPrefix(ref, "&#x") {
			if !strings.HasSuffix(ref, ";") {
				return "Invalid XML: Missing semicolon after hex reference: " + ref
			}
			hex := ref[3 : len(ref)-1]
			if hex == "" || !hexDigitsRe.MatchString(hex) {
				return "Invalid XML: Invalid hex character reference: " + ref
			}
		} else if strings.HasPrefix(ref, "&#") {
			if !strings.HasSuffix(ref, ";") {
				return "Invalid XML: Missing semicolon after decimal reference: " + ref
			}
			dec := ref[2 : len(ref)-1]
			if dec == "" || !decDigitsRe.MatchString(dec) {
				return "Invalid XML: Invalid decimal character reference: " + ref
			}
		}
	}
	return ""
}

// ampWithPrefixRe matches "&" followed by one of the valid entity prefixes
// (the TypeScript source uses a negative lookahead, which RE2 lacks; counting
// is equivalent because every "&" either has such a prefix or does not).
var ampWithPrefixRe = regexp.MustCompile(`&(?:lt|gt|amp|quot|apos|#)`)
var invalidEntityRe = regexp.MustCompile(`&([a-zA-Z][a-zA-Z0-9]*);`)

// checkEntityReferences checks for unescaped ampersands and invalid entity
// references.
func checkEntityReferences(x string) string {
	stripped := stripComments(x)
	if strings.Count(stripped, "&") > len(ampWithPrefixRe.FindAllStringIndex(stripped, -1)) {
		return "Invalid XML: Found unescaped & character(s). Replace & with &amp;"
	}
	for _, m := range invalidEntityRe.FindAllStringSubmatch(stripped, -1) {
		if !validEntities[m[1]] {
			return fmt.Sprintf("Invalid XML: Invalid entity reference: &%s; - use only valid XML entities (lt, gt, amp, quot, apos)", m[1])
		}
	}
	return ""
}

var cellTagRe = regexp.MustCompile(`</?mxCell[^>]*>`)
var labelGeometryRe = regexp.MustCompile(`\sas\s*=\s*["'](valueLabel|geometry)["']`)

// checkNestedMxCells checks for nested mxCell tags using regex.
func checkNestedMxCells(x string) string {
	var stack []int
	for _, loc := range cellTagRe.FindAllStringIndex(x, -1) {
		tag := x[loc[0]:loc[1]]
		if strings.HasPrefix(tag, "</mxCell>") {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		} else if !strings.HasSuffix(tag, "/>") {
			isLabelOrGeometry := labelGeometryRe.MatchString(tag)
			if !isLabelOrGeometry {
				stack = append(stack, loc[0])
				if len(stack) > 1 {
					return "Invalid XML: Found nested mxCell tags. Cells should be siblings, not nested inside other mxCell elements."
				}
			}
		}
	}
	return ""
}

// checkUnknownElements checks for element names draw.io does not know.
func checkUnknownElements(x string) string {
	tags := parseXmlTags(stripComments(x))
	for _, t := range tags {
		if !validDrawioTags[t.tagName] {
			return fmt.Sprintf("Invalid XML: Unknown element <%s>. draw.io only understands %s (names are case-sensitive).", t.tagName, strings.Join(validDrawioTagList, ", "))
		}
	}
	return ""
}

var arrayOpenRe = regexp.MustCompile(`<Array\b[^>]*>`)
var mxPointRe = regexp.MustCompile(`<mxPoint\b[^>]*?(?:/>|>\s*</mxPoint>)`)
var asAttrRe = regexp.MustCompile(`\sas\s*=`)

// findOrphanMxPoints finds <mxPoint> elements without an "as" attribute
// outside <Array as="points">. draw.io rejects them with "Could not add
// object mxPoint".
func findOrphanMxPoints(x string) []span {
	var arrays []span
	for _, loc := range arrayOpenRe.FindAllStringIndex(x, -1) {
		openTag := x[loc[0]:loc[1]]
		// an empty <Array/> has no points inside
		if strings.HasSuffix(openTag, "/>") {
			continue
		}
		rel := strings.Index(x[loc[1]:], "</Array>")
		if rel == -1 {
			continue
		}
		arrays = append(arrays, span{loc[0], loc[1] + rel + len("</Array>")})
	}
	var orphans []span
	for _, loc := range mxPointRe.FindAllStringIndex(x, -1) {
		if asAttrRe.MatchString(x[loc[0]:loc[1]]) {
			continue
		}
		inside := false
		for _, a := range arrays {
			if loc[0] > a.start && loc[0] < a.end {
				inside = true
				break
			}
		}
		if !inside {
			orphans = append(orphans, span{loc[0], loc[1]})
		}
	}
	return orphans
}

// findTextBetweenTags returns the first non-blank text under el, skipping a
// page's compressed data.
func findTextBetweenTags(el *Node) string {
	if el == nil {
		return ""
	}
	// A <diagram> with only text holds the page compressed
	compressed := el.Tag == "diagram" && len(el.ElementChildren()) == 0
	for _, child := range el.Children {
		if child.Type == ElementNode {
			if text := findTextBetweenTags(child); text != "" {
				return text
			}
		} else if child.Type == TextNode && !compressed {
			text := strings.TrimSpace(child.Text)
			if text != "" {
				return truncateRunes(text, 40)
			}
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ============================================================================
// Main validation function
// ============================================================================

var cdataStartRe = regexp.MustCompile(`^\s*<!\[CDATA\[`)
var doubleQuotedValueRe = regexp.MustCompile(`=\s*"([^"]*)"`)
var emptyIDCellRe = regexp.MustCompile(`<mxCell[^>]*\sid\s*=\s*["']\s*["'][^>]*>`)
var commentContentRe = regexp.MustCompile(`(?s)<!--([\s\S]*?)-->`)

// ValidateMxCellStructure validates draw.io XML structure for common issues,
// using DOM parsing plus regex checks. With strict it also rejects unknown
// element names and orphan <mxPoint>s (for XML the model wrote, not for
// files or browser state). Returns "" when valid.
func ValidateMxCellStructure(x string, strict bool) string {
	if len(x) > maxXMLSize {
		stderrLog.Printf("[validateMxCellStructure] XML size (%d) exceeds %d bytes, may cause performance issues", len(x), maxXMLSize)
	}

	// 0. DOM-based checks. Syntax errors are caught by the strict check at
	// the end: linkedom's DOMParser never reported them either.
	if doc, err := ParseXML(x); err == nil {
		for _, cell := range doc.QuerySelectorAll("mxCell") {
			if cell.Parent != nil && cell.Parent.Type == ElementNode && cell.Parent.Tag == "mxCell" {
				id := cell.GetAttrOr("id", "unknown")
				return fmt.Sprintf("Invalid XML: Found nested mxCell (id=\"%s\"). Cells should be siblings, not nested inside other mxCell elements.", id)
			}
		}
		// draw.io reads any text inside a page as compressed page data and
		// then fails to open the page
		if text := findTextBetweenTags(doc.DocumentElement()); text != "" {
			return fmt.Sprintf("Invalid XML: Found text \"%s\" between tags. Labels belong in the value attribute; remove any other text between tags.", text)
		}
	} else {
		stderrLog.Printf("[validateMxCellStructure] DOM parse failed, falling back to regex validation: %v", err)
	}

	// 1. Check for CDATA wrapper (invalid at document root)
	if cdataStartRe.MatchString(x) {
		return "Invalid XML: XML is wrapped in CDATA section - remove <![CDATA[ from start and ]]> from end"
	}

	// 2. Check for duplicate structural attributes
	if err := checkDuplicateAttributes(x); err != "" {
		return err
	}

	// 3. Check for unescaped < in attribute values
	for _, m := range doubleQuotedValueRe.FindAllStringSubmatch(x, -1) {
		value := m[1]
		if strings.Contains(value, "<") && !strings.Contains(value, "&lt;") {
			return "Invalid XML: Unescaped < character in attribute values. Replace < with &lt;"
		}
	}

	// 4. Check for duplicate IDs
	if err := checkDuplicateIds(x); err != "" {
		return err
	}

	// 5. Check for tag mismatches
	if err := checkTagMismatches(x); err != "" {
		return err
	}

	// 6. Check invalid character references
	if err := checkCharacterReferences(x); err != "" {
		return err
	}

	// 7. Check for invalid comment syntax (-- inside comments)
	for _, m := range commentContentRe.FindAllStringSubmatch(x, -1) {
		if strings.Contains(m[1], "--") {
			return "Invalid XML: Comment contains -- (double hyphen) which is not allowed"
		}
	}

	// 8. Check for unescaped entity references and invalid entity names
	if err := checkEntityReferences(x); err != "" {
		return err
	}

	// 9. Check for empty id attributes on mxCell
	if emptyIDCellRe.MatchString(x) {
		return "Invalid XML: Found mxCell element(s) with empty id attribute"
	}

	// 10. Check for nested mxCell tags
	if err := checkNestedMxCells(x); err != "" {
		return err
	}

	if strict {
		if err := checkUnknownElements(x); err != "" {
			return err
		}
		if len(findOrphanMxPoints(x)) > 0 {
			return `Invalid XML: Found <mxPoint> without an "as" attribute outside <Array as="points">. Put waypoints inside <Array as="points"> or remove the point.`
		}
	}

	// 11. Strict XML syntax check, run last so the checks above can give
	// more specific messages. Catches what they miss, e.g. duplicate or
	// unquoted attributes, which make draw.io refuse to load the diagram.
	if err := GetXmlSyntaxError(x); err != "" {
		return fmt.Sprintf("Invalid XML: syntax error at %s Escape special characters in attribute values (&lt; for <, &amp; for &, &quot; for \"), quote every attribute value, and do not repeat an attribute.", err)
	}

	return ""
}

// ============================================================================
// Auto-fix
// ============================================================================

var jsonEscapeRe = regexp.MustCompile(`=\\"`)
var escapedQuoteRe = regexp.MustCompile(`\\"`)
var escapedNRe = regexp.MustCompile(`\\n`)
var wsGapRe = regexp.MustCompile(`(?:\s|\\[nrt])+`) // applied between > and < only
var cdataEndRe = regexp.MustCompile(`\]\]>\s*$`)
var xmlStartRe = regexp.MustCompile(`(?i)<(\?xml|mxGraphModel|mxfile)`)
var startsWithTagRe = regexp.MustCompile(`^<[a-zA-Z]`)
var bareAmpEntityRe = regexp.MustCompile(`^(?:lt|gt|amp|quot|apos|#[0-9]+|#x[0-9a-fA-F]+);`)
var attrQuotedOrBadQuotesRe = regexp.MustCompile(`("[^"]*"|'[^']*')|(\s[a-zA-Z][a-zA-Z0-9_:-]*)=&quot;([^&]*?)&quot;`)
var malformedClosingTagRe = regexp.MustCompile(`</([a-zA-Z][a-zA-Z0-9]*)\s*/>`)
var missingSpaceRe = regexp.MustCompile(`("[^"]*"|'[^']*')([a-zA-Z_:])?`)
var quotedColorRe = regexp.MustCompile(`;([a-zA-Z]*[Cc]olor)="#`)
var whitespaceOrEscapesRe = regexp.MustCompile(`^(?:\s|\\[nrt])+$`)
var attrWithLtRe = regexp.MustCompile(`=\s*"[^"]*<[^"]*"`)
var hexRefRe = regexp.MustCompile(`&#x([^;]*);`)
var hexValidRe = regexp.MustCompile(`^[0-9a-fA-F]+$`)
var decRefRe = regexp.MustCompile(`&#([^x][^;]*);`)
var decValidRe = regexp.MustCompile(`^[0-9]+$`)
var cellOpenRe = regexp.MustCompile(`(?i)<Cell(\s)`)
var cellEmptyRe = regexp.MustCompile(`(?i)<Cell>`)
var cellCloseRe = regexp.MustCompile(`(?i)</Cell>`)
var mxElementCloseRe = regexp.MustCompile(`(?i)</mxElement>`)
var foreignTagRe = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9_]*)[^>]*>`)
var fullTagRe = regexp.MustCompile(`<(\/?[a-zA-Z][a-zA-Z0-9]*)[^>]*>`)
var lastClosingTagRe = regexp.MustCompile(`</[a-zA-Z][a-zA-Z0-9]*>|/>`)
var mxCellOpenLineRe = regexp.MustCompile(`<mxCell\s`)
var idExtractRe = regexp.MustCompile(`\bid\s*=\s*["']([^"']+)["']`)
var emptyCloseLineRe = regexp.MustCompile(`^\s*</mxCell>\s*$`)
var leadingWSRe = regexp.MustCompile(`^(\s*)`)
var emptyIDGenRe = regexp.MustCompile(`<mxCell([^>]*)\sid\s*=\s*["']\s*["']([^>]*)>`)

// escapeBareAmpersands replaces every "&" that does not start a complete
// valid entity with "&amp;" (equivalent to the TypeScript negative-lookahead
// regex in fix step 4).
func escapeBareAmpersands(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '&' && !bareAmpEntityRe.MatchString(s[i+1:]) {
			b.WriteString("&amp;")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// AutoFixXML attempts to fix common XML issues in draw.io diagrams. Every
// step maps 1:1 to autoFixXml in xml-validation.ts.
func AutoFixXML(x string) (fixed string, fixes []string) {
	fixed = x
	fixes = []string{}

	// 0. Fix JSON-escaped XML
	if jsonEscapeRe.MatchString(fixed) {
		fixed = escapedQuoteRe.ReplaceAllString(fixed, `"`)
		fixed = escapedNRe.ReplaceAllString(fixed, "\n")
		fixes = append(fixes, "Fixed JSON-escaped XML")
	}

	// 0b. Literal \n, \t or \r between tags, from escaping the XML twice
	unescaped := replaceBetweenTags(fixed, func(gap string) string {
		if !gapIsWhitespaceOrEscapes(gap) {
			return gap
		}
		gap = strings.ReplaceAll(gap, `\n`, "\n")
		gap = strings.ReplaceAll(gap, `\t`, "\t")
		gap = strings.ReplaceAll(gap, `\r`, "")
		return gap
	})
	if unescaped != fixed {
		fixed = unescaped
		fixes = append(fixes, `Replaced literal \n between tags with line breaks`)
	}

	// 1. Remove CDATA wrapper
	if cdataStartRe.MatchString(fixed) {
		fixed = cdataStartRe.ReplaceAllString(fixed, "")
		fixed = cdataEndRe.ReplaceAllString(fixed, "")
		fixes = append(fixes, "Removed CDATA wrapper")
	}

	// 2. Remove text before XML declaration or root element
	if loc := xmlStartRe.FindStringIndex(fixed); loc != nil && loc[0] > 0 && !startsWithTagRe.MatchString(strings.TrimSpace(fixed)) {
		fixed = fixed[loc[0]:]
		fixes = append(fixes, "Removed text before XML root")
	}

	// 3. Fix duplicate attributes (keep the first of each, drop later ones)
	dupAttrFixed := false
	fixed = anyTagRe.ReplaceAllStringFunc(fixed, func(tag string) string {
		seen := map[string]bool{}
		var newTag strings.Builder
		last := 0
		for _, attr := range ReadAttributes(tag) {
			if !isStructural(attr.Name) {
				continue
			}
			if !seen[attr.Name] {
				seen[attr.Name] = true
				continue
			}
			newTag.WriteString(tag[last:attr.Start])
			last = attr.End
			dupAttrFixed = true
		}
		return newTag.String() + tag[last:]
	})
	if dupAttrFixed {
		fixes = append(fixes, "Removed duplicate structural attributes")
	}

	// 4. Fix unescaped & characters
	ampFixed := escapeBareAmpersands(fixed)
	if ampFixed != fixed {
		fixed = ampFixed
		fixes = append(fixes, "Escaped unescaped & characters")
	}

	// 5. Fix invalid entity names (double-escaping)
	for _, e := range []struct{ bad, good, name string }{
		{"&ampquot;", "&quot;", "&ampquot;"},
		{"&amplt;", "&lt;", "&amplt;"},
		{"&ampgt;", "&gt;", "&ampgt;"},
		{"&ampapos;", "&apos;", "&ampapos;"},
		{"&ampamp;", "&amp;", "&ampamp;"},
	} {
		if strings.Contains(fixed, e.bad) {
			fixed = strings.ReplaceAll(fixed, e.bad, e.good)
			fixes = append(fixes, "Fixed double-escaped entity "+e.name)
		}
	}

	// 6. Fix malformed attribute quotes (name=&quot;value&quot;). Quoted
	// values are matched first and kept, so &quot; inside a rich-text label
	// is left alone.
	quotesFixed := false
	fixed = replaceInOpeningTags(fixed, func(tag string) string {
		return attrQuotedOrBadQuotesRe.ReplaceAllStringFunc(tag, func(m string) string {
			idx := attrQuotedOrBadQuotesRe.FindStringSubmatchIndex(m)
			if idx[2] >= 0 { // a quoted value: keep as is
				return m
			}
			name := m[idx[4]:idx[5]]
			value := m[idx[6]:idx[7]]
			quotesFixed = true
			return name + `="` + value + `"`
		})
	})
	if quotesFixed {
		fixes = append(fixes, "Fixed malformed attribute quotes")
	}

	// 7. Fix malformed closing tags
	if malformedClosingTagRe.MatchString(fixed) {
		fixed = malformedClosingTagRe.ReplaceAllString(fixed, "</$1>")
		fixes = append(fixes, "Fixed malformed closing tags")
	}

	// 8. Fix missing space between attributes (id="2"vertex="1")
	spaceAdded := false
	fixed = replaceInOpeningTags(fixed, func(tag string) string {
		return missingSpaceRe.ReplaceAllStringFunc(tag, func(m string) string {
			g := missingSpaceRe.FindStringSubmatch(m)
			quoted, next := g[1], g[2]
			if next == "" {
				return m
			}
			spaceAdded = true
			return quoted + " " + next
		})
	})
	if spaceAdded {
		fixes = append(fixes, "Added missing space between attributes")
	}

	// 9. Fix unescaped quotes in style color values
	if quotedColorRe.MatchString(fixed) {
		fixed = quotedColorRe.ReplaceAllString(fixed, ";$1=#")
		fixes = append(fixes, "Removed quotes around color values in style")
	}

	// 10. Fix unescaped < and > in attribute values
	if attrWithLtRe.MatchString(fixed) {
		fixed = doubleQuotedValueRe.ReplaceAllStringFunc(fixed, func(m string) string {
			g := doubleQuotedValueRe.FindStringSubmatch(m)
			escaped := strings.ReplaceAll(g[1], "<", "&lt;")
			escaped = strings.ReplaceAll(escaped, ">", "&gt;")
			return `="` + escaped + `"`
		})
		fixes = append(fixes, "Escaped <> characters in attribute values")
	}

	// 11. Fix invalid hex character references
	invalidHex := 0
	fixed = hexRefRe.ReplaceAllStringFunc(fixed, func(m string) string {
		g := hexRefRe.FindStringSubmatch(m)
		if hexValidRe.MatchString(g[1]) && g[1] != "" {
			return m
		}
		invalidHex++
		return ""
	})
	if invalidHex > 0 {
		fixes = append(fixes, fmt.Sprintf("Removed %d invalid hex character reference(s)", invalidHex))
	}

	// 12. Fix invalid decimal character references
	invalidDec := 0
	fixed = decRefRe.ReplaceAllStringFunc(fixed, func(m string) string {
		g := decRefRe.FindStringSubmatch(m)
		if decValidRe.MatchString(g[1]) && g[1] != "" {
			return m
		}
		invalidDec++
		return ""
	})
	if invalidDec > 0 {
		fixes = append(fixes, fmt.Sprintf("Removed %d invalid decimal character reference(s)", invalidDec))
	}

	// 13. Fix invalid comment syntax
	fixed = commentContentRe.ReplaceAllStringFunc(fixed, func(m string) string {
		g := commentContentRe.FindStringSubmatch(m)
		content := g[1]
		if strings.Contains(content, "--") {
			fixedContent := content
			for strings.Contains(fixedContent, "--") {
				fixedContent = strings.ReplaceAll(fixedContent, "--", "-")
			}
			fixes = append(fixes, "Fixed invalid comment syntax")
			return "<!--" + fixedContent + "-->"
		}
		return m
	})

	// 14. Fix <Cell> tags to <mxCell>
	if regexp.MustCompile(`(?i)</?Cell[\s>]`).MatchString(fixed) {
		fixed = cellOpenRe.ReplaceAllString(fixed, "<mxCell$1")
		fixed = cellEmptyRe.ReplaceAllString(fixed, "<mxCell>")
		fixed = cellCloseRe.ReplaceAllString(fixed, "</mxCell>")
		fixes = append(fixes, "Fixed <Cell> tags to <mxCell>")
	}

	// 15. Fix closing tag typos and wrong tag case (must run before foreign
	// tag removal, which would otherwise delete them)
	if mxElementCloseRe.MatchString(fixed) {
		fixed = mxElementCloseRe.ReplaceAllString(fixed, "</mxCell>")
		fixes = append(fixes, "Fixed typo </mxElement> to </mxCell>")
	}
	for _, name := range []string{"mxCell", "mxGeometry", "mxPoint", "mxGraphModel"} {
		changed := false
		re := regexp.MustCompile(`(?i)<(/?)` + name + `([\s/>])`)
		fixed = re.ReplaceAllStringFunc(fixed, func(m string) string {
			g := re.FindStringSubmatch(m)
			right := "<" + g[1] + name
			if m[:len(m)-1] != right {
				changed = true
			}
			return right + g[2]
		})
		if changed {
			fixes = append(fixes, fmt.Sprintf("Fixed tag case of <%s>", name))
		}
	}

	// 16. Remove non-draw.io tags (after the case fixes above). Removes only
	// exact tag occurrences and skips quoted attribute values.
	q16 := newQuoteTracker(fixed)
	var foreignPositions []span
	foreignTags := map[string]bool{}
	var foreignOrder []string
	for _, loc := range foreignTagRe.FindAllStringIndex(fixed, -1) {
		tagName := foreignTagName(fixed[loc[0]:loc[1]])
		if validDrawioTags[tagName] {
			continue
		}
		if q16.inside(loc[0]) {
			continue
		}
		if !foreignTags[tagName] {
			foreignTags[tagName] = true
			foreignOrder = append(foreignOrder, tagName)
		}
		foreignPositions = append(foreignPositions, span{loc[0], loc[1]})
	}
	if len(foreignPositions) > 0 {
		// remove from the end so earlier positions stay valid
		for i := len(foreignPositions) - 1; i >= 0; i-- {
			p := foreignPositions[i]
			fixed = fixed[:p.start] + fixed[p.end:]
		}
		fixes = append(fixes, "Removed foreign tags: "+strings.Join(foreignOrder, ", "))
	}

	// 16b. Remove orphan <mxPoint>s
	if orphans := findOrphanMxPoints(fixed); len(orphans) > 0 {
		for i := len(orphans) - 1; i >= 0; i-- {
			p := orphans[i]
			fixed = fixed[:p.start] + fixed[p.end:]
		}
		fixes = append(fixes, fmt.Sprintf("Removed %d orphan <mxPoint>(s)", len(orphans)))
	}

	// 17. Fix unclosed tags
	var stack []string
	for _, t := range parseXmlTags(fixed) {
		if t.isClosing {
			lastIdx := -1
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i] == t.tagName {
					lastIdx = i
					break
				}
			}
			if lastIdx != -1 {
				stack = append(stack[:lastIdx], stack[lastIdx+1:]...)
			}
		} else if !t.isSelfClosing {
			stack = append(stack, t.tagName)
		}
	}
	if len(stack) > 0 {
		var tagsToClose []string
		for i := len(stack) - 1; i >= 0; i-- {
			tagName := stack[i]
			openRe := regexp.MustCompile(`(?i)<` + regexp.QuoteMeta(tagName) + `[\s>]`)
			closeRe := regexp.MustCompile(`(?i)</` + regexp.QuoteMeta(tagName) + `>`)
			if len(openRe.FindAllStringIndex(fixed, -1)) > len(closeRe.FindAllStringIndex(fixed, -1)) {
				tagsToClose = append(tagsToClose, tagName)
			}
		}
		if len(tagsToClose) > 0 {
			closingTags := make([]string, len(tagsToClose))
			for i, t := range tagsToClose {
				closingTags[i] = "</" + t + ">"
			}
			fixed = strings.TrimRight(fixed, " \t\n\r") + "\n" + strings.Join(closingTags, "\n")
			fixes = append(fixes, fmt.Sprintf("Closed %d unclosed tag(s): %s", len(tagsToClose), strings.Join(tagsToClose, ", ")))
		}
	}

	// 18. Remove extra closing tags. Counts only draw.io tags outside quoted
	// attribute values (value="<b>Title</b>" holds HTML, not elements).
	type tagCounts struct{ opens, closes, selfClosing int }
	counts := map[string]*tagCounts{}
	q18 := newQuoteTracker(fixed)
	for _, loc := range fullTagRe.FindAllStringIndex(fixed, -1) {
		if q18.inside(loc[0]) {
			continue
		}
		full := fixed[loc[0]:loc[1]]
		tagPart := fullTagName(full)
		isClosing := strings.HasPrefix(tagPart, "/")
		isSelfClosing := strings.HasSuffix(full, "/>")
		tagName := tagPart
		if isClosing {
			tagName = tagPart[1:]
		}
		if !validDrawioTags[tagName] {
			continue
		}
		c := counts[tagName]
		if c == nil {
			c = &tagCounts{}
			counts[tagName] = c
		}
		if isClosing {
			c.closes++
		} else if isSelfClosing {
			c.selfClosing++
		} else {
			c.opens++
		}
	}
	for tagName, c := range counts {
		if extra := c.closes - c.opens; extra > 0 {
			removed := 0
			closeRe := regexp.MustCompile(`</` + regexp.QuoteMeta(tagName) + `>`)
			matches := closeRe.FindAllStringIndex(fixed, -1)
			for i := len(matches) - 1; i >= 0 && removed < extra; i-- {
				m := matches[i]
				fixed = fixed[:m[0]] + fixed[m[1]:]
				removed++
			}
			if removed > 0 {
				fixes = append(fixes, fmt.Sprintf("Removed %d extra </%s> closing tag(s)", removed, tagName))
			}
		}
	}

	// 19. Remove trailing garbage after last XML tag
	lastValidTagEnd := -1
	for _, loc := range lastClosingTagRe.FindAllStringIndex(fixed, -1) {
		lastValidTagEnd = loc[1]
	}
	if lastValidTagEnd > 0 && lastValidTagEnd < len(fixed) {
		trailing := strings.TrimSpace(fixed[lastValidTagEnd:])
		if trailing != "" {
			fixed = fixed[:lastValidTagEnd]
			fixes = append(fixes, "Removed trailing garbage after last XML tag")
		}
	}

	// 20. Fix nested mxCell by flattening duplicate-ID adjacent cells
	lines := strings.Split(fixed, "\n")
	var newLines []string
	nestedFixed := 0
	extraClosingToRemove := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		nextLine := ""
		if i+1 < len(lines) {
			nextLine = lines[i+1]
		}
		if nextLine != "" &&
			mxCellOpenLineRe.MatchString(line) &&
			mxCellOpenLineRe.MatchString(nextLine) &&
			!strings.Contains(line, "/>") &&
			!strings.Contains(nextLine, "/>") {
			id1 := firstID(line)
			id2 := firstID(nextLine)
			if id1 != "" && id1 == id2 {
				nestedFixed++
				extraClosingToRemove++
				continue
			}
		}
		if extraClosingToRemove > 0 && emptyCloseLineRe.MatchString(line) {
			extraClosingToRemove--
			continue
		}
		newLines = append(newLines, line)
	}
	if nestedFixed > 0 {
		fixed = strings.Join(newLines, "\n")
		fixes = append(fixes, fmt.Sprintf("Flattened %d duplicate-ID nested mxCell(s)", nestedFixed))
	}

	// 21. Fix true nested mxCell (different IDs)
	var lines2 []string
	if checkNestedMxCells(fixed) != "" {
		lines2 = strings.Split(fixed, "\n")
	}
	newLines = []string{}
	trueNestedFixed := 0
	cellDepth := 0
	pendingCloseRemoval := 0
	for _, line := range lines2 {
		trimmed := strings.TrimSpace(line)
		isOpenCell := mxCellOpenLineRe.MatchString(trimmed) &&
			!strings.HasSuffix(trimmed, "/>") &&
			!strings.HasSuffix(trimmed, "</mxCell>")
		isCloseCell := trimmed == "</mxCell>"
		if isOpenCell {
			if cellDepth > 0 {
				indent := ""
				if g := leadingWSRe.FindStringSubmatch(line); g != nil {
					indent = g[1]
				}
				newLines = append(newLines, indent+"</mxCell>")
				trueNestedFixed++
				pendingCloseRemoval++
			}
			cellDepth = 1
			newLines = append(newLines, line)
		} else if isCloseCell {
			if pendingCloseRemoval > 0 {
				pendingCloseRemoval--
			} else {
				if cellDepth > 0 {
					cellDepth--
				}
				newLines = append(newLines, line)
			}
		} else {
			newLines = append(newLines, line)
		}
	}
	if trueNestedFixed > 0 {
		fixed = strings.Join(newLines, "\n")
		fixes = append(fixes, fmt.Sprintf("Fixed %d true nested mxCell(s)", trueNestedFixed))
	}

	// 22. Fix duplicate IDs by appending a suffix. Skipped for multi-page
	// <mxfile> documents - the root cells "0" and "1" repeat per page
	// legitimately, and page-scoped validation reports true duplicates.
	if !mxfileRe.MatchString(fixed) {
		seenIDs := map[string]int{}
		var idOrder []string
		for _, m := range legacyIDRe.FindAllStringSubmatch(fixed, -1) {
			if seenIDs[m[1]] == 0 {
				idOrder = append(idOrder, m[1])
			}
			seenIDs[m[1]]++
		}
		var duplicateIDs []string
		for _, id := range idOrder {
			if seenIDs[id] > 1 {
				duplicateIDs = append(duplicateIDs, id)
			}
		}
		if len(duplicateIDs) > 0 {
			dupSet := map[string]bool{}
			for _, id := range duplicateIDs {
				dupSet[id] = true
			}
			idCounters := map[string]int{}
			idRenameRe := regexp.MustCompile(`(?i)(\bid\s*=\s*["'])([^"']+)(["'])`)
			fixed = idRenameRe.ReplaceAllStringFunc(fixed, func(m string) string {
				g := idRenameRe.FindStringSubmatch(m)
				before, id, after := g[1], g[2], g[3]
				if !dupSet[id] {
					return m
				}
				count := idCounters[id]
				idCounters[id] = count + 1
				if count == 0 {
					return m
				}
				return before + id + "_dup" + strconv.Itoa(count) + after
			})
			fixes = append(fixes, fmt.Sprintf("Renamed %d duplicate ID(s)", len(duplicateIDs)))
		}
	}

	// 23. Fix empty id attributes
	emptyIDCount := 0
	fixed = emptyIDGenRe.ReplaceAllStringFunc(fixed, func(m string) string {
		g := emptyIDGenRe.FindStringSubmatch(m)
		emptyIDCount++
		newID := fmt.Sprintf("cell_%d_%d", time.Now().UnixMilli(), emptyIDCount)
		return "<mxCell" + g[1] + ` id="` + newID + `"` + g[2] + ">"
	})
	if emptyIDCount > 0 {
		fixes = append(fixes, fmt.Sprintf("Generated %d missing ID(s)", emptyIDCount))
	}

	return fixed, fixes
}

// replaceBetweenTags applies fn to every gap between ">" and "<".
func replaceBetweenTags(s string, fn func(string) string) string {
	var b strings.Builder
	for {
		gt := strings.IndexByte(s, '>')
		if gt == -1 {
			b.WriteString(s)
			break
		}
		lt := strings.IndexByte(s[gt+1:], '<')
		if lt == -1 {
			b.WriteString(s)
			break
		}
		end := gt + 1 + lt
		b.WriteString(s[:gt+1])
		b.WriteString(fn(s[gt+1 : end]))
		s = s[end:]
	}
	return b.String()
}

// gapIsWhitespaceOrEscapes reports whether a between-tags gap consists
// entirely of whitespace or literal escape sequences (matches the TypeScript
// regex `>(\s|\\[nrt])+<`, which never fires on arbitrary text).
func gapIsWhitespaceOrEscapes(gap string) bool {
	return whitespaceOrEscapesRe.MatchString(gap)
}

func foreignTagName(tag string) string {
	g := foreignTagRe.FindStringSubmatch(tag)
	if g == nil {
		return ""
	}
	return g[1]
}

func fullTagName(tag string) string {
	g := fullTagRe.FindStringSubmatch(tag)
	if g == nil {
		return ""
	}
	return g[1]
}

func firstID(line string) string {
	g := idExtractRe.FindStringSubmatch(line)
	if g == nil {
		return ""
	}
	return g[1]
}

// ============================================================================
// Combined validation and fix
// ============================================================================

// ValidateAndFixXML validates XML and attempts to fix if invalid. By default
// runs the strict checks (unknown elements, orphan mxPoints), meant for XML
// the model wrote. Pass strict=false for a diagram that also holds the
// user's own content.
func ValidateAndFixXML(x string, strict bool) (valid bool, errMsg string, fixedXML string, fixes []string) {
	errMsg = ValidateMxCellStructure(x, strict)
	if errMsg == "" {
		return true, "", "", nil
	}

	fixedXML, fixes = AutoFixXML(x)
	errMsg = ValidateMxCellStructure(fixedXML, strict)
	if errMsg == "" {
		return true, "", fixedXML, fixes
	}

	if len(fixes) > 0 {
		return false, errMsg, fixedXML, fixes
	}
	return false, errMsg, "", fixes
}
