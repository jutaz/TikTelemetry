package otlp

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/jutaz/tiktelemetry/internal/config"
)

// newResource builds an OTel resource from the agent's config and merges it
// with the SDK default resource (which carries telemetry.sdk.* attributes).
func newResource(ctx context.Context, cfg config.Config) (*resource.Resource, error) {
	// The resource identifies the agent (hub). Per-router identity travels as
	// the "target" attribute on each metric/log, added by the collection layer.
	custom, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create custom resource: %w", err)
	}

	merged, err := resource.Merge(resource.Default(), custom)
	if err != nil {
		return nil, fmt.Errorf("merge resource: %w", err)
	}
	return merged, nil
}
