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
	"io"
	"log"
	"os"
	"strings"
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

	// apiReadyTimeout bounds how long we wait for the RouterOS binary API to
	// accept a login for a single container attempt. The API service (port
	// 8728) comes up LATE in the boot, well after the login prompt, and on slow
	// GitHub-runner CPU SKUs (e.g. Intel Emerald Rapids, which has a known
	// nested-KVM slowdown) that can be 60-180s even with hardware
	// acceleration. Budget generously so a slow-but-healthy boot is not killed
	// prematurely — that was the main cause of e2e flakiness.
	apiReadyTimeout = 6 * time.Minute

	// bootAttempts recreates the container only as a safety net for a genuinely
	// crashed QEMU (container exited), NOT to paper over slow boots — the long
	// apiReadyTimeout above already absorbs slow SKUs. Kept small so we do not
	// throw away a container that was seconds from becoming ready.
	bootAttempts = 2
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
	// Budget for all boot attempts plus overhead (image pull, teardown).
	ctx, cancel := context.WithTimeout(context.Background(), bootAttempts*apiReadyTimeout+3*time.Minute)
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

	// The image's entrypoint only enables QEMU host-port forwarding (SLIRP
	// `-nic user,hostfwd=...`, which is what makes the API reachable through
	// Docker's published port) when a *second* interface `eth1` exists inside
	// the container at boot. See /routeros_source/entrypoint.sh: it runs
	// `ip link show eth1` once and sets USE_HOSTFWD=1 only if eth1 is present.
	//
	// We therefore attach the container to TWO dedicated networks. The first
	// becomes eth0 (and carries Docker's published port), the second becomes
	// eth1 (and triggers the hostfwd path). We deliberately do NOT use the
	// default `bridge` network by name: on some Docker daemons (notably GitHub
	// runners) the create-time vs connect-time interface ordering of
	// `["bridge", custom]` is non-deterministic, so eth1 sometimes does not
	// exist when the entrypoint probes for it — the API port then never
	// forwards and the login handshake is dropped (EOF / reset after TCP
	// connect). Two explicit custom networks make eth1's presence
	// deterministic across daemons.
	netA, err := tcnetwork.New(ctx)
	if err != nil {
		return router{}, nil, fmt.Errorf("create routeros network A: %w", err)
	}
	netB, err := tcnetwork.New(ctx)
	if err != nil {
		_ = netA.Remove(ctx)
		return router{}, nil, fmt.Errorf("create routeros network B: %w", err)
	}
	removeNets := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := netA.Remove(cctx); err != nil {
			log.Printf("remove routeros network A: %v", err)
		}
		if err := netB.Remove(cctx); err != nil {
			log.Printf("remove routeros network B: %v", err)
		}
	}

	// A CHR boot occasionally stalls even with KVM. Recreating the container is
	// far more reliable than waiting longer, so try a fresh QEMU up to
	// bootAttempts times before giving up.
	var lastErr error
	for attempt := 1; attempt <= bootAttempts; attempt++ {
		if ctx.Err() != nil {
			removeNets()
			return router{}, nil, fmt.Errorf("context cancelled before RouterOS was ready: %w", ctx.Err())
		}
		log.Printf("RouterOS container boot attempt %d/%d", attempt, bootAttempts)

		r, stop, err := bootRouterContainer(ctx, netA.Name, netB.Name)
		if err == nil {
			cleanup := func() {
				stop()
				removeNets()
			}
			return r, cleanup, nil
		}
		lastErr = err
		log.Printf("boot attempt %d/%d failed: %v", attempt, bootAttempts, err)
	}

	removeNets()
	return router{}, nil, fmt.Errorf("RouterOS never became ready after %d attempts: %w", bootAttempts, lastErr)
}

// bootRouterContainer starts one CHR container on the given networks and waits
// for its API to accept a login. On any failure it dumps diagnostics, tears the
// container down, and returns an error so the caller can retry with a fresh one.
// The returned stop function terminates the container (networks are the
// caller's responsibility so they can be reused across attempts).
func bootRouterContainer(ctx context.Context, netAName, netBName string) (router, func(), error) {
	req := testcontainers.ContainerRequest{
		Image:        routerImage,
		ExposedPorts: []string{"8728/tcp"},
		Networks:     []string{netAName, netBName},
		// NOTE on vCPUs: the image hardcodes `-smp 4`. It is tempting to lower
		// this to match GitHub's 2-vCPU runners, but this image boots the guest
		// off an emulated IDE disk (not virtio), which is slow enough that
		// FEWER vCPUs makes boot *slower*, not faster (verified: `-smp 1` times
		// out even on a fast host). The default `-smp 4` boots fastest here, so
		// we leave it and instead absorb slow runner CPU SKUs with a generous
		// apiReadyTimeout.
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.CapAdd = append(hc.CapAdd, "NET_ADMIN")
			hc.Devices = append(hc.Devices, container.DeviceMapping{
				PathOnHost:        "/dev/net/tun",
				PathInContainer:   "/dev/net/tun",
				CgroupPermissions: "rwm",
			})
			// Pass /dev/kvm through when the host exposes it so QEMU runs with
			// hardware acceleration (-enable-kvm) instead of falling back to TCG
			// software emulation. Without this the CHR boot takes 5+ minutes and
			// blows the API-ready budget; with it the guest is up in ~30-60s.
			// The host's /dev/kvm existing does NOT help unless the device is
			// actually mapped into the container. Guarded so hosts without KVM
			// (e.g. some laptops) still run, just slowly.
			if _, err := os.Stat("/dev/kvm"); err == nil {
				hc.Devices = append(hc.Devices, container.DeviceMapping{
					PathOnHost:        "/dev/kvm",
					PathInContainer:   "/dev/kvm",
					CgroupPermissions: "rwm",
				})
			}
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
		return router{}, nil, fmt.Errorf("start RouterOS container: %w", err)
	}

	stop := func() {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ctr.Terminate(cctx); err != nil {
			log.Printf("terminate RouterOS container: %v", err)
		}
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		stop()
		return router{}, nil, fmt.Errorf("container host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "8728/tcp")
	if err != nil {
		stop()
		return router{}, nil, fmt.Errorf("container mapped port: %w", err)
	}

	r := router{addr: host + ":" + port.Port(), user: routerUser, pass: routerPass}
	if err := waitForAPI(ctx, r); err != nil {
		dumpContainerDiagnostics(ctx, ctr)
		stop()
		return router{}, nil, err
	}
	return r, stop, nil
}

// dumpContainerDiagnostics logs the container's network interfaces and recent
// stdout when the API never became ready. This distinguishes the two failure
// modes we care about: a missing `eth1` (so the entrypoint never enabled
// hostfwd — look for "KVM not available"/no hostfwd) versus a genuinely slow
// CHR boot. Best-effort: any error here is itself logged and ignored.
func dumpContainerDiagnostics(ctx context.Context, ctr testcontainers.Container) {
	if code, reader, err := ctr.Exec(ctx, []string{"ip", "-o", "link", "show"}); err == nil {
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, reader)
		log.Printf("e2e diag: container interfaces (exit %d):\n%s", code, buf.String())
	} else {
		log.Printf("e2e diag: could not list container interfaces: %v", err)
	}

	if reader, err := ctr.Logs(ctx); err == nil {
		defer func() { _ = reader.Close() }()
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, reader)
		log.Printf("e2e diag: container logs:\n%s", buf.String())
	} else {
		log.Printf("e2e diag: could not read container logs: %v", err)
	}
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
