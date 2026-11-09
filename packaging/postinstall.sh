#!/bin/sh
# Allow raw ICMP sockets without root (fallback when ping sockets are disabled
# via net.ipv4.ping_group_range). Failure is not fatal: advp still works with
# unprivileged ICMP sockets or as root.
if command -v setcap >/dev/null 2>&1; then
	setcap cap_net_raw+ep /usr/bin/advp || true
fi
