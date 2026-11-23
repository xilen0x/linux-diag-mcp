// linux-diag Host Monitor: polls /api/snapshot and redraws every panel.
// No data is ever written with innerHTML: all text goes through textContent.
'use strict';

(() => {
  const DISK_WARN = 80, DISK_BAD = 90, CORE_BAD = 90;
  const HISTORY = 60;
  const GiB = 1024 ** 3, MiB = 1024 ** 2;
  const NS = 'http://www.w3.org/2000/svg';

  // The token arrives in the URL fragment, which browsers never send to the
  // server. Keep it for this tab only and remove it from the address bar.
  const TOKEN_KEY = 'linux-diag-token';
  let token = location.hash.slice(1);
  try {
    if (token) sessionStorage.setItem(TOKEN_KEY, token);
    else token = sessionStorage.getItem(TOKEN_KEY) || '';
  } catch (_) { /* storage blocked: keep the token in memory */ }
  if (location.hash) history.replaceState(null, '', location.pathname);

  const $ = (id) => document.getElementById(id);
  const el = (tag, props = {}, ...kids) => {
    const n = document.createElement(tag);
    Object.assign(n, props);
    kids.forEach((k) => n.append(k));
    return n;
  };
  const svgEl = (tag, attrs = {}) => {
    const n = document.createElementNS(NS, tag);
    for (const k in attrs) n.setAttribute(k, attrs[k]);
    return n;
  };
  const gib = (b, d = 2) => (b / GiB).toFixed(d);
  const mib = (b) => (b / MiB).toFixed(1);
  const tone = (v, warn, bad) => (v >= bad ? 'bad' : v >= warn ? 'warn' : 'ok');
  const fmtTime = (iso) => new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });

  const history = { cpu: [], iowait: [], mem: [], load1: [] };
  const push = (key, v) => {
    if (typeof v !== 'number') return;
    history[key].push(v);
    if (history[key].length > HISTORY) history[key].shift();
  };
  let prevNet = null; // { at: ms, byName: { name: {rx, tx} } } from the previous new snapshot
  let netRates = {};  // name -> { rx, tx } in KiB/s, computed once per new snapshot
  let last = null;    // last good response, redrawn on resize

  // ---------- small renderers ----------

  function unavailable(container, snapshot, key) {
    const msg = (snapshot.errors && snapshot.errors[key]) || 'no data';
    container.replaceChildren(el('p', { className: 'unavailable', textContent: `Unavailable: ${msg}` }));
  }

  function sparkline(values) {
    const W = 120, H = 26, pad = 2;
    const svg = svgEl('svg', { class: 'spark', viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none', 'aria-hidden': 'true' });
    if (values.length < 2) return svg;
    const max = Math.max(...values), min = Math.min(...values);
    const span = max - min || 1;
    const x = (i) => (i / (values.length - 1)) * W;
    const y = (v) => H - pad - ((v - min) / span) * (H - 2 * pad);
    const pts = values.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`);
    svg.append(svgEl('path', { class: 'area', d: `M0,${H} L${pts.join(' L')} L${W},${H} Z` }));
    svg.append(svgEl('polyline', { class: 'line', points: pts.join(' ') }));
    svg.append(svgEl('circle', { class: 'end', cx: x(values.length - 1), cy: y(values[values.length - 1]), r: 2.2 }));
    return svg;
  }

  function attachTip(container, target, text) {
    let tip = null;
    target.addEventListener('pointermove', (ev) => {
      if (!tip) { tip = el('div', { className: 'tip' }); container.append(tip); }
      const r = container.getBoundingClientRect();
      tip.textContent = text;
      tip.style.left = Math.min(ev.clientX - r.left + 12, container.clientWidth - 180) + 'px';
      tip.style.top = (ev.clientY - r.top - 32) + 'px';
    });
    target.addEventListener('pointerleave', () => { if (tip) tip.remove(); tip = null; });
  }

  function table(id, cols, rows) {
    const t = $(id);
    const tr = el('tr');
    cols.forEach((c) => tr.append(el('th', { textContent: c.h, className: c.cls || '' })));
    const tbody = el('tbody');
    if (!rows.length) {
      tbody.append(el('tr', {}, el('td', { textContent: 'Nothing to show.', colSpan: cols.length })));
    }
    rows.forEach((r) => {
      const row = el('tr');
      cols.forEach((c) => {
        const td = el('td', { className: c.cls || '' });
        const v = c.v(r);
        if (v instanceof Node) td.append(v);
        else td.textContent = v === null || v === undefined || v === '' ? '—' : String(v);
        row.append(td);
      });
      tbody.append(row);
    });
    t.replaceChildren(el('thead', {}, tr), tbody);
  }

  function tableUnavailable(id, snapshot, key) {
    const t = $(id);
    const td = el('td', { className: 'unavailable', textContent: `Unavailable: ${(snapshot.errors && snapshot.errors[key]) || 'no data'}` });
    t.replaceChildren(el('tbody', {}, el('tr', {}, td)));
  }

  const pill = (text, cls) => el('span', { className: `pill ${cls}`, textContent: text });

  function bar(pct) {
    const f = el('div', { className: `f ${tone(pct, DISK_WARN, DISK_BAD)}` });
    f.style.width = Math.max(0, Math.min(100, pct)) + '%';
    return el('div', { className: 'bar' }, el('div', { className: 't' }, f), el('span', { textContent: pct + '%' }));
  }

  function scope(addr) {
    const a = String(addr);
    if (a.startsWith('127.') || a === '::1') return pill('This machine only', 'ok');
    if (a === '0.0.0.0' || a === '::') return pill('All interfaces', 'warn');
    const first = Number(a.split('.')[0]);
    if ((first >= 224 && first <= 239) || a.toLowerCase().startsWith('ff')) return pill('Multicast group', 'muted');
    return pill('One interface', 'muted');
  }

  // ---------- panels ----------

  function drawHeader(s) {
    const sys = s.tools.system_info;
    const when = fmtTime(s.captured_at);
    $('sub').textContent = sys
      ? `${sys.os && sys.os.pretty_name ? sys.os.pretty_name : 'Linux'} · kernel ${sys.kernel_release}`
      : 'System information unavailable';

    const take = $('take');
    const disks = (s.tools.disk_usage && s.tools.disk_usage.filesystems) || [];
    const fullest = disks.slice().sort((a, b) => b.used_percent - a.used_percent)[0];
    const failed = s.tools.failed_units ? (s.tools.failed_units.units || []).length : null;
    const cpu = s.tools.cpu_usage;
    const parts = [];
    const b = (text, bad) => el('b', { textContent: text, className: bad ? 'bad' : '' });
    if (fullest) parts.push(['The fullest filesystem is ', el('code', { textContent: fullest.mount_point }), ' at ', b(`${fullest.used_percent}%`, fullest.used_percent >= DISK_BAD), '. ']);
    if (failed !== null) parts.push([b(String(failed), failed > 0), failed === 1 ? ' systemd unit is failed. ' : ' systemd units are failed. ']);
    if (cpu) parts.push(['At ', when, ' the CPU was ', b(`${cpu.usage_percent}%`), ' busy with ', b(`${cpu.iowait_percent}%`), ' I/O wait.']);
    take.replaceChildren(...parts.flat());
  }

  function drawTiles(s) {
    const cpu = s.tools.cpu_usage, mem = s.tools.memory_usage, load = s.tools.load_average, sys = s.tools.system_info;
    const failed = s.tools.failed_units ? (s.tools.failed_units.units || []).length : null;
    const procs = s.tools.top_processes_cpu;
    const tiles = [
      cpu && { label: 'CPU usage', value: cpu.usage_percent, unit: '%', status: tone(cpu.usage_percent, 70, 90), spark: 'cpu',
        notes: [`user ${cpu.user_percent}% · system ${cpu.system_percent}%`] },
      cpu && { label: 'I/O wait', value: cpu.iowait_percent, unit: '%', status: tone(cpu.iowait_percent, 10, 30), spark: 'iowait',
        notes: [`idle ${cpu.idle_percent}% · steal ${cpu.steal_percent}%`] },
      mem && { label: 'Memory used', value: mem.used_percent, unit: '%', status: tone(mem.used_percent, 80, 90), spark: 'mem',
        notes: [`${gib(mem.used_bytes)} of ${gib(mem.total_bytes)} GiB`] },
      mem && { label: 'Swap used', value: mem.swap_used_percent, unit: '%', status: tone(mem.swap_used_percent, 25, 50),
        notes: [`${gib(mem.swap_used_bytes)} of ${gib(mem.swap_total_bytes)} GiB`] },
      load && { label: 'Load, 1 min', value: load.load1, unit: '', status: '', spark: 'load1',
        notes: [`5 min ${load.load5} · 15 min ${load.load15}`] },
      sys && { label: 'Uptime', value: (sys.uptime_seconds / 3600).toFixed(1), unit: 'h', status: '',
        notes: [procs ? `${procs.total_processes} processes` : ''] },
      failed !== null && { label: 'Failed units', value: failed, unit: '', status: failed ? 'bad' : 'ok',
        notes: ['systemd units in the failed state'] },
    ].filter(Boolean);
    $('tiles').replaceChildren(...tiles.map((t) => {
      const big = el('span', { className: 'big', textContent: String(t.value) });
      if (t.unit) big.append(el('small', { textContent: t.unit }));
      const tile = el('div', { className: `tile ${t.status === 'ok' ? '' : t.status}` },
        el('span', { className: 'label', textContent: t.label }), big,
        ...t.notes.filter(Boolean).map((n) => el('p', { className: 'note', textContent: n })));
      if (t.spark) tile.append(sparkline(history[t.spark]));
      return tile;
    }));
  }

  function drawCpu(s) {
    const box = $('cpu-chart');
    const cpu = s.tools.cpu_usage;
    if (!cpu || !Array.isArray(cpu.per_cpu)) { unavailable(box, s, 'cpu_usage'); return; }
    const rows = cpu.per_cpu;
    const W = Math.max(260, box.clientWidth), rowH = 30, m = { l: 52, r: 60, t: 6, b: 24 };
    const H = m.t + m.b + rowH * rows.length;
    const x = (v) => m.l + (Math.min(100, Math.max(0, v)) / 100) * (W - m.l - m.r);
    const svg = svgEl('svg', { viewBox: `0 0 ${W} ${H}`, role: 'img',
      'aria-label': 'Busy share per logical CPU: ' + rows.map((r) => `CPU ${r.cpu} ${r.usage_percent}%`).join(', ') });
    const ticks = W < 360 ? [0, 50, 100] : [0, 25, 50, 75, 100];
    ticks.forEach((t) => {
      svg.append(svgEl('line', { x1: x(t), x2: x(t), y1: m.t, y2: H - m.b, class: 'grid' }));
      const tx = svgEl('text', { x: x(t), y: H - 6, 'text-anchor': 'middle', class: 'tick' });
      tx.textContent = t + '%';
      svg.append(tx);
    });
    box.replaceChildren(svg);
    rows.forEach((r, i) => {
      const y = m.t + i * rowH;
      const lbl = svgEl('text', { x: 0, y: y + rowH / 2, dy: '0.35em' });
      lbl.textContent = 'CPU ' + r.cpu;
      const rect = svgEl('rect', { x: x(0), y: y + 5, height: rowH - 10, width: Math.max(0, x(r.usage_percent) - x(0)), rx: 3,
        class: r.usage_percent >= CORE_BAD ? 'core hot' : 'core' });
      const val = svgEl('text', { x: x(r.usage_percent) + 6, y: y + rowH / 2, dy: '0.35em' });
      val.textContent = r.usage_percent + '%';
      svg.append(lbl, rect, val);
      attachTip(box, rect, `CPU ${r.cpu}: ${r.usage_percent}% busy`);
    });
  }

  function drawMemory(s) {
    const mem = s.tools.memory_usage;
    const box = $('mem-bar');
    if (!mem) { unavailable(box, s, 'memory_usage'); $('mem-note').textContent = ''; $('mem-kv').replaceChildren(); return; }
    $('mem-note').textContent = `${gib(mem.used_bytes)} GiB in use, ${gib(mem.available_bytes)} GiB available to new programs.`;
    const track = el('div', { className: 'memtrack' });
    track.setAttribute('role', 'img');
    track.setAttribute('aria-label', `${gib(mem.used_bytes)} GiB used and ${gib(mem.available_bytes)} GiB available of ${gib(mem.total_bytes)} GiB`);
    box.replaceChildren(track);
    [['Used', mem.used_bytes, 'seg-used'], ['Available', mem.available_bytes, 'seg-avail']].forEach(([name, bytes, cls]) => {
      const seg = el('div', { className: cls });
      seg.style.width = (mem.total_bytes ? (bytes / mem.total_bytes) * 100 : 0) + '%';
      track.append(seg);
      attachTip(box, seg, `${name}: ${gib(bytes)} GiB of ${gib(mem.total_bytes)} GiB`);
    });
    const rows = [['Used', mem.used_bytes, 'seg-used'], ['Available', mem.available_bytes, 'seg-avail'],
      ['Free', mem.free_bytes], ['Page cache', mem.cached_bytes], ['Buffers', mem.buffers_bytes], ['Swap used', mem.swap_used_bytes]];
    $('mem-kv').replaceChildren(...rows.map(([k, v, cls]) => {
      const dt = el('dt');
      if (cls) dt.append(el('span', { className: `sw ${cls}` }));
      dt.append(k);
      return el('div', {}, dt, el('dd', { textContent: `${gib(v)} GiB` }));
    }));
  }

  function drawTables(s) {
    const t = s.tools;
    if (t.disk_usage) {
      table('t-disks', [
        { h: 'Mount', cls: 'mono', v: (d) => d.mount_point },
        { h: 'Device', cls: 'mono', v: (d) => d.device },
        { h: 'Type', v: (d) => d.fs_type },
        { h: 'Used', v: (d) => bar(d.used_percent) },
        { h: 'Used GiB', cls: 'num', v: (d) => gib(d.used_bytes, 1) },
        { h: 'Size GiB', cls: 'num', v: (d) => gib(d.total_bytes, 1) },
        { h: 'Free GiB', cls: 'num', v: (d) => gib(d.available_bytes, 1) },
        { h: 'Inodes %', cls: 'num', v: (d) => d.inodes_used_percent },
      ], t.disk_usage.filesystems || []);
    } else tableUnavailable('t-disks', s, 'disk_usage');

    const procCols = (memFirst) => {
      const pid = { h: 'PID', cls: 'num', v: (p) => p.pid };
      const comm = { h: 'Process', cls: 'mono', v: (p) => p.comm };
      const cpuC = { h: 'CPU %', cls: 'num', v: (p) => p.cpu_percent };
      const rss = { h: 'RSS MiB', cls: 'num', v: (p) => mib(p.rss_bytes) };
      const st = { h: 'State', v: (p) => p.state };
      return memFirst ? [pid, comm, rss, cpuC, st] : [pid, comm, st, cpuC, rss];
    };
    if (t.top_processes_cpu) table('t-pcpu', procCols(false), t.top_processes_cpu.processes || []);
    else tableUnavailable('t-pcpu', s, 'top_processes_cpu');
    if (t.top_processes_mem) table('t-pmem', procCols(true), t.top_processes_mem.processes || []);
    else tableUnavailable('t-pmem', s, 'top_processes_mem');

    const ports = [];
    ['tcp', 'udp'].forEach((p) => {
      const r = t['listening_ports_' + p];
      if (r) (r.listeners || []).forEach((l) => ports.push({ ...l, protocol: p }));
    });
    if (t.listening_ports_tcp || t.listening_ports_udp) {
      table('t-ports', [
        { h: 'Proto', v: (p) => p.protocol.toUpperCase() },
        { h: 'Address', cls: 'mono', v: (p) => p.address },
        { h: 'Port', cls: 'num', v: (p) => p.port },
        { h: 'Reachable from', v: (p) => scope(p.address) },
      ], ports);
    } else tableUnavailable('t-ports', s, 'listening_ports_tcp');

    if (t.network_interfaces) {
      const ifs = t.network_interfaces.interfaces || [];
      const rate = (n, dir) => {
        const r = netRates[n.name];
        return r && r[dir] !== null ? r[dir].toFixed(1) : '—';
      };
      table('t-net', [
        { h: 'Interface', cls: 'mono', v: (n) => n.name },
        { h: 'RX KiB/s', cls: 'num', v: (n) => rate(n, 'rx') },
        { h: 'TX KiB/s', cls: 'num', v: (n) => rate(n, 'tx') },
        { h: 'RX GiB', cls: 'num', v: (n) => gib(n.rx_bytes) },
        { h: 'TX GiB', cls: 'num', v: (n) => gib(n.tx_bytes) },
        { h: 'RX drop', cls: 'num', v: (n) => n.rx_dropped },
        { h: 'Errors', cls: 'num', v: (n) => n.rx_errors + n.tx_errors },
      ], ifs);
    } else tableUnavailable('t-net', s, 'network_interfaces');

    if (t.failed_units) {
      const services = s.services || {};
      table('t-units', [
        { h: 'Unit', cls: 'mono', v: (u) => u.name },
        { h: 'Description', v: (u) => u.description },
        { h: 'State', v: (u) => pill(u.active_state, u.active_state === 'failed' ? 'bad' : 'muted') },
        { h: 'Result', cls: 'mono', v: (u) => (services[u.name] || {}).result },
        { h: 'Unit file', v: (u) => (services[u.name] || {}).unit_file_state },
      ], t.failed_units.units || []);
    } else tableUnavailable('t-units', s, 'failed_units');
  }

  // Rates need two different snapshots; counters that went backwards
  // (interface reset) give no rate.
  function updateNetRates(s) {
    const ni = s.tools && s.tools.network_interfaces;
    if (!ni) return;
    const at = Date.parse(s.captured_at);
    const ifs = ni.interfaces || [];
    const secs = prevNet ? (at - prevNet.at) / 1000 : 0;
    netRates = {};
    ifs.forEach((n) => {
      const p = prevNet && prevNet.byName[n.name];
      const r = (cur, old) => (p && secs > 0 && cur >= old ? (cur - old) / 1024 / secs : null);
      netRates[n.name] = { rx: p ? r(n.rx_bytes, p.rx) : null, tx: p ? r(n.tx_bytes, p.tx) : null };
    });
    prevNet = { at, byName: Object.fromEntries(ifs.map((n) => [n.name, { rx: n.rx_bytes, tx: n.tx_bytes }])) };
  }

  // ---------- polling ----------

  function setLive(state, text) {
    $('live').className = `live ${state}`;
    $('live-text').textContent = text;
  }

  function showBanner(text) {
    const b = $('banner');
    b.textContent = text;
    b.hidden = !text;
  }

  function render(resp) {
    const s = resp.snapshot;
    s.tools = s.tools || {};
    drawHeader(s);
    drawTiles(s);
    drawCpu(s);
    drawMemory(s);
    drawTables(s);
  }

  let timer = null;
  let interval = 5000;
  let lastCaptured = null;

  async function tick() {
    timer = null;
    if (document.hidden) return; // resumes on visibilitychange
    if (!token) {
      setLive('down', 'No access token');
      showBanner('Open the full URL that linux-diag-dashboard printed when it started. It ends in #<token>.');
      return;
    }
    try {
      const res = await fetch('/api/snapshot', { headers: { Authorization: 'Bearer ' + token }, cache: 'no-store' });
      if (res.status === 401) {
        setLive('down', 'Access denied');
        showBanner('The access token was rejected. Restarting linux-diag-dashboard creates a new token: open the new URL it printed.');
        return; // a wrong token will not fix itself
      }
      if (!res.ok) throw new Error('HTTP ' + res.status);
      const body = await res.json();
      interval = Math.max(1000, body.refresh_ms || interval);
      const s = body.snapshot;
      if (s.captured_at !== lastCaptured) {
        lastCaptured = s.captured_at;
        const t = s.tools || {};
        if (t.cpu_usage) { push('cpu', t.cpu_usage.usage_percent); push('iowait', t.cpu_usage.iowait_percent); }
        if (t.memory_usage) push('mem', t.memory_usage.used_percent);
        if (t.load_average) push('load1', t.load_average.load1);
        updateNetRates(s);
      }
      last = body;
      render(body);
      showBanner('');
      setLive('ok', `Live · updated ${fmtTime(s.captured_at)} · every ${Math.round(interval / 1000)} s`);
    } catch (_) {
      setLive(last ? 'stale' : 'down', last ? `Connection lost · showing ${fmtTime(last.snapshot.captured_at)} · retrying` : 'Cannot reach linux-diag-dashboard · retrying');
    }
    timer = setTimeout(tick, interval);
  }

  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && timer === null) tick();
  });

  let resizeTimer = null;
  window.addEventListener('resize', () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => { if (last) drawCpu(last.snapshot); }, 120);
  });

  tick();
})();
