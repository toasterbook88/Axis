package facts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

const (
	diskWeightMinSize      = 20 << 20 // 20 MiB; tokenizers and stubs fall below this
	diskWeightMaxFiles     = 400
	diskWeightScanBudget   = 8 * time.Second
	diskWeightVolumeBudget = 3 * time.Second
)

var ggufMagic = []byte("GGUF")

var skipDirNames = map[string]struct{}{
	".git": {}, "node_modules": {}, "__pycache__": {}, ".venv": {}, "venv": {},
	"target": {}, ".tox": {}, ".cargo": {}, ".npm": {}, ".rustup": {},
	"Library": {}, "Containers": {}, "Applications": {}, ".Trash": {},
	"lost+found": {}, ".cache": {}, ".ollama": {}, "blobs": {},
}

var systemPathPrefixes = []string{
	"/usr", "/bin", "/sbin", "/lib", "/lib64", "/boot", "/proc", "/sys",
	"/dev", "/run", "/snap", "/var/lib/docker", "/var/lib/containerd",
	"/var/log", "/var/cache", "/System", "/Library", "/Applications", "/private",
}

var systemdModelRE = regexp.MustCompile(`(?:--model(?:-path)?|-m)(?:=|\s+)(\S+)`)

// DiskWeightScanConfig controls a disk-weight inventory scan.
type DiskWeightScanConfig struct {
	Home     string
	Volumes  []models.Volume
	MinSize  int64
	MaxFiles int
}

// DiskWeightScanResult is the inventory plus whether the walk was cut short.
type DiskWeightScanResult struct {
	Weights   []models.DiskWeight
	Truncated bool
}

type fileHit struct {
	Path   string
	Volume string
	Source string
	Bytes  int64
}

var scanDiskWeightsFn = ScanDiskWeights

// ScanDiskWeights inventories weight artifacts on local Axis volumes.
// Catalogs (HF hub, ollama manifests, systemd unit paths) run first.
// A bounded find then covers the rest of each local volume (-xdev via mount prefix).
func ScanDiskWeights(ctx context.Context, cfg DiskWeightScanConfig) DiskWeightScanResult {
	if cfg.MinSize <= 0 {
		cfg.MinSize = diskWeightMinSize
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = diskWeightMaxFiles
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, diskWeightScanBudget)
	defer cancel()

	out := DiskWeightScanResult{}
	seen := map[string]struct{}{}
	add := func(w models.DiskWeight) {
		if w.Path == "" {
			return
		}
		key := filepath.Clean(w.Path)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if w.Kind == "" {
			w.Kind = "model"
		}
		out.Weights = append(out.Weights, w)
	}

	home := strings.TrimSpace(cfg.Home)
	vols := localVolumesOnly(cfg.Volumes)

	scanHFHub(ctx, home, vols, cfg.MinSize, add)
	scanOllamaManifests(home, vols, add)
	scanSystemdUnits(home, vols, cfg.MinSize, add)

	wellKnown := wellKnownRoots(home)
	hits := make([]fileHit, 0, 64)
	truncated := false
	collectHits := func(root, source string, budget time.Duration) {
		if truncated || ctx.Err() != nil {
			return
		}
		h, trunc := walkWeightFiles(ctx, root, vols, source, cfg.MinSize, cfg.MaxFiles-len(hits), budget)
		hits = append(hits, h...)
		if trunc || len(hits) >= cfg.MaxFiles {
			truncated = true
		}
	}

	for _, root := range wellKnown {
		collectHits(root, "well-known", 2*time.Second)
	}
	for _, v := range vols {
		if v.Mount == "" {
			continue
		}
		root := v.Mount
		if root == "/" && home != "" {
			root = home
		}
		collectHits(root, "find", diskWeightVolumeBudget)
	}

	for _, w := range groupDiskWeights(dedupeHits(hits)) {
		add(w)
	}
	out.Truncated = truncated || ctx.Err() != nil
	sort.Slice(out.Weights, func(i, j int) bool {
		return out.Weights[i].Path < out.Weights[j].Path
	})
	return out
}

