#!/bin/sh
# WDTT installer for OpenWrt (apk / opkg) — Cudy TR3000 256MB and compatible
#
# Публичный репозиторий:
#   sh <(wget -O - https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/main/install.sh)
#
# Приватный репозиторий (нужен GitHub token с правом Contents: Read):
#   export GITHUB_TOKEN="github_pat_..."
#   sh <(wget --header="Authorization: Bearer $GITHUB_TOKEN" -O - \
#     https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/main/install.sh)
#
# Или скопировать скрипт на роутер:
#   scp install.sh root@192.168.1.1:/tmp/ && ssh root@192.168.1.1 'GITHUB_TOKEN=xxx sh /tmp/install.sh'

# Не прерываем установку при ошибках apk (обрабатываем вручную)
set +e

WDTT_INSTALL_VERSION="3.19.1"
WDTT_ROUTING_VERSION="3.19.0"
WDTT_BIN_TAG="v3.19.1"

GITHUB_REPO="cWDTT/WDTT-OpenWRT"
GITHUB_BRANCH="main"
# jsDelivr кэширует @main — pin на коммит (обновлять при релизе)
REPO_REF="b10e150"
RAW_URL="https://raw.githubusercontent.com/${GITHUB_REPO}/${GITHUB_BRANCH}"
RAW_PIN="https://raw.githubusercontent.com/${GITHUB_REPO}/${REPO_REF}"
JSDELIVR_URL="https://cdn.jsdelivr.net/gh/${GITHUB_REPO}@${GITHUB_BRANCH}"
JSDELIVR_PIN="https://cdn.jsdelivr.net/gh/${GITHUB_REPO}@${REPO_REF}"
RELEASE_API="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"
RELEASE_BIN_URL="https://github.com/${GITHUB_REPO}/releases/download/v3.19.1/wdttd-linux-arm64"
DOWNLOAD_DIR="/tmp/wdtt-install"
SECRETS_BACKUP="/tmp/wdtt-secrets-backup"
COUNT=3
HTTP_TIMEOUT=30

# Токен: только если задан явно (приватный репо). Файл — опционально.
if [ -z "$GITHUB_TOKEN" ] && [ -f /etc/wdtt/github_token ]; then
	GITHUB_TOKEN="$(tr -d '[:space:]' < /etc/wdtt/github_token)"
fi

PKG_IS_APK=0
command -v apk >/dev/null 2>&1 && PKG_IS_APK=1

msg()  { printf "\033[32;1m%s\033[0m\n" "$1"; }
warn() { printf "\033[33;1m%s\033[0m\n" "$1"; }
err()  { printf "\033[31;1m%s\033[0m\n" "$1"; }

# wget-nossl ломает apk: зеркала OpenWrt только HTTPS
fix_broken_wget() {
	local target

	if apk info -e wget-nossl >/dev/null 2>&1; then
		warn "Удаляем wget-nossl (ломает apk HTTPS)..."
		apk del wget-nossl >/dev/null 2>&1
	fi

	if [ -x /bin/uclient-fetch ]; then
		target="$(readlink -f /usr/bin/wget 2>/dev/null || echo "")"
		case "$target" in
			*wget-nossl*|*nossl*)
				warn "Восстанавливаем wget → uclient-fetch"
				ln -sf /bin/uclient-fetch /usr/bin/wget
				;;
		esac
		# apk на OpenWrt использует wget для загрузки пакетов
		if [ ! -x /usr/bin/wget ] || [ "$target" = "" ]; then
			ln -sf /bin/uclient-fetch /usr/bin/wget
		fi
	fi
}

# Универсальная загрузка: uclient-fetch (OpenWrt) → curl → wget
http_get() {
	local url="$1" dest="$2" token="$3"

	if [ -x /bin/uclient-fetch ]; then
		if [ -n "$token" ]; then
			uclient-fetch -t "$HTTP_TIMEOUT" -q -O "$dest" \
				--header="Authorization: Bearer $token" "$url" 2>/dev/null \
				&& [ -s "$dest" ] && return 0
		fi
		uclient-fetch -t "$HTTP_TIMEOUT" -q -O "$dest" "$url" 2>/dev/null \
			&& [ -s "$dest" ] && return 0
	fi

	if command -v curl >/dev/null 2>&1; then
		if [ -n "$token" ]; then
			curl -fsSL --connect-timeout "$HTTP_TIMEOUT" -m 120 \
				-H "Authorization: Bearer $token" -o "$dest" "$url" 2>/dev/null \
				&& [ -s "$dest" ] && return 0
		fi
		curl -fsSL --connect-timeout "$HTTP_TIMEOUT" -m 120 -L -o "$dest" "$url" 2>/dev/null \
			&& [ -s "$dest" ] && return 0
	fi

	if [ -n "$token" ]; then
		wget -T "$HTTP_TIMEOUT" -q -O "$dest" --header="Authorization: Bearer $token" "$url" 2>/dev/null \
			&& [ -s "$dest" ] && return 0
	fi
	wget -T "$HTTP_TIMEOUT" -q -O "$dest" "$url" 2>/dev/null && [ -s "$dest" ] && return 0
	return 1
}

# Скачать файл из репозитория через зеркала (если raw.githubusercontent.com недоступен)
download_repo_file() {
	local relpath="$1" dest="$2" label="$3"
	local base url

	if [ -n "$WDTT_LOCAL_REPO" ] && [ -f "$WDTT_LOCAL_REPO/$relpath" ]; then
		msg "  local repo: $relpath"
		cp -f "$WDTT_LOCAL_REPO/$relpath" "$dest"
		[ -s "$dest" ] && return 0
	fi

	for base in "$JSDELIVR_PIN" "$RAW_URL" "$JSDELIVR_URL"; do
		url="${base}/${relpath}"
		msg "  try: $url"
		if download_file "$url" "$dest"; then
			return 0
		fi
	done

	err "FAILED: $label (все зеркала недоступны)"
	return 1
}

_github_fetch() {
	local url="$1" out="$2" token="$3"
	http_get "$url" "$out" "$token"
}

# Скачивание URL в файл
download_file() {
	local url="$1" dest="$2" attempt=0

	while [ "$attempt" -lt "$COUNT" ]; do
		msg "Download $(basename "$dest") (attempt $((attempt + 1)))..."
		if http_get "$url" "$dest" ""; then return 0; fi
		if [ -n "$GITHUB_TOKEN" ] && http_get "$url" "$dest" "$GITHUB_TOKEN"; then return 0; fi
		rm -f "$dest"
		attempt=$((attempt + 1))
	done
	return 1
}

github_api_get() {
	local url="$1" out="$2"

	# Сначала публичный API (репозиторий открытый)
	if _github_fetch "$url" "$out" ""; then
		return 0
	fi

	# Потом с токеном
	if [ -n "$GITHUB_TOKEN" ]; then
		if _github_fetch "$url" "$out" "$GITHUB_TOKEN"; then
			return 0
		fi
		warn "GITHUB_TOKEN невалиден — игнорируем. Удалите: rm -f /etc/wdtt/github_token"
		GITHUB_TOKEN=""
	fi

	return 1
}

