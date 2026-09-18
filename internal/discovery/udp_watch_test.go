package discovery

import (
	"context"
	"net"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestWatchBeaconChangesReportsListenFailure(t *testing.T) {
	holder, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	port := holder.LocalAddr().(*net.UDPAddr).Port

	err = WatchBeaconChanges(context.Background(), &config.Config{
		Discovery: &config.DiscoveryConfig{Enabled: true, UDPPort: port},
	}, NewBeaconRegistry(), nil)
	if err == nil {
		t.Fatal("expected listen error when the beacon port is already bound")
	}
}

func TestWatchBeaconChangesDisabledIsNotAnError(t *testing.T) {
	err := WatchBeaconChanges(context.Background(), &config.Config{}, NewBeaconRegistry(), nil)
	if err != nil {
		t.Fatalf("disabled discovery: %v", err)
	}
}
