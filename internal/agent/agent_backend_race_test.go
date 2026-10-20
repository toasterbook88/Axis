package agent

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/toasterbook88/axis/internal/chat"
)

// TestSetBackendConcurrentWithReads models the console startup path: the
// backend is swapped from a startup goroutine while the UI and slash commands
// read it. Run under -race; any unguarded access to the backend or its
// security class is reported as a data race.
func TestSetBackendConcurrentWithReads(t *testing.T) {
	a := New(Config{
		Backend:     noopChatBackend{},
		MaxTurns:    1,
		MaxTokens:   200,
		Output:      io.Discard,
		ToolContext: NewToolContext(&RuntimeView{}, nil),
	})
	backends := []ChatBackend{noopChatBackend{}, &scriptedBackend{
		responses: []chat.Message{{Role: chat.RoleAssistant, Content: "summary"}},
	}}
	classes := []BackendSecurityClass{BackendLocal, BackendRemote}

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			a.SetBackend(backends[i%2], classes[i%2])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = a.Backend()
			_ = securityClassOf(a)
			_ = a.Stats()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Compaction path (ChatStream on the backend) and the
			// evidence path (security-class check).
			_, _ = a.summarizeViaBackend(context.Background(), a.Backend(), "p")
			_ = a.retrieveEvidence("show nodes", securityClassOf(a))
		}
	}()
	wg.Wait()
}

// securityClassOf reads the active backend's classification through the
// same consistent pair production code uses.
func securityClassOf(a *Agent) BackendSecurityClass {
	_, sc := a.backendAndClass()
	return sc
}
