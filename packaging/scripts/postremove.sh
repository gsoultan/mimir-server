#!/bin/sh
# Lets systemd forget the removed units. The data in /var/lib/mimir-server,
# the backups, the settings in /etc/mimir-server and the mimir-server
# account stay, so a reinstall picks up where it left off; remove them by
# hand if you want them gone.
set -e

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi
