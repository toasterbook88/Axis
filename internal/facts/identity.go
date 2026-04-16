package facts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

var readLocalIdentityFile = os.ReadFile
var runLocalIdentityCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func detectLocalNodeIdentity(ctx context.Context, osName string) *models.NodeIdentity {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux":
		for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			data, err := readLocalIdentityFile(path)
			if err != nil {
				continue
			}
			if identity := models.NewNodeIdentity(string(data), "linux-machine-id"); identity != nil {
				return identity
			}
		}
	case "darwin":
		return detectLocalDarwinIdentity(ctx)
	}
	return nil
}

func detectLocalDarwinIdentity(ctx context.Context) *models.NodeIdentity {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	if out, err := runLocalIdentityCommand(probeCtx, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice"); err == nil {
		if identity := models.NewNodeIdentity(models.ParseDarwinPlatformUUID(out), "darwin-platform-uuid"); identity != nil {
			return identity
		}
	}
	return nil
}

func detectRemoteNodeIdentity(ctx context.Context, exec transport.Executor, osName string) *models.NodeIdentity {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux":
		out, err := exec.Run(ctx, "cat /etc/machine-id 2>/dev/null || cat /var/lib/dbus/machine-id 2>/dev/null")
		if err != nil {
			return nil
		}
		return models.NewNodeIdentity(out, "linux-machine-id")
	case "darwin":
		out, err := exec.Run(ctx, `ioreg -rd1 -c IOPlatformExpertDevice 2>/dev/null | awk -F '"' '/IOPlatformUUID/ {print $4; exit}'`)
		if err == nil {
			if identity := models.NewNodeIdentity(out, "darwin-platform-uuid"); identity != nil {
				return identity
			}
		}
	}
	return nil
}

func detectRemoteHostname(ctx context.Context, exec transport.Executor) (string, error) {
	if out, err := exec.Run(ctx, "hostname"); err == nil {
		if hostname := strings.TrimSpace(out); hostname != "" {
			return hostname, nil
		}
	}
	out, err := exec.Run(ctx, "uname -n")
	if err != nil {
		return "", err
	}
	hostname := strings.TrimSpace(out)
	if hostname == "" {
		return "", errors.New("remote hostname probe returned empty hostname")
	}
	return hostname, nil
}
