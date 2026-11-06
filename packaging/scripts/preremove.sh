#!/bin/sh
# On removal (not upgrade), stops the services so their binary isn't
# pulled out from under them. The data, settings and account stay.
set -e

# An upgrade: deb passes "upgrade" or "failed-upgrade", rpm the number of
# versions left (1 or more). Arch and Alpine run this only on removal.
case "$1" in
upgrade | failed-upgrade) exit 0 ;;
esac
if [ "$1" -ge 1 ] 2>/dev/null; then
	exit 0
fi

units="mimir-server mimir-server-worker"
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemctl disable --now mimir-server-backup.timer >/dev/null 2>&1 || true
	for u in $units; do
		systemctl disable --now "$u" >/dev/null 2>&1 || true
	done
elif command -v rc-service >/dev/null 2>&1; then
	for u in $units; do
		rc-service "$u" stop >/dev/null 2>&1 || true
		rc-update del "$u" >/dev/null 2>&1 || true
	done
fi
