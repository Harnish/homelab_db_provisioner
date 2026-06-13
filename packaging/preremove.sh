#!/bin/sh
systemctl stop homelab-db-provisioner homelab-db-provisioner-oneshot 2>/dev/null || true
systemctl disable homelab-db-provisioner homelab-db-provisioner-oneshot 2>/dev/null || true
