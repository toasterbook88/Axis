package ui

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Table renders aligned columns via stdlib tabwriter.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable creates a table with the given column headers.
func NewTable(headers ...string) *Table {
	return &Table{headers: headers}
}

// AddRow appends a row. Column count must match headers.
func (t *Table) AddRow(cols ...string) {
	t.rows = append(t.rows, cols)
}

// Render writes the formatted table to w.
// Headers are bold; columns are tab-aligned.
func (t *Table) Render(w io.Writer) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)

	// Header row
	colored := make([]string, len(t.headers))
	for i, h := range t.headers {
		colored[i] = Bold(h)
	}
	fmt.Fprintln(tw, strings.Join(colored, "\t"))

	// Data rows
	for _, row := range t.rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}

	tw.Flush()
}

// RowCount returns the number of data rows.
func (t *Table) RowCount() int { return len(t.rows) }
