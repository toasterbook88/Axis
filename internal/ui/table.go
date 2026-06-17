package ui

import (
	"fmt"
	"io"
	"strings"
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

// Render writes the formatted table to w using thin unicode borders.
func (t *Table) Render(w io.Writer) {
	if len(t.headers) == 0 {
		return
	}

	// Calculate maximum width for each column (excluding ANSI formatting codes)
	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = len(stripANSI(h))
	}
	for _, row := range t.rows {
		for i, val := range row {
			if i < len(widths) {
				plainLen := len(stripANSI(val))
				if plainLen > widths[i] {
					widths[i] = plainLen
				}
			}
		}
	}

	// Prepare horizontal dividers
	var top []string
	var mid []string
	var bot []string
	for _, w := range widths {
		dash := strings.Repeat("─", w+2)
		top = append(top, dash)
		mid = append(mid, dash)
		bot = append(bot, dash)
	}

	// 1. Top border
	fmt.Fprintf(w, "┌%s┐\n", strings.Join(top, "┬"))

	// 2. Header row
	var headParts []string
	for i, h := range t.headers {
		plainHead := stripANSI(h)
		pad := widths[i] - len(plainHead)
		headParts = append(headParts, fmt.Sprintf(" %s%s ", Bold(h), strings.Repeat(" ", pad)))
	}
	fmt.Fprintf(w, "│%s│\n", strings.Join(headParts, "│"))

	// 3. Middle border
	fmt.Fprintf(w, "├%s┤\n", strings.Join(mid, "┼"))

	// 4. Data rows
	for _, row := range t.rows {
		var rowParts []string
		for i := 0; i < len(t.headers); i++ {
			val := ""
			if i < len(row) {
				val = row[i]
			}
			plainLen := len(stripANSI(val))
			pad := widths[i] - plainLen
			rowParts = append(rowParts, fmt.Sprintf(" %s%s ", val, strings.Repeat(" ", pad)))
		}
		fmt.Fprintf(w, "│%s│\n", strings.Join(rowParts, "│"))
	}

	// 5. Bottom border
	fmt.Fprintf(w, "└%s┘\n", strings.Join(bot, "┴"))
}

func stripANSI(str string) string {
	var sb strings.Builder
	inEscape := false
	for i := 0; i < len(str); i++ {
		if str[i] == '\033' || str[i] == '\u001b' {
			inEscape = true
			continue
		}
		if inEscape {
			if str[i] == 'm' {
				inEscape = false
			}
			continue
		}
		sb.WriteByte(str[i])
	}
	return sb.String()
}

// RowCount returns the number of data rows.
func (t *Table) RowCount() int { return len(t.rows) }
