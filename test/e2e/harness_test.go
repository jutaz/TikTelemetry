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
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// routerImage is the community docker-routeros image that boots a real CHR
	// under QEMU and forwards the binary API port. Pinned for reproducibility.
	routerImage = "evilfreelancer/docker-routeros:7"

	routerUser = "admin"
	routerPass = "" // CHR default: admin with a blank password.

	// apiReadyTimeout bounds how long we wait for the RouterOS binary API to
	// accept a login for a single container attempt. With the virtio boot (see
	// qemuBootScript) the guest reaches an API login in ~30-45s even on slow
	// KVM SKUs; without KVM (TCG) it can take a few minutes. Budget generously
	// so a slow-but-healthy boot is never killed prematurely.
	apiReadyTimeout = 5 * time.Minute

	// bootAttempts recreates the container only as a safety net for a genuinely
	// crashed QEMU (container exited), NOT to paper over slow boots — the
	// apiReadyTimeout above already absorbs those. Kept small so we do not throw
	// away a container that was seconds from becoming ready.
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

	// A CHR boot occasionally crashes QEMU outright (container exits). Recreate
	// the container up to bootAttempts times before giving up. Slow-but-healthy
	// boots are handled by the generous apiReadyTimeout, not by retrying.
	var lastErr error
	for attempt := 1; attempt <= bootAttempts; attempt++ {
		if ctx.Err() != nil {
			return router{}, nil, fmt.Errorf("context cancelled before RouterOS was ready: %w", ctx.Err())
		}
		log.Printf("RouterOS container boot attempt %d/%d", attempt, bootAttempts)

		r, stop, err := bootRouterContainer(ctx)
		if err == nil {
			return r, stop, nil
		}
		lastErr = err
		log.Printf("boot attempt %d/%d failed: %v", attempt, bootAttempts, err)
	}

	return router{}, nil, fmt.Errorf("RouterOS never became ready after %d attempts: %w", bootAttempts, lastErr)
}

// qemuBootScript is the container entrypoint we inject to boot the CHR. It
// deliberately bypasses the image's own entrypoint, which boots the guest off
// an emulated IDE disk (`-hda`) behind a tap/bridge and only enables host-port
// forwarding when a second `eth1` interface happens to exist. That setup is
// both fragile (eth1 ordering) and slow: on GitHub-runner CPU SKUs with slow
// nested KVM (e.g. Intel Emerald Rapids) the IDE I/O emulation pushes
// boot-to-API past several minutes and the suite flakes.
//
// Instead we run QEMU directly with a paravirtualised virtio disk and
// virtio-net NIC over plain SLIRP user networking (hostfwd for the API port).
// Virtio replaces per-register IDE traps with a batched shared-memory ring, so
// boot is 2-4x faster on the same slow hosts, and pure SLIRP removes the eth1
// dependency entirely (Docker just publishes 8728). KVM is used when /dev/kvm
// is writable, otherwise QEMU falls back to TCG so KVM-less hosts still run.
//
// The VDI is read directly by QEMU (format=vdi); no qemu-img conversion is
// needed. The disk lives at /routeros_source/<image>.vdi in the image.
const qemuBootScript = `set -e
img="/routeros_source/${ROUTEROS_IMAGE}"
accel=""
cpu="qemu64"
if [ -w /dev/kvm ]; then
  accel="-enable-kvm"
  cpu="host,kvm=on"
  echo "tiktelemetry-e2e: KVM available, enabling hardware acceleration"
else
  echo "tiktelemetry-e2e: KVM unavailable, using TCG (slow)"
fi
exec qemu-system-x86_64 \
  -display none -serial mon:stdio \
  ${accel} -cpu "${cpu}" -m 512 -smp 2 \
  -drive file="${img}",format=vdi,if=virtio \
  -netdev user,id=net0,hostfwd=tcp::8728-:8728 \
  -device virtio-net-pci,netdev=net0
`

// bootRouterContainer starts one CHR container and waits for its API to accept
// a login. On any failure it dumps diagnostics, tears the container down, and
// returns an error so the caller can retry with a fresh one.
func bootRouterContainer(ctx context.Context) (router, func(), error) {
	req := testcontainers.ContainerRequest{
		Image:        routerImage,
		ExposedPorts: []string{"8728/tcp"},
		// Override the image entrypoint to boot QEMU with virtio directly (see
		// qemuBootScript). Pure SLIRP means no second network / eth1 dance.
		Entrypoint: []string{"sh", "-c", qemuBootScript},
		HostConfigModifier: func(hc *container.HostConfig) {
			// Pass /dev/kvm through when the host exposes it so QEMU accelerates
			// with -enable-kvm instead of falling back to slow TCG emulation.
			// The host having /dev/kvm does NOT help unless it is mapped into the
			// container. Guarded so KVM-less hosts (e.g. macOS/Windows Docker
			// Desktop) still run, just slower.
			if _, err := os.Stat("/dev/kvm"); err == nil {
				hc.Devices = append(hc.Devices, container.DeviceMapping{
					PathOnHost:        "/dev/kvm",
					PathInContainer:   "/dev/kvm",
					CgroupPermissions: "rwm",
				})
			}
		},
		// A listening TCP port only means QEMU/SLIRP forwards it; real readiness
		// is the API login probe in waitForAPI.
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

// dumpContainerDiagnostics logs the container's recent stdout (the QEMU serial
// console) when the API never became ready, so a CI failure shows whether KVM
// engaged and how far the guest booted. Best-effort: any error here is itself
// logged and ignored.
func dumpContainerDiagnostics(ctx context.Context, ctr testcontainers.Container) {
	if reader, err := ctr.Logs(ctx); err == nil {
		defer func() { _ = reader.Close() }()
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, reader)
		log.Printf("e2e diag: QEMU serial console:\n%s", buf.String())
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
