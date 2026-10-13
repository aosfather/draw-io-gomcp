// Package xmlutil ports the XML layer of the TypeScript MCP server
// (dom.ts, xml-syntax.ts, xml-attributes.ts, xml-validation.ts): a small
// mutable DOM with a faithful serializer, a strict syntax check, an
// attribute reader, and the draw.io XML validation/auto-fix engine.
package xmlutil

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// NodeType mirrors the DOM node types the helpers use.
type NodeType int

const (
	DocumentNode NodeType = iota
	ElementNode
	TextNode
	CommentNode
)

// Attribute is a single element attribute. Name keeps the spelling used in
// the document, including a namespace prefix such as "xmlns:xlink".
type Attribute struct {
	Name  string
	Value string
}

// Node is a document, element, text or comment node. Children is only used
// by Document and Element nodes; Text only by Text and Comment nodes.
type Node struct {
	Type     NodeType
	Tag      string // element tag name
	Attrs    []Attribute
	Children []*Node
	Text     string
	Parent   *Node
}

// XML decoders resolve character references but, unlike the XML spec, keep
// literal tabs and newlines inside attribute values. The TypeScript port
// pre-normalizes them for the same reason (dom.ts, normalizeAttributeWhitespace).
var tagRe = regexp.MustCompile(`<[A-Za-z][^"'<>]*(?:(?:"[^"]*"|'[^']*')[^"'<>]*)*>`)
var quotedValueRe = regexp.MustCompile(`"[^"]*"|'[^']*'`)
var literalWSErrorRe = regexp.MustCompile(`\r\n|[\t\n\r]`)

func normalizeAttributeWhitespace(s string) string {
	return tagRe.ReplaceAllStringFunc(s, func(tag string) string {
		return quotedValueRe.ReplaceAllStringFunc(tag, func(value string) string {
			return literalWSErrorRe.ReplaceAllString(value, " ")
		})
	})
}

// nameAsString rebuilds an attribute/element name as written in the document.
func nameAsString(n xml.Name) string {
	switch n.Space {
	case "":
		return n.Local
	case "xmlns":
		return "xmlns:" + n.Local
	default:
		return n.Space + ":" + n.Local
	}
}

func appendChild(parent, child *Node) *Node {
	child.Parent = parent
	parent.Children = append(parent.Children, child)
	return child
}

// ParseXML parses a complete XML document. It returns an error for anything
// a strict parser rejects, including duplicate attributes, unclosed or
// mismatched tags and multiple root elements. Character references are
// resolved; a comment or CDATA section becomes a Comment or Text node.
func ParseXML(s string) (*Node, error) {
	s = normalizeAttributeWhitespace(strings.TrimSpace(s))
	d := xml.NewDecoder(strings.NewReader(s))
	d.Strict = true

	doc := &Node{Type: DocumentNode}
	stack := []*Node{doc}
	rootSeen := false
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if rootSeen && len(stack) == 1 {
				return nil, errors.New("only one root element is allowed")
			}
			parent := stack[len(stack)-1]
			el := &Node{Type: ElementNode, Tag: nameAsString(t.Name), Parent: parent}
			seen := make(map[string]bool, len(t.Attr))
			for _, a := range t.Attr {
				name := nameAsString(a.Name)
				if seen[name] {
					return nil, fmt.Errorf("attribute %q repeated", name)
				}
				seen[name] = true
				el.Attrs = append(el.Attrs, Attribute{Name: name, Value: a.Value})
			}
			appendChild(parent, el)
			if parent.Type == DocumentNode {
				rootSeen = true
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) == 1 {
				return nil, fmt.Errorf("unexpected closing tag </%s>", nameAsString(t.Name))
			}
			top := stack[len(stack)-1]
			if nameAsString(t.Name) != top.Tag {
				return nil, fmt.Errorf("unexpected closing tag </%s>, expected </%s>", nameAsString(t.Name), top.Tag)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			text := string(t)
			parent := stack[len(stack)-1]
			if parent.Type == DocumentNode {
				if strings.TrimSpace(text) != "" {
					return nil, errors.New("text data outside of root element")
				}
				continue
			}
			appendChild(parent, &Node{Type: TextNode, Text: text, Parent: parent})
		case xml.Comment:
			parent := stack[len(stack)-1]
			if parent.Type == DocumentNode {
				continue
			}
			appendChild(parent, &Node{Type: CommentNode, Text: string(t), Parent: parent})
		case xml.ProcInst, xml.Directive:
			// XML declaration and DOCTYPE are parsed and dropped
		}
	}
	if len(stack) != 1 {
		return nil, fmt.Errorf("unclosed tag <%s>", stack[len(stack)-1].Tag)
	}
	if !rootSeen {
		return nil, errors.New("no root element found")
	}
	return doc, nil
}

// DocumentElement returns the first element child of a Document node.
func (n *Node) DocumentElement() *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Type == ElementNode {
			return c
		}
	}
	return nil
}

