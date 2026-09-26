// Copyright (c) 2026 Smith Software Solutions
package api

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
	"github.com/toasterbook88/axis/internal/placement"
)

// cacheObserve adapts snapshotCache to a2a.ObserveData — read helpers only (F5).
type cacheObserve struct {
	cache snapshotCache
}

func (o cacheObserve) StatusJSON() ([]byte, error) {
	if o.cache == nil {
		return []byte(`{"ok":true,"note":"snapshot cache unavailable","surface":"a2a-task"}`), nil
	}
	snap, ok := o.cache.Snapshot()
	meta := o.cache.Meta()
	payload := map[string]any{
		"ok":        true,
		"surface":   "a2a-task",
		"cache_age": meta.CacheAgeSec,
		"version":   meta.Version,
	}
	if ok && snap != nil {
		payload["status"] = snap.Status
		payload["node_count"] = len(snap.Nodes)
		payload["summary"] = snap.Summary
	} else {
		payload["note"] = "snapshot cache not ready"
	}
	return json.Marshal(payload)
}

func (o cacheObserve) FactsJSON() ([]byte, error) {
	if o.cache == nil {
		return []byte(`{"ok":true,"note":"local facts unavailable without snapshot","surface":"a2a-task"}`), nil
	}
	snap, ok := o.cache.Snapshot()
	if !ok || snap == nil {
		return []byte(`{"ok":true,"note":"snapshot cache not ready","surface":"a2a-task"}`), nil
	}
	if node, found := models.FindLocalNode(snap.Nodes); found {
		return json.Marshal(map[string]any{"ok": true, "surface": "a2a-task", "node": node})
	}
	return json.Marshal(map[string]any{"ok": true, "surface": "a2a-task", "note": "local node not found", "nodes": len(snap.Nodes)})
}

func (o cacheObserve) PlaceJSON(description string) ([]byte, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		description = "observe placement advisory"
	}
	if o.cache == nil {
		return []byte(fmt.Sprintf(`{"ok":true,"note":"placement advisory unavailable without snapshot","description":%q,"surface":"a2a-task"}`, description)), nil
	}
	snap, ok := o.cache.Snapshot()
	if !ok || snap == nil {
		return []byte(fmt.Sprintf(`{"ok":true,"note":"snapshot cache not ready","description":%q,"surface":"a2a-task"}`, description)), nil
	}
	reqs := placement.InferRequirements(description)
	decision := placement.SelectBestNode(reqs, snap.Nodes, nil)
	return json.Marshal(map[string]any{
		"ok":           true,
		"surface":      "a2a-task",
		"description":  description,
		"advisory":     true,
		"requirements": reqs,
		"decision":     decision,
	})
}

func (o cacheObserve) ReservationsJSON() ([]byte, error) {
	if o.cache == nil {
		return []byte(`{"ok":true,"reservations":[],"note":"ledger unavailable","surface":"a2a-task"}`), nil
	}
	ledger := o.cache.Ledger()
	if ledger == nil {
		return []byte(`{"ok":true,"reservations":[],"note":"ledger nil","surface":"a2a-task"}`), nil
	}
	list := ledger.Entries()
	return json.Marshal(map[string]any{"ok": true, "surface": "a2a-task", "reservations": list, "count": len(list)})
}

func (o cacheObserve) WorkspaceReadJSON(query string) ([]byte, error) {
	// Constrained read: list AXIS home entries only (no shell, no arbitrary path).
	home := persist.AxisPath()
	entries, err := os.ReadDir(home)
	names := make([]string, 0, 32)
	if err == nil {
		for i, e := range entries {
			if i >= 32 {
				break
			}
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			names = append(names, name)
		}
	}
	return json.Marshal(map[string]any{
		"ok":      true,
		"surface": "a2a-task",
		"note":    "workspace-read observe facade lists AXIS home only (no shell)",
		"query":   strings.TrimSpace(query),
		"home":    home,
		"entries": names,
	})
}
