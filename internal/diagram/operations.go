package diagram

import (
	"fmt"
	"sort"
	"strings"

	"aosfather.drawio-mcp/internal/mcplog"
	"aosfather.drawio-mcp/internal/xmlutil"
)

// Operation is one ID-based diagram operation.
type Operation struct {
	Operation string // "update" | "add" | "delete"
	CellID    string
	NewXML    string
}

// OperationError is a per-operation failure.
type OperationError struct {
	Type    string
	CellID  string
	Message string
}

// ApplyResult carries the result XML (same shape as the input) and any
// per-op errors.
type ApplyResult struct {
	Result string
	Errors []OperationError
}

// Cells with links, tooltips or custom data are stored as
// <UserObject id="..."><mxCell .../></UserObject> (or <object>): the id sits
// on the wrapper, so the wrapper is treated as the cell.
var cellSelector = []string{"mxCell", "UserObject", "object"}

// cellAttr reads parent/source/target, which a wrapped cell keeps on its
// inner mxCell.
func cellAttr(cell *xmlutil.Node, name string) string {
	inner := cell
	if cell.Tag != "mxCell" {
		inner = cell.QuerySelector("mxCell")
		if inner == nil {
			return ""
		}
	}
	return inner.GetAttrOr(name, "")
}

// ensurePageRoot returns the <root> of a <diagram> page, creating it when
// missing. An empty page gets a blank model with the "0" and "1" root cells;
// a page whose text is draw.io's compressed format is decompressed in place.
// Returns nil if the text is neither empty nor decompressible.
func ensurePageRoot(doc, page *xmlutil.Node) *xmlutil.Node {
	if existing := page.QuerySelector("root"); existing != nil {
		return existing
	}

	model := page.QuerySelector("mxGraphModel")
	if model == nil {
		text := strings.TrimSpace(page.TextContent())
		if text != "" {
			xml := DecompressPageContent(text)
			if xml == "" || xmlutil.GetXmlSyntaxError(xml) != "" {
				return nil
			}
			parsed, err := xmlutil.ParseXML(xml)
			if err != nil {
				return nil
			}
			if root := parsed.DocumentElement(); root == nil || root.Tag != "mxGraphModel" {
				return nil
			}
			page.SetTextContent("")
			model = page.AppendChild(parsed.DocumentElement().CloneNode(true))
			if decompressedRoot := model.QuerySelector("root"); decompressedRoot != nil {
				return decompressedRoot
			}
		} else {
			model = page.AppendChild(&xmlutil.Node{Type: xmlutil.ElementNode, Tag: "mxGraphModel"})
		}
	}

	blank, err := xmlutil.ParseXML(`<root><mxCell id="0"/><mxCell id="1" parent="0"/></root>`)
	if err != nil {
		return nil
	}
	return model.AppendChild(blank.DocumentElement().CloneNode(true))
}

