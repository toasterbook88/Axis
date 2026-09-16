package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func discoverOllamaLocal(ctx context.Context) (models.OllamaInfo, []models.ResidentModel) {
	info := models.OllamaInfo{Installed: false}

	out, err := runOllamaDiscoveryFn(ctx)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}

	// parse the JSON blob
	var parsed ollamaDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil {
		ApplyOllamaWarmth(&parsed.OllamaInfo, parsed.ResidentModels)
		return parsed.OllamaInfo, parsed.ResidentModels
	}
	return info, nil
}

// applyOllamaWarmth populates ExpiresAt and WarmthScore for each ResidentModel
// from Ollama's /api/ps payload. The Ollama probe emits an `expires_at` field
// per resident model and a process-level `default_keep_alive` duration
// (Ollama 0.3.10+). Warmth is a continuous score in [0, 1] computed as
// remaining / total, where total falls back to 5m (Ollama's stock default)
// when `default_keep_alive` is absent or unparseable. When `expires_at` is
// missing or already past, WarmthScore is 0 (cold). Both fields are
// advisory metadata only — placement consumes them as a bounded
// tiebreaker in internal/placement/ranker.go modelWarmthRank.
//
// Exported for testability from internal/placement and from
// internal/facts tests.

func ApplyOllamaWarmth(info *models.OllamaInfo, rms []models.ResidentModel) {
	if len(rms) == 0 {
		return
	}
	now := time.Now()
	total := DefaultOllamaKeepAlive(info)
	for i := range rms {
		rm := &rms[i]
		if rm.ExpiresAt.IsZero() {
			continue
		}
		if !rm.ExpiresAt.After(now) {
			rm.WarmthScore = 0
			continue
		}
		remaining := rm.ExpiresAt.Sub(now)
		score := float64(remaining) / float64(total)
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		rm.WarmthScore = score
	}
}

// DefaultOllamaKeepAlive resolves the process-level default_keep_alive
// duration from an Ollama /api/ps payload, falling back to 5m (Ollama's
// stock default since 0.3.10) when the field is absent or unparseable.
// Returns a positive duration on success. Exported for testability.

