package facts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// HardwareCollector collects only local hardware. Inventory is retained across
// Sample calls, so watch mode does not repeatedly enumerate buses or firmware.
type HardwareCollector struct {
	osName, arch string
	read         func(string) ([]byte, error)
	glob         func(string) ([]string, error)
	run          func(context.Context, string, ...string) ([]byte, error)
	now          func() time.Time
	inventory    *models.HardwareReport
}

func NewHardwareCollector() *HardwareCollector {
	return &HardwareCollector{osName: runtime.GOOS, arch: runtime.GOARCH,
		read: os.ReadFile, glob: filepath.Glob, run: runHardwareProbe, now: time.Now}
}

// outputLimit prevents optional utilities from allocating unbounded buffers.
type hardwareOutput struct{ data []byte }

func (b *hardwareOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 4<<20 {
		return 0, errors.New("hardware probe output exceeds 4 MiB")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func runHardwareProbe(ctx context.Context, name string, args ...string) ([]byte, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.WaitDelay = 250 * time.Millisecond
	var stderr hardwareStderr
	cmd.Stderr = &stderr
	var out hardwareOutput
	cmd.Stdout = &out
	// Do not retain stderr: optional tools can print identifiers or host details.
	err := cmd.Run()
	if probeCtx.Err() != nil {
		return nil, probeCtx.Err()
	}
	if err != nil {
		message := strings.ToLower(string(stderr.data))
		if hardwarePermissionDenied(message) {
			return out.data, fs.ErrPermission
		}
	}
	return out.data, err
}

func hardwarePermissionDenied(message string) bool {
	text := strings.ToLower(message)
	return strings.Contains(text, "permission denied") || strings.Contains(text, "operation not permitted")
}

// Retain only a small private prefix for classification, never in the report.
type hardwareStderr struct{ data []byte }

func (b *hardwareStderr) Write(p []byte) (int, error) {
	if remaining := 4096 - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return len(p), nil
}

// hardwareIssue contains fixed diagnostic text authored by the collector; raw
// command errors and stderr are never promoted into operator output.
type hardwareIssue string

func (e hardwareIssue) Error() string { return string(e) }

func (c *HardwareCollector) reading(name string, value any, unit, source string) models.HardwareReading {
	switch value.(type) {
	case string, bool, int, int64, uint64, float64:
	default:
		return c.unavailable(name, source, hardwareIssue("unsupported hardware value type"))
	}
	if n, ok := value.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
		return c.unavailable(name, source, hardwareIssue("non-finite hardware value"))
	}
	return models.HardwareReading{Name: name, Value: value, Unit: unit, Source: source,
		Status: "observed", ObservedAt: c.now().UTC()}
}

func (c *HardwareCollector) unavailable(name, source string, err error) models.HardwareReading {
	status, reason := "unavailable", "not exposed by this device or operating system"
	var issue hardwareIssue
	switch {
	case errors.Is(err, fs.ErrPermission):
		status, reason = "permission_denied", "read permission denied; sudo is never invoked"
	case errors.Is(err, exec.ErrNotFound):
		status, reason = "tool_missing", "optional utility is not installed"
	case errors.Is(err, context.DeadlineExceeded):
		status, reason = "timeout", "probe deadline exceeded"
	case errors.Is(err, context.Canceled):
		status, reason = "cancelled", "collection cancelled"
	case errors.As(err, &issue):
		reason = string(issue)
	case err != nil:
		reason = "probe failed or returned no usable reading"
	}
	return models.HardwareReading{Name: name, Source: source, Status: status, Reason: reason, ObservedAt: c.now().UTC()}
}

func hardwareDevice(id, name string) models.HardwareDevice {
	return models.HardwareDevice{ID: id, Name: name, Specs: []models.HardwareReading{}, Metrics: []models.HardwareReading{}}
}

func hardwareSection(r *models.HardwareReport, name string) *models.HardwareSection {
	for i := range r.Sections {
		if r.Sections[i].Name == name {
			return &r.Sections[i]
		}
	}
	panic("unknown hardware section: " + name)
}

func (c *HardwareCollector) inventoryReport(ctx context.Context) (*models.HardwareReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	r := &models.HardwareReport{SchemaVersion: "axis.hardware/v1", Hostname: hostname, OS: c.osName, Arch: c.arch}
	for _, name := range []string{"CPU", "Memory", "GPU", "Storage", "Network", "System", "Sensors", "Devices"} {
		r.Sections = append(r.Sections, models.HardwareSection{Name: name, Devices: []models.HardwareDevice{}})
	}
	switch c.osName {
	case "linux":
		c.linuxHardwareInventory(ctx, r)
	case "darwin":
		c.macHardwareInventory(ctx, r)
	default:
		return nil, fmt.Errorf("hardware facts are supported on macOS and Linux, not %s", c.osName)
	}
	for i := range r.Sections {
		s := &r.Sections[i]
		if len(s.Devices) == 0 {
			d := hardwareDevice(strings.ToLower(s.Name), "No accessible device readings")
			d.Specs = append(d.Specs, c.unavailable("inventory", c.osName, nil))
			s.Devices = append(s.Devices, d)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.InventoryAt = c.now().UTC()
	return r, nil
}

// Sample always measures fresh load. It never reads the daemon cache or AXIS
// configuration, and never discovers models, tools, or network endpoints.
func (c *HardwareCollector) Sample(ctx context.Context, interval time.Duration) (*models.HardwareReport, error) {
	if interval < 100*time.Millisecond || interval > time.Minute {
		return nil, errors.New("hardware sample interval must be between 100ms and 1m")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.inventory == nil {
		r, err := c.inventoryReport(ctx)
		if err != nil {
			return nil, err
		}
		c.inventory = r
	}
	r := cloneHardwareInventory(c.inventory)
	start := c.hardwareCounters(ctx)
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	end := c.hardwareCounters(ctx)
	c.applyHardwareCounters(r, start, end)
	switch c.osName {
	case "linux":
		c.linuxHardwareMetrics(ctx, r)
	case "darwin":
		c.macHardwareMetrics(ctx, r)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.CollectedAt = c.now().UTC()
	r.Status = "complete"
	for _, s := range r.Sections {
		for _, d := range s.Devices {
			for _, v := range append(append([]models.HardwareReading{}, d.Specs...), d.Metrics...) {
				if v.Status != "observed" && v.Status != "derived" {
					r.Status = "partial"
				}
			}
		}
	}
	return r, nil
}

func cloneHardwareInventory(src *models.HardwareReport) *models.HardwareReport {
	r := *src
	r.Sections = append([]models.HardwareSection{}, src.Sections...)
	for i := range r.Sections {
		r.Sections[i].Devices = append([]models.HardwareDevice{}, src.Sections[i].Devices...)
		for j := range r.Sections[i].Devices {
			d := &r.Sections[i].Devices[j]
			d.Specs = append([]models.HardwareReading{}, d.Specs...)
			d.Metrics = []models.HardwareReading{}
		}
	}
	return &r
}

func hardwareKeyValues(raw string, separator string) map[string]string {
	v := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		if k, x, ok := strings.Cut(line, separator); ok {
			v[strings.TrimSpace(k)] = strings.TrimSpace(x)
		}
	}
	return v
}

func hardwareNumber(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (c *HardwareCollector) fileReading(name, path, unit string, scale float64) models.HardwareReading {
	b, err := c.read(path)
	if err != nil {
		return c.unavailable(name, path, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return c.unavailable(name, path, nil)
	}
	if scale != 0 {
		n, ok := hardwareNumber(s)
		if !ok {
			return c.unavailable(name, path, errors.New("invalid number"))
		}
		if n < 0 && unit != "C" && unit != "V" && unit != "A" && unit != "W" {
			return c.unavailable(name, path, hardwareIssue("negative sentinel; no measurement available"))
		}
		return c.reading(name, n*scale, unit, path)
	}
	return c.reading(name, s, unit, path)
}
