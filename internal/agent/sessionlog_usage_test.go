package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// A turn whose backend reported no usage must be distinguishable in the
// retained log from a turn that reported usage. tokens_in/out are omitempty,
// so without an explicit flag both cases look identical on disk.
func TestDailySessionLogDistinguishesMissingUsageFromReported(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	day := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	log.now = func() time.Time { return day }

	a := newSessionLogAgent(t, log, &sessionLogObserver{}, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, Content: "reported", UsageTokensIn: 5, UsageTokensOut: 2},
		{Role: chat.RoleAssistant, Content: "no usage from backend"},
	}})
	if err := a.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := a.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second run: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "agent-2026-10-23.jsonl"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	finished := finishedRecords(t, raw)
	if len(finished) != 2 {
		t.Fatalf("finished records = %d, want 2", len(finished))
	}
	if finished[0].Usage == nil || !*finished[0].Usage {
		t.Fatalf("first turn usage_reported = %v, want true", finished[0].Usage)
	}
	if finished[0].TokensIn != 5 || finished[0].TokensOut != 2 {
		t.Fatalf("first turn tokens = %d/%d, want 5/2", finished[0].TokensIn, finished[0].TokensOut)
	}
	if finished[1].Usage == nil || *finished[1].Usage {
		t.Fatalf("second turn usage_reported = %v, want explicit false (unknown, not zero)", finished[1].Usage)
	}

	// The flag must be on disk for the missing case, not dropped by omitempty,
	// and must not appear on other record kinds.
	var finishedLines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, `"turn.finished"`) {
			finishedLines = append(finishedLines, line)
		} else if strings.Contains(line, "usage_reported") {
			t.Fatalf("usage_reported leaked onto a non-finished record: %s", line)
		}
	}
	if len(finishedLines) != 2 || !strings.Contains(finishedLines[1], `"usage_reported":false`) {
		t.Fatalf("second finished line lacks explicit usage_reported:false: %q", finishedLines)
	}
}
