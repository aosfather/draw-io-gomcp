package xmlutil

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// GetXmlSyntaxError returns the first XML syntax error as
// "line:column: message", or "" when the XML is well-formed. Surrounding
// whitespace is ignored because every caller trims before the XML reaches
// the browser (mirrors xml-syntax.ts on top of saxes).
//
// In addition to what Go's decoder rejects, this catches tag mismatches,
// unclosed tags, repeated attributes and stray text outside the root, which
// draw.io's own parser refuses too.
func GetXmlSyntaxError(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	d := xml.NewDecoder(strings.NewReader(s))
	d.Strict = true

	lineCol := func(offset int64) (int, int) {
		line, col := 1, 1
		for i := int64(0); i < offset && int(i) < len(s); i++ {
			if s[i] == '\n' {
				line++
				col = 1
			} else {
				col++
			}
		}
		return line, col
	}
	fail := func(offset int64, err error) string {
		line, col := lineCol(offset)
		return fmt.Sprintf("%d:%d: %v", line, col, err)
	}

	var stack []string
	rootSeen := false
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fail(d.InputOffset(), err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if rootSeen && len(stack) == 0 {
				return fail(d.InputOffset(), fmt.Errorf("only one root element is allowed"))
			}
			name := nameAsString(t.Name)
			seen := make(map[string]bool, len(t.Attr))
			for _, a := range t.Attr {
				an := nameAsString(a.Name)
				if seen[an] {
					return fail(d.InputOffset(), fmt.Errorf("attribute %q repeated", an))
				}
				seen[an] = true
			}
			stack = append(stack, name)
			rootSeen = true
		case xml.EndElement:
			if len(stack) == 0 {
				return fail(d.InputOffset(), fmt.Errorf("unexpected closing tag </%s>", nameAsString(t.Name)))
			}
			top := stack[len(stack)-1]
			if top != nameAsString(t.Name) {
				return fail(d.InputOffset(), fmt.Errorf("unexpected closing tag </%s>, expected </%s>", nameAsString(t.Name), top))
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 && strings.TrimSpace(string(t)) != "" {
				return fail(d.InputOffset(), fmt.Errorf("text data outside of root element"))
			}
		}
	}
	if len(stack) > 0 {
		return fail(int64(len(s)), fmt.Errorf("unclosed tag <%s>", stack[len(stack)-1]))
	}
	return ""
}
