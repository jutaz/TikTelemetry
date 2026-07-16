# =============================================================================
# TikTelemetry — RouterOS v7 container deployment script
# =============================================================================
#
# This script configures the networking and container needed to run
# TikTelemetry directly on a MikroTik router.
#
# It is deliberately written to be READ AND EDITED, not blindly imported.
# Every command is commented. Review it, change the placeholders marked
# "<CHANGE ...>", then apply the sections in order.
#
# PREREQUISITES (do these first — see the README "Running on RouterOS" guide):
#   1. Your board is arm, arm64, or x86 (check: /system/resource/print).
#   2. The `container` package is installed and the router rebooted.
#   3. Containers are enabled in device-mode (needs a physical power-cycle
#      or reset-button press — see the README).
#   4. You have external storage (USB/NVMe/SATA, ext4) with room to spare.
#      Adjust the "disk1" paths below to match your storage device name
#      (check: /disk/print  or  /file/print).
#   5. You have created the API user (run scripts/routeros-setup.rsc first).
#
# You can paste this into the terminal section by section, or edit and
# /import it. Nothing here is destructive on its own, but review before running.
# =============================================================================

# -----------------------------------------------------------------------------
# 0. Placeholders — edit these to match your setup
# -----------------------------------------------------------------------------
# Container subnet / addresses (safe defaults; change only if 172.17.0.0/24
# collides with an existing network on your router).
#   ROUTER side (gateway the container talks to): 172.17.0.1
#   CONTAINER side:                               172.17.0.2
#
# Storage device name: replace "disk1" throughout with your device
# (e.g. "usb1", "nvme1"). Find it with: /disk/print
#
# Image source: choose EITHER the offline tarball (recommended) OR a registry
# pull below. Do not do both.

# -----------------------------------------------------------------------------
# 1. Networking: a bridge + veth, so the container can reach the router API
#    and the internet.
# -----------------------------------------------------------------------------
# The container runs in its own network namespace. It reaches the router's API
# at the GATEWAY address (172.17.0.1), NOT 127.0.0.1.

# Create a bridge dedicated to containers.
/interface/bridge/add name=containers comment="TikTelemetry container bridge"

# Give the router an address on that bridge — this is the container's gateway
# and the address the agent connects to for the RouterOS API.
/ip/address/add address=172.17.0.1/24 interface=containers comment="TikTelemetry gateway"

# Create the veth (virtual ethernet) for the container.
/interface/veth/add name=veth-tik address=172.17.0.2/24 gateway=172.17.0.1 comment="TikTelemetry veth"

# Attach the veth to the bridge.
/interface/bridge/port/add bridge=containers interface=veth-tik

# Allow the container subnet to reach the internet (for the outbound HTTPS push).
# NOTE: place this rule appropriately relative to your existing NAT rules.
/ip/firewall/nat/add chain=srcnat action=masquerade src-address=172.17.0.0/24 comment="TikTelemetry outbound"

# DNS is REQUIRED — without a resolvable DNS server the container will not start.
# Either the router must have DNS servers configured (below), or you pass dns=
# on /container/add in step 4. Setting it on the router is simplest:
/ip/dns/set servers=1.1.1.1,8.8.8.8

# -----------------------------------------------------------------------------
# 2. Lock down the API to the container subnet only (recommended)
# -----------------------------------------------------------------------------
# The agent uses the plaintext API (port 8728). Restricting the service to the
# container subnet means no other host can even attempt to authenticate.
/ip/service/set api address=172.17.0.0/24

# -----------------------------------------------------------------------------
# 3. Container config + environment variables (your settings & secrets)
# -----------------------------------------------------------------------------
# tmpdir must be on external storage: it holds image layers during extraction.
/container/config/set tmpdir=disk1/tik-tmp

# Environment variables. These are TikTelemetry's configuration. See the README
# "Configuration" section for the full list. Secrets (ROUTER_PASS, tokens) are
# stored in PLAINTEXT in the RouterOS config export — treat the router config
# as sensitive.
#
# <CHANGE> the values below before running.
/container/envs/add list=tik key=ROUTER_ADDRESS value="172.17.0.1:8728"
/container/envs/add list=tik key=ROUTER_USER    value="tiktelemetry"
/container/envs/add list=tik key=ROUTER_PASS    value="<CHANGE ROUTER API PASSWORD>"

# Choose your exporter(s). Example: Grafana Cloud native (Prometheus + Loki).
/container/envs/add list=tik key=EXPORTERS value="grafanacloud"

# Prometheus (metrics) — Grafana Cloud: user is the Metrics instance ID,
# pass is an access-policy token with metrics:write.
/container/envs/add list=tik key=PROMETHEUS_ENDPOINT value="<CHANGE https://prometheus-prod-XX-REGION.grafana.net/api/prom/push>"
/container/envs/add list=tik key=PROMETHEUS_USER     value="<CHANGE metrics instance id>"
/container/envs/add list=tik key=PROMETHEUS_PASS     value="<CHANGE access policy token>"

