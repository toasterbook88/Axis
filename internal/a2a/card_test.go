package a2a

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCardObserveScopeSkills(t *testing.T) {
	card := Card(CardOptions{Name: "cranium", Version: "0.19.3", URL: "http://100.81.205.4:8080", Scope: ScopeObserve})
	if card.Name != "cranium" {
		t.Fatalf("name = %q", card.Name)
	}
	ids := map[string]bool{}
	for _, s := range card.Skills {
		ids[s.ID] = true
		if s.Tags[0] != "observe" {
			t.Errorf("skill %q tagged %q, want observe", s.ID, s.Tags[0])
		}
	}
	for _, want := range []string{"axis-status", "workspace-read"} {
		if !ids[want] {
			t.Errorf("observe card missing skill %q", want)
		}
	}
	for _, banned := range []string{"workspace-write", "guarded-exec"} {
		if ids[banned] {
			t.Errorf("observe card advertises %q", banned)
		}
	}
}

func TestCardEditScopeAddsWrite(t *testing.T) {
	card := Card(CardOptions{Name: "n", Version: "1", URL: "http://x", Scope: ScopeEdit})
	ids := map[string]bool{}
	for _, s := range card.Skills {
		ids[s.ID] = true
	}
	if !ids["workspace-write"] {
		t.Error("edit card missing workspace-write")
	}
	if ids["guarded-exec"] {
		t.Error("edit card must not advertise guarded-exec")
	}
}

func TestCardExecScopeAddsGuardedExec(t *testing.T) {
	card := Card(CardOptions{Name: "n", Version: "1", URL: "http://x", Scope: ScopeExec})
	ids := map[string]bool{}
	for _, s := range card.Skills {
		ids[s.ID] = true
	}
	if !ids["guarded-exec"] || !ids["workspace-write"] {
		t.Fatalf("exec card missing exec skills: %v", ids)
	}
}

func TestServeCardWellKnownRoute(t *testing.T) {
	card := Card(CardOptions{Name: "probe-node", Version: "test", URL: "http://probe", Scope: ScopeObserve})
	mux := http.NewServeMux()
	ServeCard(mux, func() AgentCard { return card })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	var got AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "probe-node" {
		t.Errorf("decoded name = %q", got.Name)
	}
	if !strings.Contains(got.Description, "observe") {
		t.Errorf("description missing scope: %q", got.Description)
	}
}

func TestServeCardRejectsNonGet(t *testing.T) {
	mux := http.NewServeMux()
	ServeCard(mux, func() AgentCard { return Card(CardOptions{Name: "n", Version: "1", Scope: ScopeObserve}) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/.well-known/agent-card.json", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", resp.StatusCode)
	}
}