check_github_access() {
	if [ "$WDTT_SKIP_PROBE" = "1" ]; then
		warn "Пропуск проверки CDN (WDTT_SKIP_PROBE=1)"
		return 0
	fi

	# Проверяем доступ (jsDelivr pin → raw → jsDelivr @main)
	if download_file "$JSDELIVR_PIN/install.sh" "$DOWNLOAD_DIR/probe.sh"; then
		rm -f "$DOWNLOAD_DIR/probe.sh"
		msg "CDN: jsDelivr (pin ${REPO_REF}) OK"
		return 0
	fi

	if download_file "$RAW_URL/install.sh" "$DOWNLOAD_DIR/probe.sh"; then
		rm -f "$DOWNLOAD_DIR/probe.sh"
		msg "GitHub: публичный доступ OK"
		return 0
	fi

	if download_file "$JSDELIVR_URL/install.sh" "$DOWNLOAD_DIR/probe.sh"; then
		rm -f "$DOWNLOAD_DIR/probe.sh"
		msg "CDN: jsDelivr @main OK"
		return 0
	fi

	# install.sh уже на роутере (raw GitHub сработал) — продолжаем
	if [ -s /tmp/wdtt-install.sh ] && grep -q WDTT_INSTALL_VERSION /tmp/wdtt-install.sh 2>/dev/null; then
		warn "CDN probe failed — используем локальный /tmp/wdtt-install.sh"
		return 0
	fi

	if [ -n "$WDTT_LOCAL_BIN" ] && [ -f "$WDTT_LOCAL_BIN" ]; then
		warn "CDN probe failed — продолжаем (WDTT_LOCAL_BIN задан)"
		return 0
	fi

	if [ -z "$GITHUB_TOKEN" ]; then
		err "Не удаётся скачать файлы из GitHub / CDN."
		err "На роутере: WDTT_SKIP_PROBE=1 WDTT_LOCAL_BIN=/tmp/wdttd sh /tmp/wdtt-install.sh"
		err "С ПК: sh scripts/install-from-pc.sh root@192.168.1.1 --clean"
		exit 1
	fi

	err "GitHub недоступен. Проверьте GITHUB_TOKEN и интернет."
	exit 1
}

pkg_list_update() {
	if [ "$PKG_IS_APK" -eq 1 ]; then
		apk update
	else
		opkg update
	fi
}

pkg_install() {
	if [ "$PKG_IS_APK" -eq 1 ]; then
		apk add "$@"
	else
		opkg install "$@"
	fi
}

pkg_install_file() {
	local f="$1"
	if [ "$PKG_IS_APK" -eq 1 ]; then
		apk add --allow-untrusted "$f"
	else
		opkg install "$f"
	fi
}

detect_arch() {
	local m
	m="$(uname -m)"
	case "$m" in
		aarch64|arm64) echo "aarch64" ;;
		armv7l|armv7)  echo "arm" ;;
		x86_64|amd64)  echo "x86_64" ;;
		*)             echo "$m" ;;
	esac
}

install_from_release() {
	local pattern filename filepath installed=0

	if [ "$PKG_IS_APK" -eq 1 ]; then
		pattern='\.apk'
	else
		pattern='\.ipk'
	fi

	if ! github_api_get "$RELEASE_API" "$DOWNLOAD_DIR/release.json"; then
		return 1
	fi

	# browser_download_url из JSON релиза
	grep -o 'https://[^"]*'"$pattern" "$DOWNLOAD_DIR/release.json" 2>/dev/null | \
		sort -u > "$DOWNLOAD_DIR/urls.txt" || true

	if [ ! -s "$DOWNLOAD_DIR/urls.txt" ]; then
		return 1
	fi

	while read -r url; do
		[ -z "$url" ] && continue
		filename="$(basename "$url")"
		case "$filename" in
			wdtt-client*|luci-app-wdtt*) ;;
			*) continue ;;
		esac
		filepath="$DOWNLOAD_DIR/$filename"
		if download_file "$url" "$filepath"; then
			msg "Installing $filename..."
			pkg_install_file "$filepath"
			installed=1
		fi
	done < "$DOWNLOAD_DIR/urls.txt"

	[ "$installed" -eq 1 ]
}

install_file() {
	local url="$1" dest="$2" label="$3"

	if download_file "$url" "$dest"; then
		return 0
	fi
	err "FAILED: $label"
	err "  URL: $url"
	return 1
}

install_repo_file() {
	local relpath="$1" dest="$2" label="$3"
	download_repo_file "$relpath" "$dest" "$label"
}

bin_is_valid() {
	local f="$1" min="${2:-1048576}" sz magic

	[ -f "$f" ] || return 1
	sz="$(wc -c < "$f" 2>/dev/null | tr -d ' ')"
	[ -n "$sz" ] && [ "$sz" -ge "$min" ] || return 1
	magic="$(head -c 4 "$f" 2>/dev/null | od -An -tx1 2>/dev/null | tr -d ' \n')"
	case "$magic" in
		7f454c46*) return 0 ;;
	esac
	head -c 4 "$f" 2>/dev/null | grep -q 'ELF' && return 0
	return 1
}

bin_invalid_hint() {
	local f="$1" sz magic
	sz="$(wc -c < "$f" 2>/dev/null | tr -d ' ')"
	magic="$(head -c 4 "$f" 2>/dev/null | od -An -tx1 2>/dev/null | tr -d ' \n')"
	err "  размер=${sz:-0} байт, magic=${magic:-??} (ожидается 7f454c46 / ELF arm64)"
}

try_download_bin() {
	local url="$1" dest="$2"

	rm -f "$dest"
	msg "  try bin: $url"
	if download_file "$url" "$dest" 2>/dev/null && bin_is_valid "$dest"; then
		return 0
	fi
	rm -f "$dest"
	return 1
}

