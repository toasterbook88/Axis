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

func TestRemoteEvidenceAllowlist(t *testing.T) {
	store := &skills.Store{
		Skills: []skills.LearnedSkill{
			{
				ID:            "skill-1",
				Description:   "run a benchmark",
				Command:       "sysbench cpu run",
				SuccessCount:  5,
				LastUsed:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				PreferredNode: "node-a",
				NodeCount:     map[string]int{"node-a": 3, "node-b": 2},
			},
		},
	}

	a := New(Config{
		Backend:              noopChatBackend{},
		BackendSecurityClass: BackendRemote,
		ToolContext: NewToolContext(&RuntimeView{
			Skills: store,
			State: &state.ClusterState{
				Decisions: []string{"placed 'sysbench cpu run' on node-a"},
			},
		}, nil),
		Output: io.Discard,
	})

	evidence := a.retrieveEvidence("benchmark")
	if evidence == "" {
		t.Fatal("expected evidence for remote backend when skill matched")
	}
	if strings.Contains(evidence, "run a benchmark") {
		t.Error("remote evidence must not contain free-form skill description")
	}
	if strings.Contains(evidence, "sysbench cpu run") {
		t.Error("remote evidence must not contain raw command text")
	}
	if strings.Contains(evidence, "Recent placement decisions") {
		t.Error("remote evidence must not contain free-form decision summaries")
	}
	if !strings.Contains(evidence, "skill-1") {
		t.Error("remote evidence should include allowlisted skill ID")
	}
	if !strings.Contains(evidence, "node-a") {
		t.Error("remote evidence should include preferred node")
	}
	if !strings.Contains(evidence, "Success Count: 5") {
		t.Error("remote evidence should include success count")
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

func TestLocalEvidenceIncludesDescriptionsAndRedactsCommands(t *testing.T) {
	skillsStore := &skills.Store{
		Skills: []skills.LearnedSkill{
			{
				ID:            "skill-1",
				Description:   "run a benchmark",
				Command:       "sysbench cpu run",
				SuccessCount:  5,
				LastUsed:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				PreferredNode: "node-a",
			},
		},
	}
	a := New(Config{
		Backend:              noopChatBackend{},
		BackendSecurityClass: BackendLocal,
		ToolContext: NewToolContext(&RuntimeView{
			Skills: skillsStore,
			State: &state.ClusterState{
				Decisions: []string{"placed 'sysbench cpu run' on node-a"},
			},
		}, nil),
		Output: io.Discard,
	})

	evidence := a.retrieveEvidence("benchmark")
	if !strings.Contains(evidence, "run a benchmark") {
		t.Error("local evidence should include skill description")
	}
	if !strings.Contains(evidence, "Recent placement decisions") {
		t.Error("local evidence should include recent decisions")
	}
	if strings.Contains(evidence, "sysbench cpu run") {
		t.Error("local evidence should redact raw commands by default")
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
