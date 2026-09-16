package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

type noopChatBackend struct{}

func (noopChatBackend) ChatStream(ctx context.Context, msgs []chat.Message, tools []chat.ToolDef, w io.Writer) (chat.Message, error) {
	return chat.Message{Role: chat.RoleAssistant}, nil
}

func TestBackendLocalityDefaultsToRemoteForCustomBackend(t *testing.T) {
	a := New(Config{
		Backend:     noopChatBackend{},
		ToolContext: NewToolContext(&RuntimeView{}, nil),
		Output:      io.Discard,
	})
	if a.securityClass != BackendRemote {
		t.Fatalf("expected BackendRemote for custom backend with default config, got %v", a.securityClass)
	}
}

func TestBackendLocalityRespectsExplicitLocal(t *testing.T) {
	a := New(Config{
		Backend:              noopChatBackend{},
		BackendSecurityClass: BackendLocal,
		ToolContext:          NewToolContext(&RuntimeView{}, nil),
		Output:               io.Discard,
	})
	if a.securityClass != BackendLocal {
		t.Fatalf("expected BackendLocal when explicitly configured, got %v", a.securityClass)
	}
}

func TestRemoteEvidenceEmptyWithoutSkillMatch(t *testing.T) {
	a := New(Config{
		Backend:              noopChatBackend{},
		BackendSecurityClass: BackendRemote,
		ToolContext: NewToolContext(&RuntimeView{
			State: &state.ClusterState{
				Decisions: []string{"placed task on node-a"},
			},
		}, nil),
		Output: io.Discard,
	})
	if a.retrieveEvidence("unmatched query") != "" {
		t.Error("remote evidence should be empty when no skill matches")
	}
}

func TestLocalEvidenceAllowsRawCommandsWhenEnabled(t *testing.T) {
	skillsStore := &skills.Store{
		Skills: []skills.LearnedSkill{
			{
				ID:           "skill-1",
				Description:  "run a benchmark",
				Command:      "sysbench cpu run",
				SuccessCount: 5,
				LastUsed:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	a := New(Config{
		Backend:                 noopChatBackend{},
		BackendSecurityClass:    BackendLocal,
		AllowRawCommandEvidence: true,
		ToolContext: NewToolContext(&RuntimeView{
			Skills: skillsStore,
			State: &state.ClusterState{
				Decisions: []string{"placed 'sysbench cpu run' on node-a"},
			},
		}, nil),
		Output: io.Discard,
	})

	evidence := a.retrieveEvidence("benchmark")
	if !strings.Contains(evidence, "sysbench cpu run") {
		t.Error("local evidence should include raw commands when explicitly allowed")
	}
}

func TestSetBackendUpdatesSecurityClass(t *testing.T) {
	a := New(Config{
		Backend:              noopChatBackend{},
		BackendSecurityClass: BackendRemote,
		ToolContext:          NewToolContext(&RuntimeView{}, nil),
		Output:               io.Discard,
	})
	a.SetBackend(chat.NewClient("http://localhost:11434", "test"), BackendLocal)
	if a.securityClass != BackendLocal {
		t.Fatalf("expected BackendLocal after SetBackend, got %v", a.securityClass)
	}
}