install_bin() {
	local dest="$1" goarch="${2:-arm64}" relpath url gh_url local_bin

	mkdir -p "$(dirname "$dest")" 2>/dev/null || true

	relpath="bin/wdttd-linux-${goarch}"

	# Локальный бинарник (скопирован с ПК): WDTT_LOCAL_BIN=/tmp/wdttd sh install.sh
	for local_bin in "$WDTT_LOCAL_BIN" "$WDTT_LOCAL_REPO/bin/wdttd-linux-${goarch}"; do
		[ -n "$local_bin" ] || continue
		[ -f "$local_bin" ] || continue
		msg "  using local: $local_bin"
		cp -f "$local_bin" "$dest"
		chmod 0755 "$dest"
		if bin_is_valid "$dest"; then
			return 0
		fi
		bin_invalid_hint "$local_bin"
		err "  WDTT_LOCAL_BIN не похож на ELF arm64 — перекачайте с ПК (gzip|ssh gunzip)"
		rm -f "$dest"
	done

	# jsDelivr (файл в репо bin/) — работает когда github.com/releases заблокирован
	for url in \
		"${JSDELIVR_PIN}/${relpath}" \
		"${JSDELIVR_URL}/${relpath}" \
		"https://cdn.jsdelivr.net/gh/${GITHUB_REPO}@${WDTT_BIN_TAG}/${relpath}" \
		"${RAW_PIN}/${relpath}" \
		"${RAW_URL}/${relpath}"
	do
		try_download_bin "$url" "$dest" && return 0
	done

	# GitHub Releases + зеркала
	for gh_url in \
		"$RELEASE_BIN_URL" \
		"https://ghproxy.net/$RELEASE_BIN_URL" \
		"https://mirror.ghproxy.com/$RELEASE_BIN_URL" \
		"https://ghfast.top/$RELEASE_BIN_URL"
	do
		try_download_bin "$gh_url" "$dest" && return 0
	done

	if find_release_binary "$goarch" "$DOWNLOAD_DIR/binurl.txt"; then
		url="$(cat "$DOWNLOAD_DIR/binurl.txt" 2>/dev/null)"
		try_download_bin "$url" "$dest" && return 0
		for gh_url in \
			"https://ghproxy.net/$url" \
			"https://mirror.ghproxy.com/$url"
		do
			try_download_bin "$gh_url" "$dest" && return 0
		done
	fi

	if bin_is_valid /usr/sbin/wdttd; then
		warn "  CDN/GitHub недоступны — используем уже установленный /usr/sbin/wdttd"
		cp -f /usr/sbin/wdttd "$dest"
		return 0
	fi

	return 1
}

find_release_binary() {
	local goarch="$1" out="$2"

	if ! github_api_get "$RELEASE_API" "$DOWNLOAD_DIR/release.json"; then
		return 1
	fi

	grep -o "https://[^\"]*wdttd-linux-${goarch}[^\"]*" "$DOWNLOAD_DIR/release.json" 2>/dev/null | head -n1 > "$out"
	[ -s "$out" ]
}

install_hotplug_firewall_wdtt() {
	local dest="/etc/hotplug.d/firewall/99-wdtt"

	mkdir -p /etc/hotplug.d/firewall
	if [ -n "$WDTT_LOCAL_REPO" ] && [ -f "$WDTT_LOCAL_REPO/luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" ]; then
		cp -f "$WDTT_LOCAL_REPO/luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" "$dest"
	elif download_file "${JSDELIVR_PIN}/luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" "$dest" 2>/dev/null \
		|| download_file "${RAW_PIN}/luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" "$dest" 2>/dev/null \
		|| install_repo_file "luci-app-wdtt/root/etc/hotplug.d/firewall/99-wdtt" "$dest" "hotplug-fw" 2>/dev/null; then
		:
	else
		cat > "$dest" <<'EOF'
#!/bin/sh
[ "$ACTION" = "reload" ] || exit 0
[ -f /var/run/wdtt/routing.mode ] || exit 0
[ -f /var/run/wdtt/firewall-refresh.lock ] && exit 0
routing_mode="$(uci -q get wdtt.globals.routing_mode 2>/dev/null)"
[ "$routing_mode" = "full" ] && exit 0
case "$routing_mode" in external|podkop|tunnel|tunnel_only) exit 0 ;; esac
iface="$(uci -q get wdtt.globals.iface 2>/dev/null)"; iface="${iface:-wg-wdtt}"
/usr/libexec/wdtt/routing restore "$iface" 2>/dev/null || /usr/libexec/wdtt/routing reload "$iface" 2>/dev/null
exit 0
EOF
	fi
	chmod 0755 "$dest"
	msg "  OK: hotplug firewall"
}

install_wdtt_fix_config_inline() {
	cat > /usr/libexec/wdtt/fix-config <<'WFC_EOF'
#!/bin/sh
WDtt_CFG=/etc/config/wdtt
log() { logger -t wdtt-fix-config "$*"; }
[ -f "$WDtt_CFG" ] || exit 1
grep -qE '^[[:space:]]*option domain[[:space:]]' "$WDtt_CFG" 2>/dev/null && {
	t="$(mktemp)"; grep -Ev '^[[:space:]]*option domain[s]*[[:space:]]' "$WDtt_CFG" > "$t" && mv "$t" "$WDtt_CFG"
}
grep -qE "^[[:space:]]*option hashes '[^']+'" "$WDtt_CFG" 2>/dev/null || {
	t="$(mktemp)"; awk '/option hashes/{h=$0; if(h~/'\''[^'\'']+'\''/){print h; next}; getline; while(getline&&$0!~/'\''/){gsub(/^[[:space:]]+/,"",$0); gsub(/'\''/,"",$0); h=h","$0} print h; next} {print}' "$WDtt_CFG" > "$t" 2>/dev/null && mv "$t" "$WDtt_CFG"
}
uci show wdtt 2>&1 | grep -qi parse || {
	v="$(uci -q get wdtt.globals.hashes 2>/dev/null)"; [ -n "$v" ] && uci set wdtt.globals.hashes="$(echo "$v"|tr '\n\r\t ' ','|sed 's/,,*/,/g')" && uci commit wdtt
}
WFC_EOF
	chmod 755 /usr/libexec/wdtt/fix-config
	msg "  OK: fix-config (inline)"
}

install_wdtt_doctor_inline() {
	cat > /usr/libexec/wdtt/doctor <<'WDR_EOF'
#!/bin/sh
IFACE="$(uci -q get wdtt.globals.iface 2>/dev/null)"; IFACE="${IFACE:-wg-wdtt}"
echo "=== WDTT doctor ==="
[ -x /usr/libexec/wdtt/fix-config ] && /usr/libexec/wdtt/fix-config || true
grep -q parse_list_domain_line /usr/libexec/wdtt/routing 2>/dev/null && echo "OK: routing" || echo "FAIL: update routing script"
uci show wdtt 2>&1 | grep -qi parse && echo "FAIL: uci parse" || echo "OK: uci"
ip link show "$IFACE" >/dev/null 2>&1 && echo "OK: $IFACE up" || echo "WARN: connect tunnel first"
/usr/libexec/wdtt/routing reload "$IFACE" 2>/dev/null; /usr/libexec/wdtt/routing status
WDR_EOF
	chmod 755 /usr/libexec/wdtt/doctor
	msg "  OK: doctor (inline)"
}

