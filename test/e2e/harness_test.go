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
// A single RouterOS container is started once for the whole package (via
// TestMain) and shared by all tests, so the slow CHR boot is paid only once.
// The container is started with testcontainers-go and torn down afterwards.
//
// To run against an already running RouterOS (e.g. a docker-compose stack or a
// real device), set ROUTEROS_ADDR (host:port) and no container is started:
//
//	ROUTEROS_ADDR=127.0.0.1:8728 go test -tags e2e -count=1 ./test/e2e/...
package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// routerImage is the community docker-routeros image that boots a real CHR
	// under QEMU and forwards the binary API port. Pinned for reproducibility.
	routerImage = "evilfreelancer/docker-routeros:7"

	routerUser = "admin"
	routerPass = "" // CHR default: admin with a blank password.

	// apiReadyTimeout bounds how long we wait for the RouterOS API to accept a
	// login after the container starts. CHR boots in ~30-60s under TCG
	// emulation, faster with KVM.
	apiReadyTimeout = 5 * time.Minute
)

// router describes a reachable RouterOS API endpoint for the tests.
type router struct {
	addr string
	user string
	pass string
}

// sharedRouter is populated by TestMain and used by every test in the package.
var sharedRouter router

// TestMain provisions a single RouterOS endpoint (external or containerised),
// waits for the API to be ready, runs the suite, and cleans up.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), apiReadyTimeout+2*time.Minute)
	defer cancel()

	r, cleanup, err := provisionRouter(ctx)
	if err != nil {
		log.Printf("e2e setup failed: %v", err)
		if cleanup != nil {
			cleanup()
		}
		os.Exit(1)
	}
	sharedRouter = r

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// provisionRouter returns a ready RouterOS endpoint and a cleanup function.
func provisionRouter(ctx context.Context) (router, func(), error) {
	if addr := os.Getenv("ROUTEROS_ADDR"); addr != "" {
		log.Printf("using external RouterOS at %s (ROUTEROS_ADDR set)", addr)
		r := router{addr: addr, user: routerUser, pass: routerPass}
		if u := os.Getenv("ROUTEROS_USER"); u != "" {
			r.user = u
		}
		if p, ok := os.LookupEnv("ROUTEROS_PASS"); ok {
			r.pass = p
		}
		if err := waitForAPI(ctx, r); err != nil {
			return router{}, nil, err
		}
		return r, func() {}, nil
	}

	log.Printf("starting RouterOS container from %s (this can take a minute)", routerImage)

	// The image only enables QEMU host port forwarding (so the API is reachable
	// from the host) when a second interface (eth1) is present. Attach the
	// container to the default bridge (eth0 + host port mapping) AND a dedicated
	// network (eth1) to trigger that path.
	net, err := tcnetwork.New(ctx)
	if err != nil {
		return router{}, nil, fmt.Errorf("create routeros network: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        routerImage,
		ExposedPorts: []string{"8728/tcp"},
		Networks:     []string{"bridge", net.Name},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.CapAdd = append(hc.CapAdd, "NET_ADMIN")
			hc.Devices = append(hc.Devices, container.DeviceMapping{
				PathOnHost:        "/dev/net/tun",
				PathInContainer:   "/dev/net/tun",
				CgroupPermissions: "rwm",
			})
		},
		// A listening TCP port only means QEMU forwards it; real readiness is
		// the API login probe in waitForAPI.
		WaitingFor: wait.ForListeningPort("8728/tcp").WithStartupTimeout(apiReadyTimeout),
	}

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		_ = net.Remove(ctx)
		return router{}, nil, fmt.Errorf("start RouterOS container: %w", err)
	}

	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ctr.Terminate(cctx); err != nil {
			log.Printf("terminate RouterOS container: %v", err)
		}
		if err := net.Remove(cctx); err != nil {
			log.Printf("remove routeros network: %v", err)
		}
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		cleanup()
		return router{}, nil, fmt.Errorf("container host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "8728/tcp")
	if err != nil {
		cleanup()
		return router{}, nil, fmt.Errorf("container mapped port: %w", err)
	}

	r := router{addr: host + ":" + port.Port(), user: routerUser, pass: routerPass}
	if err := waitForAPI(ctx, r); err != nil {
		cleanup()
		return router{}, nil, err
	}
	return r, cleanup, nil
}

// waitForAPI blocks until a RouterOS API login succeeds or the deadline passes.
// A successful TCP connection is not sufficient — the VM must finish booting and
// the API service must accept authentication.
func waitForAPI(ctx context.Context, r router) error {
	deadline := time.Now().Add(apiReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for RouterOS API: %w", ctx.Err())
		default:
		}

		client, err := routeros.DialTimeout(r.addr, r.user, r.pass, 5*time.Second)
		if err == nil {
			client.Close()
			log.Printf("RouterOS API ready at %s", r.addr)
			return nil
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("RouterOS API not ready at %s after %s: %w", r.addr, apiReadyTimeout, lastErr)
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
