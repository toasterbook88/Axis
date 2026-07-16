package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	axisbuildinfo "github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/versioncmp"
)

const (
	updateOwner = "toasterbook88"
	updateRepo  = "axis"
	// axisModulePath is the Go module path used to verify a candidate is AXIS
	// without executing it (via debug/buildinfo.ReadFile).
	axisModulePath = "github.com/toasterbook88/axis"
)

// updateAPIBase and updateGetFunc are vars so tests can override them.
var (
	updateAPIBase = "https://api.github.com"
	updateGetFunc = func(rawURL string) (*http.Response, error) {
		return safeGet(rawURL)
	}
	// inspectBinary is swappable in tests.
	inspectBinary = inspectAxisInstall
)

// allowedUpdateHosts is the set of HTTPS hosts the updater may contact.
var allowedUpdateHosts = []string{
	"api.github.com",
	"github.com",
	"objects.githubusercontent.com",
	"releases.githubusercontent.com",
}

func updateCmd() *cobra.Command {
	var checkOnly bool
	var updateAll bool
	var targetPath string

	cmd := &cobra.Command{
		Use: "update",
		// Short must not contain the substring "discover" — root help tests
		// assert the old `discover` command is gone via that token.
		Short: "Update the axis binary to the latest published release",
		Long: "Check GitHub Releases for the latest published axis version and install it in-place.\n\n" +
			"By default, updates only the currently running installation (bounded self-update).\n" +
			"Other known installs on $PATH and common locations are reported as shadows but not\n" +
			"modified unless you pass --all (or target one path with --path).\n\n" +
			"Before replacing any file, axis verifies:\n" +
			"  • Go build identity (module path " + axisModulePath + ") without executing it\n" +
			"  • per-target version (never silently downgrades a newer install)\n" +
			"  • package-manager ownership (Homebrew/Nix/… paths are skipped)\n\n" +
			"Use --check to report the running binary and any shadows without installing.\n" +
			"Use --all to refresh every validated install that is older than the latest release.\n" +
			"Use --path to update a single explicit path.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, checkOnly, updateAll, targetPath)
		},
	}
	cmd.Flags().BoolVarP(&checkOnly, "check", "c", false, "report update status for the running binary and known shadows without installing")
	cmd.Flags().BoolVar(&updateAll, "all", false, "update all validated older installs (PATH + common locations), not only the running binary")
	cmd.Flags().StringVar(&targetPath, "path", "", "update a specific axis binary at this path")
	return cmd
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// installInfo describes one on-disk candidate after identity inspection.
type installInfo struct {
	// Path is the discovered install path (absolute; may be a symlink).
	Path string
	// Resolved is the symlink target when Path is a symlink (for ownership checks
	// and for the actual file we replace — we never convert a symlink into a
	// regular file in place).
	Resolved string
	// IsSymlink is true when Path is a symbolic link.
	IsSymlink bool
	// Version is the embedded release version when known (no leading v).
	Version string
	// ManagedBy is non-empty when the path is owned by a package manager.
	ManagedBy string
	// IsAxis is true when Go buildinfo identifies the AXIS module.
	IsAxis bool
	// Reason explains skip/invalid when IsAxis is false or version is unusable.
	Reason string
}

// updateMode selects how unknown-version targets are treated.
type updateMode int

const (
	// modeSelf: only the running binary (unknown version allowed via process buildinfo).
	modeSelf updateMode = iota
	// modeAll: implicit multi-install discovery — unknown-version secondaries refused.
	modeAll
	// modePath: explicit --path — operator-authorized; unknown version allowed after identity.
	modePath
)

