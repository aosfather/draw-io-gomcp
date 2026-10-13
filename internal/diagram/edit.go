package diagram

import (
	"fmt"
	"strings"

	"aosfather.drawio-mcp/internal/xmlutil"
)

// EditOutcome is the result of EditDiagram. All-or-nothing, like the web
// app: if any operation fails, nothing is written and every failure is
// reported, so the model never builds on a half-applied edit.
type EditOutcome struct {
	OK        bool
	XML       string
	Applied   int
	Fixes     []string
	Errors    []string
	PageError bool
}

// countTopLevelElements counts the top-level elements of an XML fragment.
// On a syntax error the count is 1 (the syntax is checked later).
func countTopLevelElements(fragment string) int {
	doc, err := xmlutil.ParseXML("<wrapper>" + fragment + "</wrapper>")
	if err != nil {
		return 1
	}
	return len(doc.DocumentElement().ElementChildren())
}

// TargetPageXML returns the target page as a one-page <mxfile>, or the whole
// document when the page cannot be projected.
func TargetPageXML(xml string, selector *PageSelector) string {
	projection := ProjectPage(xml, selector)
	if projection.OK {
		return projection.XML
	}
	return xml
}

// EditDiagram runs the core of the edit_diagram tool, free of session state.
func EditDiagram(xml string, operations []Operation, selector *PageSelector) EditOutcome {
	var errs []string
	var fixes []string
	var prepared []Operation

	for _, op := range operations {
		if op.Operation == "delete" || op.NewXML == "" {
			prepared = append(prepared, op)
			continue
		}
		// Checked before validation: several cells fail the strict parser
		// with a misleading "only one root" syntax error.
		if countTopLevelElements(op.NewXML) > 1 {
			errs = append(errs, fmt.Sprintf(
				"%s %s: new_xml must contain exactly one cell; use one add operation per cell", op.Operation, op.CellID))
			continue
		}
		valid, errMsg, fixed, opFixes := xmlutil.ValidateAndFixXML(op.NewXML, true)
		if !valid {
			errs = append(errs, fmt.Sprintf(
				"%s %s: invalid new_xml: %s", op.Operation, op.CellID, errMsg))
			continue
		}
		if fixed != "" {
			fixes = append(fixes, fmt.Sprintf("%s: %s", op.CellID, strings.Join(opFixes, ", ")))
		}
		if fixed != "" {
			op.NewXML = fixed
		}
		prepared = append(prepared, op)
	}
	if len(errs) > 0 {
		return EditOutcome{Errors: errs}
	}

	applied := ApplyDiagramOperations(xml, prepared, selector)
	result, opErrors := applied.Result, applied.Errors
	// An empty cellId means the page itself could not be edited
	for _, e := range opErrors {
		if e.CellID == "" {
			return EditOutcome{Errors: []string{e.Message}, PageError: true}
		}
	}
	if len(opErrors) > 0 {
		msgs := make([]string, 0, len(opErrors))
		for _, e := range opErrors {
			msgs = append(msgs, fmt.Sprintf("%s %s: %s", e.Type, e.CellID, e.Message))
		}
		return EditOutcome{Errors: msgs}
	}

	// Validate only the target page, and reject only errors this edit
	// introduced: problems already in other pages or in a loaded file must
	// not block every edit.
	after := xmlutil.ValidateMxCellStructure(TargetPageXML(result, selector), true)
	before := xmlutil.ValidateMxCellStructure(TargetPageXML(xml, selector), true)
	if after != "" && before == "" {
		return EditOutcome{Errors: []string{fmt.Sprintf("the edit would make the page invalid: %s", after)}}
	}

	return EditOutcome{OK: true, XML: result, Applied: len(operations), Fixes: fixes}
}
