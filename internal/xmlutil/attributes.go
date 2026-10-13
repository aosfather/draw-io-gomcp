package xmlutil

import "regexp"

// TagAttribute is one attribute as written in a tag: name="value" or
// name='value'. A quoted value is read whole, so text inside it such as
// value="Use parent='1'" is never taken for an attribute.
type TagAttribute struct {
	Name  string
	Value string
	// Start/End are the attribute's text bounds (including the leading
	// whitespace) inside the tag string.
	Start int
	End   int
}

var attrRe = regexp.MustCompile(`\s*([A-Za-z_:][\w:.-]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`)

// ReadAttributes mirrors xml-attributes.ts readAttributes.
func ReadAttributes(tag string) []TagAttribute {
	var attributes []TagAttribute
	for _, m := range attrRe.FindAllStringSubmatchIndex(tag, -1) {
		// group 2 holds a double-quoted value, group 3 a single-quoted one
		value := ""
		if m[4] >= 0 {
			value = tag[m[4]:m[5]]
		} else {
			value = tag[m[6]:m[7]]
		}
		attributes = append(attributes, TagAttribute{
			Name:  tag[m[2]:m[3]],
			Value: value,
			Start: m[0],
			End:   m[1],
		})
	}
	return attributes
}