func runUpdate(cmd *cobra.Command, checkOnly, updateAll bool, targetPath string) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()
	current := axisbuildinfo.Version
	fmt.Fprintf(out, "Current version: v%s\n", current)

	if axisbuildinfo.UpdateManagedBy != "" {
		fmt.Fprintf(out, "Managed by:      %s\n", axisbuildinfo.UpdateManagedBy)
	}

	fmt.Fprintf(out, "Checking for updates...\n")

	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	fmt.Fprintf(out, "Latest version:  v%s\n", latest)

	selfPath := resolveSelfPath()
	shadows := listKnownInstallPaths(selfPath)
	reportShadows(out, shadows, latest, selfPath)

	relation, err := compareReleaseVersions(current, latest)
	if err != nil {
		return fmt.Errorf("comparing versions: %w", err)
	}

	if checkOnly {
		return reportCheck(out, current, latest, relation, selfPath, shadows)
	}

	// Select mutation targets: self by default; --all expands; --path is exclusive.
	mode := modeSelf
	switch {
	case targetPath != "":
		mode = modePath
	case updateAll:
		mode = modeAll
	}
	targets, err := selectUpdateTargets(targetPath, updateAll, selfPath)
	if err != nil {
		return err
	}

	// Self-only path still respects package-manager and downgrade rules for the
	// running binary before download.
	if mode == modeSelf {
		if axisbuildinfo.UpdateManagedBy != "" {
			fmt.Fprintf(out, "Refusing in-place update. This installation is managed by '%s'. Please use your package manager to upgrade.\n", axisbuildinfo.UpdateManagedBy)
			printShadowHint(out, shadows, latest, selfPath)
			return nil
		}
		switch relation {
		case 0:
			fmt.Fprintf(out, "Already up to date.\n")
			printShadowHint(out, shadows, latest, selfPath)
			return nil
		case 1:
			fmt.Fprintf(out, "Current build is newer than the latest published release.\n")
			fmt.Fprintf(out, "Refusing to downgrade the currently running binary.\n")
			printShadowHint(out, shadows, latest, selfPath)
			return nil
		case -1:
			fmt.Fprintf(out, "Update available: v%s → v%s\n", current, latest)
		}
	}

	return installRelease(cmd, rel, latest, targets, selfPath, mode, errOut, out)
}

func reportCheck(out io.Writer, current, latest string, relation int, selfPath string, shadows []string) error {
	switch relation {
	case 0:
		fmt.Fprintf(out, "Running binary: already up to date (v%s).\n", current)
	case -1:
		fmt.Fprintf(out, "Running binary: update available v%s → v%s\n", current, latest)
		if axisbuildinfo.UpdateManagedBy != "" {
			fmt.Fprintf(out, "Run your package manager (e.g. brew upgrade axis) to install.\n")
		} else {
			fmt.Fprintf(out, "Run `axis update` (without --check) to install.\n")
		}
	case 1:
		fmt.Fprintf(out, "Running binary: newer than the latest published release (v%s > v%s).\n", current, latest)
		fmt.Fprintf(out, "No update available for the running binary; refusing to suggest a downgrade.\n")
	}

	// Multi-install status: the condition plain --check used to miss.
	stale := 0
	for _, p := range shadows {
		info, err := inspectBinary(p)
		if err != nil || !info.IsAxis {
			continue
		}
		if info.ManagedBy != "" {
			fmt.Fprintf(out, "Shadow (managed/%s, skipped): %s v%s\n", info.ManagedBy, info.Path, displayVersion(info.Version))
			continue
		}
		if info.Version == "" {
			fmt.Fprintf(out, "Shadow (version unknown): %s\n", info.Path)
			continue
		}
		rel, err := compareReleaseVersions(info.Version, latest)
		if err != nil {
			continue
		}
		switch rel {
		case -1:
			stale++
			fmt.Fprintf(out, "Shadow (stale): %s v%s → v%s\n", info.Path, info.Version, latest)
		case 0:
			fmt.Fprintf(out, "Shadow (current): %s v%s\n", info.Path, info.Version)
		case 1:
			fmt.Fprintf(out, "Shadow (newer): %s v%s (will not be downgraded)\n", info.Path, info.Version)
		}
	}
	if stale > 0 {
		fmt.Fprintf(out, "Run `axis update --all` to refresh %d stale shadow install(s).\n", stale)
	}
	return nil
}

