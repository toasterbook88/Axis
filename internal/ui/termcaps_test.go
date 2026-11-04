package ui

import "testing"

func envFn(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestDetectColorDepth(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want ColorDepth
	}{
		{"no_color wins", map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor", "TERM": "xterm-256color"}, ColorNone},
		{"dumb", map[string]string{"TERM": "dumb"}, ColorNone},
		{"empty term", map[string]string{}, ColorNone},
		{"colorterm truecolor", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, ColorTrue},
		{"colorterm 24bit", map[string]string{"TERM": "xterm", "COLORTERM": "24bit"}, ColorTrue},
		{"ghostty", map[string]string{"TERM": "xterm-ghostty"}, ColorTrue},
		{"kitty", map[string]string{"TERM": "xterm-kitty"}, ColorTrue},
		{"wezterm", map[string]string{"TERM": "wezterm"}, ColorTrue},
		{"direct suffix", map[string]string{"TERM": "xterm-direct"}, ColorTrue},
		{"apple terminal 256", map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, Color256},
		{"tmux 256", map[string]string{"TERM": "tmux-256color"}, Color256},
		{"linux vt", map[string]string{"TERM": "linux"}, Color16},
		{"dumb ignores colorterm", map[string]string{"TERM": "dumb", "COLORTERM": "truecolor"}, ColorNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectColorDepth(envFn(tc.env)); got != tc.want {
				t.Errorf("DetectColorDepth(%v) = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
	if ColorNone != 0 || !(ColorNone < Color16 && Color16 < Color256 && Color256 < ColorTrue) {
		t.Error("ColorDepth constants must be ordered None < 16 < 256 < True with None == 0")
	}
}

func TestUnicodeSupported(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"linux console", map[string]string{"TERM": "linux", "LANG": "en_US.UTF-8"}, false},
		{"dumb", map[string]string{"TERM": "dumb", "LANG": "en_US.UTF-8"}, false},
		{"LC_ALL=C", map[string]string{"LC_ALL": "C"}, false},
		{"LANG=POSIX", map[string]string{"LANG": "POSIX"}, false},
		{"C.UTF-8", map[string]string{"LC_ALL": "C.UTF-8"}, true},
		{"utf8 spelling", map[string]string{"LANG": "en_US.utf8"}, true},
		{"latin1", map[string]string{"LANG": "en_US.ISO-8859-1"}, false},
		{"no locale", map[string]string{"TERM": "xterm-256color"}, true},
		{"LC_ALL beats LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false},
		{"LC_CTYPE beats LANG", map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnicodeSupported(envFn(tc.env)); got != tc.want {
				t.Errorf("UnicodeSupported(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestFgRGB(t *testing.T) {
	cases := []struct {
		name    string
		r, g, b uint8
		d       ColorDepth
		want    string
	}{
		{"true", 0, 240, 255, ColorTrue, "\x1b[38;2;0;240;255m"},
		{"256 cyan", 0, 240, 255, Color256, "\x1b[38;5;51m"},
		{"256 magenta", 185, 30, 255, Color256, "\x1b[38;5;171m"},
		{"16 cyan", 0, 240, 255, Color16, "\x1b[96m"},
		{"16 magenta", 185, 30, 255, Color16, "\x1b[95m"},
		{"16 black", 0, 0, 0, Color16, "\x1b[90m"},
		{"none", 0, 240, 255, ColorNone, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FgRGB(tc.r, tc.g, tc.b, tc.d); got != tc.want {
				t.Errorf("FgRGB = %q, want %q", got, tc.want)
			}
		})
	}
}
