package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestV2ModelsReturnsCatalogFromCache(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{
		snap: &models.ClusterSnapshot{
			Publication: &models.PublicationEnvelope{ID: "pub-v2-models"},
			Nodes: []models.NodeFacts{
				{
					Name: "worker", Status: models.StatusComplete,
					Ollama: &models.OllamaInfo{Installed: true, Catalog: []models.OllamaModelEntry{
						{Name: "embed:latest", Capabilities: []string{"embedding"}},
					}},
					ResidentModels: []models.ResidentModel{{Name: "bitnet-2B", Runtime: "llama.cpp", Port: 8080}},
				},
				{Name: "gone", Status: models.StatusUnreachable, Error: "ssh: unable to authenticate"},
			},
		},
	}, "")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got models.ModelCatalog
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "daemon-cache" || got.PublicationID != "pub-v2-models" || len(got.Entries) != 2 || len(got.Unobserved) != 1 {
		t.Fatalf("catalog = %+v", got)
	}
}

func TestV2ModelsRejectsNonGET(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{snap: &models.ClusterSnapshot{}}, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v2/models", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestV2ModelsRequiresCache(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, nil, "")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/models", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestV2ModelsRequiresToken(t *testing.T) {
	mux := http.NewServeMux()
	registerRoutes(mux, &fakeCache{snap: &models.ClusterSnapshot{}}, "secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without bearer", rec.Code)
	}
}
