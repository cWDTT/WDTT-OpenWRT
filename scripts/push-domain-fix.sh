#!/bin/sh
# Быстрое обновление routing/firewall/doctor без полной переустановки.
# На ПК:
#   sh scripts/push-domain-fix.sh root@192.168.10.1

set -e

ROUTER="${1:-root@192.168.10.1}"
DIR="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="3.18.4"

wdtt_scp() {
	local src="$1" dest="$2"
	scp -O "$src" "$ROUTER:$dest" 2>/dev/null && return 0
	scp "$src" "$ROUTER:$dest"
}

echo "=== WDTT domain/firewall fix v${VERSION} → ${ROUTER} ==="

ssh "$ROUTER" "mkdir -p /usr/libexec/wdtt"

wdtt_scp "$DIR/wdtt-client/files/wdtt-domain-lib.sh" \
	"/usr/libexec/wdtt/domain-lib.sh"
wdtt_scp "$DIR/wdtt-client/files/wdtt-firewall-refresh" \
	"/usr/libexec/wdtt/firewall-refresh"
wdtt_scp "$DIR/wdtt-client/files/wdtt-datapath" \
	"/usr/libexec/wdtt/datapath"
wdtt_scp "$DIR/wdtt-client/files/wdtt-keepalive" \
	"/usr/libexec/wdtt/keepalive"
wdtt_scp "$DIR/wdtt-client/files/wdtt-routing" \
	"/usr/libexec/wdtt/routing"
wdtt_scp "$DIR/luci-app-wdtt/htdocs/luci-static/resources/view/wdtt/overview.js" \
	"/www/luci-static/resources/view/wdtt/overview.js"
wdtt_scp "$DIR/luci-app-wdtt/root/usr/libexec/rpcd/wdtt" \
	"/usr/libexec/rpcd/wdtt"
wdtt_scp "$DIR/luci-app-wdtt/root/etc/init.d/wdtt" \
	"/etc/init.d/wdtt"
wdtt_scp "$DIR/wdtt-client/files/wdtt-fix-config" \
	"/usr/libexec/wdtt/fix-config"
wdtt_scp "$DIR/wdtt-client/files/wdtt-routing" \
	"/usr/libexec/wdtt/routing"
wdtt_scp "$DIR/wdtt-client/files/wdtt-set-domains" \
	"/usr/libexec/wdtt/set-domains"
wdtt_scp "$DIR/wdtt-client/files/wdtt-doctor" \
	"/usr/libexec/wdtt/doctor"
wdtt_scp "$DIR/wdtt-client/files/wdtt-podkop" \
	"/usr/libexec/wdtt/podkop"
wdtt_scp "$DIR/wdtt-client/files/wdtt-full-tunnel" \
	"/usr/libexec/wdtt/full-tunnel"
wdtt_scp "$DIR/luci-app-wdtt/root/etc/firewall.wdtt" \
	"/etc/firewall.wdtt"
ssh "$ROUTER" "mkdir -p /etc/hotplug.d/firewall"
wdtt_scp "$DIR/luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" \
	"/etc/hotplug.d/firewall/99-wdtt"

ssh "$ROUTER" "chmod 755 /usr/libexec/rpcd/wdtt /usr/libexec/wdtt/* /etc/firewall.wdtt \
	/etc/init.d/wdtt /etc/hotplug.d/firewall/99-wdtt 2>/dev/null; \
	printf '%s\n' '${VERSION}' > /usr/share/wdtt/version; \
	touch /etc/crontabs/root; \
	grep -q 'wdtt/keepalive' /etc/crontabs/root 2>/dev/null \
		|| echo '* * * * * /usr/libexec/wdtt/keepalive' >> /etc/crontabs/root; \
	/etc/init.d/cron restart 2>/dev/null || /etc/init.d/crond restart 2>/dev/null || true; \
	/usr/libexec/wdtt/datapath ensure 2>/dev/null \
		|| /usr/libexec/wdtt/routing reload wg-wdtt 2>/dev/null || true; \
	/etc/init.d/rpcd restart; rm -rf /tmp/luci-*"

echo "=== OK. В браузере: Ctrl+F5 на странице WDTT ==="
echo "На роутере:"
echo "  /usr/libexec/wdtt/podkop status"
echo "  /usr/libexec/wdtt/doctor"