// GetAttr returns the value of the named attribute and whether it exists.
func (n *Node) GetAttr(name string) (string, bool) {
	if n == nil {
		return "", false
	}
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// GetAttrOr returns the named attribute value, or def when absent.
func (n *Node) GetAttrOr(name, def string) string {
	if v, ok := n.GetAttr(name); ok {
		return v
	}
	return def
}

// SetAttr sets or adds an attribute, keeping the original order.
func (n *Node) SetAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, Attribute{Name: name, Value: value})
}

// TextContent concatenates the text of all descendant text nodes.
func (n *Node) TextContent() string {
	var b strings.Builder
	var walk func(*Node)
	walk = func(node *Node) {
		if node.Type == TextNode {
			b.WriteString(node.Text)
		}
		for _, c := range node.Children {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// SetTextContent replaces all children with a single text node.
func (n *Node) SetTextContent(s string) {
	n.Children = nil
	if n == nil {
		return
	}
	n.Children = []*Node{{Type: TextNode, Text: s, Parent: n}}
}

// ElementChildren returns the direct element children.
func (n *Node) ElementChildren() []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, c := range n.Children {
		if c.Type == ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// QuerySelector returns the first descendant element whose tag is one of
// tags (document order), or nil.
func (n *Node) QuerySelector(tags ...string) *Node {
	all := n.QuerySelectorAll(tags...)
	if len(all) == 0 {
		return nil
	}
	return all[0]
}

// QuerySelectorAll returns every descendant element whose tag is one of
// tags, in document order. Self is never included.
func (n *Node) QuerySelectorAll(tags ...string) []*Node {
	var out []*Node
	match := make(map[string]bool, len(tags))
	for _, t := range tags {
		match[t] = true
	}
	var walk func(*Node)
	walk = func(node *Node) {
		for _, c := range node.Children {
			if c.Type == ElementNode {
				if match[c.Tag] {
					out = append(out, c)
				}
				walk(c)
			}
		}
	}
	if n != nil {
		walk(n)
	}
	return out
}

// AppendChild appends child and sets its Parent. Returns the child.
func (n *Node) AppendChild(child *Node) *Node {
	return appendChild(n, child)
}

// RemoveChild detaches child from n. No-op when child is not a child of n.
func (n *Node) RemoveChild(child *Node) {
	if n == nil || child == nil || child.Parent != n {
		return
	}
	for i, c := range n.Children {
		if c == child {
			n.Children = append(n.Children[:i], n.Children[i+1:]...)
			child.Parent = nil
			return
		}
	}
}

// ReplaceChild replaces oldChild with newChild. No-op when oldChild is not
// a child of n.
func (n *Node) ReplaceChild(newChild, oldChild *Node) {
	if n == nil || oldChild == nil || oldChild.Parent != n {
		return
	}
	for i, c := range n.Children {
		if c == oldChild {
			newChild.Parent = n
			n.Children[i] = newChild
			oldChild.Parent = nil
			return
		}
	}
}

// CloneNode copies a node. With deep, descendants are copied too.
func (n *Node) CloneNode(deep bool) *Node {
	if n == nil {
		return nil
	}
	clone := &Node{Type: n.Type, Tag: n.Tag, Text: n.Text}
	clone.Attrs = append(clone.Attrs, n.Attrs...)
	if deep {
		for _, c := range n.Children {
			appendChild(clone, c.CloneNode(true))
		}
	}
	return clone
}

// Document walks up to the Document node, or nil for detached nodes.
func (n *Node) Document() *Node {
	for node := n; node != nil; node = node.Parent {
		if node.Type == DocumentNode {
			return node
		}
	}
	return nil
}

// Escapers follow dom.ts serializeXml exactly: attribute values escape
// tabs and line breaks as numeric references so multi-line labels such as
// value="a&#xa;b" survive a round trip.
var attrEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"\t", "&#9;",
	"\n", "&#xa;",
	"\r", "&#xd;",
)
var textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// SerializeXML renders a node as XML, matching the TypeScript serializer:
// empty elements self-close, attribute values use the attribute escaper,
// text uses the text escaper, comments round-trip verbatim. A Document
// serializes its root element.
func SerializeXML(n *Node) string {
	var b strings.Builder
	serializeInto(&b, n)
	return b.String()
}

func serializeInto(b *strings.Builder, n *Node) {
	if n == nil {
		return
	}
	switch n.Type {
	case DocumentNode:
		if root := n.DocumentElement(); root != nil {
			serializeInto(b, root)
		}
	case ElementNode:
		b.WriteString("<")
		b.WriteString(n.Tag)
		for _, a := range n.Attrs {
			b.WriteString(" ")
			b.WriteString(a.Name)
			b.WriteString(`="`)
			b.WriteString(attrEscaper.Replace(a.Value))
			b.WriteString(`"`)
		}
		if len(n.Children) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteString(">")
		for _, c := range n.Children {
			serializeInto(b, c)
		}
		b.WriteString("</")
		b.WriteString(n.Tag)
		b.WriteString(">")
	case TextNode:
		b.WriteString(textEscaper.Replace(n.Text))
	case CommentNode:
		b.WriteString("<!--")
		b.WriteString(n.Text)
		b.WriteString("-->")
	}
}
