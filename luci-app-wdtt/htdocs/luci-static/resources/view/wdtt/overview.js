'use strict';
/* WDTT overview.js — профили + красные ошибки в логе v3.18.4 */
'require view';
'require ui';
'require dom';
'require form';
'require uci';
'require poll';
'require rpc';

var callStatus = rpc.declare({
	object: 'wdtt',
	method: 'status'
});

var callLogs = rpc.declare({
	object: 'wdtt',
	method: 'logs',
	params: [ 'tail' ]
});

var callCaptcha = rpc.declare({
	object: 'wdtt',
	method: 'captcha',
	params: [ 'token' ]
});

var callConnect = rpc.declare({
	object: 'wdtt',
	method: 'connect'
});

var callDisconnect = rpc.declare({
	object: 'wdtt',
	method: 'disconnect'
});

var callApplyConfig = rpc.declare({
	object: 'wdtt',
	method: 'apply_config'
});

var callApplyRules = rpc.declare({
	object: 'wdtt',
	method: 'apply_rules',
	params: [ 'rules' ]
});

var callImportURI = rpc.declare({
	object: 'wdtt',
	method: 'import_uri',
	params: [ 'peer', 'password', 'hashes', 'workers', 'listen' ]
});

var callRoutingInfo = rpc.declare({
	object: 'wdtt',
	method: 'routing_info'
});

function decodeURIComponentSafe(s) {
	try {
		return decodeURIComponent(String(s || '').replace(/\+/g, ' '));
	} catch (e) {
		return String(s || '');
	}
}

function parseQueryString(qs) {
	var out = {};
	String(qs || '').split('&').forEach(function(pair) {
		if (!pair)
			return;
		var i = pair.indexOf('=');
		var k = i < 0 ? pair : pair.slice(0, i);
		var v = i < 0 ? '' : pair.slice(i + 1);
		out[decodeURIComponentSafe(k)] = decodeURIComponentSafe(v);
	});
	return out;
}

function tryDecodeBase64JSON(raw) {
	try {
		var bin = atob(String(raw || '').replace(/\s+/g, ''));
		var s = decodeURIComponent(Array.prototype.map.call(bin, function(c) {
			return '%' + ('00' + c.charCodeAt(0).toString(16)).slice(-2);
		}).join(''));
		return s.trim();
	} catch (e) {
		return '';
	}
}

/**
 * Импорт профиля из wdtt://, qwdtt://config?... или JSON (.qwdtt).
 * Возвращает { peer, password, hashes, workers, listen, name }.
 */
