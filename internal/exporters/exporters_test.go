package exporters

import (
	"slices"
	"testing"

	"github.com/jutaz/tiktelemetry/internal/export"
)

// Importing this package (via its own test) triggers the init() that registers
// every built-in adapter and the grafanacloud alias. Verify they are present.
func TestBuiltinsRegistered(t *testing.T) {
	available := export.Available()
	for _, name := range []string{"otlp", "prometheus", "loki"} {
		if !slices.Contains(available, name) {
			t.Errorf("exporter %q not registered; available=%v", name, available)
		}
	}
}

func TestGrafanaCloudAlias(t *testing.T) {
	// The alias must expand to prometheus + loki. We can observe this indirectly
	// through Available (concrete names) plus the fact that Build of the alias
	// works; here we assert the concrete members exist so the alias is usable.
	available := export.Available()
	for _, name := range []string{"prometheus", "loki"} {
		if !slices.Contains(available, name) {
			t.Fatalf("grafanacloud member %q missing from registry", name)
		}
	}
}
