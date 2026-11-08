#!/bin/sh
# WDTT bootstrap
# Качает install.sh: сначала GitHub, затем зеркала. Дальше установщик сам печатает шаги.
# Запуск на роутере (ash, без bash):
#   wget -O /tmp/wdtt-bootstrap.sh URL && sh /tmp/wdtt-bootstrap.sh
OUT="/tmp/wdtt-install.sh"
REPO_REF="0dec5dd"

fetch_to() {
	url="$1"
	dest="$2"
	rm -f "$dest"
	if [ -x /bin/uclient-fetch ]; then
		uclient-fetch -T 25 -O "$dest" "$url" && [ -s "$dest" ] && return 0
	fi
	if command -v wget >/dev/null 2>&1; then
		wget -T 25 -O "$dest" "$url" && [ -s "$dest" ] && return 0
	fi
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 25 -m 60 -o "$dest" "$url" && [ -s "$dest" ] && return 0
	fi
	return 1
}

echo "WDTT: загрузка установщика"
ok=0
for url in \
	"https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/${REPO_REF}/install.sh" \
	"https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@${REPO_REF}/install.sh" \
	"https://ghfast.top/https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/${REPO_REF}/install.sh" \
	"https://ghproxy.net/https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/${REPO_REF}/install.sh"
do
	echo "--> $url"
	if fetch_to "$url" "$OUT" && grep -q WDTT_INSTALL_VERSION "$OUT"; then
		echo "    получен"
		ok=1
		break
	fi
	echo "    нет ответа, следующее зеркало"
done

if [ "$ok" != 1 ]; then
	echo "ERROR: GitHub и зеркала недоступны."
	echo "С компьютера в одной сети с роутером:"
	echo "  git clone https://github.com/cWDTT/WDTT-OpenWRT.git"
	echo "  cd WDTT-OpenWRT"
	echo "  sh scripts/install-from-pc.sh root@192.168.1.1"
	exit 1
fi

echo "WDTT: запуск установки"
sh "$OUT" "$@"
exit $?
