package facts

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Model bundles are directories: Core AI exports (.aimodel) and Core ML
// packages (.mlpackage, compiled .mlmodelc). Their contents do not say
// whether they hold a language model (fleet examples were mostly speech
// models), so they are recorded with kind unknown and never treated as
// runnable weights.
var modelBundleFormats = map[string]string{
	".aimodel":   "aimodel",
	".mlpackage": "coreml",
	".mlmodelc":  "coreml",
}

// modelBundleMaxEntries bounds the size walk inside one bundle.
const modelBundleMaxEntries = 2000

// modelBundleBytes reports whether dir is a model bundle and, if so, the
// bytes of its files. Bytes is 0 when the bundle is below minSize. Dataless
// (iCloud placeholder) entries are skipped without reading them.
func modelBundleBytes(path, name string, minSize int64) (int64, bool) {
	if _, ok := modelBundleFormats[strings.ToLower(filepath.Ext(name))]; !ok {
		return 0, false
	}
	var total int64
	entries := 0
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if entries++; entries > modelBundleMaxEntries {
			return fs.SkipAll
		}
		if d.IsDir() {
			if entryDataless(d) {
				return fs.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && !isDataless(info) {
			total += info.Size()
		}
		return nil
	})
	if total < minSize {
		return 0, true
	}
	return total, true
}

// addBundleHit records a bundle that met the size floor while the walk has
// room, and never descends into it.
func addBundleHit(hits *[]fileHit, h fileHit, remaining int) error {
	if h.Bytes > 0 && remaining-len(*hits) > 0 {
		*hits = append(*hits, h)
	}
	return fs.SkipDir
}

// singleWeightFormat classifies hits that stand alone rather than grouping
// by directory: GGUF files (model, or projector for mmproj-*) and bundles.
func singleWeightFormat(ext, base string) (format, kind string, single bool) {
	if ext == ".gguf" {
		if strings.HasPrefix(base, "mmproj") {
			return "gguf", "projector", true
		}
		return "gguf", "model", true
	}
	if format, ok := modelBundleFormats[ext]; ok {
		return format, "unknown", true
	}
	return "", "", false
}
