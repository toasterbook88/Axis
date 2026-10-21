package console

import (
	"strings"
	"testing"
)

// An emoji followed by VS16 (U+FE0F) draws two columns although each rune
// measures one; clipping must still respect the limit.
func TestClipWidth_VariationSelectorClusters(t *testing.T) {
	for _, s := range []string{"ab☺️cdefgh", "☺️☺️☺️☺️zz", "✔️✔️ ok", "x❤️"} {
		for w := 1; w <= 8; w++ {
			if got := clipWidth(s, w); visibleWidth(got) > w {
				t.Errorf("clipWidth(%q, %d) = %q, width %d", s, w, got, visibleWidth(got))
			}
		}
	}
}

// A model name with VS16 clusters must neither panic the footer nor push it
// past width-1 at any width.
func TestStatusFooter_VariationSelectorModelNeverOverflows(t *testing.T) {
	footer := NewStatusFooter(StatusFooterConfig{
		Model:      func() string { return strings.Repeat("☺️", 30) },
		UsedTokens: func() int { return 9200 },
		MaxTokens:  func() int { return 32768 },
		Mode:       func() string { return "default" },
		Snapshot:   func() string { return "snap pub-1" },
	})
	for w := 1; w <= 120; w++ {
		for _, l := range footer.Render(w, FooterState{}) {
			if vw := visibleWidth(l.Text); vw > max(w-1, 0) {
				t.Fatalf("width %d: footer is %d columns: %q", w, vw, l.Text)
			}
		}
	}
}
