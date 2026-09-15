#!/bin/sh
# Установка WDTT с компьютера (jsDelivr/GitHub недоступны с роутера)
# Запуск на ПК:
#   sh scripts/install-from-pc.sh root@192.168.10.1
#   sh scripts/install-from-pc.sh root@192.168.10.1 --clean

set -e

ROUTER="${1:-root@192.168.1.1}"
CLEAN_ARG=""
[ "$2" = "--clean" ] && CLEAN_ARG="--clean"
DIR="$(cd "$(dirname "$0")/.." && pwd)"
TMP="/tmp/wdtt-pc-install"
VERSION="3.18.4"
REPO="https://github.com/cWDTT/WDTT-OpenWRT"
PIN="1933519"
MIN_BIN_SIZE=1048576

wdtt_local_bin_ok() {
	local f="$1" sz
	[ -f "$f" ] || return 1
	sz="$(wc -c < "$f" | tr -d ' ')"
	[ "$sz" -ge "$MIN_BIN_SIZE" ] || return 1
	head -c 4 "$f" | grep -q 'ELF' && return 0
	head -c 4 "$f" | od -An -tx1 2>/dev/null | grep -q '7f 45 4c 46'
}

wdtt_ssh_put() {
	local src="$1" dest="$2"
	scp -O "$src" "$ROUTER:$dest" 2>/dev/null && return 0
	scp "$src" "$ROUTER:$dest" 2>/dev/null && return 0
	echo "  scp failed, fallback: ssh cat → $dest"
	ssh "$ROUTER" "cat > $dest" < "$src"
}

wdtt_ssh_put_bin() {
	local src="$1" dest="$2" sz_pc sz_rt

	sz_pc="$(wc -c < "$src" | tr -d ' ')"
	echo "  push binary ($sz_pc bytes) → $dest"

	scp -O "$src" "$ROUTER:$dest" 2>/dev/null && :
	scp "$src" "$ROUTER:$dest" 2>/dev/null && :
	if ssh "$ROUTER" "wc -c < $dest 2>/dev/null" | tr -d ' ' | grep -qx "$sz_pc"; then
		ssh "$ROUTER" "chmod 755 $dest"
		return 0
	fi

	echo "  scp size mismatch, trying gzip stream..."
	gzip -c "$src" | ssh "$ROUTER" "gunzip -c > $dest && chmod 755 $dest"

	sz_rt="$(ssh "$ROUTER" "wc -c < $dest 2>/dev/null" | tr -d ' ')"
	if [ "$sz_pc" != "$sz_rt" ]; then
		echo "ERROR: binary size mismatch PC=$sz_pc router=$sz_rt" >&2
		return 1
	fi
}

mkdir -p "$TMP"

echo "=== wdttd v${VERSION} on PC ==="
if [ -f "$DIR/bin/wdttd-linux-arm64" ]; then
	cp -f "$DIR/bin/wdttd-linux-arm64" "$TMP/wdttd"
	echo "  using local bin/wdttd-linux-arm64"
else
	curl -fsSL -L -o "$TMP/wdttd" \
		"https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@${PIN}/bin/wdttd-linux-arm64" || \
	curl -fsSL -L -o "$TMP/wdttd" \
		"${REPO}/releases/download/v${VERSION}/wdttd-linux-arm64" || \
	curl -fsSL -L -o "$TMP/wdttd" \
		"https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/${PIN}/bin/wdttd-linux-arm64"
fi
chmod +x "$TMP/wdttd"
wdtt_local_bin_ok "$TMP/wdttd" || {
	echo "ERROR: invalid wdttd binary on PC" >&2
	exit 1
}
ls -la "$TMP/wdttd"

echo "=== Bundle repo files (offline) ==="
tar czf "$TMP/wdtt-repo.tar.gz" -C "$DIR" \
	install.sh \
	bin/wdttd-linux-arm64 \
	wdtt-client/files/wdtt-routing \
	wdtt-client/files/wdtt-fix-config \
	wdtt-client/files/wdtt-doctor \
	wdtt-client/files/wdtt-full-tunnel \
	wdtt-client/files/wdtt-firewall-refresh \
	wdtt-client/files/wdtt-keepalive \
	wdtt-client/files/wdtt-uplink \
	wdtt-client/files/wdtt-domain-lib.sh \
	wdtt-client/files/wdtt-set-domains \
	luci-app-wdtt/root/etc/init.d/wdtt \
	luci-app-wdtt/root/etc/config/wdtt \
	luci-app-wdtt/root/etc/firewall.wdtt \
	luci-app-wdtt/root/etc/uci-defaults/99-wdtt \
	luci-app-wdtt/root/etc/hotplug.d/iface/99-wdtt \
	luci-app-wdtt/root/usr/libexec/rpcd/wdtt \
	luci-app-wdtt/root/usr/share/luci/menu.d/luci-app-wdtt.json \
	luci-app-wdtt/root/usr/share/rpcd/acl.d/luci-app-wdtt.json \
	luci-app-wdtt/htdocs/luci-static/resources/view/wdtt/overview.js

echo "=== Copy to router $ROUTER ==="
wdtt_ssh_put_bin "$TMP/wdttd" "/tmp/wdttd"
wdtt_ssh_put "$TMP/wdtt-repo.tar.gz" "/tmp/wdtt-repo.tar.gz"
wdtt_ssh_put "$DIR/install.sh" "/tmp/wdtt-install.sh"

echo "=== Verify on router ==="
ssh "$ROUTER" "ls -la /tmp/wdttd; wc -c < /tmp/wdttd; head -c 4 /tmp/wdttd | grep -aq ELF && echo ELF_OK || echo ELF_CHECK_SKIP"

echo "=== Run offline installer on router ==="
ssh "$ROUTER" "set -e
	rm -rf /tmp/wdtt-repo /tmp/wdtt-install
	mkdir -p /tmp/wdtt-repo
	tar xzf /tmp/wdtt-repo.tar.gz -C /tmp/wdtt-repo
	chmod +x /tmp/wdttd /tmp/wdtt-install.sh
	WDTT_SKIP_PROBE=1 \
	WDTT_SKIP_DEPS=1 \
	WDTT_LOCAL_BIN=/tmp/wdttd \
	WDTT_LOCAL_REPO=/tmp/wdtt-repo \
	WDTT_KEEP_SECRETS=1 \
	WDTT_FRESH_CONFIG=1 \
	sh /tmp/wdtt-install.sh ${CLEAN_ARG}"

echo "=== Done ==="
echo "LuCI: Services → WDTT VPN → peer/password → Подключить"
echo "Проверка: ssh $ROUTER '/usr/libexec/wdtt/doctor'"