func reportShadows(out io.Writer, shadows []string, latest, selfPath string) {
	if len(shadows) == 0 {
		return
	}
	// Lightweight note during non-check runs; detailed status is in --check / printShadowHint.
	fmt.Fprintf(out, "Other known installs: %d (use --check for details, --all to update stale ones)\n", len(shadows))
}

func printShadowHint(out io.Writer, shadows []string, latest, selfPath string) {
	stale := 0
	for _, p := range shadows {
		info, err := inspectBinary(p)
		if err != nil || !info.IsAxis || info.ManagedBy != "" || info.Version == "" {
			continue
		}
		rel, err := compareReleaseVersions(info.Version, latest)
		if err == nil && rel < 0 {
			stale++
		}
	}
	if stale > 0 {
		fmt.Fprintf(out, "Note: %d other install(s) are older than v%s. Run `axis update --all` to refresh them.\n", stale, latest)
	}
}

func displayVersion(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func selectUpdateTargets(explicitPath string, updateAll bool, selfPath string) ([]string, error) {
	if explicitPath != "" {
		return []string{explicitPath}, nil
	}
	if updateAll {
		// Self first, then other known installs (PATH + common).
		seen := map[string]bool{}
		var out []string
		add := func(p string) {
			p = strings.TrimSpace(p)
			if p == "" {
				return
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			if seen[abs] {
				return
			}
			seen[abs] = true
			out = append(out, abs)
		}
		if selfPath != "" {
			add(selfPath)
		}
		for _, p := range listKnownInstallPaths(selfPath) {
			add(p)
		}
		// Also raw PATH/common entries that listKnownInstallPaths already covers.
		if len(out) == 0 {
			return nil, fmt.Errorf("could not determine axis binary location to update")
		}
		return out, nil
	}
	if selfPath == "" {
		return nil, fmt.Errorf("could not determine the running axis binary path")
	}
	return []string{selfPath}, nil
}

func compareReleaseVersions(current, latest string) (int, error) {
	return versioncmp.Compare(current, latest)
}

func axisBinaryName() string {
	if runtime.GOOS == "windows" {
		return "axis.exe"
	}
	return "axis"
}

// commonAxisInstallCandidates returns well-known install paths that often shadow
// each other when operators mix go install, make install-user, and system paths.
func commonAxisInstallCandidates() []string {
	name := axisBinaryName()
	var out []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".local", "bin", name),
			filepath.Join(home, "go", "bin", name),
		)
	}
	out = append(out,
		filepath.Join("/usr/local/bin", name),
		filepath.Join("/opt/homebrew/bin", name),
	)
	return out
}

// listKnownInstallPaths returns other install paths (not the running binary)
// found on $PATH and in common locations. Symlinks are kept as the install
// path (not resolved) so ownership checks can inspect the destination separately.
func listKnownInstallPaths(selfPath string) []string {
	seen := map[string]bool{}
	var paths []string
	selfAbs := ""
	if selfPath != "" {
		if a, err := filepath.Abs(selfPath); err == nil {
			selfAbs = a
		}
	}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if selfAbs != "" && abs == selfAbs {
			return
		}
		// Also skip if same resolved file as self.
		if selfAbs != "" {
			if sameFile(abs, selfAbs) {
				return
			}
		}
		if seen[abs] {
			return
		}
		info, err := os.Lstat(abs)
		if err != nil || info.IsDir() {
			return
		}
		seen[abs] = true
		paths = append(paths, abs)
	}

	name := axisBinaryName()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		add(filepath.Join(dir, name))
	}
	for _, c := range commonAxisInstallCandidates() {
		add(c)
	}
	return paths
}

func sameFile(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 != nil {
		ra = a
	}
	if err2 != nil {
		rb = b
	}
	aa, _ := filepath.Abs(ra)
	ab, _ := filepath.Abs(rb)
	return aa == ab
}

// resolveSelfPath returns the absolute path of the running binary without
// forcing symlink resolution for the install path (Lstat identity). When the
// executable path cannot be determined, returns "".
func resolveSelfPath() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(self)
	if err != nil {
		return self
	}
	return abs
}