# Loki (logs) — Grafana Cloud: user is the Logs instance ID,
# pass is an access-policy token with logs:write.
/container/envs/add list=tik key=LOKI_ENDPOINT value="<CHANGE https://logs-prod-XXX.grafana.net>"
/container/envs/add list=tik key=LOKI_USER     value="<CHANGE logs instance id>"
/container/envs/add list=tik key=LOKI_PASS     value="<CHANGE access policy token>"

# Keep memory bounded on constrained boards.
/container/envs/add list=tik key=GOMEMLIMIT value="24MiB"

# Optional: a stable name for this router in the telemetry.
# /container/envs/add list=tik key=INSTANCE_ID value="home-router"

# -----------------------------------------------------------------------------
# 4. Create the container
# -----------------------------------------------------------------------------
# Pick ONE of the two methods below.
#
# --- Method A (recommended): offline tarball ---------------------------------
# 1. On your workstation, build and save the image for YOUR router's arch:
#        make image-tar ARCH=arm64      # arm | armv5 | arm64 | amd64
#    Match the board (see the README arch table). Most `arm` boards use
#    ARCH=arm; the EN7562CT boards (hEX Refresh, hEX S 2025) MUST use
#    ARCH=armv5 or they crash with "Illegal instruction".
#    (or download the matching tiktelemetry-<version>-<arch>.tar.gz from the
#    GitHub Releases page and gunzip it — e.g. -armv5 for EN7562CT boards).
# 2. Upload the .tar to the router (WinBox Files drag-and-drop, or scp) into
#    your storage, e.g. disk1/tiktelemetry.tar.
# 3. Create the container from the file:
/container/add file=disk1/tiktelemetry.tar interface=veth-tik root-dir=disk1/tik-root envlist=tik logging=yes start-on-boot=yes comment="TikTelemetry"

# --- Method B (registry pull) ------------------------------------------------
# Requires internet on the router during pull. GHCR usually needs auth even for
# public images; set credentials first if so:
#   /container/config/set registry-url=https://ghcr.io username=<gh-user> password=<gh-PAT-with-read:packages>
# Then (use the :latest-armv5 tag instead on EN7562CT boards — hEX Refresh,
# hEX S 2025 — because RouterOS ignores the image's ARM variant and would
# otherwise pull the FPU-requiring default):
#   /container/add remote-image=ghcr.io/jutaz/tiktelemetry:latest interface=veth-tik root-dir=disk1/tik-root envlist=tik logging=yes start-on-boot=yes comment="TikTelemetry"

# -----------------------------------------------------------------------------
# 5. Start it and watch the logs
# -----------------------------------------------------------------------------
# Find the container number/name:
#   /container/print
#
# Start it (replace 0 with the number from /container/print):
#   /container/start 0
#
# TikTelemetry logs go to the RouterOS log under the "container" topic
# (because logging=yes). View them with:
#   /log/print where topics~"container"
#
# On a healthy start you'll see the agent's JSON log lines, e.g.
#   {"level":"INFO","msg":"starting tiktelemetry",...}
#   {"level":"INFO","msg":"agent started",...}
#
# The image is `scratch` (no shell), so /container/shell will NOT work — the
# RouterOS log is the way to see output.

# -----------------------------------------------------------------------------
# 6. Troubleshooting
# -----------------------------------------------------------------------------
# If the agent logs connection or push errors, the built-in preflight check
# tells you exactly what is wrong. Run it as a one-shot container command:
#   /container/add file=disk1/tiktelemetry.tar interface=veth-tik \
#       root-dir=disk1/tik-check envlist=tik cmd="/tiktelemetry --check" \
#       logging=yes comment="TikTelemetry preflight"
#   /container/start <number>
#   /log/print where topics~"container"
# It reports the router board/version/arch it reached and whether each exporter
# endpoint is reachable (DNS + TCP + TLS), then exits. Remove it when done:
#   /container/remove <number>
#
# Common issues:
#   - Container won't start        -> device-mode not enabled, or arch mismatch
#                                     (check /system/resource/print architecture-name;
#                                      arm->arm image, arm64->arm64, x86->amd64).
#   - Starts then "signal 4 (Illegal instruction)" on an `arm` board
#                                  -> EN7562CT board (hEX Refresh, hEX S 2025):
#                                     it is ARMv5/no-FPU. Use the -armv5 image
#                                     (ARCH=armv5 tarball or :latest-armv5 tag).
#                                     Every other arm board uses the default arm
#                                     image. Check /system/resource/print `cpu`.
#   - "won't start", no clear log  -> DNS not set (see step 1).
#   - Push fails, router API OK     -> no route/NAT to internet, or wrong token.
#   - API connect fails            -> /ip/service api address allow-list, or the
#                                     API user/password is wrong.
# =============================================================================
