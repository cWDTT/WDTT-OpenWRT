# WDTT OpenWRT — Cudy TR3000 256MB

OpenWRT-клиент WDTT (WireGuard over VK TURN) с полным или выборочным туннелем и отдельными правилами маршрутизации.

## Быстрая установка на роутер

### Рабочие ссылки v3.18.4 (список профилей + повтор запроса конфига)

| Назначение | URL |
|------------|-----|
| **Установщик** | `https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh` |
| routing (selective) | `https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@1933519/wdtt-client/files/wdtt-routing` |
| firewall-refresh | `https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@1933519/wdtt-client/files/wdtt-firewall-refresh` |
| push-domain-fix (с ПК) | `sh scripts/push-domain-fix.sh root@IP` |

Установка одной командой (pin):

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh
sh /tmp/wdtt-install.sh
```

Альтернатива через `@main` (внутри pin на коммит для остальных файлов; jsDelivr может кэшировать 5–15 мин):

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@main/install.sh
sh /tmp/wdtt-install.sh
```

### Вариант A — wget (если HTTPS работает)

Сначала проверьте, что wget не `wget-nossl`:

```bash
readlink -f /usr/bin/wget
```

Должно быть `/bin/uclient-fetch` или `wget-ssl`. Если `wget-nossl`:

```bash
ln -sf /bin/uclient-fetch /usr/bin/wget
apk del wget-nossl
```

Установка одной командой:

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh
sh /tmp/wdtt-install.sh
```

Или через `@main` (если pin недоступен):

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@main/install.sh
sh /tmp/wdtt-install.sh
```

