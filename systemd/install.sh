#!/usr/bin/env bash
# Installs homelab-db-provisioner as a systemd service.
# Run as root. Defaults to watch mode; pass --oneshot to use the one-shot service.
set -euo pipefail

BINARY_SRC="${BINARY_SRC:-./homelab-db-provisioner}"
BINARY_DST="/usr/local/bin/homelab-db-provisioner"
CONFIG_DIR="/etc/homelab-db-provisioner"
DATA_DIR="/var/lib/homelab-db-provisioner"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE="homelab-db-provisioner"

if [[ "${1:-}" == "--oneshot" ]]; then
    SERVICE_FILE="homelab-db-provisioner-oneshot.service"
    SERVICE_NAME="homelab-db-provisioner-oneshot"
else
    SERVICE_FILE="homelab-db-provisioner.service"
    SERVICE_NAME="homelab-db-provisioner"
fi

if [[ $EUID -ne 0 ]]; then
    echo "Error: run as root" >&2
    exit 1
fi

# Create dedicated user if missing
if ! id db-provisioner &>/dev/null; then
    useradd --system --no-create-home --shell /sbin/nologin db-provisioner
fi

# Directories
install -d -o db-provisioner -g db-provisioner -m 750 "$CONFIG_DIR" "$DATA_DIR"

# Binary
install -o root -g root -m 755 "$BINARY_SRC" "$BINARY_DST"

# Environment file (skip if already exists)
if [[ ! -f "$CONFIG_DIR/env" ]]; then
    install -o root -g root -m 640 "$(dirname "$0")/env.example" "$CONFIG_DIR/env"
    echo "Created $CONFIG_DIR/env — edit before starting the service."
fi

# Service file
install -o root -g root -m 644 "$(dirname "$0")/$SERVICE_FILE" "$SYSTEMD_DIR/$SERVICE_NAME.service"

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo "Installed. Edit $CONFIG_DIR/env and $CONFIG_DIR/config.json, then:"
echo "  systemctl start $SERVICE_NAME"
echo "  journalctl -u $SERVICE_NAME -f"
