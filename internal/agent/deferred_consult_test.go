package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

func TestDeferredTools_ConsultKeepsResolverAndApprovalGates(t *testing.T) {
	var asked []string
	seat := &consultRecorder{response: chat.Message{Role: chat.RoleAssistant, Content: "a note"}}
	a := New(Config{
		Backend:     &kvCacheProbeBackend{},
		AutoApprove: true,
		Autonomy:    AutonomyFull,
		EagerTools:  []string{"read_file"},
		Confirm: func(tool, _ string, _ int) ConfirmResult {
			asked = append(asked, tool)
			return ConfirmYes
		},
	})
	call := toolCall("deferred-consult", toolCallName, `{"name":"consult_model","arguments":{"seat":"seat-a","prompt":"review"}}`)
	if a.tools.Visible(consultModelTool) || a.tools.IsDeferred(consultModelTool) {
		t.Fatal("consult without a resolver must stay unavailable before deferral")
	}
	if _, err := a.dispatchToolCall(context.Background(), call); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("deferred bridge must refuse consult without a resolver: %v", err)
	}
	a.SetConsultResolver(func(context.Context, string) (ChatBackend, string, func(), error) {
		return seat, "deferred-consult-seat", nil, nil
	})
	if !a.tools.IsDeferred(consultModelTool) || containsName(defNames(a.tools), consultModelTool) {
		t.Fatal("eager allowlist must defer a permitted consult tool")
	}
	search, err := a.tools.Execute(context.Background(), toolSearchName, json.RawMessage(`{"query":"select:consult_model"}`))
	if err != nil {
		t.Fatal(err)
	}
	fns := extractFunctionPayloads(t, search)
	if len(fns) != 1 || fns[0].Name != consultModelTool {
		t.Fatalf("deferred consult schema was not discoverable: %q", search)
	}
	result, err := a.dispatchToolCall(context.Background(), call)
	if err != nil || result != "a note" {
		t.Fatalf("deferred consult result=%q err=%v", result, err)
	}
	if len(asked) != 1 || asked[0] != consultModelTool || seat.calls != 1 || len(seat.tools) != 0 {
		t.Fatalf("consult must ask for the target and send no tools even under full/auto-approve: asked=%v calls=%d tools=%d", asked, seat.calls, len(seat.tools))
	}
	a.SetAutonomy(AutonomyCluster)
	if a.tools.Visible(consultModelTool) || a.tools.Visible(toolCallName) || a.tools.IsDeferred(consultModelTool) {
		t.Fatal("cluster scope must hide consult and the deferred bridge")
	}
	a.SetAutonomy(AutonomyFull)
	a.SetConsultResolver(nil)
	if a.tools.Visible(consultModelTool) || a.tools.IsDeferred(consultModelTool) || strings.Contains(visibleToolsMessage(a.tools), consultModelTool) {
		t.Fatal("removing the resolver must remove consult from deferred discovery")
	}
}