install_wdtt_helpers() {
	local f dest src ok=0

	mkdir -p /usr/libexec/wdtt
	# dest:имя на роутере  src:путь в репо
	for f in \
		"fix-config:wdtt-client/files/wdtt-fix-config" \
		"doctor:wdtt-client/files/wdtt-doctor" \
		"full-tunnel:wdtt-client/files/wdtt-full-tunnel" \
		"uplink:wdtt-client/files/wdtt-uplink" \
		"set-domains:wdtt-client/files/wdtt-set-domains" \
		"domain-lib.sh:wdtt-client/files/wdtt-domain-lib.sh" \
		"firewall-refresh:wdtt-client/files/wdtt-firewall-refresh" \
		"keepalive:wdtt-client/files/wdtt-keepalive" \
		"datapath:wdtt-client/files/wdtt-datapath" \
		"podkop:wdtt-client/files/wdtt-podkop" \
		"clash:wdtt-client/files/wdtt-clash"
	do
		dest="/usr/libexec/wdtt/${f%%:*}"
		src="${f#*:}"
		if download_file "$RAW_URL/$src" "$dest" 2>/dev/null \
			|| download_file "${JSDELIVR_PIN}/$src" "$dest" 2>/dev/null \
			|| install_repo_file "$src" "$dest" "${f%%:*}" 2>/dev/null; then
			chmod 0755 "$dest"
			msg "  OK: ${f%%:*}"
			ok=1
		fi
	done
	[ -x /usr/libexec/wdtt/fix-config ] || install_wdtt_fix_config_inline
	[ -x /usr/libexec/wdtt/doctor ] || install_wdtt_doctor_inline

	# cron: раз в минуту чинит policy routing / nft если слетели
	if [ -x /usr/libexec/wdtt/keepalive ]; then
		if [ -d /etc/crontabs ] || mkdir -p /etc/crontabs 2>/dev/null; then
			touch /etc/crontabs/root
			grep -q 'wdtt/keepalive' /etc/crontabs/root 2>/dev/null \
				|| echo '* * * * * /usr/libexec/wdtt/keepalive' >> /etc/crontabs/root
			/etc/init.d/cron restart 2>/dev/null || /etc/init.d/crond restart 2>/dev/null || true
			msg "  OK: keepalive cron (* * * * *)"
		fi
	fi
}

install_from_source() {
	local arch goarch LUCI_VIEW

	arch="$(detect_arch)"
	case "$arch" in
		aarch64) goarch="arm64" ;;
		arm)     goarch="arm" ;;
		x86_64)  goarch="amd64" ;;
		*)
			err "Unsupported architecture: $arch"
			exit 1
			;;
	esac

	msg "Installing WDTT from GitHub (arch=${goarch})..."

	mkdir -p /usr/sbin /usr/libexec/wdtt /var/run/wdtt \
		/etc/init.d /etc/config /etc/hotplug.d/iface /etc/hotplug.d/firewall \
		/etc/uci-defaults /usr/share/luci/menu.d \
		/usr/share/rpcd/acl.d /usr/libexec/rpcd 2>/dev/null || true

	LUCI_VIEW="/www/luci-static/resources/view/wdtt"
	mkdir -p "$LUCI_VIEW"

	msg "Step 1/3: wdttd binary..."
	if ! install_bin "$DOWNLOAD_DIR/wdttd" "$goarch"; then
		err "Cannot download wdttd-linux-${goarch}"
		err "jsDelivr/GitHub недоступны с роутера? Установка с ПК:"
		err "  sh scripts/install-from-pc.sh root@ROUTER --clean"
		err "Или вручную:"
		err "  scp bin/wdttd-linux-arm64 install.sh root@ROUTER:/tmp/"
		err "  scp -r wdtt-client luci-app-wdtt root@ROUTER:/tmp/wdtt-repo/"
		err "  ssh root@ROUTER 'WDTT_SKIP_PROBE=1 WDTT_LOCAL_BIN=/tmp/wdttd-linux-arm64 \\"
		err "    WDTT_LOCAL_REPO=/tmp/wdtt-repo sh /tmp/install.sh --clean'"
		exit 1
	fi
	cp -f "$DOWNLOAD_DIR/wdttd" /usr/sbin/wdttd
	chmod 0755 /usr/sbin/wdttd
	msg "  OK: /usr/sbin/wdttd ($(wc -c < /usr/sbin/wdttd) bytes)"

	msg "Step 2/3: scripts and config..."
	ensure_routing_script || exit 1
	install_wdtt_helpers

	install_repo_file "luci-app-wdtt/root/etc/init.d/wdtt" /etc/init.d/wdtt "init.d" || exit 1
	chmod 0755 /etc/init.d/wdtt

	if [ "$WDTT_FRESH_CONFIG" = "1" ] || [ ! -f /etc/config/wdtt ]; then
		install_repo_file "luci-app-wdtt/root/etc/config/wdtt" /etc/config/wdtt "config" || exit 1
	fi
	install_repo_file "luci-app-wdtt/root/etc/firewall.wdtt" /etc/firewall.wdtt "firewall" || exit 1
	chmod 0755 /etc/firewall.wdtt

	install_repo_file "luci-app-wdtt/root/etc/uci-defaults/99-wdtt" /etc/uci-defaults/99-wdtt "uci-defaults" || exit 1
	chmod 0755 /etc/uci-defaults/99-wdtt

	install_repo_file "luci-app-wdtt/root/etc/hotplug.d/iface/99-wdtt" /etc/hotplug.d/iface/99-wdtt "hotplug" || exit 1
	chmod 0755 /etc/hotplug.d/iface/99-wdtt

	install_hotplug_firewall_wdtt

	install_repo_file "luci-app-wdtt/root/usr/libexec/rpcd/wdtt" /usr/libexec/rpcd/wdtt "rpcd" || exit 1
	chmod 0755 /usr/libexec/rpcd/wdtt

	msg "Step 3/3: LuCI..."
	install_repo_file "luci-app-wdtt/root/usr/share/luci/menu.d/luci-app-wdtt.json" \
		/usr/share/luci/menu.d/luci-app-wdtt.json "menu" || exit 1
	install_repo_file "luci-app-wdtt/root/usr/share/rpcd/acl.d/luci-app-wdtt.json" \
		/usr/share/rpcd/acl.d/luci-app-wdtt.json "acl" || exit 1
	install_repo_file "luci-app-wdtt/htdocs/luci-static/resources/view/wdtt/overview.js" \
		"$LUCI_VIEW/overview.js" "LuCI view" || exit 1

	mkdir -p /tmp/dnsmasq.d
	msg "WDTT files installed."
	msg "Step 4/4: зависимости (apk) — может занять 1–5 мин, подождите..."
}

	routing_is_current() {
	local f="${1:-/usr/libexec/wdtt/routing}"

	[ -f "$f" ] || return 1
	grep -qE '^NFT_HOOK=/etc/nftables\.d' "$f" 2>/dev/null && return 1
	grep -q "WDTT_ROUTING_VERSION=${WDTT_ROUTING_VERSION}" "$f" 2>/dev/null \
		&& grep -q 'parse_list_domain_line' "$f" 2>/dev/null \
		&& grep -q 'remove_legacy_option_domains' "$f" 2>/dev/null \
		&& ! grep -q '|| echo 0)"' "$f" 2>/dev/null \
		&& return 0
	return 1
}

routing_version_hint() {
	local f="${1:-/usr/libexec/wdtt/routing}"
	local ver bytes

	ver="$(grep -m1 'WDTT_ROUTING_VERSION=' "$f" 2>/dev/null || echo 'unknown')"
	bytes="$(wc -c < "$f" 2>/dev/null | tr -d ' ')"
	[ -n "$bytes" ] || bytes=0
	printf '%s (%s bytes)' "$ver" "$bytes"
}

