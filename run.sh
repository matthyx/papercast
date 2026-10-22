#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

echo "==> Ensuring clean xdg-desktop-portal state..."
systemctl --user restart xdg-desktop-portal.service 2>/dev/null || true

echo "==> Setting up ADB reverse port forwarding (port 1701)..."
adb reverse tcp:1701 tcp:1701 2>/dev/null || echo "(ADB device not found or already forwarded - continuing)"

echo "==> Starting Papercast..."
PYTHONPATH="$DIR" python3 -m papercast "$@"
