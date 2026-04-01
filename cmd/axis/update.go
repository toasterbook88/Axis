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
// Extended in tests to include 127.0.0.1.
var allowedUpdateHosts = []string{
	"api.github.com",
	"github.com",
	"objects.githubusercontent.com",
	"releases.githubusercontent.com",
}

func updateCmd() *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the axis binary to the latest release",
		Long: "Check GitHub Releases for a newer version of axis and install it in-place.\n\n" +
			"Use --check to report whether an update is available without downloading.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd, checkOnly)
		},
	}
	cmd.Flags().BoolVarP(&checkOnly, "check", "c", false, "report whether an update is available without installing")
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

func runUpdate(cmd *cobra.Command, checkOnly bool) error {
	current := buildinfo.Version
	fmt.Fprintf(cmd.OutOrStdout(), "Current version: v%s\n", current)
	fmt.Fprintf(cmd.OutOrStdout(), "Checking for updates...\n")

	rel, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("checking latest release: %w", err)
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	fmt.Fprintf(cmd.OutOrStdout(), "Latest version:  %s\n", rel.TagName)

	if latest == current {
		fmt.Fprintf(cmd.OutOrStdout(), "Already up to date.\n")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Update available: v%s → %s\n", current, rel.TagName)

	if checkOnly {
		fmt.Fprintf(cmd.OutOrStdout(), "Run `axis update` (without --check) to install.\n")
		return nil
	}

	return installRelease(cmd, rel, latest)
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

func installRelease(cmd *cobra.Command, rel *ghRelease, version string) error {
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
		return fmt.Errorf("no release asset found for %s/%s (expected %s)", goos, goarch, archiveName)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Downloading %s...\n", archiveName)
	archiveData, err := downloadBytes(archiveURL)
	if err != nil {
		return fmt.Errorf("downloading release: %w", err)
	}

	if checksumURL != "" {
		if err := verifyChecksum(archiveData, archiveName, checksumURL); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Checksum verified.\n")
	}

	binary, err := extractBinary(archiveData, "axis")
	if err != nil {
		return fmt.Errorf("extracting binary: %w", err)
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("finding current executable: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolving executable path: %w", err)
	}

	if err := replaceExecutable(execPath, binary); err != nil {
		return fmt.Errorf("replacing binary at %s: %w", execPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "axis updated to %s → %s\n", rel.TagName, execPath)
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