func localVolumesOnly(vols []models.Volume) []models.Volume {
	var out []models.Volume
	for _, v := range vols {
		if v.Kind == "network" || v.Mount == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func wellKnownRoots(home string) []string {
	var roots []string
	if home != "" {
		roots = append(roots, filepath.Join(home, "models"), filepath.Join(home, "Storage"))
	}
	roots = append(roots, "/opt/models", "/models")
	var existing []string
	for _, r := range roots {
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			existing = append(existing, r)
		}
	}
	return existing
}

func volumeMountFor(path string, vols []models.Volume) string {
	clean := filepath.Clean(path)
	best := ""
	for _, v := range vols {
		m := filepath.Clean(v.Mount)
		if clean == m || strings.HasPrefix(clean, m+string(os.PathSeparator)) {
			if len(m) > len(best) {
				best = m
			}
		}
	}
	return best
}

func scanHFHub(ctx context.Context, home string, vols []models.Volume, minSize int64, add func(models.DiskWeight)) {
	if home == "" || ctx.Err() != nil {
		return
	}
	hub := filepath.Join(home, ".cache", "huggingface", "hub")
	if resolved, err := filepath.EvalSymlinks(hub); err == nil {
		hub = resolved
	}
	entries, err := os.ReadDir(hub)
	if err != nil {
		return
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "models--") {
			continue
		}
		dir := filepath.Join(hub, e.Name())
		name := hfHubName(e.Name())
		bytes := sumWeightFiles(filepath.Join(dir, "snapshots"), 3)
		if bytes < minSize {
			continue
		}
		format := "safetensors"
		if hasExtUnder(dir, ".gguf", 4) {
			format = "gguf"
		}
		add(models.DiskWeight{
			Name: name, Path: dir, Bytes: bytes, Format: format,
			Volume: volumeMountFor(dir, vols), Source: "hf-hub", Kind: "model",
		})
	}
}

func hfHubName(dir string) string {
	rest := strings.TrimPrefix(dir, "models--")
	parts := strings.Split(rest, "--")
	if len(parts) >= 2 {
		return strings.Join(parts, "/")
	}
	return rest
}

func scanOllamaManifests(home string, vols []models.Volume, add func(models.DiskWeight)) {
	if home == "" {
		return
	}
	root := filepath.Join(home, ".ollama", "models", "manifests")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		name := ollamaManifestName(rel)
		if name == "" {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		add(models.DiskWeight{
			Name: name, Path: path, Bytes: st.Size(), Format: "ollama",
			Volume: volumeMountFor(path, vols), Source: "ollama-manifest", Kind: "model",
		})
		return nil
	})
}

func ollamaManifestName(rel string) string {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return ""
	}
	// registry.ollama.ai/library/name/tag or .../library/name
	name := parts[len(parts)-2]
	tag := parts[len(parts)-1]
	if name == "library" {
		return tag
	}
	return name + ":" + tag
}

func scanSystemdUnits(home string, vols []models.Volume, minSize int64, add func(models.DiskWeight)) {
	dirs := []string{"/etc/systemd/system"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "systemd", "user"))
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".service") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			for _, m := range systemdModelRE.FindAllSubmatch(body, -1) {
				p := strings.Trim(string(m[1]), `"'`)
				if p == "" || strings.HasPrefix(p, "-") {
					continue
				}
				st, err := os.Stat(p)
				if err != nil {
					continue
				}
				bytes := st.Size()
				format := "gguf"
				path := p
				if st.IsDir() {
					bytes = sumWeightFiles(p, 3)
					format = "safetensors"
					if hasExtUnder(p, ".gguf", 3) {
						format = "gguf"
					}
				} else if !strings.EqualFold(filepath.Ext(p), ".gguf") && bytes < minSize {
					continue
				}
				if bytes <= 0 {
					continue
				}
				add(models.DiskWeight{
					Name: filepath.Base(strings.TrimSuffix(path, filepath.Ext(path))),
					Path: path, Bytes: bytes, Format: format,
					Volume: volumeMountFor(path, vols), Source: "systemd", Kind: "model",
				})
			}
		}
	}
}

func walkWeightFiles(ctx context.Context, root string, vols []models.Volume, source string, minSize int64, remaining int, budget time.Duration) ([]fileHit, bool) {
	if remaining <= 0 {
		return nil, true
	}
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil, false
	}
	rootMount := volumeMountFor(root, vols)
	deadline := time.Now().Add(budget)
	var hits []fileHit
	truncated := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			name := d.Name()
			if path != root {
				if _, skip := skipDirNames[name]; skip {
					return fs.SkipDir
				}
				if isSystemPath(path) {
					return fs.SkipDir
				}
				if rootMount != "" {
					m := volumeMountFor(path, vols)
					if m != "" && m != rootMount {
						return fs.SkipDir
					}
				}
			}
			return nil
		}
		if remaining-len(hits) <= 0 {
			truncated = true
			return fs.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".gguf" && ext != ".safetensors" {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < minSize {
			return nil
		}
		if ext == ".gguf" && !fileHasGGUFMagic(path) {
			return nil
		}
		hits = append(hits, fileHit{
			Path: path, Bytes: info.Size(), Source: source,
			Volume: volumeMountFor(path, vols),
		})
		return nil
	})
	return hits, truncated
}

