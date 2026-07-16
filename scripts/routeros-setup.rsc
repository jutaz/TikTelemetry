# ═══════════════════════════════════════════════════════════════════════════════
# TikTelemetry — RouterOS setup script
# ═══════════════════════════════════════════════════════════════════════════════
# Creates a least-privilege API user for the telemetry agent.
#
# Run on your MikroTik router via:
#   /import routeros-setup.rsc
#
# Or paste line-by-line in the terminal / WinBox.
#
# To run TikTelemetry directly ON the router (RouterOS container feature),
# run scripts/container-setup.rsc afterwards — see the README
# "Running on RouterOS" section.
#
# ── Multi-router hub deployments ───────────────────────────────────────────
# In a hub deployment, one TikTelemetry agent monitors many routers. Run
# this script on EACH router you want to monitor. The same shared credentials
# can be reused across routers, or you can set per-router overrides in the
# agent configuration (ROUTER_<NAME>_PASS etc.).
# ═══════════════════════════════════════════════════════════════════════════════

# Create a read-only user group for API access
/user group add name=telemetry policy=api,read,test comment="TikTelemetry read-only group"

# Create the agent user (CHANGE THE PASSWORD)
/user add name=tiktelemetry group=telemetry password="CHANGE_ME" comment="TikTelemetry agent"

# Enable the API service (required, disabled by default on RouterOS v7)
/ip service set api disabled=no

# ── Optional: Restrict API access to the container host ─────────────────────────
# If running TikTelemetry on the router itself (RouterOS v7 container), the
# API connection originates from the container's virtual Ethernet interface.
# Find its IP with /container print and restrict accordingly:
#
#   /ip service set api address=172.17.0.2/32

# ── Optional: Enable API-SSL for encrypted transport ────────────────────────────
# RouterOS v7 has its own certificate store. To use API-SSL:
#
#   1. Generate or import a certificate:
#      /certificate add name=api-cert common-name=router.local days-valid=3650
#      /certificate sign api-cert
#
#   2. Enable the API-SSL service:
#      /ip service set api-ssl disabled=no certificate=api-cert
#
#   3. In .env set:
#      ROUTER_ADDRESS=192.168.88.1:8729
#      ROUTER_TLS=true
#      ROUTER_TLS_INSECURE=true   # if using self-signed cert
#
# For production, replace the certificate with a proper CA-signed one and
# set ROUTER_TLS_INSECURE=false.