func DefaultOllamaKeepAlive(info *models.OllamaInfo) time.Duration {
	const fallback = 5 * time.Minute
	if info == nil {
		return fallback
	}
	val := strings.TrimSpace(info.DefaultKeepAlive)
	if val == "" {
		return fallback
	}
	// If it's a bare integer (seconds), append "s" so ParseDuration can parse it.
	if _, err := strconv.Atoi(val); err == nil {
		val += "s"
	}
	d, err := time.ParseDuration(val)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// discoverLlamaServerLocal probes for a running llama-server process and
// returns its resident models. Returns nil if llama-server is not installed or
// not running.

func discoverLlamaServerLocal(ctx context.Context) []models.ResidentModel {
	out, err := runLlamaServerDiscoveryFn(ctx)
	if err != nil {
		return nil
	}
	var parsed llamaServerDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

// discoverMLXLocal probes for a running mlx_lm.server process on the local
// node and queries its /v1/models endpoint to enumerate resident models.
// Returns nil if mlx_lm is not installed or no server is running.

func discoverMLXLocal(ctx context.Context) []models.ResidentModel {
	out, err := runMLXDiscoveryFn(ctx)
	if err != nil {
		return nil
	}
	var parsed mlxDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

func detectAppleFoundationModels(ctx context.Context, osName, arch, osVersion string, tools []models.ToolInfo) *models.AppleFoundationModelsInfo {
	if !strings.EqualFold(osName, "darwin") || !strings.Contains(strings.ToLower(arch), "arm64") {
		return nil
	}
	if !supportsAppleFoundationModelsOS(osVersion) {
		return &models.AppleFoundationModelsInfo{
			Version: osVersion,
			Error:   "requires macOS 26 or later on Apple silicon (Apple platform versioning)",
		}
	}
	if _, ok := findToolInfo(tools, "swift"); !ok {
		return &models.AppleFoundationModelsInfo{
			Version: osVersion,
			Error:   "swift toolchain not detected",
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	out, err := runAppleFoundationModelsProbeFn(probeCtx)
	trimmedOut := strings.TrimSpace(out)
	info := &models.AppleFoundationModelsInfo{
		Version:   osVersion,
		Available: err == nil,
		Verified:  err == nil && trimmedOut != "",
	}
	if err != nil {
		info.Error = trimmedOut
		if info.Error == "" {
			info.Error = err.Error()
		}
	} else if trimmedOut == "" {
		info.Error = "apple foundation models probe returned empty output"
	}
	return info
}

func supportsAppleFoundationModelsOS(osVersion string) bool {
	// Current Apple platform releases report macOS 26.x via sw_vers -productVersion.
	fields := strings.SplitN(strings.TrimSpace(osVersion), ".", 2)
	if len(fields) == 0 || fields[0] == "" {
		return false
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return false
	}
	return major >= 26
}

func runAppleFoundationModelsProbe(ctx context.Context) (string, error) {
	type buildResult struct {
		path string
		err  error
	}
	ch := make(chan buildResult, 1)
	go func() {
		// Compilation is a one-time, potentially slow operation (first-time
		// xcrun swiftc can take tens of seconds). Give it its own generous
		// timeout so the cache is populated correctly on first run, without
		// blocking the caller context which has a short probe deadline.
		buildCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		path, err := buildAppleFoundationModelsHelperFn(buildCtx)
		ch <- buildResult{path, err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		if result.err != nil {
			return "", result.err
		}
		out, err := appleFoundationModelsProbeCommandFn(ctx, result.path, "--self-test").CombinedOutput()
		return string(out), err
	}
}

func buildAppleFoundationModelsHelper(ctx context.Context) (string, error) {
	cacheDir := appleFoundationModelsCacheDirFn()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create apple foundation models cache directory: %w", err)
	}

	helperSource := filepath.Join(cacheDir, "apple-foundation-models.swift")
	if err := ensureAppleFoundationModelsHelperSource(helperSource); err != nil {
		return "", err
	}

	helperBinary := filepath.Join(cacheDir, "apple-foundation-models-helper")
	upToDate, err := appleFoundationModelsHelperUpToDate(helperSource, helperBinary)
	if err != nil {
		return "", err
	}
	if upToDate {
		return helperBinary, nil
	}

	tmpFile, err := os.CreateTemp(cacheDir, "apple-foundation-models-helper-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary file for apple foundation models helper: %w", err)
	}
	tmpBinary := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("close temporary helper file: %w", err)
	}
	defer os.Remove(tmpBinary)

	out, err := appleFoundationModelsBuildCommandFn(
		ctx,
		"xcrun",
		"swiftc",
		helperSource,
		"-o",
		tmpBinary,
	).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("build apple foundation models helper: %s", msg)
	}
	if err := os.Rename(tmpBinary, helperBinary); err != nil {
		return "", fmt.Errorf("install apple foundation models helper: %w", err)
	}
	return helperBinary, nil
}

func ensureAppleFoundationModelsHelperSource(helperSource string) error {
	existing, err := appleFoundationModelsReadFileFn(helperSource)
	switch {
	case err == nil && string(existing) == appleFoundationModelsHelperSource:
		return nil
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read apple foundation models helper source: %w", err)
	}

	if err := appleFoundationModelsWriteFileFn(helperSource, []byte(appleFoundationModelsHelperSource), 0o644); err != nil {
		return fmt.Errorf("write apple foundation models helper source: %w", err)
	}
	return nil
}

func appleFoundationModelsHelperUpToDate(helperSource, helperBinary string) (bool, error) {
	sourceInfo, err := os.Stat(helperSource)
	if err != nil {
		return false, fmt.Errorf("stat apple foundation models helper source: %w", err)
	}
	binaryInfo, err := os.Stat(helperBinary)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat apple foundation models helper binary: %w", err)
	}
	if binaryInfo.IsDir() {
		return false, fmt.Errorf("apple foundation models helper binary path is a directory: %s", helperBinary)
	}
	return !binaryInfo.ModTime().Before(sourceInfo.ModTime()), nil
}

func runLocalTurboQuantProbe(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-lc", cmd).CombinedOutput()
	return string(out), err
}

func localOSVersion(ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "sw_vers", "-productVersion").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, err := exec.CommandContext(ctx, "uname", "-r").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
