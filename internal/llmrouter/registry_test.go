package llmrouter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/llmrouter"
)

func newLocalProvider(name string, healthy bool) *mockProvider {
	return &mockProvider{
		name:         name,
		providerType: llmrouter.ProviderLocal,
		health: llmrouter.HealthStatus{
			OK:      healthy,
			Latency: 5 * time.Millisecond,
		},
	}
}

func newCloudProvider(name string, healthy bool) *mockProvider {
	return &mockProvider{
		name:         name,
		providerType: llmrouter.ProviderCloud,
		health: llmrouter.HealthStatus{
			OK:      healthy,
			Latency: 50 * time.Millisecond,
		},
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := llmrouter.NewRegistry()
	ollama := newLocalProvider("ollama", true)
	openai := newCloudProvider("openai", true)

	if err := r.Register(ollama); err != nil {
		t.Fatalf("Register(ollama) error = %v", err)
	}
	if err := r.Register(openai); err != nil {
		t.Fatalf("Register(openai) error = %v", err)
	}
	if r.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", r.Len())
	}

	got, ok := r.Lookup("openai")
	if !ok {
		t.Fatal("Lookup(openai) = false, want true")
	}
	if got != openai {
		t.Fatalf("Lookup(openai) returned %p, want %p", got, openai)
	}

	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("Lookup(missing) = true, want false")
	}
}

func TestRegistryRegisterRejectsInvalidProviders(t *testing.T) {
	t.Run("nil provider", func(t *testing.T) {
		r := llmrouter.NewRegistry()
		if err := r.Register(nil); err == nil {
			t.Fatal("Register(nil) error = nil, want non-nil")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		r := llmrouter.NewRegistry()
		if err := r.Register(&mockProvider{providerType: llmrouter.ProviderLocal}); err == nil {
			t.Fatal("Register(empty name) error = nil, want non-nil")
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		r := llmrouter.NewRegistry()
		r.MustRegister(newLocalProvider("ollama", true))
		if err := r.Register(newCloudProvider("ollama", true)); err == nil {
			t.Fatal("Register(duplicate) error = nil, want non-nil")
		}
	})
}

func TestRegistryMustRegisterPanicsOnDuplicate(t *testing.T) {
	r := llmrouter.NewRegistry()
	r.MustRegister(newLocalProvider("ollama", true))

	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister(duplicate) did not panic")
		}
	}()

	r.MustRegister(newLocalProvider("ollama", true))
}

func TestRegistryListAndListByType(t *testing.T) {
	r := llmrouter.NewRegistry()
	r.MustRegister(newCloudProvider("openai", true))
	r.MustRegister(newLocalProvider("ollama", true))
	r.MustRegister(newLocalProvider("llama.cpp", true))

	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List() len = %d, want 3", len(got))
	}
	wantOrder := []string{"llama.cpp", "ollama", "openai"}
	for i, provider := range got {
		if provider.Name() != wantOrder[i] {
			t.Fatalf("List()[%d] = %q, want %q", i, provider.Name(), wantOrder[i])
		}
	}
	if got := r.ListByType(llmrouter.ProviderLocal); len(got) != 2 {
		t.Fatalf("ListByType(local) len = %d, want 2", len(got))
	}
	if got := r.ListByType(llmrouter.ProviderCloud); len(got) != 1 {
		t.Fatalf("ListByType(cloud) len = %d, want 1", len(got))
	}
	if got := llmrouter.NewRegistry().List(); len(got) != 0 {
		t.Fatalf("empty List() len = %d, want 0", len(got))
	}
}

func TestRegistryCheckHealthCapturesStatusesAndErrors(t *testing.T) {
	r := llmrouter.NewRegistry()
	r.MustRegister(newLocalProvider("ollama", true))
	r.MustRegister(newCloudProvider("openai", false))
	r.MustRegister(&mockProvider{
		name:         "broken",
		providerType: llmrouter.ProviderCloud,
		healthErr:    errors.New("connection refused"),
	})

	statuses := r.CheckHealth(context.Background())
	if len(statuses) != 3 {
		t.Fatalf("CheckHealth() len = %d, want 3", len(statuses))
	}
	if !statuses["ollama"].OK {
		t.Fatal("ollama should be healthy")
	}
	if statuses["openai"].OK {
		t.Fatal("openai should be unhealthy")
	}
	if statuses["broken"].OK {
		t.Fatal("broken should be unhealthy")
	}
	if statuses["broken"].Message == "" {
		t.Fatal("broken message should contain the provider error")
	}
}

func TestRegistryHealthyReturnsOnlyOKProviders(t *testing.T) {
	r := llmrouter.NewRegistry()
	ollama := newLocalProvider("ollama", true)
	openai := newCloudProvider("openai", false)
	groq := newCloudProvider("groq", true)

	r.MustRegister(ollama)
	r.MustRegister(openai)
	r.MustRegister(groq)

	healthy := r.Healthy(context.Background())
	if len(healthy) != 2 {
		t.Fatalf("Healthy() len = %d, want 2", len(healthy))
	}

	names := map[string]bool{}
	for _, provider := range healthy {
		names[provider.Name()] = true
	}
	if !names["ollama"] || !names["groq"] {
		t.Fatalf("Healthy() names = %#v, want ollama and groq", names)
	}
	if names["openai"] {
		t.Fatalf("Healthy() names = %#v, openai should be excluded", names)
	}
}
