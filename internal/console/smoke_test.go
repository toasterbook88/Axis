package console

import (
	"testing"
	"time"
)

func TestNewUserEntryRenders(t *testing.T) {
	e := NewUserEntry(time.Unix(0, 0).UTC(), "hello")
	lines := e.Render(80)
	if len(lines) == 0 {
		t.Fatal("expected render lines")
	}
}