func dedupeHits(hits []fileHit) []fileHit {
	seen := map[string]struct{}{}
	out := hits[:0]
	for _, h := range hits {
		key := filepath.Clean(h.Path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, h)
	}
	return out
}

func groupDiskWeights(hits []fileHit) []models.DiskWeight {
	type acc struct {
		bytes int64
		files []fileHit
		vol   string
		src   string
	}
	byDir := map[string]*acc{}
	var singles []models.DiskWeight
	for _, h := range hits {
		ext := strings.ToLower(filepath.Ext(h.Path))
		base := strings.ToLower(filepath.Base(h.Path))
		if ext == ".gguf" {
			kind := "model"
			if strings.HasPrefix(base, "mmproj") {
				kind = "projector"
			}
			singles = append(singles, models.DiskWeight{
				Name: strings.TrimSuffix(filepath.Base(h.Path), filepath.Ext(h.Path)),
				Path: h.Path, Bytes: h.Bytes, Format: "gguf",
				Volume: h.Volume, Source: h.Source, Kind: kind,
			})
			continue
		}
		dir := filepath.Dir(h.Path)
		a := byDir[dir]
		if a == nil {
			a = &acc{vol: h.Volume, src: h.Source}
			byDir[dir] = a
		}
		a.bytes += h.Bytes
		a.files = append(a.files, h)
	}
	for dir, a := range byDir {
		name := filepath.Base(dir)
		path := dir
		if _, err := os.Stat(filepath.Join(dir, "model.safetensors.index.json")); err != nil && len(a.files) == 1 {
			path = a.files[0].Path
			name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		singles = append(singles, models.DiskWeight{
			Name: name, Path: path, Bytes: a.bytes, Format: "safetensors",
			Volume: a.vol, Source: a.src, Kind: "model",
		})
	}
	return singles
}

func fileHasGGUFMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false
	}
	return n == 4 && bytes.Equal(buf, ggufMagic)
}

func sumWeightFiles(root string, maxDepth int) int64 {
	var total int64
	root = filepath.Clean(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(os.PathSeparator)) + 1
		}
		if d.IsDir() {
			if path != root {
				if _, skip := skipDirNames[d.Name()]; skip {
					return fs.SkipDir
				}
			}
			if depth > maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".gguf" && ext != ".safetensors" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

func hasExtUnder(root, ext string, maxDepth int) bool {
	found := false
	root = filepath.Clean(root)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if found || err != nil || d == nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(os.PathSeparator)) + 1
		}
		if d.IsDir() {
			if depth > maxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(d.Name()), ext) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func isSystemPath(p string) bool {
	p = filepath.Clean(p)
	for _, s := range systemPathPrefixes {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

func applyLocalDiskWeights(ctx context.Context, facts *models.NodeFacts) {
	if facts == nil {
		return
	}
	home, _ := os.UserHomeDir()
	var vols []models.Volume
	if facts.Resources != nil {
		vols = facts.Resources.Volumes
	}
	res := scanDiskWeightsFn(ctx, DiskWeightScanConfig{Home: home, Volumes: vols})
	facts.DiskWeights = res.Weights
	facts.DiskWeightsTruncated = res.Truncated
}

func parseDiskWeightsJSON(raw string) DiskWeightScanResult {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DiskWeightScanResult{}
	}
	var payload struct {
		Weights   []models.DiskWeight `json:"weights"`
		Truncated bool                `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return DiskWeightScanResult{}
	}
	return DiskWeightScanResult{Weights: payload.Weights, Truncated: payload.Truncated}
}

// ParseDiskWeightFindTSV groups find-style "bytes\tpath" rows into weights.
func ParseDiskWeightFindTSV(r io.Reader, volume string, source string) []models.DiskWeight {
	var hits []fileHit
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		bytesStr, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		var n int64
		for _, c := range bytesStr {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int64(c-'0')
		}
		if path == "" {
			continue
		}
		hits = append(hits, fileHit{Path: path, Bytes: n, Volume: volume, Source: source})
	}
	return groupDiskWeights(hits)
}
