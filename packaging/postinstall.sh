#!/bin/sh
set -e

if ! id db-provisioner >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /sbin/nologin db-provisioner
fi

install -d -o db-provisioner -g db-provisioner -m 750 \
    /etc/homelab-db-provisioner \
    /var/lib/homelab-db-provisioner

if [ ! -f /etc/homelab-db-provisioner/env ]; then
    cp /etc/homelab-db-provisioner/env.example /etc/homelab-db-provisioner/env
    chown db-provisioner:db-provisioner /etc/homelab-db-provisioner/env
fi

systemctl daemon-reload
