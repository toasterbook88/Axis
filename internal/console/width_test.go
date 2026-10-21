package console

import "testing"

func TestClipWidth(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abc", 5, "abc"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"模型ab", 3, "模"},  // wide rune must not be split
		{"模型ab", 4, "模型"}, // exactly fits
		{"模型", 1, ""},
	}
	for _, c := range cases {
		got := clipWidth(c.in, c.w)
		if got != c.want {
			t.Errorf("clipWidth(%q,%d)=%q want %q", c.in, c.w, got, c.want)
		}
		if visibleWidth(got) > c.w && c.w > 0 {
			t.Errorf("clipWidth(%q,%d) too wide", c.in, c.w)
		}
	}
}
