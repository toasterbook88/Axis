package buildinfo

// Commit, Date, and GoVersion are set at build time via -ldflags.
// Example:
//
//	go build -ldflags "-X github.com/toasterbook88/axis/internal/buildinfo.Commit=abc1234
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.Date=2026-04-01T12:00:00Z
//	                    -X github.com/toasterbook88/axis/internal/buildinfo.GoVersion=go1.26.2"
var (
	Commit    string
	Date      string
	GoVersion string
)