// inspectAxisInstall validates that path is an AXIS binary without executing it,
// reads version metadata when available, and detects package-manager ownership.
func inspectAxisInstall(path string) (installInfo, error) {
	info := installInfo{Path: path}
	abs, err := filepath.Abs(path)
	if err == nil {
		info.Path = abs
	}

	fi, err := os.Lstat(info.Path)
	if err != nil {
		return info, err
	}
	if fi.IsDir() {
		info.Reason = "is a directory"
		return info, nil
	}

	resolved := info.Path
	if fi.Mode()&os.ModeSymlink != 0 {
		info.IsSymlink = true
		if r, err := filepath.EvalSymlinks(info.Path); err == nil {
			resolved = r
			if a, err := filepath.Abs(r); err == nil {
				resolved = a
			}
		}
	}
	info.Resolved = resolved

	// Package-manager check on both the install path and resolved destination.
	if mgr, ok := packageManagerForPath(info.Path); ok {
		info.ManagedBy = mgr
	} else if mgr, ok := packageManagerForPath(resolved); ok {
		info.ManagedBy = mgr
	}

	// Running binary: trust process buildinfo for version/identity when path matches self.
	if self := resolveSelfPath(); self != "" && (info.Path == self || sameFile(info.Path, self)) {
		info.IsAxis = true
		info.Version = strings.TrimPrefix(axisbuildinfo.Version, "v")
		if axisbuildinfo.UpdateManagedBy != "" && info.ManagedBy == "" {
			info.ManagedBy = axisbuildinfo.UpdateManagedBy
		}
		return info, nil
	}

	bi, err := buildinfo.ReadFile(info.Path)
	if err != nil {
		// Try resolved path if different (broken open on some symlink cases).
		if resolved != info.Path {
			bi, err = buildinfo.ReadFile(resolved)
		}
	}
	if err != nil {
		info.Reason = fmt.Sprintf("not a Go binary: %v", err)
		return info, nil
	}
	if !isAxisBuildInfo(bi) {
		info.Reason = "Go binary is not " + axisModulePath
		return info, nil
	}
	info.IsAxis = true
	info.Version = versionFromBuildInfo(bi)
	if info.Version == "" {
		info.Reason = "AXIS binary but version metadata unavailable"
	}
	return info, nil
}

func isAxisBuildInfo(bi *buildinfo.BuildInfo) bool {
	if bi == nil {
		return false
	}
	// Exact module identity only — reject axis-tools and path-injection lookalikes.
	if bi.Main.Path == axisModulePath {
		return true
	}
	switch bi.Path {
	case axisModulePath, axisModulePath + "/cmd/axis":
		return true
	default:
		return false
	}
}

func versionFromBuildInfo(bi *buildinfo.BuildInfo) string {
	if bi == nil {
		return ""
	}
	// Prefer explicit -X ldflags when present (release/dev tooling may set them).
	for _, s := range bi.Settings {
		if s.Key == "-X" {
			// format: path.var=value
			if v := parseLdflagVersion(s.Value); v != "" {
				return v
			}
		}
	}
	// go install / module builds often stamp Main.Version (e.g. v0.14.4).
	if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
		return strings.TrimPrefix(v, "v")
	}
	return ""
}

func parseLdflagVersion(value string) string {
	// Accept:
	//   github.com/toasterbook88/axis/internal/buildinfo.Version=0.14.4
	//   github.com/toasterbook88/axis/cmd/axis.Version=0.14.4
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	key, val := parts[0], strings.TrimSpace(parts[1])
	if !strings.HasSuffix(key, ".Version") && !strings.HasSuffix(key, ".version") {
		return ""
	}
	if !strings.Contains(key, "toasterbook88/axis") && !strings.Contains(key, "buildinfo") {
		// Still allow generic main.Version for this repo's builds.
		if !strings.HasSuffix(key, "/cmd/axis.Version") {
			return ""
		}
	}
	val = strings.TrimPrefix(val, "v")
	if val == "" || val == "(devel)" {
		return ""
	}
	return val
}

