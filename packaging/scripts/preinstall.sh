#!/bin/sh
# Makes the mimir-server system account the services run as. It has no
# login shell and its home is the data folder. Runs on every install and
# upgrade, so it only adds what is missing.
set -e

user=mimir-server
home=/var/lib/mimir-server

nologin=/usr/sbin/nologin
[ -x "$nologin" ] || nologin=/sbin/nologin
[ -x "$nologin" ] || nologin=/bin/false

if ! getent group "$user" >/dev/null 2>&1; then
	if command -v groupadd >/dev/null 2>&1; then
		groupadd --system "$user"
	else
		addgroup -S "$user" # BusyBox (Alpine)
	fi
fi

if ! getent passwd "$user" >/dev/null 2>&1; then
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --gid "$user" --home-dir "$home" --no-create-home \
			--shell "$nologin" --comment "mimir-server" "$user"
	else
		adduser -S -D -H -G "$user" -h "$home" -s "$nologin" -g "mimir-server" "$user"
	fi
fi
