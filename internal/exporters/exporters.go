// Package exporters is the composition root for the concrete export adapters.
// Importing it (blank import from main) registers every built-in sink with the
// export registry via each adapter's init(), and defines convenience aliases
// that bundle several sinks under one name.
//
// To add a new backend: implement a Sink in internal/export/<name>/, register
// it in that package's init(), then add a blank import here. No other file
// needs to change.
package exporters

import (
	"github.com/jutaz/tiktelemetry/internal/export"

	// Blank imports register each adapter's factory with the export registry.
	_ "github.com/jutaz/tiktelemetry/internal/export/loki"
	_ "github.com/jutaz/tiktelemetry/internal/export/otlp"
	_ "github.com/jutaz/tiktelemetry/internal/export/prometheus"
)

func init() {
	// "grafanacloud" is a convenience bundle that pushes metrics via Prometheus
	// remote_write and logs via the Loki push API — the most efficient native
	// path into Grafana Cloud (no OTLP translation on the ingest side).
	export.RegisterAlias("grafanacloud", []string{"prometheus", "loki"})
}