func packageManagerForPath(path string) (string, bool) {
	p := filepath.ToSlash(path)
	switch {
	case strings.Contains(p, "/nix/store/"), strings.HasPrefix(p, "/run/current-system/"):
		return "nix", true
	case strings.Contains(p, "/Cellar/"), strings.Contains(p, "/opt/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return "homebrew", true
	case strings.Contains(p, "/var/lib/flatpak/"):
		return "flatpak", true
	case strings.Contains(p, "/snap/"):
		return "snap", true
	default:
		return "", false
	}
}

func fetchLatestRelease() (*ghRelease, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/%s/releases/latest", updateAPIBase, updateOwner, updateRepo)
	resp, err := updateGetFunc(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// downloadReleaseBinary downloads and extracts the axis binary from a release.
func downloadReleaseBinary(cmd *cobra.Command, rel *ghRelease, version string) ([]byte, error) {
	out := cmd.OutOrStdout()
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	archiveName := fmt.Sprintf("axis_%s_%s_%s.tar.gz", version, goos, goarch)

	var archiveURL, checksumURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case archiveName:
			archiveURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumURL = a.BrowserDownloadURL
		}
	}
	if archiveURL == "" {
		return nil, fmt.Errorf("no release asset found for %s/%s (expected %s)", goos, goarch, archiveName)
	}

	fmt.Fprintf(out, "Downloading %s...\n", archiveName)
	archiveData, err := downloadBytes(archiveURL)
	if err != nil {
		return nil, fmt.Errorf("downloading release: %w", err)
	}

	if checksumURL != "" {
		if err := verifyChecksum(archiveData, archiveName, checksumURL); err != nil {
			return nil, fmt.Errorf("checksum verification failed: %w", err)
		}
		fmt.Fprintf(out, "Checksum verified.\n")
	}

	binary, err := extractBinary(archiveData, "axis")
	if err != nil {
		return nil, fmt.Errorf("extracting binary: %w", err)
	}
	return binary, nil
}

func installRelease(cmd *cobra.Command, rel *ghRelease, latest string, targets []string, selfPath string, mode updateMode, errOut, out io.Writer) error {
	// Validate every target before downloading.
	type planned struct {
		// replacePath is the regular file written by replaceExecutable.
		replacePath string
		// label is the user-facing path (symlink path when applicable).
		label   string
		version string
	}
	var plan []planned
	seenReplace := map[string]bool{}

	for _, target := range targets {
		info, err := inspectBinary(target)
		if err != nil {
			fmt.Fprintf(errOut, "warning: skipping %s: %v\n", target, err)
			continue
		}
		if !info.IsAxis {
			fmt.Fprintf(errOut, "warning: skipping %s: %s\n", target, orDefault(info.Reason, "not an AXIS binary"))
			continue
		}
		if info.ManagedBy != "" {
			fmt.Fprintf(out, "Skipped (managed by %s): %s\n", info.ManagedBy, info.Path)
			continue
		}

		// Per-target version gate: never silently downgrade.
		if info.Version != "" {
			reln, err := compareReleaseVersions(info.Version, latest)
			if err != nil {
				fmt.Fprintf(errOut, "warning: skipping %s: cannot compare version %q: %v\n", info.Path, info.Version, err)
				continue
			}
			switch reln {
			case 0:
				fmt.Fprintf(out, "Already current: %s (v%s)\n", info.Path, info.Version)
				continue
			case 1:
				fmt.Fprintf(out, "Skipped (newer than release v%s): %s is v%s\n", latest, info.Path, info.Version)
				continue
			}
		} else {
			// Unknown version:
			//  - explicit --path: operator authorized → allow after identity check
			//  - running self: process buildinfo usually supplies version; if not, allow
			//  - implicit --all secondaries: refuse (may hide a newer local build)
			isSelf := selfPath != "" && (info.Path == selfPath || sameFile(info.Path, selfPath) || sameFile(info.Resolved, selfPath))
			if mode == modeAll && !isSelf {
				fmt.Fprintf(errOut, "warning: skipping %s: AXIS binary but version metadata unavailable (refusing implicit --all replace; use --path to force)\n", info.Path)
				continue
			}
			if mode == modeSelf && !isSelf {
				fmt.Fprintf(errOut, "warning: skipping %s: AXIS binary but version metadata unavailable\n", info.Path)
				continue
			}
			// modePath, or self under modeSelf/modeAll: proceed
		}

		// Preserve symlink topology: replace the resolved regular file, not the
		// symlink node (which would turn the link into a standalone binary).
		replacePath := info.Path
		label := info.Path
		if info.IsSymlink {
			if info.Resolved == "" || info.Resolved == info.Path {
				fmt.Fprintf(errOut, "warning: skipping %s: symlink with unresolvable target\n", info.Path)
				continue
			}
			// Re-check management on the resolved destination alone (already done
			// in inspect, but be explicit if destination differs).
			if mgr, ok := packageManagerForPath(info.Resolved); ok {
				fmt.Fprintf(out, "Skipped (symlink → managed by %s): %s → %s\n", mgr, info.Path, info.Resolved)
				continue
			}
			replacePath = info.Resolved
			label = fmt.Sprintf("%s → %s", info.Path, info.Resolved)
			fmt.Fprintf(out, "Symlink install: will update target %s (keeping link %s)\n", info.Resolved, info.Path)
		}

		// Deduplicate aliases that resolve to the same file.
		if seenReplace[replacePath] {
			fmt.Fprintf(out, "Already planned (alias): %s\n", label)
			continue
		}
		seenReplace[replacePath] = true
		plan = append(plan, planned{replacePath: replacePath, label: label, version: info.Version})
	}

	if len(plan) == 0 {
		fmt.Fprintf(out, "Nothing to update.\n")
		return nil
	}

	binary, err := downloadReleaseBinary(cmd, rel, latest)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Updating %d install(s)...\n", len(plan))
	updated := 0
	failed := 0
	for _, p := range plan {
		if err := replaceExecutable(p.replacePath, binary); err != nil {
			failed++
			fmt.Fprintf(errOut, "warning: could not update %s: %v\n", p.label, err)
			continue
		}
		updated++
		from := p.version
		if from == "" {
			from = "unknown"
		}
		fmt.Fprintf(out, "Updated: %s (v%s → v%s)\n", p.label, from, latest)
	}
	if updated == 0 {
		return fmt.Errorf("failed to update any axis binary (%d target(s), %d error(s))", len(plan), failed)
	}
	if failed > 0 {
		fmt.Fprintf(errOut, "warning: updated %d install(s); %d failed (often permissions on /usr/local/bin — re-run with elevated rights for system paths)\n", updated, failed)
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// safeGet performs an HTTPS GET restricted to allowed GitHub domains.
func safeGet(rawURL string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("only HTTPS downloads are permitted (got %q)", u.Scheme)
	}
	for _, h := range allowedUpdateHosts {
		if u.Host == h {
			c := &http.Client{Timeout: 60 * time.Second}
			return c.Get(rawURL) //nolint:noctx
		}
	}
	return nil, fmt.Errorf("host %q is not an allowed GitHub domain", u.Host)
}

func downloadBytes(rawURL string) ([]byte, error) {
	resp, err := updateGetFunc(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func verifyChecksum(data []byte, archiveName, checksumURL string) error {
	csData, err := downloadBytes(checksumURL)
	if err != nil {
		return fmt.Errorf("downloading checksums.txt: %w", err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(string(csData), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == archiveName {
			if fields[0] != got {
				return fmt.Errorf("expected %s got %s", fields[0], got)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum entry for %s in checksums.txt", archiveName)
}

func extractBinary(archiveData []byte, name string) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, fmt.Errorf("reading gzip: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if filepath.Base(hdr.Name) == name {
			const maxBinarySize = 128 << 20 // 128 MiB sanity cap
			return io.ReadAll(io.LimitReader(tr, maxBinarySize))
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

func replaceExecutable(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".axis-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		tmp.Close()
		if cleanup {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0755); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	cleanup = false
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