**Чистая переустановка** (удаление, очистка кэша, свежий конфиг; peer/password/hashes **сохраняются**):

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh
sh /tmp/wdtt-install.sh --clean
```

**Полное удаление** (WDTT снят с роутера, `/etc/config/wdtt` **удаляется**):

```bash
wget -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh
sh /tmp/wdtt-install.sh --uninstall
```

Если `install.sh` уже на роутере: `sh /tmp/wdtt-install.sh --uninstall`.

В LuCI **ничего отключать не нужно** — установщик сам вызывает `stop`, снимает routing (`wg-wdtt`, nft, ip rule) и удаляет LuCI-файлы. Кнопка «Отключить» нужна только если хотите временно остановить туннель **без** удаления.

При зависшем `wdttd` перед `--uninstall`:

```bash
/etc/init.d/wdtt stop
killall wdttd 2>/dev/null
ip link del wg-wdtt 2>/dev/null
sh /tmp/wdtt-install.sh --uninstall
```

Проверка после удаления:

```bash
ls /usr/sbin/wdttd /etc/config/wdtt 2>&1
pgrep wdttd || echo "OK: wdttd not running"
```

| Команда | Конфиг UCI | LuCI |
|--------|------------|------|
| `--uninstall` | удаляется | удаляется |
| `--clean` | сохраняется (peer/password/hashes) | переустанавливается |

После `--clean`: `vk_auth_mode=vkcalls`, `captcha_mode=wv`, **домены пустые** — добавьте в LuCI → Правила маршрутизации. Проверьте peer/password/hashes → Подключить.

Должно быть `WDTT installer v3.18.4+`, проверки `[OK] routing (nft+nftset)`, `dnsmasq nftset`, `firewall lan→wdtt`.

После **Подключить** datapath (selective/full) поднимается сам: `/usr/libexec/wdtt/datapath ensure`. Ручной `routing start` не нужен.

**wdttd** качается с **jsDelivr** (`bin/wdttd-linux-arm64` в репо) — GitHub Releases с роутера не обязателен.

**Selective routing** требует **`dnsmasq-full`** (nftset). Пакет `dnsmasq` без `-full` не подходит — они взаимоисключающие на OpenWrt.

**Если что-то не работает — одна команда:**

```bash
/usr/libexec/wdtt/doctor
```

(после install v3.6.7+; чинит config, перезагружает routing, показывает статус)

LuCI **Подключить / Отключить** (v3.6.7+) — через ubus, без зависания на Save.

### Нет трафика в любом режиме (v3.10.0)

Частая причина: `wg-wdtt` поднят, а **NAT/зона firewall** не привязаны (selective раньше не делал `fw4 reload`).

```bash
/usr/libexec/wdtt/firewall-refresh wg-wdtt
/usr/libexec/wdtt/doctor
# смотри: handshake, masquerade, lan→wdtt, endpoint 127.0.0.1:9000
```

Если `FAIL: нет свежего handshake` — проблема TURN/uplink, не правил:
`uci get wdtt.globals.uplink_iface; /usr/libexec/wdtt/uplink status`

### Selective: трафик есть, но медленно (v3.10.0)

1. **MTU** — LuCI → MTU **1240**. WG→DTLS→TURN: выше 1280 часто даёт фрагментацию.
2. **dnsmasq-full** — обязателен для nftset: `apk del dnsmasq && apk add dnsmasq-full`
3. **flow offloading** — выключить: `uci set firewall.@defaults[0].flow_offloading=0; uci set firewall.@defaults[0].flow_offloading_hw=0; uci commit firewall; /etc/init.d/firewall reload`
4. **routing v3.10.0+** — MSS clamp + `rp_filter=loose` + firewall-refresh:

```bash
uclient-fetch -O /usr/libexec/wdtt/routing \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@1933519/wdtt-client/files/wdtt-routing
uclient-fetch -O /usr/libexec/wdtt/firewall-refresh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@1933519/wdtt-client/files/wdtt-firewall-refresh
chmod 755 /usr/libexec/wdtt/routing /usr/libexec/wdtt/firewall-refresh
/usr/libexec/wdtt/firewall-refresh wg-wdtt
/usr/libexec/wdtt/routing reload wg-wdtt
/usr/libexec/wdtt/doctor
```

### Домены не сохраняются / склеиваются / wdtt.conf пуст (v3.10.0)

Обновление с ПК (без полной переустановки):

```bash
sh scripts/push-domain-fix.sh root@192.168.10.1
```

В браузере **Ctrl+F5** → LuCI → Правила → **Принять изменения**.

Ручная запись:

```bash
/usr/libexec/wdtt/set-domains route1 youtube.com,googlevideo.com
/usr/libexec/wdtt/doctor
```

После установки: LuCI → **WDTT VPN** → peer/password/hashes → **VKCalls** → **Правила** → домены → **Принять изменения** → Подключить.

Routing поднимается **авоматически** при `connected`. Проверка:

```bash
/usr/libexec/wdtt/routing status
nslookup 2ip.io 127.0.0.1
nft list set inet wdtt wdtt_route
```

Если `wdtt_route` пуст — обновите routing-скрипт (v3.6.3+) и перезапустите:

```bash
/usr/libexec/wdtt/routing reload wg-wdtt
```

Если jsDelivr и GitHub недоступны с роутера, скопируйте бинарник с ПК:

```bash
# на ПК: скачайте wdttd-linux-arm64 с Releases
scp wdttd-linux-arm64 root@192.168.1.1:/tmp/wdttd
ssh root@192.168.1.1 'WDTT_LOCAL_BIN=/tmp/wdttd sh /tmp/wdtt-install.sh'
```

### Вариант B — uclient-fetch

```bash
uclient-fetch -q -O /tmp/wdtt-install.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@d6e3123/install.sh
sh /tmp/wdtt-install.sh
```

### Вариант C — bootstrap (зеркала + fallback)

```bash
uclient-fetch -q -O /tmp/wdtt-bootstrap.sh \
  https://cdn.jsdelivr.net/gh/cWDTT/WDTT-OpenWRT@main/bootstrap.sh