ensure_routing_script() {
	local dest="/usr/libexec/wdtt/routing"
	local url

	if routing_is_current "$dest"; then
		msg "  OK: routing v${WDTT_ROUTING_VERSION}"
		return 0
	fi

	warn "Обновляем routing → v${WDTT_ROUTING_VERSION}..."
	if [ -n "$WDTT_LOCAL_REPO" ] && [ -f "$WDTT_LOCAL_REPO/wdtt-client/files/wdtt-routing" ]; then
		cp -f "$WDTT_LOCAL_REPO/wdtt-client/files/wdtt-routing" "$dest"
		chmod 0755 "$dest"
		if routing_is_current "$dest"; then
			msg "  OK: /usr/libexec/wdtt/routing (local repo v${WDTT_ROUTING_VERSION})"
			return 0
		fi
	fi
	for url in \
		"${JSDELIVR_PIN}/wdtt-client/files/wdtt-routing" \
		"${RAW_PIN}/wdtt-client/files/wdtt-routing" \
		"${RAW_URL}/wdtt-client/files/wdtt-routing" \
		"${JSDELIVR_URL}/wdtt-client/files/wdtt-routing"
	do
		rm -f "$dest"
		if download_file "$url" "$dest" 2>/dev/null && routing_is_current "$dest"; then
			chmod 0755 "$dest"
			msg "  OK: /usr/libexec/wdtt/routing (v${WDTT_ROUTING_VERSION})"
			return 0
		fi
	done

	err "routing: не удалось получить v${WDTT_ROUTING_VERSION} ($(routing_version_hint "$dest"))"
	err "  uclient-fetch -O $dest ${JSDELIVR_PIN}/wdtt-client/files/wdtt-routing"
	return 1
}

verify_install() {
	local ok=1

	if [ -x /usr/sbin/wdttd ]; then
		msg "  [OK] /usr/sbin/wdttd"
	else
		err "  [!!] /usr/sbin/wdttd missing"
		ok=0
	fi
	if [ -x /etc/init.d/wdtt ]; then
		msg "  [OK] /etc/init.d/wdtt"
	else
		err "  [!!] /etc/init.d/wdtt missing"
		ok=0
	fi
	if [ -f /www/luci-static/resources/view/wdtt/overview.js ]; then
		if grep -q "WV (ручной" /www/luci-static/resources/view/wdtt/overview.js 2>/dev/null; then
			msg "  [OK] LuCI view (WV mode)"
		else
			warn "  [??] LuCI view устарел — jsDelivr cache? Перекачайте overview.js"
			warn "      uclient-fetch -O /www/luci-static/resources/view/wdtt/overview.js \\"
			warn "        ${JSDELIVR_PIN}/luci-app-wdtt/htdocs/luci-static/resources/view/wdtt/overview.js"
		fi
	else
		warn "  [??] LuCI view missing — очистите кэш браузера"
	fi

	if routing_is_current /usr/libexec/wdtt/routing; then
		msg "  [OK] routing (nft+nftset)"
	elif [ -f /usr/libexec/wdtt/routing ]; then
		warn "  [??] routing устарел (nftables.d) — sh install.sh или:"
		warn "      uclient-fetch -O /usr/libexec/wdtt/routing \\"
		warn "        ${JSDELIVR_PIN}/wdtt-client/files/wdtt-routing"
	else
		warn "  [??] /usr/libexec/wdtt/routing missing"
	fi
	if command -v nft >/dev/null 2>&1; then
		msg "  [OK] nft"
	else
		warn "  [??] nft missing — apk add nftables kmod-nft-core"
	fi
	if dnsmasq_has_nftset; then
		msg "  [OK] dnsmasq nftset"
	else
		warn "  [??] dnsmasq без nftset — apk del dnsmasq && apk add dnsmasq-full"
	fi
	if uci -q get firewall.wdtt_lan_fwd.src >/dev/null 2>&1; then
		msg "  [OK] firewall lan→wdtt"
	else
		warn "  [??] firewall forward missing — sh /etc/uci-defaults/99-wdtt"
	fi

	return "$ok"
}

backup_wdtt_secrets() {
	local f="$SECRETS_BACKUP"
	rm -rf "$f"
	mkdir -p "$f"
	[ -f /etc/config/wdtt ] || return 0

	msg "Сохраняем учётные данные WDTT..."
	uci -q get wdtt.globals.peer 2>/dev/null > "$f/peer"
	uci -q get wdtt.globals.password 2>/dev/null > "$f/password"
	uci -q get wdtt.globals.hashes 2>/dev/null > "$f/hashes"
	uci -q get wdtt.globals.hash1 2>/dev/null > "$f/hash1"
	uci -q get wdtt.globals.hash2 2>/dev/null > "$f/hash2"
	uci -q get wdtt.globals.hash3 2>/dev/null > "$f/hash3"
	uci -q get wdtt.globals.hash4 2>/dev/null > "$f/hash4"
	uci -q get wdtt.globals.enabled 2>/dev/null > "$f/enabled"
	uci -q get wdtt.globals.captcha_mode 2>/dev/null > "$f/captcha_mode"
	uci -q get wdtt.globals.vk_auth_mode 2>/dev/null > "$f/vk_auth_mode"
	uci -q get wdtt.globals.obfs_mode 2>/dev/null > "$f/obfs_mode"
	uci -q get wdtt.globals.go_dns 2>/dev/null > "$f/go_dns"
	uci -q get wdtt.globals.turn_transport 2>/dev/null > "$f/turn_transport"
	uci -q get wdtt.globals.tunnel_mode 2>/dev/null > "$f/tunnel_mode"
	# device_id сохраняем обязательно: сервер привязывает к нему пароль, и новый
	# ID после переустановки получит DENIED:device_mismatch
	uci -q get wdtt.globals.device_id 2>/dev/null > "$f/device_id"
	uci -q get wdtt.globals.workers 2>/dev/null > "$f/workers"
	uci -q get wdtt.globals.routing_mode 2>/dev/null > "$f/routing_mode"
	uci -q get wdtt.globals.uplink_iface 2>/dev/null > "$f/uplink_iface"
	mkdir -p "$f/rules"
	for section in $(uci -q show wdtt 2>/dev/null | sed -n "s/^wdtt\\.\\([^.=]*\\)=rule\$/\\1/p"); do
		uci -q get "wdtt.${section}.domain_list" 2>/dev/null > "$f/rules/${section}.domain_list"
		uci -q get "wdtt.${section}.enabled" 2>/dev/null > "$f/rules/${section}.enabled"
		uci -q get "wdtt.${section}.type" 2>/dev/null > "$f/rules/${section}.type"
	done
}

