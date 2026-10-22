#!/usr/bin/env bash
set -e

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

# Ensure clean xdg-desktop-portal state
systemctl --user restart xdg-desktop-portal.service 2>/dev/null || true

# If binary not built yet, build it
if [ ! -f "$DIR/papercast" ]; then
    echo "==> Building Papercast (Go)..."
    export PATH=/usr/local/go/bin:$PATH
    (cd "$DIR" && go build -o papercast .)
fi

exec "$DIR/papercast" "$@"
