package workload

import (
	"strings"
	"unicode"
)

type descriptionView struct {
	text string
}

func newDescriptionView(desc string) descriptionView {
	return descriptionView{text: canonicalize(desc)}
}

func (d descriptionView) has(term string) bool {
	term = canonicalize(term)
	if term == "" {
		return false
	}
	return strings.Contains(" "+d.text+" ", " "+term+" ")
}

func (d descriptionView) hasAny(terms ...string) bool {
	for _, term := range terms {
		if d.has(term) {
			return true
		}
	}
	return false
}

func (d descriptionView) hasAll(terms ...string) bool {
	for _, term := range terms {
		if !d.has(term) {
			return false
		}
	}
	return true
}

func canonicalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	space := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}

	return strings.TrimSpace(b.String())
}
