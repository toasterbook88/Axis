package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func stubModelCatalog(t *testing.T, catalog models.ModelCatalog, gotLive *bool) {
	t.Helper()
	previous := loadModelCatalog
	t.Cleanup(func() { loadModelCatalog = previous })
	loadModelCatalog = func(_ context.Context, live bool, _ string) (models.ModelCatalog, error) {
		if gotLive != nil {
			*gotLive = live
		}
		return catalog, nil
	}
}

func testModelCatalog() models.ModelCatalog {
	return models.ModelCatalog{
		Source:        "daemon-cache",
		PublicationID: "pub-cat-1",
		Entries: []models.ModelCatalogEntry{
			{Model: "bitnet-2B", Node: "node-a", NodeStatus: models.StatusComplete, Engine: "llama.cpp", State: models.ModelCatalogLoaded, Locality: models.ModelLocalityOnNode, Port: 8080, InstanceID: "mi-1"},
			{Model: "big:cloud", Node: "node-b", NodeStatus: models.StatusComplete, Engine: "ollama", State: models.ModelCatalogInstalled, Locality: models.ModelLocalityCloudProxy, RemoteHost: "https://cloud.example.com:443", Capabilities: []string{"completion"}},
			{Model: "embed:latest", Node: "node-b", NodeStatus: models.StatusComplete, Engine: "ollama", State: models.ModelCatalogInstalled, Locality: models.ModelLocalityOnNode, Capabilities: []string{"embedding"}},
		},
		Unobserved: []models.UnobservedNode{{Node: "node-c", Status: models.StatusUnreachable, Reason: "ssh: unable to authenticate"}},
	}
}

func TestModelCatalogReadsDaemonCacheByDefault(t *testing.T) {
	gotLive := true
	stubModelCatalog(t, testModelCatalog(), &gotLive)

	cmd := modelCatalogCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if gotLive {
		t.Fatal("model catalog performed live discovery by default")
	}
	var got models.ModelCatalog
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if got.PublicationID != "pub-cat-1" || len(got.Entries) != 3 || len(got.Unobserved) != 1 {
		t.Fatalf("output = %+v", got)
	}
}

func TestModelCatalogLiveIsExplicit(t *testing.T) {
	var gotLive bool
	stubModelCatalog(t, testModelCatalog(), &gotLive)
	cmd := modelCatalogCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--live", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !gotLive {
		t.Fatal("--live did not request a live collection")
	}
}

// The text view must say what each model is and where it runs, and must name
// nodes it could not see instead of silently omitting them.
func TestModelCatalogTextShowsStateLocalityAndUnobserved(t *testing.T) {
	stubModelCatalog(t, testModelCatalog(), nil)
	cmd := modelCatalogCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"MODEL CATALOG (3)",
		"bitnet-2B", "loaded", "llama.cpp",
		"big:cloud", "cloud-proxy",
		"embed:latest", "embedding",
		"Not observed: node-c (unreachable): ssh: unable to authenticate",
		"Source: daemon-cache",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
}