function parseWdttImport(raw) {
	var trimmed = String(raw || '').trim();
	if (!trimmed)
		throw new Error(_('Вставьте ссылку wdtt:// или qwdtt://'));

	if (/^wdtt:\/\//i.test(trimmed)) {
		/* wdtt://IP:dtls:wg:local:password:vk_hash  (hash may contain ':') */
		var body = trimmed.replace(/^wdtt:\/\//i, '');
		var parts = body.split(':');
		if (parts.length < 6)
			throw new Error(_('Неверный формат wdtt:// — нужно IP:dtls:wg:local:пароль:хеш'));
		var ip = parts[0];
		var dtls = parts[1];
		var localPort = parseInt(parts[3], 10) || 9000;
		var pass = parts[4];
		var hash = parts.slice(5).join(':');
		if (!ip || !dtls || !pass || !hash)
			throw new Error(_('В wdtt:// не хватает peer / пароля / хеша'));
		return {
			name: 'WDTT ' + ip,
			peer: ip + ':' + dtls,
			password: pass,
			hashes: hash,
			workers: '18',
			listen: '127.0.0.1:' + localPort
		};
	}

	if (/^qwdtt:\/\/config/i.test(trimmed) || /^qwdtt:config/i.test(trimmed)) {
		var norm = trimmed.replace(/^qwdtt:config/i, 'qwdtt://config');
		var qidx = norm.indexOf('?');
		if (qidx < 0)
			throw new Error(_('Неверный формат qwdtt:// — нет параметров'));
		var q = parseQueryString(norm.slice(qidx + 1));
		var peerRaw = q.peer || '';
		var dtlsPort = q.dtls_port || q.server_port || '';
		var peer = peerRaw;
		if (peerRaw && dtlsPort && peerRaw.indexOf(':') < 0)
			peer = peerRaw + ':' + dtlsPort;
		var hashes = q.hashes || q.vkHashes || '';
		var pass = q.pass || q.password || '';
		var workers = parseInt(q.workers || q.workersPerHash || '16', 10) || 16;
		var port = parseInt(q.port || q.listenPort || '9000', 10) || 9000;
		if (!peer || !pass || !hashes)
			throw new Error(_('В qwdtt:// нужны peer, pass и hashes'));
		return {
			name: q.name || 'qWDTT',
			peer: peer,
			password: pass,
			hashes: hashes,
			workers: String(workers),
			listen: '127.0.0.1:' + port
		};
	}

	var jsonStr = trimmed;
	if (jsonStr.charAt(0) !== '{') {
		var decoded = tryDecodeBase64JSON(trimmed);
		if (decoded.charAt(0) === '{')
			jsonStr = decoded;
	}
	if (jsonStr.charAt(0) === '{') {
		var obj;
		try {
			obj = JSON.parse(jsonStr);
		} catch (e) {
			throw new Error(_('Неверный JSON профиля'));
		}
		var jPeer = obj.peer || '';
		var jPass = obj.password || obj.pass || '';
		var jHashes = obj.hashes || obj.vkHashes || '';
		var jWorkers = parseInt(obj.workers || obj.workersPerHash || 16, 10) || 16;
		var jPort = parseInt(obj.port || obj.listenPort || 9000, 10) || 9000;
		if (!jPeer || !jPass || !jHashes)
			throw new Error(_('В JSON нужны peer, password/pass и hashes'));
		return {
			name: obj.name || 'JSON',
			peer: jPeer,
			password: jPass,
			hashes: jHashes,
			workers: String(jWorkers),
			listen: '127.0.0.1:' + jPort
		};
	}

	throw new Error(_('Неизвестный формат. Ожидается wdtt://, qwdtt://config?... или JSON'));
}

function splitHashList(raw) {
	return String(raw || '').split(/[,;|\s]+/).map(function(h) {
		return String(h || '').trim();
	}).filter(Boolean);
}

function promoteHashesToSlots() {
	var have = false;
	var i;
	for (i = 1; i <= 4; i++) {
		if (String(uci.get('wdtt', 'globals', 'hash' + i) || '').trim()) {
			have = true;
			break;
		}
	}
	if (!have) {
		var parts = splitHashList(uci.get('wdtt', 'globals', 'hashes') || '');
		for (i = 0; i < 4; i++)
			uci.set('wdtt', 'globals', 'hash' + (i + 1), parts[i] || '');
	}
}

function joinHashSlots() {
	var parts = [];
	for (var i = 1; i <= 4; i++) {
		var v = String(uci.get('wdtt', 'globals', 'hash' + i) || '').trim();
		if (v)
			parts.push(v);
	}
	uci.set('wdtt', 'globals', 'hashes', parts.join(','));
	return parts;
}

function applyImportedHashes(raw) {
	var parts = splitHashList(raw);
	for (var i = 0; i < 4; i++) {
		var val = parts[i] || '';
		uci.set('wdtt', 'globals', 'hash' + (i + 1), val);
		syncGlobalsFormField('hash' + (i + 1), val);
	}
	uci.set('wdtt', 'globals', 'hashes', parts.join(','));
}

var PROFILE_FIELDS = [
	'peer', 'password', 'hash1', 'hash2', 'hash3', 'hash4', 'hashes',
	'workers', 'mtu', 'listen', 'captcha_mode', 'vk_auth_mode',
	'obfs_mode', 'go_dns', 'turn_transport', 'tunnel_mode',
	'turn_host', 'turn_port'
];

function cleanProfileName(s) {
	return String(s || '').replace(/['"\n\r]/g, '').trim().slice(0, 48);
}

function listProfiles() {
	var out = [];
	uci.sections('wdtt', 'profile', function(s) {
		if (!s || !s['.name'])
			return;
		out.push({
			id: s['.name'],
			name: s.name || s['.name'],
			peer: s.peer || ''
		});
	});
	return out;
}

function findProfileByName(name) {
	var want = cleanProfileName(name).toLowerCase();
	var found = null;
	if (!want)
		return null;
	uci.sections('wdtt', 'profile', function(s) {
		if (found || !s)
			return;
		if (String(s.name || s['.name'] || '').toLowerCase() === want)
			found = s['.name'];
	});
	return found;
}

function activeProfileId() {
	return String(uci.get('wdtt', 'globals', 'active_profile') || '').trim();
}

function activeProfileLabel() {
	var id = activeProfileId();
	if (!id)
		return _('не выбран');
	var name = uci.get('wdtt', id, 'name') || id;
	var peer = uci.get('wdtt', id, 'peer') || '';
	return peer ? (name + ' — ' + peer) : name;
}

function globalsFieldSelector(option) {
	return '[data-widget-id="wdtt.globals.' + option + '"] input, ' +
		'[data-widget-id="wdtt.globals.' + option + '"] textarea, ' +
		'[data-widget-id="wdtt.globals.' + option + '"] select, ' +
		'input[name="cbid.wdtt.globals.' + option + '"], ' +
		'textarea[name="cbid.wdtt.globals.' + option + '"], ' +
		'select[name="cbid.wdtt.globals.' + option + '"]';
}

function readGlobalsFormField(option) {
	var nodes = document.querySelectorAll(globalsFieldSelector(option));
	return nodes.length ? nodes[0].value : null;
}

function pullGlobalsFromForm() {
	PROFILE_FIELDS.forEach(function(opt) {
		var v = readGlobalsFormField(opt);
		if (v !== null)
			uci.set('wdtt', 'globals', opt, v);
	});
	joinHashSlots();
}

function copyGlobalsToProfile(sid) {
	pullGlobalsFromForm();
	PROFILE_FIELDS.forEach(function(opt) {
		uci.set('wdtt', sid, opt, uci.get('wdtt', 'globals', opt) || '');
	});
}

function applyProfileToGlobals(sid) {
	PROFILE_FIELDS.forEach(function(opt) {
		var val = uci.get('wdtt', sid, opt);
		if (val == null)
			val = '';
		uci.set('wdtt', 'globals', opt, val);
		syncGlobalsFormField(opt, val);
	});
	joinHashSlots();
	uci.set('wdtt', 'globals', 'active_profile', sid);
}

// sel передаётся при рендере: узла ещё нет в документе, getElementById вернёт
// null, и список молча останется пустым.
function fillProfileSelect(sel) {
	sel = sel || document.getElementById('wdtt-profile-select');
	if (!sel)
		return;
	var cur = activeProfileId();
	var profiles = listProfiles();
	sel.innerHTML = '';
	sel.appendChild(E('option', { 'value': '' }, profiles.length
		? _('— выбрать профиль —')
		: _('нет сохранённых профилей')));
	profiles.forEach(function(p) {
		var label = p.peer ? (p.name + ' — ' + p.peer) : p.name;
		var opt = E('option', { 'value': p.id }, label);
		if (p.id === cur)
			opt.selected = true;
		sel.appendChild(opt);
	});
	if (!cur && sel.value === '' && profiles.length)
		sel.selectedIndex = 0;
}

function renderProfileSelect() {
	var sel = E('select', {
		'id': 'wdtt-profile-select',
		'style': 'width:100%;max-width:420px'
	});
	fillProfileSelect(sel);
	return sel;
}

function logLineKind(line) {
	var s = String(line || '');
	if (/FATAL|DENIED|error_code|хеш мёртв|Invalid join|non-retryable|Ошибка |FAIL:|failed|не удалось|некорректн|сломан|FATAL_AUTH/i.test(s))
		return 'err';
	if (/WARN|Warning|предупрежд/i.test(s))
		return 'warn';
	if (/Креды OK|поднят|READY|Соединение установлено|TUN |Конфиг получен|RAW-конфиг получен/i.test(s))
		return 'ok';
	return '';
}

function logLineColor(kind) {
	if (kind === 'err')
		return '#f44747';
	if (kind === 'warn')
		return '#dcdcaa';
	if (kind === 'ok')
		return '#4ec9b0';
	return '';
}

function renderColoredLog(container, lines, emptyText, defaultColor) {
	if (!container)
		return;
	if (!lines || !lines.length) {
		dom.content(container, emptyText || _('Лог пуст'));
		return;
	}
	dom.content(container, lines.map(function(line) {
		var kind = logLineKind(line);
		var color = logLineColor(kind) || defaultColor || '#d4d4d4';
		return E('div', { 'style': 'color:' + color }, line);
	}));
}

function formatConnectSteps(st, logLines) {
	var steps = (st && st.steps) || [];
	if (steps.length)
		return steps.join('\n');
	var markers = ['[Основной]', '[СЕТЬ]', '[КЛИЕНТ]', '[WRAP]', '[ЯДРО]', '[TURN]', 'Креды OK', '[WG]', '[RAW]', '[ПРЯМОЙ]', 'WireGuard', 'device_id:', 'Конфиг получен', 'Ошибка', 'FATAL', 'DENIED', 'хеш мёртв', 'error_code'];
	var out = [];
	(logLines || []).forEach(function(line) {
		for (var i = 0; i < markers.length; i++) {
			if (String(line).indexOf(markers[i]) !== -1) {
				out.push(line);
				return;
			}
		}
	});
	return out.join('\n');
}

function syncGlobalsFormField(option, value) {
	var nodes = document.querySelectorAll(globalsFieldSelector(option));
	for (var i = 0; i < nodes.length; i++) {
		nodes[i].value = value;
	}
}

function annotateDomainFields() {
	document.querySelectorAll('textarea').forEach(function(ta) {
		var blob = (ta.name || '') + ' ' + (ta.id || '');
		if (/captcha|hashes/i.test(blob))
			return;
		var m = /wdtt\.([^.]+)\.domain_list/.exec(blob);
		if (!m) {
			var wrap = ta.closest('[id*="domain_list"]');
			if (wrap)
				m = /wdtt\.([^.]+)\.domain_list/.exec(wrap.id || '');
		}
		if (m)
			ta.setAttribute('data-wdtt-domain-list', m[1]);
	});
}

function readDomainRulesFromPage() {
	var rules = {};

	function ensure(sid) {
		if (!sid)
			return null;
		if (!rules[sid])
			rules[sid] = { enabled: '1', domain_list: '' };
		return rules[sid];
	}

	annotateDomainFields();

	document.querySelectorAll('textarea[data-wdtt-domain-list]').forEach(function(ta) {
		var sid = ta.getAttribute('data-wdtt-domain-list');
		var normalized = normalizeDomainList(ta.value);
		if (normalized)
			ensure(sid).domain_list = normalized;
	});

	document.querySelectorAll('textarea').forEach(function(ta) {
		var blob = (ta.name || '') + ' ' + (ta.id || '');
		if (/captcha|hashes/i.test(blob))
			return;
		var m = /wdtt\.([^.]+)\.domain_list/.exec(blob);
		if (!m) {
			var wrap = ta.closest('[id*="domain_list"]');
			if (wrap)
				m = /wdtt\.([^.]+)\.domain_list/.exec(wrap.id || '');
		}
		if (!m)
			return;
		var normalized = normalizeDomainList(ta.value);
		if (normalized)
			ensure(m[1]).domain_list = normalized;
	});

	document.querySelectorAll('input[type="checkbox"]').forEach(function(el) {
		var blob = (el.name || '') + ' ' + (el.id || '');
		var m = /wdtt\.([^.]+)\.enabled/.exec(blob);
		if (m)
			ensure(m[1]).enabled = el.checked ? '1' : '0';
	});

	uci.sections('wdtt', 'rule', function(s) {
		var sid = s['.name'];
		if (sid)
			ensure(sid);
	});

	return rules;
}

function syncRuleFieldsFromDom() {
	function syncSid(sid, enabled, domains) {
		if (!sid)
			return;
		if (enabled != null)
			uci.set('wdtt', sid, 'enabled', enabled ? '1' : '0');
		if (domains != null) {
			var normalized = normalizeDomainList(domains);
			if (normalized)
				uci.set('wdtt', sid, 'domain_list', normalized);
		}
	}

	document.querySelectorAll('.cbi-section-node[data-section-id]').forEach(function(node) {
		var sid = node.getAttribute('data-section-id');
		var ta = node.querySelector('textarea[name*="domain_list"], textarea[id*="domain_list"]');
		var en = node.querySelector('input[type="checkbox"][name*=".enabled"], input[type="checkbox"][id*=".enabled"]');
		syncSid(
			sid,
			en ? en.checked : null,
			ta ? ta.value : null
		);
	});

	document.querySelectorAll('[id^="widget.cbid.wdtt."][id$=".domain_list"]').forEach(function(widget) {
		var m = /^widget\.cbid\.wdtt\.(.+)\.domain_list$/.exec(widget.id);
		var ta = widget.querySelector('textarea') || (widget.tagName === 'TEXTAREA' ? widget : null);
		if (m && ta)
			syncSid(m[1], null, ta.value);
	});

	document.querySelectorAll('textarea[name^="cbid.wdtt."][name$=".domain_list"]').forEach(function(ta) {
		var m = /^cbid\.wdtt\.(.+)\.domain_list$/.exec(ta.name);
		if (m)
			syncSid(m[1], null, ta.value);
	});

	document.querySelectorAll('input[type="checkbox"][name^="cbid.wdtt."][name$=".enabled"]').forEach(function(el) {
		var m = /^cbid\.wdtt\.(.+)\.enabled$/.exec(el.name);
		if (m)
			syncSid(m[1], el.checked, null);
	});
}

function collectRulesFromDom() {
	var rules = {};

	function addRule(sid, enabled, domains) {
		if (!sid)
			return;
		rules[sid] = {
			enabled: (enabled != null ? (enabled ? '1' : '0') : (rules[sid] && rules[sid].enabled) || '1'),
			domain_list: domains != null ? normalizeDomainList(domains) : ((rules[sid] && rules[sid].domain_list) || '')
		};
	}

	document.querySelectorAll('textarea[name*="domain_list"]').forEach(function(ta) {
		var m = /cbid\.wdtt\.([^.]+)\.domain_list/.exec(ta.name || '');
		if (m)
			addRule(m[1], null, ta.value);
	});

	document.querySelectorAll('[id^="widget.cbid.wdtt."][id$=".domain_list"]').forEach(function(widget) {
		var m = /^widget\.cbid\.wdtt\.(.+)\.domain_list$/.exec(widget.id);
		var ta = widget.querySelector('textarea') || (widget.tagName === 'TEXTAREA' ? widget : null);
		if (m && ta)
			addRule(m[1], null, ta.value);
	});

	document.querySelectorAll('.cbi-section-node[data-section-id]').forEach(function(node) {
		var sid = node.getAttribute('data-section-id');
		var ta = node.querySelector('textarea[name*="domain_list"], textarea[id*="domain_list"]');
		var en = node.querySelector('input[type="checkbox"][name*=".enabled"], input[type="checkbox"][id*=".enabled"]');
		if (!sid)
			return;
		addRule(sid, en ? en.checked : null, ta ? ta.value : null);
	});

	document.querySelectorAll('input[type="checkbox"][name$=".enabled"]').forEach(function(el) {
		var m = /cbid\.wdtt\.([^.]+)\.enabled/.exec(el.name || '');
		if (m)
			addRule(m[1], el.checked, null);
	});

	return rules;
}

function collectRulesPayload() {
	return readDomainRulesFromPage();
}

function normalizeRulesArray(rules) {
	if (!rules)
		return [];
	if (Array.isArray(rules))
		return rules;
	if (typeof rules === 'object')
		return Object.keys(rules).sort(function(a, b) {
			return Number(a) - Number(b);
		}).map(function(k) { return rules[k]; });
	return [];
}

function formatBytes(n) {
	n = Number(n) || 0;
	if (n < 1024) return n + ' B';
	if (n < 1048576) return (n / 1024).toFixed(1) + ' KiB';
	if (n < 1073741824) return (n / 1048576).toFixed(1) + ' MiB';
	return (n / 1073741824).toFixed(2) + ' GiB';
}

function stateBadge(state) {
	var cls = 'label';
	switch (state) {
	case 'connected': cls += ' success'; break;
	case 'connecting': cls += ' warning'; break;
	case 'captcha_required': cls += ' important'; break;
	case 'error': cls += ' danger'; break;
	default: cls += ' notice';
	}
	return E('span', { 'class': cls }, state || 'unknown');
}

function wdttPageTitle(status) {
	var v = String((status && status.package_version) || '').replace(/^v/i, '').trim();
	return v ? _('WDTT VPN') + ' v' + v : _('WDTT VPN');
}

function normalizeDomainList(val) {
	if (val == null || val === '')
		return '';

	return String(val).replace(/\r/g, '').split(/[\n,;]+/).map(function(d) {
		d = d.trim().replace(/^https?:\/\//i, '').split('/')[0].split(' ')[0].toLowerCase();
		return d;
	}).filter(function(d) {
		return d && d.indexOf('.') !== -1 && d.indexOf('2iw') === -1 && d.indexOf('yoltlbe') === -1;
	}).filter(function(d, i, a) {
		return a.indexOf(d) === i;
	}).join(',');
}

function wdttPageDescription(status) {
	var lines = [_('WireGuard-туннель через VK TURN/DTLS. Совместим с сервером WDTT/PWDTT.')];
	var wdttd = String((status && status.wdttd_version) || '').trim();
	if (wdttd) {
		lines.push(_('Демон wdttd:') + ' ' + wdttd);
	}
	return lines.join(' ');
}

return view.extend({
	load: function() {
		return Promise.all([
			uci.load('wdtt'),
			uci.load('network'),
			callStatus().catch(function() {
				return { running: false, state: 'stopped' };
			})
		]);
	},

	render: function(data) {
		var m, s, o, status = data[1] || {};
		var self = this;
		self.wdttMap = null;
		self._lastLogText = '';
		self._lastRulesText = '';
		self._lastConnectText = '';

		promoteHashesToSlots();

		m = new form.Map('wdtt', wdttPageTitle(status), wdttPageDescription(status));

		/* --- Сохранённые профили (как в Android / qWDTT) --- */
		s = m.section(form.NamedSection, 'globals', 'globals', _('Профили подключения'));
		s.render = L.bind(function() {
			return E('div', { 'class': 'cbi-section' }, [
				E('p', { 'class': 'hint' },
					_('Сохраните несколько серверов и переключайтесь между ними, как в Android qWDTT. Активный профиль копируется в настройки ниже.')),
				E('div', { 'class': 'cbi-value' }, [
					E('label', { 'class': 'cbi-value-title' }, _('Профиль')),
					E('div', { 'class': 'cbi-value-field' }, [
						renderProfileSelect()
					])
				]),
				E('div', { 'class': 'cbi-value' }, [
					E('label', { 'class': 'cbi-value-title' }, _('Имя нового')),
					E('div', { 'class': 'cbi-value-field' }, [
						E('input', {
							'id': 'wdtt-profile-name',
							'type': 'text',
							'style': 'width:100%;max-width:420px',
							'placeholder': _('Дом, Работа, RAW VPS…')
						})
					])
				]),
				E('div', { 'class': 'cbi-page-actions', 'style': 'margin-top:8px' }, [
					E('button', {
						'class': 'btn cbi-button cbi-button-action important',
						'click': ui.createHandlerFn(self, self.handleProfileSwitch)
					}, _('Переключить')),
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'click': ui.createHandlerFn(self, self.handleProfileSave)
					}, _('Сохранить текущий')),
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'click': ui.createHandlerFn(self, self.handleProfileSaveAs)
					}, _('Сохранить как…')),
					E('button', {
						'class': 'btn cbi-button cbi-button-reset',
						'click': ui.createHandlerFn(self, self.handleProfileDelete)
					}, _('Удалить'))
				])
			]);
		}, s);

		/* --- Импорт wdtt:// / qwdtt:// --- */
		s = m.section(form.NamedSection, 'globals', 'globals', _('Импорт профиля'));
		s.render = L.bind(function() {
			return E('div', { 'class': 'cbi-section' }, [
				E('p', { 'class': 'hint' },
					_('Вставьте ссылку из Telegram-бота / Android (wdtt:// или qwdtt://) — заполнятся peer, пароль, хеши и потоки.')),
				E('textarea', {
					'id': 'wdtt-import-uri',
					'rows': 3,
					'style': 'width:100%;font-family:monospace;font-size:12px;',
					'placeholder': 'wdtt://203.0.113.10:56000:56001:9000:пароль:хеш\nили qwdtt://config?peer=...&pass=...&hashes=...'
				}),
				E('div', { 'class': 'cbi-page-actions', 'style': 'margin-top:8px' }, [
					E('button', {
						'class': 'btn cbi-button cbi-button-apply important',
						'click': ui.createHandlerFn(self, self.handleImportURI)
					}, _('Импортировать'))
				])
			]);
		}, s);

		s = m.section(form.NamedSection, 'globals', 'globals', _('Настройки туннеля'));

		o = s.option(form.Flag, 'enabled', _('Включить'),
			_('Автозапуск при загрузке. Управление туннелем — кнопки «Подключить» / «Отключить» в блоке «Статус» (они же меняют этот флаг).'));
		o.default = '0';
		o.rmempty = false;

		o = s.option(form.Value, 'peer', _('VPS адрес'),
			_('IP или домен сервера с портом, например 203.0.113.10:56000'));
		o.placeholder = '203.0.113.10:56000';
		o.rmempty = false;

		o = s.option(form.Value, 'password', _('Пароль подключения'),
			_('Пароль туннеля с VPS (WRAP-ключ выводится из пароля).'));
		o.password = true;
		o.rmempty = false;

		o = s.option(form.Value, 'hash1', _('VK-хеш 1'),
			_('Ссылка звонка VK или сам хеш. Один хеш = одна группа из 9 потоков. До 4 хешей.'));
		o.placeholder = 'vk.com/call/join/... или хеш';
		o.rmempty = true;

		o = s.option(form.Value, 'hash2', _('VK-хеш 2'));
		o.placeholder = _('необязательно');
		o.rmempty = true;

		o = s.option(form.Value, 'hash3', _('VK-хеш 3'));
		o.placeholder = _('необязательно');
		o.rmempty = true;

		o = s.option(form.Value, 'hash4', _('VK-хеш 4'));
		o.placeholder = _('необязательно');
		o.rmempty = true;

		o = s.option(form.Value, 'workers', _('Потоки'),
			_('Кратно 9: 9 / 18 / 27 / 36. При 4 хешах ставьте 36, чтобы у каждой ссылки была своя группа.'));
		o.datatype = 'uinteger';
		o.default = '12';

		o = s.option(form.Value, 'mtu', 'MTU');
		o.datatype = 'uinteger';
		o.default = '1240';

		o = s.option(form.ListValue, 'captcha_mode', _('Режим капчи'),
			_('Auto/RJS — роутер решает капчу сам. WV — вы открываете ссылку в браузере и вставляете success_token (рекомендуется после лимита VK).'));
		o.value('auto', _('Авто (Go v2 + fallback)'));
		o.value('rjs', _('RJS (только авто Go v2)'));
		o.value('wv', _('WV (ручной — ссылка + токен)'));
		o.default = 'wv';

		o = s.option(form.ListValue, 'vk_auth_mode', _('Режим VK Auth'),
			_('VKCalls — получение TURN-кредов через anonymous flow (без капчи, рекомендуется). Legacy — старый путь через calls.getAnonymousToken с капчей.'));
		o.value('vkcalls', _('VKCalls (без капчи)'));
		o.value('legacy', _('Legacy (капча)'));
		o.default = 'vkcalls';

		o = s.option(form.ListValue, 'obfs_mode', _('Обфускация RTP'),
			_('Audio — OPUS PT 111 (по умолчанию, совместимо со всеми серверами). Video — PT 96 + больший padding (нужен wdtt-server с поддержкой PT 96).'));
		o.value('audio', _('Audio (PT 111)'));
		o.value('video', _('Video (PT 96)'));
		o.default = 'audio';

		o = s.option(form.ListValue, 'go_dns', _('DNS для VK API'),
			_('Резолвер для API VK/TURN. DoH обходит блокировки обычного DNS. custom:IP или doh:URL — вручную через UCI.'));
		o.value('doh-yandex', _('DoH Яндекс (рекомендуется)'));
		o.value('doh-cloudflare', _('DoH Cloudflare'));
		o.value('doh-google', _('DoH Google'));
		o.value('yandex', _('Яндекс UDP 77.88.8.8'));
		o.value('cloudflare', _('Cloudflare UDP 1.1.1.1'));
		o.value('google', _('Google UDP 8.8.8.8'));
		o.default = 'doh-yandex';

		o = s.option(form.ListValue, 'turn_transport', _('Транспорт TURN'),
			_('UDP — по умолчанию, быстрее. TCP — если провайдер душит или дропает UDP до TURN-relay (замечено у некоторых мобильных операторов); задержка выше, зато соединение поднимается.'));
		o.value('udp', _('UDP (рекомендуется)'));
		o.value('tcp', _('TCP (если UDP блокируют)'));
		o.default = 'udp';

		o = s.option(form.Value, 'device_id', _('ID устройства'),
			_('Сервер привязывает пароль к этому ID (обычно одно устройство на пароль). Заполняется автоматически из MAC при установке — менять не нужно. После смены ID потребуется отвязать устройство от пароля на сервере.'));
		o.placeholder = 'openwrt-<mac>';
		o.rmempty = true;

		o = s.option(form.ListValue, 'tunnel_mode', _('Транспорт'),
			_('WireGuard — обычный путь (DTLS + wg-wdtt), работает с любым WDTT-сервером. RAW — сырые IP без WireGuard/DTLS, нужен qWDTT-сервер с -listen-raw. Обычный VPS (GETCONF+DTLS) в RAW не поднимется.'));
		o.value('wg', _('WireGuard + DTLS (рекомендуется)'));
		o.value('raw', _('RAW (без WireGuard, сервер -listen-raw)'));
		o.default = 'wg';

		o = s.option(form.ListValue, 'routing_mode', _('Режим туннеля'),
			_('Podkop — WDTT только поднимает интерфейс туннеля, маршруты задаёт Podkop (sing-box). Выборочный — правила WDTT. Полный — весь трафик через WDTT.'));
		o.value('external', _('Podkop (sing-box) — рекомендуется'));
		o.value('selective', _('Выборочный (правила WDTT)'));
		o.value('full', _('Полный туннель'));
		o.default = 'external';

		o = s.option(form.ListValue, 'uplink_iface', _('Интернет (uplink)'),
			_('Физический канал: VK/TURN и пакеты к VPS (peer). В режиме «Полный» выход в интернет — через туннель на VPS, не напрямую в LTE/WAN.'));
		o.value('auto', _('Авто (default route)'));
		o.value('wan', 'WAN');
		o.value('wwan', 'WWAN (LTE)');
		uci.sections('network', 'interface', function(n) {
			var name = n['.name'];
			if (!name || name === 'loopback' || name === 'lan' || name === 'wdtt')
				return;
			if (name === 'wan' || name === 'wwan')
				return;
			o.value(name, name);
		});
		o.default = 'auto';

		o = s.option(form.DynamicList, 'routing_excluded_ip', _('Исключить IP'),
			_('Устройства, которые всегда идут напрямую (приоритет выше правил).'));
		o.datatype = 'ipaddr';
		o.placeholder = '192.168.1.100';
		o.depends('routing_mode', 'selective');

		o = s.option(form.Value, 'iface', _('Интерфейс туннеля'),
			_('wg-wdtt в режиме WireGuard. В RAW демон сам поднимает tun-wdtt, это поле можно не менять.'));
		o.default = 'wg-wdtt';
		o.readonly = true;

		/* --- Правила маршрутизации (только selective; TypedSection.depends в LuCI JS нет) --- */
		var routingMode = uci.get('wdtt', 'globals', 'routing_mode') || 'external';
		if (routingMode === 'selective') {
			s = m.section(form.TypedSection, 'rule', _('Правила маршрутизации'),
				_('Только режим «Выборочный». С Podkop — домены в LuCI Podkop, не здесь.'));
			s.anonymous = false;
			s.addremove = true;

			o = s.option(form.Flag, 'enabled', _('Включено'));
			o.default = '1';

			o = s.option(form.ListValue, 'type', _('Тип'));
			o.value('route', _('В туннель (route)'));
			o.value('exclusion', _('Напрямую (exclusion)'));
			o.default = 'route';

			o = s.option(form.TextValue, 'domain_list', _('Домены'),
				_('Через запятую или с новой строки. youtube.com сам подтягивает CDN (googlevideo, ytimg, …). Клиенты должны использовать DNS роутера (не DoH).'));
			o.rows = 4;
			o.placeholder = 'youtube.com, 2ip.ru';
			o.rmempty = true;
			o.load = function(section_id) {
				var v = uci.get('wdtt', section_id, 'domain_list');
				if (v == null)
					return '';
				if (Array.isArray(v))
					v = v.join(',');
				return String(v).replace(/,/g, '\n');
			};
			o.write = function(section_id, formvalue) {
				var normalized = normalizeDomainList(formvalue);
				if (normalized)
					uci.set('wdtt', section_id, 'domain_list', normalized);
			};
			o.remove = function(section_id) {
				uci.unset('wdtt', section_id, 'domain_list');
			};

			o = s.option(form.DynamicList, 'subnet', _('Подсети'), _('CIDR, например 203.0.113.0/24'));
			o.datatype = 'cidr';
			o.placeholder = '203.0.113.0/24';
			o.rmempty = true;

			o = s.option(form.DynamicList, 'source_ip', _('IP устройства (полная маршрутизация)'),
				_('Весь трафик этого устройства через WDTT, независимо от доменов в правиле.'));
			o.datatype = 'ipaddr';
			o.placeholder = '192.168.1.50';

			o = s.option(form.Value, 'list_url', _('URL списка доменов'),
				_('Файл: один домен на строку. Загружается при подключении.'));
			o.placeholder = 'https://example.com/list.txt';
			o.rmempty = true;

			s = m.section(form.NamedSection, 'globals', 'globals', '');
			s.render = L.bind(function() {
				return E('div', { 'class': 'cbi-section' }, [
					E('div', { 'class': 'cbi-page-actions' }, [
						E('button', {
							'class': 'btn cbi-button cbi-button-apply important',
							'click': ui.createHandlerFn(self, self.handleApplyRules)
						}, _('Принять изменения'))
					]),
					E('p', { 'class': 'hint' },
						_('Сохраняет правила в UCI и перезагружает маршрутизацию без отключения туннеля.'))
				]);
			}, s);
		}

		/* --- Статус --- */
		s = m.section(form.NamedSection, 'globals', 'globals', _('Статус'));

		s.render = L.bind(function() {
			var uplink = uci.get('wdtt', 'globals', 'uplink_iface') || 'auto';
			return E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, _('Текущее состояние')),
				E('div', { 'id': 'wdtt-status-panel' }, self.renderStatus(status)),
				E('h4', { 'style': 'margin-top:16px' }, _('Интернет (uplink)')),
				E('p', { 'class': 'hint' },
					_('Физический канал к VPS и VK. После смены — переподключение. В «Полном» режиме сайты выходят через VPS.')),
				E('div', { 'class': 'cbi-page-actions', 'id': 'wdtt-uplink-buttons' }, [
					self.uplinkButton(_('Авто'), 'auto', uplink),
					self.uplinkButton('WAN', 'wan', uplink),
					self.uplinkButton('WWAN', 'wwan', uplink)
				]),
				E('div', { 'class': 'cbi-page-actions' }, [
					E('button', {
						'class': 'btn cbi-button cbi-button-action',
						'click': ui.createHandlerFn(self, self.handleConnect)
					}, _('Подключить')),
					E('button', {
						'class': 'btn cbi-button cbi-button-reset',
						'click': ui.createHandlerFn(self, self.handleDisconnect)
					}, _('Отключить'))
				])
			]);
		}, s);

		/* --- Логи --- */
		s = m.section(form.NamedSection, 'globals', 'globals', _('Логи'));

		s.render = L.bind(function() {
			return E('div', { 'class': 'cbi-section' }, [
				E('h4', {}, _('Правила маршрутизации')),
				E('pre', {
					'id': 'wdtt-rules-log',
					'style': 'max-height:160px;overflow:auto;font-size:12px;background:#252526;color:#d4d4d4;padding:10px;border-radius:4px;margin:0 0 12px;'
				}, _('Загрузка...')),
				E('h4', {}, _('Ход подключения')),
				E('p', { 'class': 'hint' },
					_('Этапы как в qWDTT: хеши, DNS, маскировка, VK Auth, WRAP, креды TURN, подъём WireGuard.')),
				E('div', {
					'id': 'wdtt-connect-log',
					'style': 'max-height:220px;overflow:auto;font-size:12px;font-family:monospace;background:#111;color:#7dcea0;padding:10px;border-radius:4px;margin:0 0 12px;white-space:pre-wrap;line-height:1.45'
				}, _('Загрузка...')),
				E('h4', {}, _('Трафик')),
				E('pre', {
					'id': 'wdtt-traffic-line',
					'style': 'font-size:12px;background:#252526;color:#4ec9b0;padding:8px 10px;border-radius:4px;margin:0 0 12px;white-space:pre;'
				}, '-'),
				E('h4', {}, _('Лог wdttd')),
				E('div', {
					'id': 'wdtt-log-view',
					'style': 'max-height:320px;overflow:auto;font-size:12px;font-family:monospace;background:#1e1e1e;color:#d4d4d4;padding:12px;border-radius:4px;margin:0;white-space:pre-wrap;line-height:1.45'
				}, _('Загрузка...'))
			]);
		}, s);

		/* --- Капча --- */
		s = m.section(form.NamedSection, 'globals', 'globals', _('VK Smart Captcha'));

		s.render = L.bind(function() {
			return E('div', { 'class': 'cbi-section', 'id': 'wdtt-captcha-panel' },
				self.renderCaptchaPanel(status.captcha || {}));
		}, s);

		poll.add(L.bind(this.pollStatus, this), 3);

		self.wdttMap = m;

		var mapSave = m.save.bind(m);
		m.save = function() {
			var rulesPayload = readDomainRulesFromPage();
			var applyRulesP = Promise.resolve({});
			var mode = uci.get('wdtt', 'globals', 'routing_mode') || 'selective';
			if (mode === 'selective' && Object.keys(rulesPayload).some(function(sid) {
				return rulesPayload[sid].domain_list;
			}))
				applyRulesP = callApplyRules(rulesPayload).catch(function() { return {}; });

			syncRuleFieldsFromDom();
			return applyRulesP.then(function() {
				return mapSave();
			}).then(function() {
				joinHashSlots();
				return uci.save();
			}).then(function() {
				return callApplyConfig().then(function() {
					if (mode === 'selective')
						return callApplyRules(rulesPayload).catch(function() { return {}; });
					return {};
				}).catch(function() { return {}; });
			});
		};

		return Promise.resolve(m.render()).then(function(node) {
			fillProfileSelect();
			return node;
		});
	},

	renderCaptchaPanel: function(cap) {
		cap = cap || {};
		var uri = cap.redirect_uri || '';
		var nodes = [
			E('h3', {}, _('VK Smart Captcha')),
			E('p', {}, cap.required
				? _('Требуется капча VK. Ссылка живёт ~1–2 минуты — при ошибке нажмите «Подключить» снова.')
				: _('Капча не требуется.'))
		];

		if (uri) {
			nodes.push(
				E('p', { 'class': 'hint' },
					_('Если ссылка не открывается: скопируйте URL, откройте на телефоне с мобильным интернетом (не через роутер).')),
				E('div', { 'class': 'cbi-value' }, [
					E('label', { 'class': 'cbi-value-title' }, _('Ссылка на капчу')),
					E('div', { 'class': 'cbi-value-field' }, [
						E('textarea', {
							'id': 'wdtt-captcha-url',
							'readonly': 'readonly',
							'rows': 3,
							'style': 'width:100%;font-family:monospace;font-size:12px;'
						}, uri),
						E('div', { 'class': 'cbi-page-actions', 'style': 'margin-top:8px' }, [
							E('button', {
								'class': 'btn cbi-button cbi-button-action',
								'click': ui.createHandlerFn(this, this.handleOpenCaptcha)
							}, _('Открыть в новой вкладке')),
							E('button', {
								'class': 'btn cbi-button cbi-button-save',
								'click': ui.createHandlerFn(this, this.handleCopyCaptchaUrl)
							}, _('Копировать URL'))
						])
					])
				])
			);
		}

		nodes.push(
			E('p', { 'class': 'hint' },
				_('После решения капчи: F12 → Network → captchaNotRobot.check → success_token в ответе.')),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Токен капчи')),
				E('div', { 'class': 'cbi-value-field' }, [
					E('input', {
						'id': 'wdtt-captcha-token',
						'type': 'text',
						'style': 'width:100%',
						'placeholder': 'success_token_...'
					}),
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'style': 'margin-top:8px',
						'click': ui.createHandlerFn(this, this.handleCaptcha)
					}, _('Отправить токен'))
				])
			])
		);

		return nodes;
	},

	renderStatus: function(st) {
		st = st || {};
		var pkgVer = String(st.package_version || '').trim();
		var wdttdVer = String(st.wdttd_version || st.version || '').trim();
		var enabled = String(st.enabled != null ? st.enabled : '');
		var wgUp = st.wg_iface_up === true || st.wg_iface_up === 1 || st.wg_iface_up === '1';
		return E('table', { 'class': 'table' }, [
			pkgVer ? E('tr', {}, [E('td', { 'width': '200' }, _('Версия WDTT')), E('td', {}, pkgVer)]) : '',
			wdttdVer ? E('tr', {}, [E('td', {}, _('Демон wdttd')), E('td', {}, wdttdVer)]) : '',
			E('tr', {}, [E('td', { 'width': '200' }, _('Состояние')), E('td', {}, stateBadge(st.state))]),
			E('tr', {}, [E('td', {}, _('Работает')), E('td', {}, st.running ? _('Да') : _('Нет'))]),
			enabled !== '' ? E('tr', {}, [E('td', {}, _('UCI enabled')), E('td', {}, enabled === '1' ? _('1 (вкл)') : _('0 (выкл)'))]) : '',
			E('tr', {}, [E('td', {}, _('WireGuard')), E('td', {},
				st.wg_applied ? _('Поднят') : (wgUp ? _('iface up') : _('Нет')))]),
			(st.datapath_ok === false || st.datapath_ok === 0 || st.datapath_ok === '0')
				? E('tr', {}, [E('td', {}, _('Datapath')), E('td', { 'style': 'color:#c00' },
					_('сломан — автопочинка: /usr/libexec/wdtt/datapath ensure'))])
				: (st.datapath_ok === true || st.datapath_ok === 1 || st.datapath_ok === '1')
					? E('tr', {}, [E('td', {}, _('Datapath')), E('td', {}, _('OK') + (st.routing_mode ? ' (' + st.routing_mode + ')' : ''))])
					: '',
			(st.table100_ok === false || st.table100_ok === 0 || st.table100_ok === '0')
				? E('tr', {}, [E('td', {}, _('Table 100')), E('td', { 'style': 'color:#c00' },
					_('пусто — трафик не в туннель'))])
				: (st.table100_ok === true || st.table100_ok === 1 || st.table100_ok === '1')
					? E('tr', {}, [E('td', {}, _('Table 100')), E('td', {}, _('OK'))])
					: '',
			(st.nft_ok === false || st.nft_ok === 0 || st.nft_ok === '0')
				? E('tr', {}, [E('td', {}, _('nft wdtt')), E('td', { 'style': 'color:#c00' }, _('нет таблицы'))])
				: '',
			E('tr', {}, [E('td', {}, _('Профиль')), E('td', {}, activeProfileLabel())]),
			E('tr', {}, [E('td', {}, _('Воркеры')), E('td', {}, String(st.workers || 0))]),
			E('tr', {}, [E('td', {}, _('Uptime')), E('td', {}, (st.uptime_sec || 0) + ' s')]),
			st.last_error ? E('tr', {}, [E('td', {}, _('Ошибка')), E('td', { 'style': 'color:#c00' }, st.last_error)]) : ''
		]);
	},

	formatRulesLog: function(info) {
		info = info || {};
		var lines = [];
		var mode = info.routing_mode || 'selective';
		var modeLabel = mode === 'full'
			? _('Полный')
			: (mode === 'external' ? _('Podkop') : _('Выборочная'));
		var rules = normalizeRulesArray(info.rules);
		var hasEnabledRoute = false;
		var hasDisabledRoute = false;
		var hasRouteWithoutDomains = false;

		lines.push(_('Режим:') + ' «' + modeLabel + '»   ' +
			_('Состояние:') + ' ' + (info.state_file || '-'));
		if (info.uplink_iface || info.uplink_device) {
			lines.push(_('Uplink:') + ' ' + (info.uplink_iface || 'auto') +
				' → ' + (info.uplink_device || '?') +
				(info.uplink_gateway ? ' via ' + info.uplink_gateway : ''));
		}
		if (mode === 'full') {
			lines.push(_('Полный туннель — правила доменов не используются.'));
			if (info.state_file === 'selective' || info.state_file === 'stale-selective')
				lines.push(_('(!) Selective routing ещё активен: Save & Apply внизу, затем «Переподключить».'));
			else if (info.state_file !== 'full')
				lines.push(_('(!) Save & Apply внизу страницы, затем «Переподключить» для полного туннеля.'));
			return lines.join('\n');
		}
		if (mode === 'external') {
			lines.push(_('WDTT: туннель wg-wdtt (маршруты — Podkop/sing-box).'));
			lines.push(_('Podkop → section → VPN → interface: wg-wdtt'));
			lines.push(_('Списки доменов — только в Podkop, не в WDTT.'));
			lines.push(_('Проверка: ssh → /usr/libexec/wdtt/podkop status'));
			if (info.state_file && info.state_file !== 'external' && info.state_file !== 'missing')
				lines.push(_('(!) Старый datapath: Save & Apply → «Переподключить».'));
			return lines.join('\n');
		}

		lines.push(_('IP в wdtt_route:') + ' ' + String(info.route_ips || 0));

		if (info.uci_parse_ok === false)
			lines.push(_('(!) UCI parse error — правила читаются из /etc/config/wdtt'));

		if (!rules.length) {
			lines.push(_('(правил нет в UCI)'));
			lines.push(_('Введите домены выше и нажмите «Принять изменения».'));
		} else {
			rules.forEach(function(r) {
				var en = r.enabled === '0' ? _('выкл') : _('вкл');
				var dom = r.domains || _('(нет доменов)');
				if (r.enabled !== '0' && r.type === 'route' && r.domains)
					hasEnabledRoute = true;
				if (r.enabled === '0' && r.type === 'route')
					hasDisabledRoute = true;
				if (r.type === 'route' && !r.domains)
					hasRouteWithoutDomains = true;
				lines.push('[' + r.name + '] ' + r.type + ' ' + en + ': ' + dom);
			});
			if (hasRouteWithoutDomains)
				lines.push(_('(!) В поле «Домены» введите сайты и нажмите «Принять изменения».'));
			else if (hasDisabledRoute && !hasEnabledRoute)
				lines.push(_('(!) Включите правило (галочка «Включено») и нажмите «Принять изменения».'));
			else if (!hasEnabledRoute && (info.route_ips || 0) === 0)
				lines.push(_('(!) Нет активных route-правил с доменами.'));
			else if (hasEnabledRoute && (info.route_ips || 0) === 0)
				lines.push(_('(i) IP появятся через ~1 мин после DNS (routing reload).'));
		}
		return lines.join('\n');
	},

	updateTrafficLine: function(st) {
		st = st || {};
		var el = document.getElementById('wdtt-traffic-line');
		if (!el)
			return;
		el.textContent = _('RX:') + ' ' + formatBytes(st.rx_bytes) +
			'   ' + _('TX:') + ' ' + formatBytes(st.tx_bytes) +
			'   ' + _('Воркеры:') + ' ' + String(st.workers || 0) +
			'   ' + _('Uptime:') + ' ' + String(st.uptime_sec || 0) + ' s';
	},

	refreshRulesLog: function() {
		var self = this;
		return callRoutingInfo().catch(function() { return {}; }).then(function(info) {
			var rulesLog = document.getElementById('wdtt-rules-log');
			if (!rulesLog)
				return;
			var text = self.formatRulesLog(info);
			if (text !== self._lastRulesText) {
				dom.content(rulesLog, text);
				self._lastRulesText = text;
			}
		});
	},

	pollStatus: function() {
		var self = this;
		return Promise.all([
			callStatus().catch(function() { return {}; }),
			callLogs(150).catch(function() { return { lines: [] }; })
		]).then(function(res) {
			var st = res[0] || {};
			var panel = document.getElementById('wdtt-status-panel');
			if (panel)
				dom.content(panel, self.renderStatus(st));

			if (st.enabled === '0' || st.enabled === '1')
				self.syncEnabledFlag(st.enabled);

			self.updateTrafficLine(st);
			self.refreshRulesLog();

			var connectLog = document.getElementById('wdtt-connect-log');
			if (connectLog) {
				var logLines = (res[1] && res[1].lines) || [];
				var stepText = formatConnectSteps(st, logLines);
				if (!stepText)
					stepText = _('Подключение ещё не начиналось — нажмите «Подключить».');
				if (stepText !== self._lastConnectText) {
					if (stepText.indexOf('\n') === -1 && stepText.indexOf('[') === -1)
						dom.content(connectLog, stepText);
					else
						renderColoredLog(connectLog, stepText.split('\n'), stepText, '#7dcea0');
					self._lastConnectText = stepText;
					connectLog.scrollTop = connectLog.scrollHeight;
				}
			}

			var logView = document.getElementById('wdtt-log-view');
			if (logView) {
				var lines = (res[1] && res[1].lines) || [];
				var newLog = lines.length ? lines.join('\n') : _('Лог пуст');
				if (newLog !== self._lastLogText) {
					var atBottom = logView.scrollHeight - logView.scrollTop <= logView.clientHeight + 5;
					if (lines.length)
						renderColoredLog(logView, lines, _('Лог пуст'), '#d4d4d4');
					else
						dom.content(logView, _('Лог пуст'));
					self._lastLogText = newLog;
					if (atBottom)
						logView.scrollTop = logView.scrollHeight;
				}
			}

			var capPanel = document.getElementById('wdtt-captcha-panel');
			if (capPanel && st.state === 'captcha_required') {
				var cap = st.captcha || {};
				var ta = document.getElementById('wdtt-captcha-url');
				if (cap.redirect_uri && (!ta || ta.value !== cap.redirect_uri)) {
					dom.content(capPanel, self.renderCaptchaPanel(cap));
				}
			}
		});
	},

	handleOpenCaptcha: function() {
		var ta = document.getElementById('wdtt-captcha-url');
		var uri = ta ? ta.value.trim() : '';
		if (!uri) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Ссылка пуста — нажмите «Подключить» для новой капчи')), 4000, 'warning');
			return;
		}
		window.open(uri, '_blank', 'noopener,noreferrer');
	},

	handleCopyCaptchaUrl: function() {
		var ta = document.getElementById('wdtt-captcha-url');
		var uri = ta ? ta.value.trim() : '';
		if (!uri) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Нечего копировать')), 3000, 'warning');
			return;
		}
		if (ta) {
			ta.focus();
			ta.select();
		}
		try {
			document.execCommand('copy');
			ui.addTimeLimitedNotification(null, E('p', {}, _('URL скопирован')), 2000, 'success');
		} catch (e) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Выделите URL и скопируйте вручную (Ctrl+C)')), 4000, 'warning');
		}
	},

	syncEnabledFlag: function(value) {
		uci.set('wdtt', 'globals', 'enabled', value);
		var el = document.querySelector('[data-widget-id="wdtt.globals.enabled"] input[type="checkbox"]')
			|| document.querySelector('input[name="cbid.wdtt.globals.enabled"]');
		if (el) {
			el.checked = (value === '1');
		}
	},

	uplinkButton: function(label, iface, current) {
		var cls = 'btn cbi-button';
		if (current === iface)
			cls += ' cbi-button-positive';
		return E('button', {
			'class': cls,
			'click': ui.createHandlerFn(this, function() {
				return this.handleSetUplink(iface);
			})
		}, label);
	},

	syncUplinkButtons: function(iface) {
		var box = document.getElementById('wdtt-uplink-buttons');
		if (!box)
			return;
		var buttons = box.querySelectorAll('button');
		for (var i = 0; i < buttons.length; i++) {
			var b = buttons[i];
			var active = (iface === 'auto' && b.textContent.indexOf('Авто') >= 0)
				|| (iface === 'wan' && b.textContent === 'WAN')
				|| (iface === 'wwan' && b.textContent === 'WWAN');
			b.className = 'btn cbi-button' + (active ? ' cbi-button-positive' : '');
		}
		var sel = document.querySelector('[data-widget-id="wdtt.globals.uplink_iface"] select')
			|| document.querySelector('select[name="cbid.wdtt.globals.uplink_iface"]');
		if (sel)
			sel.value = iface;
	},

	handleSetUplink: function(iface) {
		var self = this;
		uci.set('wdtt', 'globals', 'uplink_iface', iface);
		return uci.save().then(function() {
			return callApplyConfig();
		}).then(function(res) {
			if (res && res.error)
				throw new Error(res.error);
			self.syncUplinkButtons(iface);
			ui.addTimeLimitedNotification(null, E('p', {},
				_('Uplink:') + ' ' + iface + '. ' + _('Переподключите туннель, если уже подключён.')), 4000, 'success');
			self._lastRulesText = '';
			return Promise.all([ self.pollStatus(), self.refreshRulesLog() ]);
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleProfileSaveAs: function() {
		var input = document.getElementById('wdtt-profile-name');
		var name = cleanProfileName(input && input.value);
		if (!name)
			name = cleanProfileName(readGlobalsFormField('peer') || uci.get('wdtt', 'globals', 'peer') || '');
		if (!name) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Введите имя профиля')), 4000, 'warning');
			return Promise.resolve();
		}
		return this.saveProfileByName(name, false);
	},

	handleProfileSave: function() {
		var sel = document.getElementById('wdtt-profile-select');
		var sid = (sel && sel.value) || activeProfileId();
		if (!sid)
			return this.handleProfileSaveAs();
		var name = uci.get('wdtt', sid, 'name') || sid;
		copyGlobalsToProfile(sid);
		uci.set('wdtt', sid, 'name', name);
		uci.set('wdtt', 'globals', 'active_profile', sid);
		return this.persistProfiles(_('Профиль сохранён:') + ' ' + name);
	},

	saveProfileByName: function(name, silent) {
		name = cleanProfileName(name);
		var sid = findProfileByName(name);
		if (!sid)
			sid = uci.add('wdtt', 'profile');
		if (!sid) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Не удалось создать профиль')), 4000, 'danger');
			return Promise.resolve();
		}
		uci.set('wdtt', sid, 'name', name);
		copyGlobalsToProfile(sid);
		uci.set('wdtt', 'globals', 'active_profile', sid);
		var input = document.getElementById('wdtt-profile-name');
		if (input)
			input.value = '';
		return this.persistProfiles(silent ? null : (_('Профиль сохранён:') + ' ' + name));
	},

	handleProfileSwitch: function() {
		var self = this;
		var sel = document.getElementById('wdtt-profile-select');
		var sid = sel && sel.value;
		if (!sid) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Выберите профиль')), 3000, 'warning');
			return Promise.resolve();
		}
		applyProfileToGlobals(sid);
		var name = uci.get('wdtt', sid, 'name') || sid;
		var wasOn = uci.get('wdtt', 'globals', 'enabled') === '1';
		return uci.save().then(function() {
			return (typeof uci.apply === 'function') ? uci.apply() : Promise.resolve();
		}).then(function() {
			return callApplyConfig();
		}).then(function(res) {
			if (res && res.error)
				throw new Error(res.error);
			fillProfileSelect();
			self._lastConnectText = '';
			self._lastLogText = '';
			if (!wasOn) {
				ui.addTimeLimitedNotification(null, E('p', {},
					_('Профиль:') + ' ' + name + '. ' + _('Нажмите «Подключить».')), 5000, 'success');
				return self.pollStatus();
			}
			return callDisconnect().then(function() {
				return callConnect();
			}).then(function(cres) {
				if (cres && cres.error)
					throw new Error(cres.error);
				self.syncEnabledFlag('1');
				ui.addTimeLimitedNotification(null, E('p', {},
					_('Переключено на') + ' «' + name + '». ' + _('Туннель перезапускается...')), 5000, 'success');
				return self.pollStatus();
			});
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleProfileDelete: function() {
		var sel = document.getElementById('wdtt-profile-select');
		var sid = sel && sel.value;
		if (!sid) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Выберите профиль')), 3000, 'warning');
			return Promise.resolve();
		}
		var name = uci.get('wdtt', sid, 'name') || sid;
		if (!window.confirm(_('Удалить профиль') + ' «' + name + '»?'))
			return Promise.resolve();
		uci.remove('wdtt', sid);
		if (activeProfileId() === sid)
			uci.set('wdtt', 'globals', 'active_profile', '');
		return this.persistProfiles(_('Профиль удалён:') + ' ' + name);
	},

	persistProfiles: function(okMsg) {
		var self = this;
		return uci.save().then(function() {
			return (typeof uci.apply === 'function') ? uci.apply() : Promise.resolve();
		}).then(function() {
			return callApplyConfig().catch(function() { return {}; });
		}).then(function() {
			fillProfileSelect();
			if (okMsg)
				ui.addTimeLimitedNotification(null, E('p', {}, okMsg), 4000, 'success');
			return self.pollStatus();
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleImportURI: function() {
		var self = this;
		var ta = document.getElementById('wdtt-import-uri');
		var raw = ta ? ta.value : '';
		var cfg;
		try {
			cfg = parseWdttImport(raw);
		} catch (e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
			return Promise.resolve();
		}

		return callImportURI(cfg.peer, cfg.password, cfg.hashes, cfg.workers, cfg.listen || '127.0.0.1:9000')
			.then(function(res) {
				if (!res || res.error)
					throw new Error((res && res.error) || _('No related RPC reply'));
				syncGlobalsFormField('peer', cfg.peer);
				syncGlobalsFormField('password', cfg.password);
				applyImportedHashes(cfg.hashes);
				syncGlobalsFormField('workers', cfg.workers);
				try { uci.unload('wdtt'); } catch (e) {}
				return uci.load('wdtt');
			}).then(function() {
				if (ta)
					ta.value = '';
				var pname = cleanProfileName(cfg.name || cfg.peer);
				var after = Promise.resolve();
				if (pname)
					after = self.saveProfileByName(pname, true);
				return after.then(function() {
					ui.addTimeLimitedNotification(null, E('p', {},
						_('Импортировано:') + ' ' + (cfg.name || cfg.peer) + '. ' +
						_('Профиль сохранён — можно переключаться. Нажмите «Подключить».')), 6000, 'success');
					fillProfileSelect();
					return self.pollStatus();
				});
			}).catch(function(e) {
				var msg = e.message || String(e);
				if (/no related rpc reply/i.test(msg))
					msg = _('Импорт не прошёл: обновите WDTT (нужен RPC import_uri) и перезапустите rpcd.');
				ui.addTimeLimitedNotification(null, E('p', {}, msg), 7000, 'danger');
			});
	},

	handleConnect: function() {
		var self = this;
		return callConnect().then(function(res) {
			if (res && res.error) {
				throw new Error(res.error);
			}
			self.syncEnabledFlag('1');
			ui.addTimeLimitedNotification(null, E('p', {}, _('Туннель запускается...')), 3000);
			return self.pollStatus();
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleDisconnect: function() {
		var self = this;
		return callDisconnect().then(function(res) {
			if (res && res.error) {
				throw new Error(res.error);
			}
			self.syncEnabledFlag('0');
			var panel = document.getElementById('wdtt-status-panel');
			if (panel)
				dom.content(panel, self.renderStatus({
					state: 'stopped',
					running: false,
					wg_applied: false,
					wg_iface_up: false,
					enabled: '0',
					workers: 0,
					uptime_sec: 0
				}));
			ui.addTimeLimitedNotification(null, E('p', {}, _('Туннель остановлен')), 3000);
			return self.pollStatus();
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleApplyRules: function() {
		var self = this;
		var map = self.wdttMap;
		if (!map)
			return Promise.resolve();
		var rulesPayload = readDomainRulesFromPage();
		var hasDomains = Object.keys(rulesPayload).some(function(sid) {
			return rulesPayload[sid].domain_list;
		});
		if (!hasDomains)
			return Promise.reject(new Error(_('В поле «Домены» введите сайты (например youtube.com) и нажмите снова.')));

		return callApplyRules(rulesPayload).then(function(res) {
			if (res && res.error)
				throw new Error(res.error);
			return map.parse(true).then(function() {
				syncRuleFieldsFromDom();
				return uci.save();
			}).catch(function() {
				syncRuleFieldsFromDom();
				return uci.save().catch(function() { return null; });
			});
		}).then(function() {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Правила применены')), 3000, 'success');
			self._lastRulesText = '';
			return self.refreshRulesLog();
		}).catch(function(e) {
			ui.addTimeLimitedNotification(null, E('p', {}, e.message || String(e)), 5000, 'danger');
		});
	},

	handleCaptcha: function() {
		var input = document.getElementById('wdtt-captcha-token');
		var token = input ? input.value : '';
		if (!token) {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Введите токен')), 3000, 'warning');
			return;
		}
		return callCaptcha(token).then(function() {
			ui.addTimeLimitedNotification(null, E('p', {}, _('Токен отправлен')), 3000, 'success');
			if (input) input.value = '';
		});
	}
});
