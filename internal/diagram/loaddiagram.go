package diagram

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"strings"

	"aosfather.drawio-mcp/internal/xmlutil"
)

// LoadResult is the outcome of ParseDrawioFileContent.
type LoadResult struct {
	OK    bool
	XML   string
	Error string
}

// DecompressPageContent decodes one compressed page body
// (base64 → raw deflate → URI-decode). Returns "" if the text isn't in that
// format. A .drawio file's default save format is
// encodeURIComponent(xml) → raw deflate → base64.
func DecompressPageContent(compressed string) string {
	// atob tolerates ASCII whitespace; strip it all
	clean := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			return -1
		}
		return r
	}, strings.TrimSpace(compressed))
	bytes64, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return ""
	}
	zr := flate.NewReader(bytes.NewReader(bytes64))
	inflated, err := io.ReadAll(zr)
	if err != nil {
		zr.Close()
		return ""
	}
	zr.Close()
	// Not URI-encoded (older files) — the inflated text is the XML.
	if decoded, err := url.PathUnescape(string(inflated)); err == nil {
		return decoded
	}
	return string(inflated)
}

// ParseDrawioFileContent parses the content of a .drawio file into the
// canonical session shape: an <mxfile> whose every page holds plain
// <mxGraphModel> XML. Accepts a bare <mxGraphModel> (wrapped into a one-page
// mxfile) and decompresses any compressed pages.
func ParseDrawioFileContent(content string) LoadResult {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return LoadResult{Error: "File is empty."}
	}

	if IsMxGraphModel(trimmed) {
		normalized := NormalizeToMxfile(trimmed, "", "")
		if normalized == "" {
			return LoadResult{Error: "Failed to parse <mxGraphModel> XML."}
		}
		// Parsed below like any <mxfile>, so a broken model is an error
		trimmed = normalized
	}
	if !IsMxFile(trimmed) {
		return LoadResult{Error: "Not a draw.io file: expected an <mxfile> or <mxGraphModel> root element."}
	}
	doc := ParseMxfile(trimmed)
	if doc == nil {
		return LoadResult{Error: "Failed to parse <mxfile> XML."}
	}

	decompressedAny := false
	for _, d := range doc.QuerySelectorAll("diagram") {
		if d.QuerySelector("mxGraphModel") != nil {
			continue
		}
		text := strings.TrimSpace(d.TextContent())
		if text == "" {
			continue // an empty page is valid
		}
		pageLabel := d.GetAttrOr("name", d.GetAttrOr("id", "unnamed"))
		xml := DecompressPageContent(text)
		if xml == "" || !IsMxGraphModel(xml) {
			return LoadResult{Error: fmt.Sprintf(
				`Page %q has content that is neither plain <mxGraphModel> XML nor draw.io's compressed format.`, pageLabel)}
		}
		if xmlutil.GetXmlSyntaxError(xml) != "" {
			return LoadResult{Error: fmt.Sprintf(
				`Page %q decompressed but its XML failed to parse.`, pageLabel)}
		}
		inner, err := xmlutil.ParseXML(xml)
		if err != nil || inner.DocumentElement() == nil || inner.DocumentElement().Tag != "mxGraphModel" {
			return LoadResult{Error: fmt.Sprintf(
				`Page %q decompressed but its XML failed to parse.`, pageLabel)}
		}
		d.SetTextContent("")
		d.AppendChild(inner.DocumentElement().CloneNode(true))
		decompressedAny = true
	}
	// Nothing changed — keep the file's own serialisation.
	if decompressedAny {
		return LoadResult{OK: true, XML: SerializeMxfile(doc)}
	}
	return LoadResult{OK: true, XML: trimmed}
}
