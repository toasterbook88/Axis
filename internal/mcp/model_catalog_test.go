package axismcp

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestModelCatalogToolReturnsCatalog(t *testing.T) {
	restore := stubMCPRuntime(t, &runtimectx.Context{
		Snapshot: &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{
				{
					Name: "worker", Status: models.StatusComplete,
					Ollama: &models.OllamaInfo{Installed: true, Catalog: []models.OllamaModelEntry{
						{Name: "big:cloud", RemoteHost: "https://cloud.example.com:443"},
					}},
				},
			},
		},
	}, nil)
	defer restore()

	result, err := modelCatalogTool(context.Background(), toolRequest(nil), NewSessionCache(30*time.Second, false, ""))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %s", toolResultText(t, result))
	}
	catalog, ok := result.StructuredContent.(models.ModelCatalog)
	if !ok {
		t.Fatalf("structured content = %#v, want models.ModelCatalog", result.StructuredContent)
	}
	if catalog.Source != "live" || len(catalog.Entries) != 1 || catalog.Entries[0].Locality != models.ModelLocalityCloudProxy {
		t.Fatalf("catalog = %+v", catalog)
	}
}
