package ui

import (
	"strings"
)

// DiffType represents the status of a line in a diff.
type DiffType int

const (
	DiffUnchanged DiffType = iota
	DiffAdded
	DiffDeleted
)

// DiffLine is a single line representation in a diff.
type DiffLine struct {
	Type DiffType
	Text string
}

// Diff performs a simple LCS line-by-line difference calculation between two strings.
// To keep execution fast, it truncates inputs to 150 lines.
func Diff(oldStr, newStr string) []DiffLine {
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	m := len(oldLines)
	n := len(newLines)

	// Truncate large inputs to prevent slow DP calculation
	if m > 150 || n > 150 {
		if m > 100 {
			oldLines = oldLines[:100]
		}
		if n > 100 {
			newLines = newLines[:100]
		}
		m = len(oldLines)
		n = len(newLines)
	}

	// DP table initialization
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if oldLines[i-1] == newLines[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}

	// Backtrack to assemble diff records
	var diff []DiffLine
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && oldLines[i-1] == newLines[j-1] {
			diff = append(diff, DiffLine{Type: DiffUnchanged, Text: oldLines[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			diff = append(diff, DiffLine{Type: DiffAdded, Text: newLines[j-1]})
			j--
		} else if i > 0 && (j == 0 || dp[i-1][j] >= dp[i][j-1]) {
			diff = append(diff, DiffLine{Type: DiffDeleted, Text: oldLines[i-1]})
			i--
		}
	}

	// Reverse the slices as we backtracked
	for l, r := 0, len(diff)-1; l < r; l, r = l+1, r-1 {
		diff[l], diff[r] = diff[r], diff[l]
	}
	return diff
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// FormatDiff computes the diff and returns a formatted unified diff preview string.
// Shows only changed lines and up to 3 lines of surrounding context.
func FormatDiff(oldStr, newStr string) string {
	diff := Diff(oldStr, newStr)

	// Identify changed indices
	changed := make([]bool, len(diff))
	for idx, line := range diff {
		if line.Type != DiffUnchanged {
			changed[idx] = true
		}
	}

	// Calculate visible lines based on context size (3 lines)
	contextSize := 3
	show := make([]bool, len(diff))
	for idx := range diff {
		if changed[idx] {
			for c := -contextSize; c <= contextSize; c++ {
				target := idx + c
				if target >= 0 && target < len(diff) {
					show[target] = true
				}
			}
		}
	}

	var result []string
	inHunk := false
	for idx, line := range diff {
		if show[idx] {
			if !inHunk {
				result = append(result, DimColor.Sprint("@@ ... @@"))
				inHunk = true
			}
			switch line.Type {
			case DiffUnchanged:
				result = append(result, "  "+line.Text)
			case DiffAdded:
				result = append(result, GreenColor.Sprint("+ "+line.Text))
			case DiffDeleted:
				result = append(result, RedColor.Sprint("- "+line.Text))
			}
		} else {
			inHunk = false
		}
	}
	return strings.Join(result, "\n")
}
