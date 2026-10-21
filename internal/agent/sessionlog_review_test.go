package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestIndependentReviewEscapedCredentialJSONStringMasked(t *testing.T) {
	text := `{"content":"{\"api_key\":\"review-marker-value\"}"}`
	raw, err := encodeSessionRecord(sessionRecord{Kind: "tool", Tool: &sessionTool{Args: text}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "review-marker-value") {
		t.Fatalf("escaped credential string persisted: %s", raw)
	}
}

func TestIndependentReviewToolErrorPreservesOutput(t *testing.T) {
	l, err := OpenDailySessionLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := newSessionLogAgent(t, l, nil, &scriptedBackend{responses: []chat.Message{
		{Role: chat.RoleAssistant, ToolCalls: []chat.ToolCall{toolCall("r1", "review_partial", `{}`)}},
		{Role: chat.RoleAssistant, Content: "done"},
	}})
	a.tools.allowExtra("review_partial")
	a.tools.add("review_partial", "review", json.RawMessage(`{"type":"object"}`), func(context.Context, json.RawMessage) (string, error) {
		return "partial-diagnostic-output", errors.New("probe exited 1")
	})
	if err = a.Run(context.Background(), "test partial tool result"); err != nil {
		t.Fatal(err)
	}
	raw, _, _ := readSessionSegments(t, l.dir)
	if !strings.Contains(string(raw), "partial-diagnostic-output") {
		t.Fatalf("lost partial tool result; records: %s", raw)
	}
	var output, failed bool
	for _, rec := range allSessionRecords(t, raw) {
		if rec.Tool == nil {
			continue
		}
		if rec.Tool.Event == "output" {
			output = true
			if rec.Tool.OK || rec.Tool.ResultBytes != len("partial-diagnostic-output") {
				t.Fatalf("partial output falsely succeeded: %+v", rec.Tool)
			}
		}
		if rec.Tool.Event == "failed" {
			failed = true
		}
	}
	if !output || !failed {
		t.Fatalf("partial tool failure trajectory incomplete: output=%v failed=%v", output, failed)
	}

}

func TestReviewDirectoryReplacementSymlinkRejected(t *testing.T) {
	dir := t.TempDir() + "/logs"
	l, err := OpenDailySessionLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err = os.Rename(dir, dir+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	l.append(sessionRecord{Kind: "review"})
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Fatalf("log-directory leaf symlink followed after open: %d outside files created", len(entries))
	}
}

func TestDailySessionLogStructuredRedactionBoundsAndPreservesJSON(t *testing.T) {
	text := `{"content":"{\"api_key\":\"review-marker-value\"}","count":12345678901234567890,"headers":{"Authorization":"Basic review-marker-value"}}`
	raw, err := encodeSessionRecord(sessionRecord{Kind: "tool", Tool: &sessionTool{Args: text}})
	if err != nil {
		t.Fatal(err)
	}
	var rec sessionRecord
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal([]byte(rec.Tool.Args), &object); err != nil {
		t.Fatalf("redaction broke argument JSON: %v %s", err, rec.Tool.Args)
	}
	if string(object["count"]) != "12345678901234567890" || strings.Contains(rec.Tool.Args, "review-marker-value") {
		t.Fatal("number changed or secret persisted")
	}
	normal := "{ \"ordinary\": \"keep formatting\" }"
	if got := redactSessionText(normal); got != normal {
		t.Fatalf("unaffected JSON reformatted: %q", got)
	}
	for _, text := range []string{
		strings.Repeat("[", 12) + `{"token":"review-marker-value"}` + strings.Repeat("]", 12),
		`{"broken":"{\"api_key\":\"review-marker-value`,
		`{"content":"` + strings.Repeat("x", sessionStructuredCap) + `review-marker-value"}`,
	} {
		if got := redactSessionText(text); strings.Contains(got, "review-marker-value") || !strings.Contains(got, "REDACTED") {
			t.Fatalf("structured fallback copied opaque input: %.160s", got)
		}
	}
}

func TestDailySessionLogStructuredCredentialKeyVariants(t *testing.T) {
	for _, key := range []string{"clientSecret", "accessToken", "AWS_ACCESS_KEY_ID", "aws_secret_access_key", "apiKey", "discovery.secret"} {
		t.Run(key, func(t *testing.T) {
			inner, _ := json.Marshal(map[string]string{key: "credential-marker-value"})
			outer, _ := json.Marshal(map[string]string{"content": string(inner)})
			raw, err := encodeSessionRecord(sessionRecord{Kind: "tool", Tool: &sessionTool{Args: string(outer)}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "credential-marker-value") {
				t.Fatalf("credential key variant survived nested decoding: %s", raw)
			}
		})
	}
}
