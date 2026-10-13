// Package assets embeds the browser preview page and the shape library
// docs, so the compiled binary is fully self-contained.
package assets

import "embed"

// PreviewFS holds the preview page served by the embedded HTTP server
// (index.html with {{CSS}}/{{SCRIPT}}/... placeholders, preview.css, preview.js).
//
//go:embed preview/index.html preview/preview.css preview/preview.js
var PreviewFS embed.FS

// ShapeLibrariesFS holds docs/shape-libraries/*.md served by get_shape_library.
//
//go:embed shape-libraries
var ShapeLibrariesFS embed.FS
