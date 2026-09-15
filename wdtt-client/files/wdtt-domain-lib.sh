#!/bin/sh
# Общие функции для domain_list (fix-config, set-domains).

normalize_domain() {
	local d="$1"
	d="${d#https://}"
	d="${d#http://}"
	d="${d%%/*}"
	d="${d%/}"
	# ВАЖНО: диапазоны A-Z/a-z, а не [:upper:]/[:lower:] — на некоторых busybox
	# классы в tr трактуются буквально и калечат домен (youtube→yoltlbe и т.п.).
	printf '%s' "$d" | tr 'A-Z' 'a-z'
}

is_ghost_domain() {
	case "$1" in
	2iw.*|*.2iw.*|*2iw.rl|*2iw.io) return 0 ;;
	yoltlbe.com|*.yoltlbe.com|yoltlbe) return 0 ;;
	esac
	return 1
}

# Склеенные домены: googlevideo.comgooglevideo.com → отдельные слова.
# busybox-safe: без alternation, перебор TLD в цикле. Никогда не возвращает
# пусто, если вход непустой (при сбое sed откатываемся на предыдущее значение).
defragment_domain_list_text() {
	local text="$1" prev="" out tld
	[ -n "$text" ] || return 0
	out="$text"
	while [ "$out" != "$prev" ]; do
		prev="$out"
		for tld in com ru net org io tv me info biz dev app xyz online site pro cc rf su; do
			out="$(printf '%s' "$out" | sed "s/\\.${tld}\\([a-z0-9]\\)/.${tld} \\1/g" 2>/dev/null)"
			[ -n "$out" ] || { out="$prev"; break; }
		done
	done
	printf '%s' "$out"
}

# Нормализация списка доменов → CSV.
# busybox-safe: разбор только через parameter expansion, БЕЗ for..in / IFS /
# многосимвольного tr. Раздельные однобайтовые замены.
normalize_domain_list_text() {
	local text="$1" work dfg item d out=""

	# lower-case (диапазоны, а не [:class:] — максимальная совместимость)
	work="$(printf '%s' "$text" | tr 'A-Z' 'a-z')"

	# все разделители → запятая (по одному символу за раз)
	work="$(printf '%s' "$work" | tr ';' ',')"
	work="$(printf '%s' "$work" | tr ' ' ',')"
	work="$(printf '%s' "$work" | tr '\t' ',')"
	work="$(printf '%s' "$work" | tr '\n' ',')"
	work="$(printf '%s' "$work" | tr '\r' ',')"

	# разбить склеенные домены (best-effort); defrag вставляет пробелы → в запятые
	dfg="$(defragment_domain_list_text "$work")"
	[ -n "$dfg" ] && work="$dfg"
	work="$(printf '%s' "$work" | tr ' ' ',')"

	# split по запятым без for..in / IFS
	while [ -n "$work" ]; do
		case "$work" in
		*,*) item="${work%%,*}"; work="${work#*,}" ;;
		*)   item="$work"; work="" ;;
		esac
		[ -n "$item" ] || continue
		d="$(normalize_domain "$item")"
		[ -n "$d" ] || continue
		is_ghost_domain "$d" && continue
		case "$d" in *.*) ;; *) continue ;; esac
		case ",$out," in *",$d,"*) continue ;; esac
		[ -n "$out" ] && out="${out},${d}" || out="$d"
	done
	printf '%s' "$out"
}

# Объединить два списка доменов. КРИТИЧНО: ровно одна ветка (иначе склейка).
merge_domain_list_text() {
	local a="$1" b="$2"
	if [ -n "$a" ] && [ -n "$b" ]; then
		normalize_domain_list_text "$a,$b"
	elif [ -n "$a" ]; then
		normalize_domain_list_text "$a"
	elif [ -n "$b" ]; then
		normalize_domain_list_text "$b"
	fi
}

wdtt_uci_clear_domain_list() {
	local section="$1"
	[ -n "$section" ] || return 1
	[ -x /sbin/uci ] || return 0

	uci -q delete "wdtt.${section}.domain_list" 2>/dev/null
	while uci -q delete "wdtt.${section}.domain_list[0]" 2>/dev/null; do :; done
	while uci -q delete "wdtt.${section}.domain[0]" 2>/dev/null; do :; done
	while uci -q delete "wdtt.${section}.domains[0]" 2>/dev/null; do :; done
	while uci -q delete "wdtt.${section}.domain" 2>/dev/null; do :; done
}

wdtt_uci_set_domain_list() {
	local section="$1" domains="$2"
	domains="$(normalize_domain_list_text "$domains")"
	[ -n "$section" ] || return 1
	[ -n "$domains" ] || return 1
	[ -x /sbin/uci ] || return 0

	wdtt_uci_clear_domain_list "$section"
	uci -q batch <<-EOB
		set wdtt.${section}.domain_list='${domains}'
		commit wdtt
	EOB
}

# Починить склеенные domain_list во всех rule-секциях (идемпотентно).
wdtt_sanitize_all_rule_domain_lists() {
	local section val fixed
	[ -x /sbin/uci ] || return 0

	for section in $(uci -q show wdtt 2>/dev/null | sed -n "s/^wdtt\.\\([^.=]*\\)=rule\$/\\1/p"); do
		val="$(uci -q get "wdtt.${section}.domain_list" 2>/dev/null)" || continue
		fixed="$(normalize_domain_list_text "$val")"
		[ -n "$fixed" ] || continue
		[ "$val" = "$fixed" ] && continue
		wdtt_uci_set_domain_list "$section" "$fixed"
		logger -t wdtt-domain "sanitized glued domain_list: ${section} (${fixed})"
	done
}

is_glued_domain() {
	case "$1" in
	*\.com[a-z0-9]*)
		case "$1" in
		*\.com\.[a-z]*) return 1 ;;
		esac
		return 0
		;;
	esac
	return 1
}
