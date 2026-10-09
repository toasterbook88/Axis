package modelinventory

import (
	"sort"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// Catalog lists every model the snapshot observed: loaded residents on any
// engine plus installed Ollama models, one entry per model per node. Each
// entry comes from the node's own report, so the catalog is a pure function
// of the snapshot. Nodes the snapshot could not observe are listed in
// Unobserved rather than appearing to have no models.
func Catalog(snap *models.ClusterSnapshot, source string) models.ModelCatalog {
	catalog := models.ModelCatalog{
		Source:  strings.TrimSpace(source),
		Entries: []models.ModelCatalogEntry{},
	}
	if snap == nil {
		return catalog
	}
	catalog.ObservedAt = snap.Timestamp
	catalog.Warnings = append([]models.Warning(nil), snap.Warnings...)
	if snap.Publication != nil {
		catalog.PublicationID = snap.Publication.ID
	}

	for _, node := range snap.Nodes {
		if node.Status == models.StatusUnreachable || node.Status == models.StatusError {
			catalog.Unobserved = append(catalog.Unobserved, models.UnobservedNode{
				Node:   node.Name,
				Status: node.Status,
				Reason: strings.TrimSpace(node.Error),
			})
			continue
		}
		catalog.Entries = append(catalog.Entries, nodeCatalogEntries(node)...)
	}

	sort.Slice(catalog.Entries, func(i, j int) bool {
		a, b := catalog.Entries[i], catalog.Entries[j]
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Engine != b.Engine {
			return a.Engine < b.Engine
		}
		return a.Port < b.Port
	})
	sort.Slice(catalog.Unobserved, func(i, j int) bool {
		return catalog.Unobserved[i].Node < catalog.Unobserved[j].Node
	})
	return catalog
}

// nodeCatalogEntries merges one node's installed Ollama models with its
// loaded residents. An Ollama resident upgrades the matching installed entry
// to loaded and keeps its metadata; other engines key on engine, model, port.
func nodeCatalogEntries(node models.NodeFacts) []models.ModelCatalogEntry {
	identity := nodeIdentityOf(node)
	byKey := map[string]*models.ModelCatalogEntry{}
	var order []string
	add := func(key string, e models.ModelCatalogEntry) *models.ModelCatalogEntry {
		if existing, ok := byKey[key]; ok {
			return existing
		}
		byKey[key] = &e
		order = append(order, key)
		return &e
	}

	if node.Ollama != nil {
		for _, m := range ollamaInstalled(node.Ollama) {
			e := models.ModelCatalogEntry{
				Model:         m.Name,
				Engine:        "ollama",
				State:         models.ModelCatalogInstalled,
				Locality:      models.ModelLocalityOnNode,
				RemoteHost:    m.RemoteHost,
				Capabilities:  append([]string(nil), m.Capabilities...),
				Family:        m.Family,
				ParameterSize: m.ParameterSize,
				Quantization:  m.Quantization,
				SizeBytes:     m.SizeBytes,
			}
			if m.RemoteHost != "" {
				e.Locality = models.ModelLocalityCloudProxy
			}
			add("ollama\x00"+m.Name, e)
		}
	}

	for _, r := range node.ResidentModels {
		engine := strings.ToLower(strings.TrimSpace(r.Runtime))
		key := engine + "\x00" + r.Name + "\x00" + strconv.Itoa(r.Port)
		if engine == "ollama" {
			key = "ollama\x00" + r.Name
		}
		e := add(key, models.ModelCatalogEntry{
			Model:    r.Name,
			Engine:   engine,
			Locality: models.ModelLocalityOnNode,
		})
		e.State = models.ModelCatalogLoaded
		e.Port = r.Port
		e.InstanceID = instanceID(identity, r.Runtime, r.Name, r.Port)
	}

	out := make([]models.ModelCatalogEntry, 0, len(order))
	for _, key := range order {
		e := *byKey[key]
		e.Node = node.Name
		e.NodeStatus = node.Status
		e.ObservedAt = node.CollectedAt
		out = append(out, e)
	}
	return out
}

// ollamaInstalled prefers the node's catalog and falls back to bare names
// from collectors that predate it; capabilities then stay unknown.
func ollamaInstalled(info *models.OllamaInfo) []models.OllamaModelEntry {
	if len(info.Catalog) > 0 {
		return info.Catalog
	}
	out := make([]models.OllamaModelEntry, 0, len(info.Models))
	for _, name := range info.Models {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, models.OllamaModelEntry{Name: name})
		}
	}
	return out
}
