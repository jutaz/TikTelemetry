//go:build e2e

// Package e2e contains end-to-end tests that exercise TikTelemetry against a
// real MikroTik RouterOS (Cloud Hosted Router) instance running under QEMU in a
// container. These tests are gated behind the `e2e` build tag so the normal
// `go test ./...` run never touches Docker.
//
// Run them with:
//
//	go test -tags e2e -count=1 ./test/e2e/...
//
// By default the harness starts a docker-routeros container via
// testcontainers-go and tears it down afterwards. To run against an already
// running RouterOS (e.g. a docker-compose stack or a real device), set
// ROUTEROS_ADDR (host:port) and the harness will use it directly and skip
// container management:
//
//	ROUTEROS_ADDR=127.0.0.1:8728 go test -tags e2e -count=1 ./test/e2e/...
package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// routerImage is the community docker-routeros image that boots a real CHR
	// under QEMU and forwards the binary API port. Pinned for reproducibility.
	routerImage = "evilfreelancer/docker-routeros:7"

	routerUser = "admin"
	routerPass = "" // CHR default: admin with a blank password.

	// apiReadyTimeout bounds how long we wait for the RouterOS API to accept a
	// login after the container's TCP port opens. CHR boots in ~30-60s under
	// TCG emulation, faster with KVM.
	apiReadyTimeout = 4 * time.Minute
)

// router describes a reachable RouterOS API endpoint for the tests.
type router struct {
	addr string
	user string
	pass string
}

// startRouter returns a reachable RouterOS endpoint. If ROUTEROS_ADDR is set it
// is used as-is (no container is started). Otherwise a docker-routeros
// container is started and registered for cleanup. In both cases the function
// blocks until the API accepts a login.
func startRouter(ctx context.Context, t *testing.T) router {
	t.Helper()

	if addr := os.Getenv("ROUTEROS_ADDR"); addr != "" {
		t.Logf("using external RouterOS at %s (ROUTEROS_ADDR set)", addr)
		r := router{addr: addr, user: routerUser, pass: routerPass}
		if u := os.Getenv("ROUTEROS_USER"); u != "" {
			r.user = u
		}
		if p, ok := os.LookupEnv("ROUTEROS_PASS"); ok {
			r.pass = p
		}
		waitForAPI(ctx, t, r)
		return r
	}

	t.Logf("starting RouterOS container from %s (this can take a minute)", routerImage)
	req := testcontainers.ContainerRequest{
		Image:        routerImage,
		ExposedPorts: []string{"8728/tcp"},
		// The QEMU wrapper needs a tun device and NET_ADMIN to set up its
		// internal networking; KVM is used automatically when /dev/kvm exists.
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.CapAdd = append(hc.CapAdd, "NET_ADMIN")
			hc.Devices = append(hc.Devices, container.DeviceMapping{
				PathOnHost:        "/dev/net/tun",
				PathInContainer:   "/dev/net/tun",
				CgroupPermissions: "rwm",
			})
		},
		// TCP port open only means QEMU forwards it; real readiness is the API
		// login probe below. Still, wait for the port to reduce churn.
		WaitingFor: wait.ForListeningPort("8728/tcp").WithStartupTimeout(apiReadyTimeout),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start RouterOS container: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ctr.Terminate(cleanupCtx); err != nil {
			t.Logf("terminate RouterOS container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "8728/tcp")
	if err != nil {
		t.Fatalf("container mapped port: %v", err)
	}

	r := router{addr: host + ":" + port.Port(), user: routerUser, pass: routerPass}
	waitForAPI(ctx, t, r)
	return r
}

// waitForAPI blocks until a RouterOS API login succeeds or the deadline passes.
// A successful TCP connection is not sufficient — the VM must finish booting and
// the API service must accept authentication.
func waitForAPI(ctx context.Context, t *testing.T, r router) {
	t.Helper()

	deadline := time.Now().Add(apiReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while waiting for RouterOS API: %v", ctx.Err())
		default:
		}

		client, err := routeros.DialTimeout(r.addr, r.user, r.pass, 5*time.Second)
		if err == nil {
			client.Close()
			t.Logf("RouterOS API ready at %s", r.addr)
			return
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("RouterOS API not ready at %s after %s: %v", r.addr, apiReadyTimeout, lastErr)
}

// dial opens a fresh API client for a test, failing the test on error.
func (r router) dial(t *testing.T) *routeros.Client {
	t.Helper()
	client, err := routeros.DialTimeout(r.addr, r.user, r.pass, 5*time.Second)
	if err != nil {
		t.Fatalf("dial RouterOS %s: %v", r.addr, err)
	}
	return client
}
