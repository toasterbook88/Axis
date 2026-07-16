package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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
	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/versioncmp"
)

const (
	updateOwner = "toasterbook88"
	updateRepo  = "axis"
)

// updateAPIBase and updateGetFunc are vars so tests can override them.
var (
	updateAPIBase = "https://api.github.com"
	updateGetFunc = func(rawURL string) (*http.Response, error) {
		return safeGet(rawURL)
	}
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
	var updateAll bool // retained for compatibility; default discovery already updates all installs
	var selfOnly bool
	var targetPath string

	cmd := &cobra.Command{
		Use: "update",
		// Short must not contain the substring "discover" — root help tests
		// assert the old `discover` command is gone via that token.
		Short: "Update axis installs to the latest published release",
		Long: "Check GitHub Releases for the latest published axis version and install it in-place.\n\n" +
			"By default, updates every known install so PATH shadows cannot leave stale copies:\n" +
			"  • the currently running binary\n" +
			"  • every `axis` found on $PATH\n" +
			"  • common install locations (~/.local/bin, ~/go/bin, /usr/local/bin, /opt/homebrew/bin)\n\n" +
			"Use --self to update only the currently running binary.\n" +
			"Use --path to update a single explicit path (allowed even when the running build is newer).\n" +
			"Use --check to report whether an update is available without downloading.\n" +
			"If the running binary is newer than the latest published release, the running binary is\n" +
			"not downgraded unless --path targets it; other known installs are still refreshed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = updateAll // accepted for back-compat; full discovery is the default
			return runUpdate(cmd, checkOnly, selfOnly, targetPath)
		},
	}
	cmd.Flags().BoolVarP(&checkOnly, "check", "c", false, "report whether an update is available without installing")
	cmd.Flags().BoolVar(&selfOnly, "self", false, "update only the currently running binary")
	cmd.Flags().BoolVar(&updateAll, "all", false, "deprecated: default already updates all discovered installs")
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

func runUpdate(cmd *cobra.Command, checkOnly, selfOnly bool, targetPath string) error {
	out := cmd.OutOrStdout()
	current := buildinfo.Version
	fmt.Fprintf(out, "Current version: v%s\n", current)

	if buildinfo.UpdateManagedBy != "" {
		fmt.Fprintf(out, "Managed by:      %s\n", buildinfo.UpdateManagedBy)
	}

	fmt.Fprintf(out, "Checking for updates...\n")

	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	fmt.Fprintf(out, "Latest version:  v%s\n", latest)

	relation, err := compareReleaseVersions(current, latest)
	if err != nil {
		return fmt.Errorf("comparing versions: %w", err)
	}

	if checkOnly {
		switch relation {
		case 0:
			fmt.Fprintf(out, "Already up to date.\n")
		case -1:
			fmt.Fprintf(out, "Update available: v%s → v%s\n", current, latest)
			if buildinfo.UpdateManagedBy != "" {
				fmt.Fprintf(out, "Run your package manager (e.g. brew upgrade axis) to install.\n")
			} else {
				fmt.Fprintf(out, "Run `axis update` (without --check) to install.\n")
			}
		case 1:
			fmt.Fprintf(out, "Current build is newer than the latest published release.\n")
			fmt.Fprintf(out, "No update available; refusing to suggest a downgrade.\n")
		}
		return nil
	}

	// Tip-of-main newer than published: never downgrade the running binary unless
	// --path explicitly targets a file. Still refresh other discovered installs
	// so PATH shadows (e.g. ~/go/bin) do not stay stale.
	skipSelf := false
	switch relation {
	case 0:
		fmt.Fprintf(out, "Running binary is already v%s; ensuring all discovered installs match...\n", latest)
	case -1:
		fmt.Fprintf(out, "Update available: v%s → v%s\n", current, latest)
	case 1:
		fmt.Fprintf(out, "Current build is newer than the latest published release.\n")
		if targetPath == "" {
			if selfOnly {
				fmt.Fprintf(out, "Refusing to downgrade the currently running binary.\n")
				return nil
			}
			skipSelf = true
			fmt.Fprintf(out, "Refusing to downgrade the running binary; refreshing other discovered installs to v%s...\n", latest)
		} else {
			fmt.Fprintf(out, "Refusing to downgrade the currently running binary, but will update requested path(s) to the latest published release.\n")
		}
	}

	if buildinfo.UpdateManagedBy != "" {
		fmt.Fprintf(out, "Refusing in-place update. This installation is managed by '%s'. Please use your package manager to upgrade.\n", buildinfo.UpdateManagedBy)
		return nil
	}

	return installRelease(cmd, rel, latest, selfOnly, targetPath, skipSelf)
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

// findAxisBinaries discovers unique axis install paths to update.
//
//   - --path: only that path
//   - --self: only the currently running binary
//   - default: running binary + every "axis" on $PATH + common install locations
//     that exist (so dual installs like ~/go/bin vs /usr/local/bin both update)
func findAxisBinaries(explicitPath string, selfOnly bool) []string {
	seen := make(map[string]bool)
	var paths []string

	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			resolved = p
		}
		abs, err := filepath.Abs(resolved)
		if err != nil {
			abs = resolved
		}
		if !seen[abs] {
			seen[abs] = true
			paths = append(paths, abs)
		}
	}

	if explicitPath != "" {
		add(explicitPath)
		return paths
	}

	// The binary that was invoked.
	if self, err := os.Executable(); err == nil {
		add(self)
	}
	if selfOnly {
		return paths
	}

	// All "axis" binaries found in PATH directories.
	name := axisBinaryName()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			add(candidate)
		}
	}

	// Common install locations even if not currently on PATH (or ordered after a
	// fresher copy that won PATH lookup).
	for _, candidate := range commonAxisInstallCandidates() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			add(candidate)
		}
	}

	return paths
}

// resolveSelfPath returns the absolute, symlink-resolved path of the running binary.
func resolveSelfPath() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		resolved = self
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return resolved
	}
	return abs
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

func installRelease(cmd *cobra.Command, rel *ghRelease, version string, selfOnly bool, explicitPath string, skipSelf bool) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// Discover targets before downloading so tip-of-main / empty-target cases
	// do not hit the network unnecessarily.
	targets := findAxisBinaries(explicitPath, selfOnly)
	if skipSelf && explicitPath == "" {
		self := resolveSelfPath()
		filtered := targets[:0]
		for _, t := range targets {
			if self != "" && t == self {
				fmt.Fprintf(out, "Skipped (running build newer than release): %s\n", t)
				continue
			}
			filtered = append(filtered, t)
		}
		targets = filtered
	}
	if len(targets) == 0 {
		if skipSelf {
			fmt.Fprintf(out, "No other installs to refresh.\n")
			return nil
		}
		return fmt.Errorf("could not determine axis binary location to update")
	}

	binary, err := downloadReleaseBinary(cmd, rel, version)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Updating %d install(s)...\n", len(targets))
	updated := 0
	failed := 0
	for _, target := range targets {
		if err := replaceExecutable(target, binary); err != nil {
			failed++
			fmt.Fprintf(errOut, "warning: could not update %s: %v\n", target, err)
			continue
		}
		updated++
		fmt.Fprintf(out, "Updated: %s → v%s\n", target, version)
	}
	if updated == 0 {
		return fmt.Errorf("failed to update any axis binary (%d target(s), %d error(s))", len(targets), failed)
	}
	if failed > 0 {
		fmt.Fprintf(errOut, "warning: updated %d install(s); %d failed (often permissions on /usr/local/bin — re-run with sudo for system paths)\n", updated, failed)
	}
	return nil
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