restore_wdtt_secrets() {
	local f="$SECRETS_BACKUP" v val

	[ -d "$f" ] || return 0
	[ -f /etc/config/wdtt ] || return 0

	for v in peer password hashes hash1 hash2 hash3 hash4 enabled captcha_mode vk_auth_mode obfs_mode go_dns turn_transport tunnel_mode device_id workers routing_mode uplink_iface; do
		[ -f "$f/$v" ] || continue
		[ -s "$f/$v" ] || continue
		val="$(cat "$f/$v")"
		if [ "$v" = "hashes" ]; then
			val="$(printf '%s' "$val" | tr '\n\r\t ' ',' | sed 's/,,*/,/g; s/^,//; s/,$//')"
		fi
		uci -q set "wdtt.globals.${v}=${val}"
	done
	if [ -d "$f/rules" ]; then
		for rf in "$f/rules"/*.domain_list; do
			[ -f "$rf" ] || continue
			section="$(basename "$rf" .domain_list)"
			[ -n "$section" ] || continue
			uci -q get "wdtt.${section}" >/dev/null 2>&1 || uci -q set "wdtt.${section}=rule"
			uci -q set "wdtt.${section}.type=route" 2>/dev/null
			val="$(cat "$rf")"
			[ -n "$val" ] && uci -q set "wdtt.${section}.domain_list=${val}"
			[ -f "$f/rules/${section}.enabled" ] && [ -s "$f/rules/${section}.enabled" ] \
				&& uci -q set "wdtt.${section}.enabled=$(cat "$f/rules/${section}.enabled")"
			[ -f "$f/rules/${section}.type" ] && [ -s "$f/rules/${section}.type" ] \
				&& uci -q set "wdtt.${section}.type=$(cat "$f/rules/${section}.type")"
		done
	fi
	uci -q commit wdtt 2>/dev/null
	msg "Учётные данные WDTT восстановлены"
}

apply_clean_routing_defaults() {
	local section

	uci -q set wdtt.globals.routing_mode='external'
	uci -q set wdtt.globals.captcha_mode='wv'
	uci -q set wdtt.globals.vk_auth_mode='vkcalls'
	uci -q set wdtt.globals.obfs_mode='audio'
	uci -q set wdtt.globals.go_dns='doh-yandex'
	uci -q set wdtt.globals.turn_transport='udp'
	sed -i '/^[[:space:]]*option domain[[:space:]]/d' /etc/config/wdtt 2>/dev/null
	sed -i '/^[[:space:]]*option domains[[:space:]]/d' /etc/config/wdtt 2>/dev/null
	sed -i '/list domain.*2iw/d' /etc/config/wdtt 2>/dev/null
	sed -i '/list domain.*yoltlbe/d' /etc/config/wdtt 2>/dev/null
	# Домены не трогаем при --clean (backup/restore) — только ghost list domain
	for section in $(uci -q show wdtt 2>/dev/null | sed -n "s/^wdtt\\.\\([^.=]*\\)=rule\$/\\1/p"); do
		while uci -q delete "wdtt.${section}.domain" 2>/dev/null; do :; done
		uci -q delete "wdtt.${section}.domains" 2>/dev/null
	done
	uci -q commit wdtt 2>/dev/null
	msg "routing: vk_auth_mode=vkcalls, captcha_mode=wv, routing_mode=external (Podkop)"
}

uninstall_wdtt() {
	msg "============================================"
	msg " WDTT: полное удаление и очистка кэша"
	msg "============================================"

	/etc/init.d/wdtt stop 2>/dev/null
	/etc/init.d/wdtt disable 2>/dev/null
	/usr/libexec/wdtt/routing stop 2>/dev/null

	rm -f /usr/sbin/wdttd
	rm -f /usr/libexec/wdtt/routing
	rm -f /etc/init.d/wdtt
	rm -f /etc/firewall.wdtt
	rm -f /etc/uci-defaults/99-wdtt
	rm -f /etc/hotplug.d/iface/99-wdtt
	rm -f /usr/libexec/rpcd/wdtt
	rm -f /usr/share/luci/menu.d/luci-app-wdtt.json
	rm -f /usr/share/rpcd/acl.d/luci-app-wdtt.json
	rm -f /usr/share/wdtt/version
	rm -f /www/luci-static/resources/view/wdtt/overview.js
	rmdir /www/luci-static/resources/view/wdtt 2>/dev/null

	rm -rf /var/run/wdtt
	rm -f /tmp/dnsmasq.d/wdtt.conf
	rm -f /etc/nftables.d/99-wdtt.nft
	rm -rf /tmp/wdtt-install /tmp/wdtt-list.* /tmp/wdtt-rpcd-log.* 2>/dev/null
	rm -rf /tmp/luci-* 2>/dev/null

	if [ "$WDTT_FRESH_CONFIG" = "1" ]; then
		rm -f /etc/config/wdtt
		msg "  removed /etc/config/wdtt (fresh config on install)"
	fi

	wdtt_iface="$(uci -q get wdtt.globals.iface 2>/dev/null)"
	wdtt_iface="${wdtt_iface:-wg-wdtt}"
	wdtt_table="$(uci -q get wdtt.globals.route_table 2>/dev/null)"
	case "$wdtt_table" in ''|*[!0-9]*) wdtt_table=7477 ;; esac
	while ip rule del fwmark 0x777474 table "$wdtt_table" 2>/dev/null; do :; done
	while ip rule del fwmark 0x777474 table 100 2>/dev/null; do :; done
	[ "$wdtt_table" = "100" ] || ip route flush table "$wdtt_table" 2>/dev/null
	# В таблице 100 может жить ssclash/Clash — сносим только свой маршрут.
	ip route del default dev "$wdtt_iface" table 100 2>/dev/null
	ip route del default dev tun-wdtt table 100 2>/dev/null
	nft delete table inet wdtt 2>/dev/null

	/etc/init.d/dnsmasq reload 2>/dev/null || /etc/init.d/dnsmasq restart 2>/dev/null
	/etc/init.d/firewall reload 2>/dev/null || true
	/etc/init.d/rpcd restart 2>/dev/null || true

	msg "WDTT удалён, кэш очищен"
}

fix_wdtt_legacy() {
	# Старые версии: table в nftables.d ломает fw4
	if [ -f /etc/nftables.d/99-wdtt.nft ]; then
		warn "Удаляем legacy /etc/nftables.d/99-wdtt.nft (ломает firewall)"
		rm -f /etc/nftables.d/99-wdtt.nft
		/etc/init.d/firewall reload 2>/dev/null || true
	fi
	# До v3.19 WDTT жил в таблице 100 — её же занимает ssclash/Clash под TPROXY.
	# Свои остатки убираем точечно, чужие маршруты не трогаем.
	local legacy_iface
	for legacy_iface in wg-wdtt tun-wdtt; do
		ip route del default dev "$legacy_iface" table 100 2>/dev/null \
			&& warn "Убран legacy-маршрут WDTT из таблицы 100 ($legacy_iface)"
	done
	while ip rule del fwmark 0x777474 table 100 2>/dev/null; do :; done

	/usr/libexec/wdtt/routing stop 2>/dev/null || true
	ensure_routing_script 2>/dev/null || true
	[ -x /usr/libexec/wdtt/fix-config ] && /usr/libexec/wdtt/fix-config 2>/dev/null || true
}

check_system() {
	local model openwrt_major space

	model="$(cat /tmp/sysinfo/model 2>/dev/null || echo unknown)"
	msg "Router model: $model"

	openwrt_major="$(grep DISTRIB_RELEASE /etc/openwrt_release 2>/dev/null | cut -d"'" -f2 | cut -d'.' -f1)"
	if [ -n "$openwrt_major" ] && [ "$openwrt_major" -lt 24 ] 2>/dev/null; then
		err "OpenWrt 24.10+ required (found: $(grep DISTRIB_RELEASE /etc/openwrt_release | cut -d"'" -f2))"
		exit 1
	fi

	space="$(df /overlay 2>/dev/null | awk 'NR==2 {print $4}')"
	if [ -n "$space" ] && [ "$space" -lt 20480 ] 2>/dev/null; then
		err "Need at least 20 MB free on /overlay (have: $((space / 1024)) MB)"
		exit 1
	fi

	if ! ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1 && ! ping -c 1 -W 3 77.88.8.8 >/dev/null 2>&1; then
		warn "No internet ping — continuing anyway..."
	fi

	case "$(detect_arch)" in
		aarch64) msg "Architecture: aarch64 (Cudy TR3000 OK)" ;;
		*)       warn "Architecture: $(detect_arch) — primary target is aarch64 (TR3000)" ;;
	esac
}

pkg_is_installed() {
	local name="$1"
	if [ "$PKG_IS_APK" -eq 1 ]; then
		apk info -e "$name" >/dev/null 2>&1
	else
		opkg list-installed 2>/dev/null | grep -q "^${name} "
	fi
}

kmod_loaded() {
	[ -d "/sys/module/$1" ]
}

apk_install_one() {
	local pkg="$1" attempt=0 errf="/tmp/wdtt-apk-$$.log"

	pkg_is_installed "$pkg" && return 0

	if [ "$PKG_IS_APK" -ne 1 ]; then
		opkg install "$pkg" && return 0
		return 1
	fi

	while [ "$attempt" -lt 3 ]; do
		msg "apk add $pkg (try $((attempt + 1))/3)..."
		if apk add "$pkg" >"$errf" 2>&1; then
			rm -f "$errf"
			return 0
		fi
		warn "$(tail -n 2 "$errf" 2>/dev/null | tr '\n' ' ')"
		rm -f /var/cache/apk/*.apk /var/cache/apk/*.adb 2>/dev/null
		apk update >/dev/null 2>&1
		attempt=$((attempt + 1))
		sleep 2
	done
	rm -f "$errf"
	return 1
}

dnsmasq_has_nftset() {
	# НЕ 'grep -qi nftset' — оно матчит 'no-nftset' и даёт ложный OK,
	# из-за чего dnsmasq-full не ставился. Точное слово 'nftset'.
	command -v dnsmasq >/dev/null 2>&1 \
		&& dnsmasq --version 2>/dev/null | tr ' ,' '\n\n' | grep -qx 'nftset'
}

# dnsmasq и dnsmasq-full — взаимоисключающие; для selective routing нужен только full (nftset)
ensure_dnsmasq_full() {
	local errf="/tmp/wdtt-dnsmasq-$$.log"

	if dnsmasq_has_nftset; then
		msg "  OK: dnsmasq (nftset)"
		return 0
	fi

	warn "  dnsmasq без nftset — ставим dnsmasq-full (заменяет dnsmasq)..."

	if [ "$PKG_IS_APK" -eq 1 ]; then
		apk del dnsmasq >/dev/null 2>&1
		if apk add dnsmasq-full >"$errf" 2>&1; then
			rm -f "$errf"
			dnsmasq_has_nftset && msg "  OK: dnsmasq-full (nftset)" && return 0
		fi
		warn "$(tail -n 3 "$errf" 2>/dev/null | tr '\n' ' ')"
		rm -f "$errf"
	else
		opkg remove dnsmasq 2>/dev/null
		opkg install dnsmasq-full 2>/dev/null && dnsmasq_has_nftset && return 0
	fi

	warn "  FAILED: selective routing требует dnsmasq-full:"
	warn "    apk del dnsmasq && apk add dnsmasq-full && /etc/init.d/dnsmasq restart"
	return 1
}

install_dependencies() {
	local pkg failed=0 optional_failed=0

	msg "Checking dependencies..."

	# Критичные — без них туннель не поднимется
	for pkg in wireguard-tools kmod-wireguard; do
		if pkg_is_installed "$pkg" || kmod_loaded wireguard; then
			msg "  OK: $pkg"
		elif apk_install_one "$pkg"; then
			msg "  installed: $pkg"
		else
			warn "  FAILED: $pkg (критично)"
			failed=1
		fi
	done

	ensure_dnsmasq_full || optional_failed=1

	# RAW-режим (tunnel_mode=raw) — /dev/net/tun; без модуля WG-путь не страдает
	if pkg_is_installed kmod-tun || [ -e /dev/net/tun ]; then
		msg "  OK: kmod-tun"
	elif apk_install_one kmod-tun; then
		msg "  installed: kmod-tun"
	else
		warn "  skip: kmod-tun (нужен только для RAW: apk add kmod-tun)"
		optional_failed=1
	fi

	# Selective routing: nft (OpenWrt 25 / fw4)
	for pkg in nftables kmod-nft-core kmod-nft-nat ca-bundle; do
		if pkg_is_installed "$pkg"; then
			msg "  OK: $pkg"
		elif apk_install_one "$pkg"; then
			msg "  installed: $pkg"
		else
			warn "  skip: $pkg (selective routing: apk add $pkg)"
			optional_failed=1
		fi
	done

	# nftables обычно уже в OpenWrt 25 + fw4
	if ! command -v nft >/dev/null 2>&1; then
		apk_install_one nftables || warn "  skip: nftables"
	fi

	modprobe wireguard 2>/dev/null

	if [ "$failed" -eq 1 ]; then
		warn ""
		warn "WireGuard не установился. Сначала почините apk:"
		warn "  ln -sf /bin/uclient-fetch /usr/bin/wget"
		warn "  apk del wget-nossl"
		warn "  apk update && apk add wireguard-tools kmod-wireguard"
		warn "Продолжаем установку WDTT..."
	fi

	if [ "$optional_failed" -eq 1 ]; then
		warn "dnsmasq-full/nftables не установились — selective routing может не работать."
		warn "Полный туннель (routing_mode=full) будет работать."
	fi
}

write_wdtt_version_stamp() {
	mkdir -p /usr/share/wdtt 2>/dev/null || true
	printf '%s\n' "$WDTT_INSTALL_VERSION" > /usr/share/wdtt/version
}

# ensure_wdtt_device_id фиксирует ID устройства во флеше. Сервер привязывает
# пароль к device_id (лимит по умолчанию — одно устройство), поэтому ID обязан
# выживать перезагрузки: /var на OpenWrt — tmpfs, machine-id оттуда меняется.
ensure_wdtt_device_id() {
	local id mac iface

	[ -f /etc/config/wdtt ] || return 0

	id="$(uci -q get wdtt.globals.device_id 2>/dev/null | tr -cd 'A-Za-z0-9._-')"
	if [ -z "$id" ] && [ -f /etc/wdtt/device_id ]; then
		id="$(tr -cd 'A-Za-z0-9._-' < /etc/wdtt/device_id)"
	fi

	if [ -z "$id" ]; then
		for iface in br-lan eth0 eth1 lan br-wan wan; do
			[ -f "/sys/class/net/$iface/address" ] || continue
			mac="$(sed 's/[^0-9A-Fa-f]//g' "/sys/class/net/$iface/address" | tr 'A-Z' 'a-z')"
			[ "${#mac}" = 12 ] && [ "$mac" != "000000000000" ] || continue
			id="openwrt-$mac"
			break
		done
	fi

	[ -n "$id" ] || return 0

	mkdir -p /etc/wdtt 2>/dev/null || true
	printf '%s\n' "$id" > /etc/wdtt/device_id 2>/dev/null || true
	uci -q set "wdtt.globals.device_id=$id"
	uci -q commit wdtt 2>/dev/null
	msg "  device_id: $id"
}

post_install() {
	if [ ! -f /etc/config/wdtt ]; then
		download_file "$RAW_URL/luci-app-wdtt/root/etc/config/wdtt" /etc/config/wdtt \
			|| download_file "${JSDELIVR_PIN}/luci-app-wdtt/root/etc/config/wdtt" /etc/config/wdtt \
			|| true
	fi

	if [ "$WDTT_KEEP_SECRETS" = "1" ] || [ "$WDTT_CLEAN" = "1" ]; then
		restore_wdtt_secrets
	fi

	if [ "$WDTT_FRESH_CONFIG" = "1" ] || [ "$WDTT_CLEAN" = "1" ]; then
		apply_clean_routing_defaults
	fi

	# v3.14+: DoH по умолчанию; v3.16+: транспорт TURN — если опций нет
	if [ -f /etc/config/wdtt ]; then
		uci -q get wdtt.globals.go_dns >/dev/null 2>&1 \
			|| uci -q set wdtt.globals.go_dns='doh-yandex'
		uci -q get wdtt.globals.turn_transport >/dev/null 2>&1 \
			|| uci -q set wdtt.globals.turn_transport='udp'
		uci -q get wdtt.globals.tunnel_mode >/dev/null 2>&1 \
			|| uci -q set wdtt.globals.tunnel_mode='wg'
		uci -q commit wdtt 2>/dev/null
	fi

	fix_wdtt_legacy

	ensure_wdtt_device_id

	write_wdtt_version_stamp

	[ -x /etc/uci-defaults/99-wdtt ] && /etc/uci-defaults/99-wdtt || true

	rm -rf /tmp/luci-* 2>/dev/null || true
	/etc/init.d/rpcd restart 2>/dev/null || true
	/etc/init.d/wdtt enable 2>/dev/null || true

	if [ -x /usr/libexec/wdtt/doctor ]; then
		msg ""
		msg "Запуск wdtt-doctor..."
		/usr/libexec/wdtt/doctor 2>/dev/null || true
	fi

	if [ -f /opt/clash/config.yaml ] || [ -x /opt/clash/bin/ssclash ] || [ -f /etc/config/openclash ]; then
		msg ""
		msg "Обнаружен Clash (SSClash-Go/ssclash/OpenClash) — проверка совместной работы:"
		/usr/libexec/wdtt/clash status 2>/dev/null || true
		msg "  Подробнее: /usr/libexec/wdtt/clash hint"
	fi

	msg ""
	msg "============================================"
	msg " WDTT v${WDTT_INSTALL_VERSION} installed!"
	msg " LuCI: Services → WDTT VPN"
	msg "============================================"
	msg ""
	msg "1) Туннель (LuCI или SSH):"
	msg "   peer, password, VK-hashes, enabled=1"
	msg "   vk_auth_mode=vkcalls — TURN без капчи (рекомендуется)"
	msg "   captcha_mode=wv — fallback при legacy или captcha gate"
	msg ""
	msg "2) Selective routing (после connected — автоматически):"
	msg "   LuCI → Правила маршрутизации → Домены (domain_list)"
	msg "   Пример: youtube.com, 2ip.io — только через LuCI, без option domain"
	msg ""
	msg "3) Проверка:"
	msg "   /usr/libexec/wdtt/routing status"
	msg "   nslookup example.com 127.0.0.1"
	msg "   nft list set inet wdtt wdtt_route"
	msg ""
	msg "Quick start:"
	msg "  uci set wdtt.globals.peer='VPS:56000'"
	msg "  uci set wdtt.globals.password='password'"
	msg "  uci set wdtt.globals.hashes='vk_hash'"
	msg "  uci set wdtt.globals.vk_auth_mode='vkcalls'"
	msg "  uci set wdtt.globals.captcha_mode='wv'"
	msg "  uci set wdtt.globals.enabled='1'"
	msg "  uci set wdtt.route1.enabled='1'"
	msg "  uci set wdtt.route1.domain_list='youtube.com,2ip.ru'"
	msg "  uci commit wdtt && /etc/init.d/wdtt restart"
}

main() {
	local arg

	if [ "$(id -u)" != "0" ]; then
		err "Run as root on the router"
		exit 1
	fi

	for arg in "$@"; do
		case "$arg" in
			--clean|--reinstall)
				WDTT_CLEAN=1
				WDTT_KEEP_SECRETS=1
				WDTT_FRESH_CONFIG=1
				;;
			--uninstall)
				WDTT_UNINSTALL_ONLY=1
				;;
		esac
	done

	msg "WDTT installer v${WDTT_INSTALL_VERSION}"

	if [ "$WDTT_UNINSTALL_ONLY" = "1" ]; then
		backup_wdtt_secrets
		WDTT_FRESH_CONFIG=1
		uninstall_wdtt
		rm -rf "$SECRETS_BACKUP"
		exit 0
	fi

	rm -rf "$DOWNLOAD_DIR"
	mkdir -p "$DOWNLOAD_DIR"

	fix_broken_wget

	if [ "$WDTT_CLEAN" = "1" ]; then
		backup_wdtt_secrets
		uninstall_wdtt
		msg "Чистая установка WDTT..."
	fi

	check_system
	check_github_access

	msg "Installing WDTT..."

	# Сразу ставим из GitHub (release .apk у нас нет — только бинарник)
	warn "Installing from GitHub release..."
	install_from_source || exit 1

	# Зависимости через apk — только если зеркала доступны
	fix_broken_wget
	if [ "$WDTT_SKIP_DEPS" = "1" ]; then
		warn "Пропуск apk (WDTT_SKIP_DEPS=1) — wireguard/dnsmasq должны быть уже установлены"
	elif apk update >/dev/null 2>&1; then
		install_dependencies
	else
		warn "apk update failed — WDTT установлен, но WireGuard нужно поставить вручную:"
		warn "  ln -sf /bin/uclient-fetch /usr/bin/wget"
		warn "  apk del wget-nossl && apk update"
		warn "  apk add wireguard-tools kmod-wireguard"
	fi

	post_install
	verify_install

	rm -rf "$DOWNLOAD_DIR"
}

main "$@"
