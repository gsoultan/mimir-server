#!/bin/sh
# Makes the folders the services use and lets the service account read its
# settings. It never starts, enables or restarts a service: you choose when
# the server runs, and an upgrade leaves a running server on the old binary
# until you restart it.
set -e

user=mimir-server

install -d -m 0750 -o root -g "$user" /etc/mimir-server
install -d -m 0700 -o "$user" -g "$user" /var/lib/mimir-server
install -d -m 0700 -o "$user" -g "$user" /var/lib/mimir-server-worker
install -d -m 0700 -o "$user" -g "$user" /var/backups/mimir-server

# The console's settings: the service account may read them (for
# `mimir-server setup`, `backup` and `migrate` run as it). The worker's hold
# its token and stay root's.
if [ -f /etc/mimir-server/mimir-server.env ]; then
	chown root:"$user" /etc/mimir-server/mimir-server.env
	chmod 0640 /etc/mimir-server/mimir-server.env
fi
if [ -f /etc/mimir-server/worker.env ]; then
	chown root:root /etc/mimir-server/worker.env
	chmod 0600 /etc/mimir-server/worker.env
fi

if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	cat <<'MSG'
mimir-server is installed but not running. Put your settings in
/etc/mimir-server/mimir-server.env, then:

  systemctl enable --now mimir-server

Open the console through your reverse proxy to finish first-run setup.
After an upgrade, `systemctl restart mimir-server` runs the new version.
See https://github.com/gsoultan/mimir-server/blob/main/docs/deploy.md
MSG
elif command -v rc-update >/dev/null 2>&1; then
	cat <<'MSG'
mimir-server is installed but not running. Put your settings in
/etc/mimir-server/mimir-server.env, then:

  rc-update add mimir-server && rc-service mimir-server start

Open the console through your reverse proxy to finish first-run setup.
After an upgrade, `rc-service mimir-server restart` runs the new version.
See https://github.com/gsoultan/mimir-server/blob/main/docs/deploy.md
MSG
fi