sh /tmp/wdtt-bootstrap.sh
```

Должно появиться `WDTT installer v3.2` и шаги `Step 1/3`, `Step 2/3`, `Step 3/3`.

### Вариант D — установка с компьютера (GitHub заблокирован на роутере)

На ПК (Linux/macOS), роутер в локальной сети:

```bash
git clone https://github.com/cWDTT/WDTT-OpenWRT.git
cd WDTT-OpenWRT
sh scripts/install-from-pc.sh root@192.168.1.1
```

Скрипт скачает `install.sh` и бинарник на ПК, скопирует на роутер по `scp` и запустит установку.

> **Не ставьте** `apk add wget` — на OpenWrt это часто `wget-nossl` без SSL и снова сломает `apk`.

### apk сломан (`unexpected end of file` на всех зеркалах)

Старый установщик поставил **wget-nossl** — он ломает HTTPS для apk. Починка:

```bash
ln -sf /bin/uclient-fetch /usr/bin/wget
apk del wget-nossl
apk update
apk add wireguard-tools kmod-wireguard
```

Или:

```bash
sh <(uclient-fetch -q -O - https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/main/scripts/fix-apk.sh)
```

Затем снова установщик WDTT (команда выше).

### Приватный репозиторий (опционально)

```bash
export GITHUB_TOKEN="github_pat_..."
sh <(uclient-fetch --header="Authorization: Bearer $GITHUB_TOKEN" -q -O - \
  https://raw.githubusercontent.com/cWDTT/WDTT-OpenWRT/main/install.sh)
```

Требуется OpenWrt **24.10+** / **25.x** (apk), интернет, ~20 МБ свободного места.

## Целевое устройство

**Cudy TR3000 256MB** (и совместимые):

| Параметр | Значение |
|----------|----------|
| SoC | MediaTek MT7981 (Filogic 820) |
| CPU | 2× Cortex-A53 @ 1.3 GHz |
| RAM | 512 МБ |
| Flash | 256 МБ NAND |
| Архитектура | `aarch64_cortex-a53` |
| OpenWrt target | `mediatek/filogic` |

Прошивка: `openwrt-25.12.4-mediatek-filogic-cudy_tr3000-256mb-v1-...`

## Состав

| Пакет | Описание |
|-------|----------|
| `wdtt-client` | Go-демон `wdttd` + selective routing |
| `luci-app-wdtt` | LuCI: туннель, правила, статус, логи |

## Туннель и правила — как это устроено

**Туннель** (кнопки **Подключить / Отключить**) — это WireGuard `wg-wdtt` и демон `wdttd`. Пока туннель открыт, он живёт отдельно от списка доменов.

**Правила маршрутизации** (LuCI → секции `rule`, **Save & Apply**) — только для режима **выборочная**: какой трафик пускать в уже открытый туннель. Добавление, изменение или удаление правила **не рвёт туннель** — применяется `routing reload` без перезапуска `wdttd`.

| Действие | Туннель | Правила |
|----------|---------|---------|
| **Подключить** | поднимается | применяются (selective) |
| **Отключить** | опускается | снимаются |
| **Save & Apply** (домены, rule) | **остаётся** | перечитываются |
| Смена peer/password/hashes | нужен **Отключить → Подключить** | — |

Режим **полный туннель** — весь трафик через WDTT; секции `rule` не используются.

Режим **external / Podkop** (v3.13.2+, **по умолчанию**) — WDTT только поднимает `wg-wdtt` + firewall/NAT; **что** идёт в туннель решает **Podkop** (sing-box). Правила WDTT не используются.

```bash
# Проверка связки WDTT + Podkop
/usr/libexec/wdtt/podkop status
/usr/libexec/wdtt/doctor
```

## Маршрутизация

По умолчанию режим **selective** — в туннель идут только выбранные ресурсы:

1. **Правила `route`** — домены (через dnsmasq **nftset** → nft sets), подсети, URL-списки
2. **Правила `exclusion`** — трафик напрямую
3. **`routing_excluded_ip`** — устройства, которые всегда мимо туннеля (высший приоритет)
4. **`source_ip`** в правиле — весь трафик выбранного устройства через WDTT

Режим **full** — весь трафик роутера через WDTT.

Режим **external** — туннель без маршрутов WDTT:

```bash
uci set wdtt.globals.routing_mode='external'
uci commit wdtt
# LuCI: Отключить → Подключить
```

Затем в **Podkop** → VPN interface: `wg-wdtt` (как обычный WireGuard: Route Allowed IPs **выкл.**).

```
Приоритет: routing_excluded_ip > source_ip (full device) > domain/subnet lists
```

## OpenWrt 25.12 — пакетный менеджер APK

В 25.x вместо `opkg` используется **apk** (Alpine Package Keeper).

| opkg | apk |
|------|-----|
| `opkg update` | `apk update` |
| `opkg install pkg` | `apk add pkg` |
| `opkg remove pkg` | `apk del pkg` |
| `opkg list-installed` | `apk info` |
| `opkg upgrade` | `apk upgrade` |

[Официальный cheatsheet opkg → apk](https://openwrt.org/docs/guide-user/additional-software/opkg-to-apk-cheatsheet)

### Установка на роутер (готовые .apk)

```bash
apk update
apk add wdtt-client luci-app-wdtt
/etc/init.d/rpcd restart
/etc/init.d/wdtt enable
```

## Сборка из исходников

```bash
cd openwrt

echo 'src-link wdtt /path/to/WDTT_OpenWRT' >> feeds.conf.default
./scripts/feeds update wdtt
./scripts/feeds install wdtt-client luci-app-wdtt

# Target: MediaTek Ralink ARM → Filogic 8x0 (MT798x)
# Subtarget: filogic
# Target Profile: Cudy TR3000 256MB v1
make menuconfig

make package/wdtt-client/compile V=s
make package/luci-app-wdtt/compile V=s
```

Готовые пакеты: `bin/packages/aarch64_cortex-a53/wdtt/`

## Настройка

### LuCI

**Сервисы → WDTT VPN**:
1. VPS (`IP:56000`), пароль, VK-хеши
2. **Интернет (uplink)** — Авто / WAN / WWAN (кнопки в «Статус» или список в настройках): через какой канал идут VK/TURN
3. **Обфускация RTP** — Audio (по умолчанию) или Video (см. ниже)
4. **Подключить** — поднимается туннель (полный или выборочный — см. «Режим туннеля»)
5. В режиме **выборочный**: добавьте правила (домены, устройства) → **Save & Apply** (туннель остаётся открытым)
6. **Отключить** — туннель опускается

### UCI

```bash
uci set wdtt.globals.enabled='1'
uci set wdtt.globals.peer='203.0.113.10:56000'
uci set wdtt.globals.password='your-password'
uci set wdtt.globals.hashes='abc123'
uci set wdtt.globals.routing_mode='external'   # или selective / full
uci set wdtt.globals.uplink_iface='auto'
uci set wdtt.globals.workers='12'
uci set wdtt.globals.obfs_mode='audio'   # или video — только если VPS принимает PT 96
uci set wdtt.globals.turn_transport='udp'   # tcp — если провайдер душит UDP

# selective: домены через WDTT; external: домены в Podkop
uci set wdtt.youtube=rule
uci set wdtt.youtube.enabled='1'
uci set wdtt.youtube.type='route'
uci set wdtt.youtube.domain_list='youtube.com,2ip.ru'
# youtube.com автоматически добавляет CDN: googlevideo, ytimg, ggpht, gvt1, …

uci commit wdtt
/etc/init.d/wdtt restart
```

## Рекомендации для TR3000

| Параметр | Значение |
|----------|----------|
| Потоки (`workers`) | 12 (макс. 24) |
| MTU | 1240 (WG→DTLS→TURN оверхед; выше — фрагментация и просадка скорости) |
| Режим | selective |
| Место на flash | ~15 МБ (бинарник + зависимости) |

512 МБ RAM достаточно для WDTT + dnsmasq nftset + LuCI.

## Архитектура

```
Подключить/Отключить → wdttd → wg-wdtt (туннель)
Save & Apply правил → routing reload (только selective, туннель не трогаем)

wdttd
  ├── core (VK TURN / DTLS)
  ├── wg-wdtt
  └── /usr/libexec/wdtt/routing  (selective)
        ├── dnsmasq nftset → inet wdtt (домены)
        ├── nft prerouting fwmark 0x777474
        └── ip rule → table 100 → wg-wdtt
```

## Обфускация RTP (obfs_mode, v3.13.2+)

LuCI → **Обфускация RTP** / UCI `wdtt.globals.obfs_mode`:

| Режим | Описание |
|-------|----------|
| **audio** (по умолчанию) | OPUS PT 111 — совместимо со всеми серверами |
| **video** | PT 96 + больший padding |

**Video mode имеет смысл только если на VPS `wdtt-server` принимает PT 96.** Старый сервер без поддержки PT 96 — оставляйте `audio`, иначе трафик не пойдёт.

```bash
uci set wdtt.globals.obfs_mode='audio'   # безопасно
uci set wdtt.globals.obfs_mode='video'   # нужен сервер с PT 96
uci commit wdtt && /etc/init.d/wdtt restart
```

## DNS для VK API (go_dns, v3.14.0+)

Резолвер имён для запросов к VK/TURN (не DNS клиентов в LAN). По умолчанию **DoH Яндекс** — обходит блокировки обычного DNS.

LuCI → **DNS для VK API** / UCI `wdtt.globals.go_dns`:

| Значение | Описание |
|----------|----------|
| **doh-yandex** (по умолчанию) | DoH `77.88.8.8` / Yandex |
| **doh-cloudflare** | DoH Cloudflare |
| **doh-google** | DoH Google |
| **yandex** / **cloudflare** / **google** | Обычный UDP :53 |
| **custom:1.2.3.4** | Свой UDP DNS |
| **doh:https://…** | Свой DoH endpoint |

```bash
uci set wdtt.globals.go_dns='doh-yandex'
uci commit wdtt && /etc/init.d/wdtt restart
```

Также в v3.14: DTLS handshake timeout 50s, `SO_REUSEADDR` на listen UDP (быстрый рестарт).

## Импорт wdtt:// / qwdtt:// (v3.15.1+)

LuCI → **Импорт профиля**: вставьте ссылку из Telegram-бота сервера или Android qWDTT — заполнятся `peer`, пароль, хеши, потоки.

```text
wdtt://203.0.113.10:56000:56001:9000:пароль:vk_hash
qwdtt://config?name=Дом&peer=1.2.3.4:56000&hashes=хеш1,хеш2&workers=18&port=9000&pass=секрет
```

Также принимается JSON профиля (как в `.qwdtt`). После импорта нажмите **Подключить**.

Если видите `No related RPC reply` — обновите до **v3.15.1+**, затем `/etc/init.d/rpcd restart` и перелогиньтесь в LuCI.

## Транспорт TURN (turn_transport, v3.16.0+)

По умолчанию до TURN-relay идёт **UDP** — он быстрее. Если провайдер душит или дропает UDP (замечено у части мобильных операторов), туннель либо не поднимается, либо еле шевелится. Тогда переключите на **TCP**: pion/turn заворачивает STUN/ChannelData в TCP-поток до того же relay. Задержка выше, зато соединение проходит.

LuCI → **Транспорт TURN** / UCI `wdtt.globals.turn_transport`:

```bash
uci set wdtt.globals.turn_transport='tcp'   # udp (по умолчанию) | tcp
uci commit wdtt && /etc/init.d/wdtt restart
```

## VK-хеши 1–4 и ход подключения (v3.17.0)

В LuCI четыре отдельных поля **VK-хеш 1…4** — как в qWDTT (один хеш = одна группа из 9 потоков). Старое поле `hashes` по-прежнему пишется автоматически (через запятую) и читается демоном вместе с `hash1`…`hash4`.

При 4 хешах ставьте **36 потоков**, чтобы у каждой ссылки была своя группа. Один хеш и 12 потоков округлятся до 9.

### Одна ссылка на несколько групп (v3.18.2)

Хеш звонка — непрозрачный токен VK, из одной ссылки **нельзя вывести** ещё три: живость проверяет сам VK (`messages.getCallPreview`), и любой самодельный хеш даст `Invalid join link (error_code=954)`.

Отдельные хеши для этого и не нужны. Группа берёт хеш по кругу (`hashIndex % len(hashes)`), поэтому **одна** ссылка в «VK-хеш 1» + **36 потоков** = 4 группы по 9 воркеров на этой ссылке. Дублировать её в поля 2–4 бессмысленно: одинаковые хеши схлопываются при разборе. Каждая группа при этом идёт в VK как отдельный аноним — свой `device_id`, имя, anonymous token и свои TURN-креды (кеш кредов у групп раздельный).

Клиент подсказывает перекос в логе:

```
[Основной] Хешей=4, Потоков=18
[Основной] Используются только первые 2 хеша(ей): при 4 хешах поставьте Потоки=36
```

Оговорка: 4 группы на одной ссылке дают вчетверо больше обращений к одному звонку — выше риск flood control VK (`error 29`, `rate limit`), и смерть ссылки роняет туннель целиком. 2–4 разных живых звонка надёжнее.

Мёртвый хеш больше **не блокирует остальные**: группа передаёт эстафету дальше даже когда её ссылка мертва (до v3.18.2 следующая группа навсегда висела в «Ожидание сигнала от предыдущей группы» со живым хешем). Если не поднялась ни одна группа, в статус уходит явная ошибка вместо тихих `Активных: 0`.

### Конфиг WireGuard запрашивает любая живая группа (v3.18.3)

До v3.18.3 конфиг у сервера просила **только группа #1**. Если её ссылка была мертва, остальные группы поднимали TURN и DTLS, писали `[READY] Туннель готов к работе`, но конфиг не приходил никогда: интерфейса `wg-wdtt` не существовало, `wg show` был пуст, а трафик стоял на `0.00 МБ` при десятках активных воркеров.

Теперь право запросить конфиг общее для всех групп (`configSent` / `configInFlight` разделяются через `Core`), поэтому его берёт первая же группа, которая получила креды. Запрос по-прежнему делает ровно один воркер за раз и ровно один раз всего.

Признак этой болезни на старых версиях: `Активных: 27` вместо 36 (нет ровно 9 воркеров группы #1) и пустой `wg show`.

### Повторный запрос конфига и список профилей (v3.18.4)

Неудачная попытка получить конфиг больше не «съедает» право на запрос. Раньше воркер после таймаута уходил в `[READY]` и жил дальше, а право возвращалось в пул только при смерти сессии — то есть конфиг не запрашивал больше никто. Теперь такая попытка завершает сессию ошибкой, воркер уходит на backoff и пробует снова, поэтому конфиг подхватится сам, как только сервер начнёт отвечать. Закрытие внутренних pipes переехало в `defer`, чтобы ранние выходы ничего не оставляли открытым.

Типичный лог в RAW-режиме, когда VPS запущен **без** `-listen-raw`:

```
[ВОРКЕР #10] Ошибка RAW-конфига: чтение ответа RAWCONF: read udp …: i/o timeout
```

Проверьте флаги сервера на VPS (`ps ax | grep wdtt`) — без `-listen-raw` режим RAW работать не может, вернитесь на `tunnel_mode=wg`.

Список **Профили подключения** в LuCI больше не пустой. Он наполнялся функцией, которая искала `<select>` через `getElementById` сразу после `m.render()` — в этот момент форма ещё не вставлена в документ, поиск давал `null`, и список молча оставался пустым (сами профили при этом исправно лежали в UCI). Теперь `<select>` наполняется прямо при рендере.

Блок **«Ход подключения»** повторяет лог старта qWDTT:

```
[Основной] Хешей=2, Потоков=18
[СЕТЬ] Режим: VPN (WireGuard over VK TURN/DTLS)
[СЕТЬ] DNS: doh-yandex
[СЕТЬ] Маскировка: Аудиозвонок (OPUS)
[КЛИЕНТ] Режим VK: vkcalls
[WRAP] Ключ выведен из пароля, RTP AEAD активен
[ГРУППА #1] Креды OK …
[WG] Туннель wg-wdtt поднят
```

По умолчанию трафик идёт **WireGuard + DTLS** (`tunnel_mode=wg`). Можно включить **RAW** — сырые IP без WireGuard и без DTLS, как на Android qWDTT (`-mode rawtun`).

Из qWDTT 1.4.3 также перенесены быстрый старт воркеров (75 мс stagger, эстафета групп ~0.5 с) и быстрый reconnect при EOF / broken pipe.

## Профили подключения и цвет логов

В LuCI блок **Профили подключения**: несколько серверов (peer/пароль/хеши/RAW) сохраняются в UCI `config profile` и переключаются одной кнопкой. Если туннель уже включён, «Переключить» перезапускает его. Импорт `wdtt://` / `qwdtt://` сразу кладёт набор в профили.

В **Ходе подключения** и **Логе wdttd** ошибки (`FATAL`, `DENIED`, `954`, «хеш мёртв», `Ошибка`) красные, предупреждения жёлтые, успешные шаги зелёные.

```bash
uci show wdtt | grep '=profile'
```

## RAW-режим (tunnel_mode=raw)

Обычный WDTT-сервер (GETCONF + DTLS + WireGuard) в RAW **не** заработает. Нужен qWDTT/PWDTT с `-listen-raw`: клиент шлёт `GETCONF_RAW:deviceID|password`, сервер отвечает `RAWCONF:ip|dns|mtu`, дальше IP-пакеты идут по TURN + RTP-obfs AEAD на интерфейс `tun-wdtt`.

LuCI → **Транспорт** / UCI:

```bash
uci set wdtt.globals.tunnel_mode='raw'   # wg (по умолчанию) | raw
uci commit wdtt && /etc/init.d/wdtt restart
```

На роутере нужен `kmod-tun` (`/dev/net/tun`). Порт RAW на VPS часто **не** тот же, что DTLS (в `.conf` Windows/Android `Endpoint` — это DTLS). Если переключили RAW на обычный сервер, в логе будет отказ на `GETCONF_RAW` — верните `tunnel_mode=wg`.

С Podkop в RAW укажите интерфейс **tun-wdtt** (не wg-wdtt). После первого успешного RAW `firewall-refresh` пропишет `network.wdtt.device=tun-wdtt`.

В «Ходе подключения» для RAW:

```
[СЕТЬ] Режим: VPN (raw-IP, без WireGuard/DTLS)
[ВОРКЕР #1] [ПРЯМОЙ] Без DTLS, только RTP-obfs AEAD ✓
[ВОРКЕР #1] RAW-конфиг получен (ip=…)
[RAW] Туннель tun-wdtt поднят
```

## ID устройства (device_id, v3.16.2)

Сервер привязывает пароль к `device_id`, и лимит по умолчанию — одно устройство на пароль. До v3.16.2 клиент при пустом `wdtt.globals.device_id` брал machine-id, в том числе из `/var/lib/dbus/machine-id`, а `/var` на OpenWrt — симлинк в tmpfs. После каждой перезагрузки ID был новый, сервер видел незнакомое устройство и отказывал:

```
FATAL_AUTH: пароль привязан к другому устройству
```

Теперь ID выводится из MAC-адреса LAN-интерфейса, сохраняется в `/etc/wdtt/device_id` (флеш, а не tmpfs) и записывается в UCI при установке. `install.sh` переносит `device_id` при переустановке вместе с остальными секретами, а `wdtt-doctor` показывает текущее значение.

Посмотреть и при необходимости задать вручную:

```bash
uci get wdtt.globals.device_id
logread -e wdtt | grep device_id
uci set wdtt.globals.device_id='openwrt-my-router' && uci commit wdtt
```

При обновлении с версий до v3.16.2 ID сменится один раз — на сервере уже висит привязка к старому случайному значению. Попросите владельца VPS отвязать устройства от пароля (в Telegram-боте это кнопка «Отвязать ВСЕ устройства») либо поднять лимит `max_devices`. Порядок: сначала обновиться, затем отвязать, затем перезапустить `/etc/init.d/wdtt restart` — после этого сервер привяжет уже постоянный ID, и перезагрузки роутера перестанут ломать доступ.

## Авторизация воркеров (v3.16.1)

Сервер закрывает любое DTLS-соединение, которое первым пакетом не прислало `GETCONF:` или `AUTH:deviceID|password`. Конфиг запрашивает только один воркер из девяти, поэтому до v3.16.1 остальные восемь молчали до keepalive и получали разрыв примерно через 15 секунд после подключения. В логах это выглядело как бесконечная карусель:

```
[ВОРКЕР #5] [READY] Туннель готов к работе ✓
[ВОРКЕР #5] Ошибка Reader: EOF
```

Трафик при этом шёл в один поток вместо девяти — скорость падала в разы. Начиная с v3.16.1 воркеры без запроса конфига сразу отправляют `AUTH` с тем же `device_id`, что и `GETCONF` (при пустом `wdtt.globals.device_id` используется machine-id роутера). Если после обновления карусель `Reader: EOF` осталась — проверьте, что на роутере действительно новый бинарь:

```bash
logread -e wdtt | grep -c 'Reader: EOF'   # не должно расти
wg show wg-wdtt | grep transfer            # должно расти в обе стороны
```

## Скорость и устойчивость (v3.16.0)

Изменения в ядре `wdttd`, настройки не требуют:

- **Адаптивные chunk'и в диспетчере.** Раньше на один TURN-relay уходило ровно 8 пакетов подряд независимо от их размера. Теперь размер группы зависит от размера пакета (крупные данные — до 64 подряд, мелкие — 1–3): меньше переключений relay на мегабайт трафика и меньше reorder, из-за которого TCP внутри туннеля ронял окно.
- **Приоритет мелких пакетов.** Пакеты до 128 байт (в основном TCP ACK) идут через отдельную очередь воркера и обгоняют данные, поэтому ACK больше не ждёт весь chunk на медленном relay.
- **Предохранитель по времени.** Если текущий relay задержал группу дольше 15 мс, диспетчер переключается на следующий, не дожидаясь конца chunk'а.
- **Бан мёртвых relay.** Адрес, ответивший quota/unreachable/timeout, исключается из пула на 5 минут — воркеры больше не долбят один и тот же недоступный relay.
- **Очередь на приём** увеличена с 384 до 512 пакетов: на 70–80 Мбит/с при RTT ~50 мс старого запаса не хватало.

## VK Auth (VKCalls без капчи)

По умолчанию **vk_auth_mode=vkcalls** — TURN-креды через VKCalls anonymous flow ([no-captcha](https://github.com/XXcipherX/proxy-turn-vk-android/releases/tag/no-captcha)):

1. `auth.getAnonymToken` → `messages.getCallPreview` → `messages.getAnonymCallToken`
2. OK.ru session → `vchat.joinConversationByLink` → TURN credentials
3. Согласованный TLS/User-Agent профиль Chrome 146

При ошибке VKCalls автоматически fallback на **legacy** (старый путь с капчей).

| Режим (LuCI → Режим VK Auth) | Описание |
|------------------------------|----------|
| **VKCalls** | Anonymous flow, без капчи (рекомендуется) |
| **Legacy** | `calls.getAnonymousToken` + капча |

```bash
uci set wdtt.globals.vk_auth_mode='vkcalls'   # по умолчанию
uci set wdtt.globals.vk_auth_mode='legacy'    # только старый путь
```

Отключить VKCalls для отладки: `VK_SKIP_VKCALLS=1` в окружении `wdttd`.

## Капча

Режим в LuCI → **Режим капчи**:

| Режим | Описание |
|-------|----------|
| **Auto** | Авто Go v2, затем fallback |
| **RJS** | Только авто Go v2 |
| **WV** | Ручной: ссылка в браузере → `success_token` в LuCI (fallback при legacy/captcha gate) |

```bash
wdttd -captcha 'token'
# или LuCI → VK Smart Captcha → Отправить токен
```

## Лицензия

GPL-3.0

## Связанные проекты

- [proxy-turn-vk-android](https://github.com/amurcanov/proxy-turn-vk-android)
- [PWDTT](https://github.com/luminescq/PWDTT)
