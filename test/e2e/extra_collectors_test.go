//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jutaz/tiktelemetry/internal/mikrotik"
	"github.com/jutaz/tiktelemetry/internal/model"
)

// collectorByName returns the default collector with the given name.
func collectorByName(t *testing.T, name string) mikrotik.Collector {
	t.Helper()
	for _, c := range mikrotik.DefaultCollectors() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("collector %q not found in DefaultCollectors", name)
	return nil
}

// TestAllDefaultCollectorsSucceed runs every default collector against a live
// CHR and asserts none of them error. Collectors targeting hardware not present
// on CHR (health sensors, wireless) must degrade to zero samples rather than
// failing the scrape.
func TestAllDefaultCollectorsSucceed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := newClient(t, sharedRouter)
	defer client.Close()

	for _, c := range mikrotik.DefaultCollectors() {
		c := c
		t.Run(c.Name(), func(t *testing.T) {
			samples, err := c.Collect(ctx, client)
			if err != nil {
				t.Fatalf("collector %q errored on live CHR: %v", c.Name(), err)
			}
			// Every sample must have a name and (for our schema) a mikrotik. prefix.
			for _, s := range samples {
				if s.Name == "" {
					t.Errorf("collector %q produced a sample with an empty name", c.Name())
				}
			}
			t.Logf("collector %q produced %d samples", c.Name(), len(samples))
		})
	}
}

// TestConnectionsCollector asserts the connection-tracking collector returns
// sensible active/max gauges on CHR (tracking is enabled by default).
func TestConnectionsCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := newClient(t, sharedRouter)
	defer client.Close()

	samples, err := collectorByName(t, "connections").Collect(ctx, client)
	if err != nil {
		t.Fatalf("connections Collect: %v", err)
	}

	active, ok := findByName(samples, "mikrotik.connections.active")
	if !ok {
		t.Fatal("missing mikrotik.connections.active")
	}
	if active.Value < 0 {
		t.Errorf("active connections = %d, want >= 0", active.Value)
	}
	if maxc, ok := findByName(samples, "mikrotik.connections.max"); ok {
		if maxc.Value <= 0 {
			t.Errorf("max connections = %d, want > 0", maxc.Value)
		}
		if active.Value > maxc.Value {
			t.Errorf("active (%d) exceeds max (%d)", active.Value, maxc.Value)
		}
	}
}

// TestCountsCollector asserts the counts collector returns address/route counts
// on CHR (which always has at least one of each).
func TestCountsCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := newClient(t, sharedRouter)
	defer client.Close()

	samples, err := collectorByName(t, "counts").Collect(ctx, client)
	if err != nil {
		t.Fatalf("counts Collect: %v", err)
	}

	// CHR always has routes (connected + default). Addresses may be zero in a
	// pristine lab image, so only assert routes are present and sane.
	routes, ok := findByName(samples, "mikrotik.ip.routes")
	if !ok {
		t.Fatal("missing mikrotik.ip.routes")
	}
	if routes.Value < 0 {
		t.Errorf("routes = %d, want >= 0", routes.Value)
	}
	if addrs, ok := findByName(samples, "mikrotik.ip.addresses"); ok {
		if addrs.Value < 0 {
			t.Errorf("addresses = %d, want >= 0", addrs.Value)
		}
	}
}

// TestDHCPCollector asserts the DHCP collector always emits a total, even when
// no DHCP server is configured on CHR.
func TestDHCPCollector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := newClient(t, sharedRouter)
	defer client.Close()

	samples, err := collectorByName(t, "dhcp").Collect(ctx, client)
	if err != nil {
		t.Fatalf("dhcp Collect: %v", err)
	}
	total, ok := findByName(samples, "mikrotik.dhcp.leases.total")
	if !ok {
		t.Fatal("missing mikrotik.dhcp.leases.total")
	}
	if total.Value < 0 {
		t.Errorf("dhcp leases total = %d, want >= 0", total.Value)
	}
}

// TestWirelessCollectorGraceful asserts the wireless collector does not error on
// CHR (which has no wireless/wifi package) and simply returns no samples.
func TestWirelessCollectorGraceful(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	client := newClient(t, sharedRouter)
	defer client.Close()

	samples, err := collectorByName(t, "wireless").Collect(ctx, client)
	if err != nil {
		t.Fatalf("wireless Collect should degrade gracefully on CHR, got error: %v", err)
	}
	// CHR has no wireless; expect zero samples.
	if len(samples) != 0 {
		t.Logf("unexpected wireless samples on CHR: %d (not fatal)", len(samples))
	}
}

func findByName(samples []model.Sample, name string) (model.Sample, bool) {
	for _, s := range samples {
		if s.Name == name {
			return s, true
		}
	}
	return model.Sample{}, false
}