// ApplyDiagramOperations applies update/add/delete operations using ID-based
// lookup. xmlContent may be a bare <mxGraphModel> or a full <mxfile>; for
// mxfile input, pageSelector picks the page (default: the first page).
func ApplyDiagramOperations(xmlContent string, operations []Operation, pageSelector *PageSelector) ApplyResult {
	var errs []OperationError

	// Check for syntax errors, then parse the XML
	if parseError := xmlutil.GetXmlSyntaxError(xmlContent); parseError != "" {
		return ApplyResult{
			Result: xmlContent,
			Errors: []OperationError{{
				Type: "update", CellID: "",
				Message: fmt.Sprintf("XML parse error: %s", parseError),
			}},
		}
	}
	doc, err := xmlutil.ParseXML(xmlContent)
	if err != nil {
		return ApplyResult{
			Result: xmlContent,
			Errors: []OperationError{{
				Type: "update", CellID: "",
				Message: fmt.Sprintf("XML parse error: %v", err),
			}},
		}
	}

	// Locate the <root> element to operate on:
	// - For <mxfile> input: resolve the page via pageSelector, then dive into
	//   its <root>, scoping all lookups to one page.
	// - For bare <mxGraphModel> input: use the document's only <root>.
	var root *xmlutil.Node
	if de := doc.DocumentElement(); de != nil && de.Tag == "mxfile" {
		found, index := FindPageElement(doc, pageSelector)
		if found == nil {
			selDesc := ""
			if HasPageSelector(pageSelector) {
				selDesc = fmt.Sprintf(" matching selector %s", DescribeSelector(pageSelector))
			}
			return ApplyResult{
				Result: xmlContent,
				Errors: []OperationError{{
					Type: "update", CellID: "",
					Message: fmt.Sprintf("Page%s not found in <mxfile>", selDesc),
				}},
			}
		}
		root = ensurePageRoot(doc, found)
		if root == nil {
			pageID := found.GetAttrOr("id", fmt.Sprintf("(index %d)", index))
			return ApplyResult{
				Result: xmlContent,
				Errors: []OperationError{{
					Type: "update", CellID: "",
					Message: fmt.Sprintf("Page %q has no <root> element and its content could not be decompressed", pageID),
				}},
			}
		}
	} else {
		if HasPageSelector(pageSelector) {
			return ApplyResult{
				Result: xmlContent,
				Errors: []OperationError{{
					Type: "update", CellID: "",
					Message: "Page selector provided but document is not multi-page (no <mxfile> wrapper). Use create_new_diagram with a full <mxfile> first, or omit the page selector.",
				}},
			}
		}
		root = doc.QuerySelector("root")
		if root == nil {
			return ApplyResult{
				Result: xmlContent,
				Errors: []OperationError{{
					Type: "update", CellID: "",
					Message: "Could not find <root> element in XML",
				}},
			}
		}
	}

	// Map of cell IDs to elements (scoped to the resolved page)
	cellMap := map[string]*xmlutil.Node{}
	for _, cell := range root.QuerySelectorAll(cellSelector...) {
		id, _ := cell.GetAttr("id")
		// A wrapped mxCell may repeat its wrapper's id; the wrapper is the cell
		wrapped := cell.Tag == "mxCell" && cell.Parent != nil &&
			(cell.Parent.Tag == "UserObject" || cell.Parent.Tag == "object")
		if id != "" && !wrapped {
			cellMap[id] = cell
		}
	}
	// Ids deleted so far in this batch; deleting one again is a no-op
	deletedIDs := map[string]bool{}

	for _, op := range operations {
		switch op.Operation {
		case "update":
			existingCell, ok := cellMap[op.CellID]
			if !ok {
				errs = append(errs, OperationError{"update", op.CellID,
					fmt.Sprintf(`Cell with id="%s" not found`, op.CellID)})
				continue
			}
			if op.NewXML == "" {
				errs = append(errs, OperationError{"update", op.CellID,
					"new_xml is required for update operation"})
				continue
			}
			// A cut-off cell (its XML still streaming in a preview) would
			// parse in Chrome to a cell without its geometry
			if syntaxError := xmlutil.GetXmlSyntaxError("<wrapper>" + op.NewXML + "</wrapper>"); syntaxError != "" {
				errs = append(errs, OperationError{"update", op.CellID,
					fmt.Sprintf("new_xml is not well-formed XML: %s", syntaxError)})
				continue
			}
			newDoc, perr := xmlutil.ParseXML("<wrapper>" + op.NewXML + "</wrapper>")
			if perr != nil {
				errs = append(errs, OperationError{"update", op.CellID,
					fmt.Sprintf("new_xml is not well-formed XML: %v", perr)})
				continue
			}
			newCell := newDoc.QuerySelector(cellSelector...)
			if newCell == nil {
				errs = append(errs, OperationError{"update", op.CellID,
					"new_xml must contain an mxCell element"})
				continue
			}
			newCellID, _ := newCell.GetAttr("id")
			if newCellID != op.CellID {
				errs = append(errs, OperationError{"update", op.CellID,
					fmt.Sprintf("ID mismatch: cell_id is %q but new_xml has id=%q", op.CellID, newCellID)})
				continue
			}
			importedNode := newCell.CloneNode(true)
			if existingCell.Parent != nil {
				existingCell.Parent.ReplaceChild(importedNode, existingCell)
			}
			cellMap[op.CellID] = importedNode

		case "add":
			if _, exists := cellMap[op.CellID]; exists {
				errs = append(errs, OperationError{"add", op.CellID,
					fmt.Sprintf(`Cell with id="%s" already exists`, op.CellID)})
				continue
			}
			if op.NewXML == "" {
				errs = append(errs, OperationError{"add", op.CellID,
					"new_xml is required for add operation"})
				continue
			}
			if syntaxError := xmlutil.GetXmlSyntaxError("<wrapper>" + op.NewXML + "</wrapper>"); syntaxError != "" {
				errs = append(errs, OperationError{"add", op.CellID,
					fmt.Sprintf("new_xml is not well-formed XML: %s", syntaxError)})
				continue
			}
			newDoc, perr := xmlutil.ParseXML("<wrapper>" + op.NewXML + "</wrapper>")
			if perr != nil {
				errs = append(errs, OperationError{"add", op.CellID,
					fmt.Sprintf("new_xml is not well-formed XML: %v", perr)})
				continue
			}
			newCell := newDoc.QuerySelector(cellSelector...)
			if newCell == nil {
				errs = append(errs, OperationError{"add", op.CellID,
					"new_xml must contain an mxCell element"})
				continue
			}
			newCellID, _ := newCell.GetAttr("id")
			if newCellID != op.CellID {
				errs = append(errs, OperationError{"add", op.CellID,
					fmt.Sprintf("ID mismatch: cell_id is %q but new_xml has id=%q", op.CellID, newCellID)})
				continue
			}
			importedNode := newCell.CloneNode(true)
			root.AppendChild(importedNode)
			cellMap[op.CellID] = importedNode

		case "delete":
			// Protect root cells from deletion
			if op.CellID == "0" || op.CellID == "1" {
				errs = append(errs, OperationError{"delete", op.CellID,
					fmt.Sprintf(`Cannot delete root cell "%s"`, op.CellID)})
				continue
			}
			if _, ok := cellMap[op.CellID]; !ok {
				// Skip cells already cascade-deleted by a previous operation
				// (AI may redundantly list children/edges); warn otherwise
				if !deletedIDs[op.CellID] {
					errs = append(errs, OperationError{"delete", op.CellID,
						fmt.Sprintf(`Cell with id="%s" not found`, op.CellID)})
				}
				continue
			}

			// Cascade delete: all children + edges + self
			cellsToDelete := map[string]bool{}

			var collectDescendants func(cellID string)
			collectDescendants = func(cellID string) {
				if cellsToDelete[cellID] {
					return
				}
				cellsToDelete[cellID] = true
				// Find children (cells where parent === cellId). cellMap only
				// holds this page's cells, so other pages' cells with the
				// same parent id (notably "1") are never touched.
				for childID, child := range cellMap {
					if childID != "0" && childID != "1" && cellAttr(child, "parent") == cellID {
						collectDescendants(childID)
					}
				}
			}
			collectDescendants(op.CellID)

			// Find edges referencing any of the cells to be deleted, then
			// recursively collect children of those edges (e.g. edge labels)
			var edgeTargets []string
			for cellID := range cellsToDelete {
				edgeTargets = append(edgeTargets, cellID)
			}
			sort.Strings(edgeTargets)
			for _, cellID := range edgeTargets {
				var edgeIDs []string
				for edgeID, edge := range cellMap {
					// Protect root cells from being added via edge references
					if edgeID == "0" || edgeID == "1" {
						continue
					}
					if cellAttr(edge, "source") == cellID || cellAttr(edge, "target") == cellID {
						edgeIDs = append(edgeIDs, edgeID)
					}
				}
				sort.Strings(edgeIDs)
				for _, edgeID := range edgeIDs {
					collectDescendants(edgeID)
				}
			}

			// Log what will be deleted (stderr: stdout carries JSON-RPC)
			if len(cellsToDelete) > 1 {
				ids := make([]string, 0, len(cellsToDelete))
				for id := range cellsToDelete {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				mcplog.Debug(`Cascade delete "%s" → deleting %d cells: %s`, op.CellID, len(cellsToDelete), strings.Join(ids, ", "))
			}

			// Delete all collected cells
			var deleteIDs []string
			for id := range cellsToDelete {
				deleteIDs = append(deleteIDs, id)
			}
			sort.Strings(deleteIDs)
			for _, cellID := range deleteIDs {
				if cell, ok := cellMap[cellID]; ok {
					if cell.Parent != nil {
						cell.Parent.RemoveChild(cell)
					}
					delete(cellMap, cellID)
				}
				deletedIDs[cellID] = true
			}
		}
	}

	return ApplyResult{Result: xmlutil.SerializeXML(doc), Errors: errs}
}
