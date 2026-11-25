"use strict";
const messages = {
    platform: '平台健康与诊断',
    overview: '运行概览', catalog: '服务目录', signals: '指标与链路', events: '事件证据',
    incidents: '告警处理', audits: '授权与审计', sources: '接入与能力', notifications: '站内通知',
    loading: '正在读取真实来源…', empty: '当前条件下没有结果；这不表示没有异常。',
    unavailable: '暂时无法读取，请稍后重试。', original: '下载来源原件',
    unknown: '当前待确认', partial: '部分证据可读', unsupported: '该来源未提供',
    firing: '需要核验', acknowledged: '已确认处理责任', resolved: '规则当前未触发',
};
const get = (id) => document.getElementById(id);
let page = 'overview';
let generation = 0;
let controller;
let platformAccess = false;
let canManage = false;
const view = get('view');
const notice = (s) => { get('notice').textContent = s; };
function node(tag, text = '', cls = '') { const e = document.createElement(tag); e.textContent = text; e.className = cls; return e; }
function panel(title) { const p = node('section', '', 'panel'); p.append(node('h2', title)); view.append(p); return p; }
const obj = (value) => value !== null && typeof value === 'object' && !Array.isArray(value) ? value : {};
const array = (value) => Array.isArray(value) ? value.map(obj) : [];
const results = (value) => array(obj(obj(value.data).data).result);
function numeric(value) {
    if (value === null || value === undefined || value === '')
        return null;
    const n = Number(value);
    return Number.isFinite(n) ? n : null;
}
function quantity(value) {
    const m = String(value ?? '').match(/^([0-9]+(?:\.[0-9]+)?)([a-zA-Z]*)$/);
    if (!m)
        return null;
    const units = { '': 1, n: 1e-9, u: 1e-6, m: 1e-3, Ki: 1024, Mi: 1024 ** 2, Gi: 1024 ** 3, Ti: 1024 ** 4, k: 1000, K: 1000, M: 1e6, G: 1e9 };
    const scale = units[m[2] || ''];
    return scale === undefined ? null : Number(m[1]) * scale;
}
function resourceUsage(usage, capacity) { const cpu = quantity(usage.cpu), mem = quantity(usage.memory), totalCPU = quantity(capacity.cpu), totalMem = quantity(capacity.memory); return (cpu === null ? 'CPU 未提供' : cpu.toFixed(2) + ' 核' + (totalCPU ? '（' + (cpu / totalCPU * 100).toFixed(1) + '%）' : '')) + ' / ' + (mem === null ? '内存未提供' : (mem / 1024 ** 3).toFixed(2) + ' GiB' + (totalMem ? '（' + (mem / totalMem * 100).toFixed(1) + '%）' : '')); }
function sample(result) { return Array.isArray(result.value) ? numeric(result.value[1]) : null; }
function metricValue(data, unit) { const values = results(data); const n = values.length ? sample(values[0]) : null; return n === null ? '暂无有效样本' : unit === '%' ? (n * 100).toFixed(2) + '%' : unit === 'ms' ? (n * 1000).toFixed(1) + ' ms' : n.toFixed(3) + unit; }
function card(parent, title, value, explain) { const c = node('section', '', 'card'); c.append(node('h3', title), node('strong', value), node('p', explain)); parent.append(c); }
function lineChart(parent, data, label) {
    const series = results(data);
    const points = series.flatMap(s => Array.isArray(s.values) ? s.values : []);
    const finite = points.map(v => numeric(v[1])).filter((v) => v !== null);
    if (!finite.length) {
        parent.append(node('p', '当前时间窗没有有效趋势样本，请检查抓取状态与数据新鲜度。', 'empty'));
        return;
    }
    const max = Math.max(...finite, 0.001);
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 800 150');
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', label + '；最高 ' + max.toFixed(3) + ' 次/秒；断点表示无有效样本');
    svg.classList.add('trend');
    let segment = [];
    const draw = () => {
        if (segment.length) {
            const line = document.createElementNS(svg.namespaceURI, 'polyline');
            line.setAttribute('points', segment.join(' '));
            line.setAttribute('fill', 'none');
            line.setAttribute('stroke', '#3158c5');
            line.setAttribute('stroke-width', '2');
            svg.append(line);
            segment = [];
        }
    };
    points.forEach((v, i) => {
        const n = numeric(v[1]);
        if (n === null) {
            draw();
            return;
        }
        segment.push((20 + i / Math.max(1, points.length - 1) * 760).toFixed(1) + ',' + (130 - n / max * 110).toFixed(1));
    });
    draw();
    parent.append(svg, node('p', label + ' · 范围 0—' + max.toFixed(3) + ' 次/秒；缺失样本不补零。', 'muted'));
}
async function platformView(current) {
    const d = await api('platform/dashboard');
    if (generation !== current)
        return;
    const grid = node('div', '', 'grid');
    card(grid, '预期节点', String(d.expectedNodes), '登记时固定的节点身份集合；发现新节点需要重新核验登记。');
    card(grid, '新鲜且就绪', String(d.readyNodes) + ' / ' + String(d.expectedNodes), '当前 Ready 条件与 90 秒观察窗口共同判断。');
    card(grid, '过期或未观察到', String(d.unknownNodes), '采集失效与节点故障分别核验；缺失节点保留在分母。');
    card(grid, '证据覆盖', '平台只读 · 部分', '当前快照与指标帮助定位问题，不改变原业务操作结果。');
    view.append(grid);
    const diagnostic = panel('当前需要处理或核验的问题');
    const diagnostics = array(d.diagnostics);
    if (!diagnostics.length)
        diagnostic.append(node('p', '当前已观察范围内未触发诊断条件；仍需核对指标、来源质量与未启用能力。'));
    diagnostics.forEach(item => { const p = node('article', '', 'diagnostic'); p.append(node('h3', String(item.title)), node('p', String(item.nextStep)), node('p', '证据时刻：' + time(item.observedAt) + ' · 手册：' + String(item.runbook), 'muted')); diagnostic.append(p); });
    const inventory = array(d.inventory);
    table(panel('节点资源与身份'), ['节点', '当前观察', 'CPU / 内存用量', '可分配容量', '用量样本时刻', '资源最后观察'], inventory.filter(v => v.kind === 'kubernetes.node').map(v => { const s = obj(v.snapshot); const conditions = array(s.conditions); const state = conditions.find(c => c.type === 'Ready'); const usage = obj(obj(s.usage).values); const capacity = obj(s.allocatable); const mem = quantity(capacity.memory); return [String(v.name), v.stale ? '观察已过期' : state?.status === 'True' ? '就绪' : '需要核验', s.usageAvailable ? resourceUsage(usage, capacity) : 'metrics-server 暂不可读或样本已过期', String(quantity(capacity.cpu) ?? '未知') + ' 核 / ' + (mem === null ? '未提供' : (mem / 1024 ** 3).toFixed(1) + ' GiB'), time(obj(s.usage).asOf), time(v.observedAt)]; }));
    table(panel('命名空间运行健康'), ['命名空间', '就绪 / 观察 Pod', '异常启动 / 副本创建失败', '待绑定 PVC', '历史失败 / 已完成', '配额与观察质量'], inventory.filter(v => v.kind === 'kubernetes.namespace.health').map(v => {
        const s = obj(v.snapshot);
        const detail = node('details');
        detail.append(node('summary', v.stale ? '数据过期 · 待确认' : '查看配额 hard / used'));
        for (const q of array(s.quotas)) {
            const hard = obj(q.hard), used = obj(q.used);
            for (const key of Object.keys(hard))
                detail.append(node('p', String(q.name) + ' · ' + key + '：' + String(used[key] ?? '未提供') + ' / ' + String(hard[key]), 'muted'));
        }
        if (!array(s.quotas).length)
            detail.append(node('p', '未获得已配置配额；这不表示容量无限。'));
        return [String(v.name), String(s.ready) + ' / ' + String(s.pods), String(s.crashLoops) + ' / ' + String(s.controllerFailures ?? '未提供'), String(s.pendingPVCs), String(s.historicalFailed) + ' / ' + String(s.completed), detail];
    }));
    const quality = panel('来源身份与采集质量');
    table(quality, ['来源', '当前状态', '最近成功观察', '预期 / 已观察', '错误代码'], array(d.sources).map(s => [String(s.source), s.stale ? '观察已过期 · 当前待确认' : s.state === 'observed_partial' ? '已观察 · 部分覆盖' : s.state === 'observed_incomplete' ? '预期采集目标缺少样本' : s.state === 'identity_changed' ? '身份变化 · 已停止接纳' : '来源暂不可读', time(s.lastSuccessAt), String(Array.isArray(s.expectedSet) ? s.expectedSet.length : 0) + ' / ' + String(Array.isArray(s.observedSet) ? s.observedSet.length : 0), String(s.errorCode || '无已记录错误')]));
    const metricPanel = panel('基础设施指标快照');
    metricPanel.append(node('p', '数据来自平台域固定查询。空结果表示未获得样本；正常零值单独显示。', 'muted'));
    for (const [metric, title, unit] of [['node_cpu', 'CPU 使用率', '%'], ['node_memory', '内存使用率', '%'], ['disk_used', '文件系统使用率', '%'], ['network_drops', '网卡丢包速率', '次/秒'], ['dns_failures', 'DNS 失败响应速率', '次/秒'], ['collector_refused', '采集入口拒绝速率', '条/秒'], ['collector_send_failed', '日志投递失败速率', '条/秒']]) {
        try {
            const data = await api('platform/query?metric=' + metric);
            if (generation !== current)
                return;
            const rows = results(data);
            table(metricPanel, [title, '当前值'], rows.map(item => { const n = sample(item); const labels = obj(item.metric); return [String(labels.node || labels.instance || labels.job || '已登记平台范围'), n === null ? '无有效样本' : unit === '%' ? (n * 100).toFixed(2) + '%' : n.toFixed(3) + ' ' + unit]; }));
        }
        catch (e) {
            metricPanel.append(node('p', title + '：' + e.message, 'muted'));
        }
    }
    panel('平台观测边界').append(node('p', '当前覆盖节点资源、命名空间健康、采集链路和服务遥测。内核级网络流、持续剖析、审计全量历史、跨节点存储高可用尚未获本环境运行资格；Neon 领域接入关闭。'));
}
function supportsSignal(source, signal) {
    if (Array.isArray(source.signals) && source.signals.length)
        return source.signals.includes(signal);
    if (source.kind === 'kubernetes_cluster')
        return signal === 'inventory';
    if (source.kind === 'legacy_archive')
        return signal === 'logs' || signal === 'traces';
    if (source.kind === 'telemetry_only')
        return signal === 'metrics';
    return true;
}
async function traceDetail(parent, source, traceID) {
    const detail = await api('query?' + new URLSearchParams({ source: String(source.id), signal: 'traces', traceID }));
    const target = node('section', '', 'trace-detail');
    target.append(node('h3', 'Trace ' + traceID));
    const spans = [];
    for (const batch of array(obj(detail.data).batches || obj(detail.data).resourceSpans))
        for (const scope of array(batch.scopeSpans || batch.instrumentationLibrarySpans))
            spans.push(...array(scope.spans));
    table(target, ['操作', 'Span ID', '耗时', '结果'], spans.map(span => { const duration = (Number(span.endTimeUnixNano) - Number(span.startTimeUnixNano)) / 1e6; const code = obj(span.status).code; return [String(span.name), String(span.spanId), Number.isFinite(duration) ? duration.toFixed(2) + ' ms' : '未提供', code === 2 || code === 'STATUS_CODE_ERROR' ? '观察到错误' : '按 Span 原始状态核验']; }));
    parent.append(target);
}
async function renderLogs(output, source) {
    const result = await api('query?' + new URLSearchParams({ source: String(source.id), signal: 'logs' }));
    output.replaceChildren(node('h3', '日志样本 · ' + (source.kind === 'legacy_archive' ? '保留范围 48 小时' : '最近 15 分钟') + ' · 最多 100 条'));
    const rows = [];
    for (const stream of array(obj(obj(result.data).data).result))
        for (const entry of Array.isArray(stream.values) ? stream.values : []) {
            const raw = String(entry[1]);
            let payload = {};
            try {
                payload = obj(JSON.parse(raw));
            }
            catch { /* Opaque text stays escaped plain text. */ }
            const labels = obj(stream.stream), metadata = obj(entry[2]);
            const traceID = String(metadata.trace_id || payload.traceId || '');
            const link = /^[a-fA-F0-9]{32}$/.test(traceID) && supportsSignal(source, 'traces') ? button('查看关联调用链', () => traceDetail(output, source, traceID)) : '此样本未提供可读取的链路上下文';
            rows.push([time(Number(entry[0]) / 1e6), String(payload.status || payload.code || labels.severity_text || labels.detected_level || '观察样本'), raw, link]);
        }
    table(output, ['观察时间', '状态 / 等级', '日志内容', '关联请求'], rows);
    output.append(node('p', '日志可能延迟、截断或采样；没有样本不代表没有异常。', 'muted'));
}
async function signalView(source, current) {
    const p = panel(String(source.displayName || source.service || source.id));
    p.append(node('p', '来源：' + String(source.id) + ' · 当前组织：' + String(source.tenant) + ' · 由服务端核验信号能力与访问范围。', 'muted'));
    const reference = source.queryProfile === 'reference-http-v1';
    if (supportsSignal(source, 'metrics')) {
        const grid = node('div', '', 'grid');
        p.append(grid);
        const metrics = reference ? [['up', '抓取可达', ''], ['requests', '请求速率', ' 次/秒'], ['error_ratio', '失败比例', '%'], ['latency_p95', 'P95 响应耗时估算', 'ms']] : [['up', '采集目标可达', '']];
        for (const [metric, title, unit] of metrics) {
            try {
                const data = await api('query?' + new URLSearchParams({ source: String(source.id), signal: 'metrics', metric: metric }));
                if (generation !== current)
                    return;
                let value = metricValue(data, unit);
                if (metric === 'up') {
                    const targets = results(data);
                    value = !targets.length ? '未获得抓取样本' : String(targets.filter(v => sample(v) === 1).length) + ' / ' + String(Array.isArray(source.expectedTargets) ? source.expectedTargets.length : targets.length) + ' 个目标可抓取';
                }
                card(grid, title, value, metric === 'up' ? '可达不等于服务正常；还需核对当前请求与采集质量。' : '最近 5 分钟窗口；低流量或无请求时可能无法计算。');
            }
            catch (e) {
                card(grid, title, '暂不可读', e.message);
            }
        }
        if (reference) {
            const graph = node('div');
            p.append(graph);
            const label = node('label', '请求趋势时间范围');
            const selector = document.createElement('select');
            selector.setAttribute('aria-label', '请求趋势时间范围');
            for (const [value, text] of [['15m', '最近 15 分钟'], ['1h', '最近 1 小时']]) {
                const option = document.createElement('option');
                option.value = value;
                option.textContent = text;
                selector.append(option);
            }
            label.append(selector);
            graph.before(label);
            const readTrend = async () => {
                graph.replaceChildren();
                try {
                    const data = await api('query?' + new URLSearchParams({ source: String(source.id), signal: 'metrics', metric: 'requests', window: selector.value }));
                    if (generation !== current)
                        return;
                    lineChart(graph, data, '实际请求速率 · ' + (selector.value === '1h' ? '最近 1 小时' : '最近 15 分钟'));
                }
                catch (e) {
                    graph.append(node('p', e.message, 'muted'));
                }
            };
            selector.addEventListener('change', () => void readTrend());
            await readTrend();
        }
    }
    const actions = node('div', '', 'actions'), output = node('div');
    if (supportsSignal(source, 'logs'))
        actions.append(button(source.kind === 'legacy_archive' ? '读取切换前历史日志' : '读取最近日志', () => renderLogs(output, source)));
    if (supportsSignal(source, 'traces'))
        actions.append(button('读取调用链样本', async () => {
            const result = await api('query?' + new URLSearchParams({ source: String(source.id), signal: 'traces' }));
            output.replaceChildren(node('h3', '调用链样本 · ' + (source.kind === 'legacy_archive' ? '保留范围 48 小时' : '最近 15 分钟') + ' · 最多 20 条'));
            table(output, ['Trace ID', '服务 / 根操作', '耗时', '明细'], array(obj(result.data).traces).map(trace => [String(trace.traceID), String(trace.rootServiceName || '根服务尚未提供') + ' / ' + String(trace.rootTraceName || '根操作尚未提供'), String(trace.durationMs) + ' ms', button('查看调用明细', () => traceDetail(output, source, String(trace.traceID)))]));
            output.append(node('p', '当前租户分区内的已观察样本；采样完整性尚未获证明。', 'muted'));
        }));
    p.append(actions, output);
}
async function api(path, init) {
    const res = await fetch('/api/v1/' + path, { credentials: 'same-origin', signal: controller?.signal, ...init });
    const body = await res.json();
    if (!res.ok) {
        if (res.status === 401)
            showLogin();
        throw new Error(String(body.message || messages.unavailable) + (body.supportId ? ' · 支持编号：' + String(body.supportId) : ''));
    }
    return body;
}
function showLogin() { get('login').hidden = false; get('content').hidden = true; get('scope').textContent = '尚未登录 · 权限待确认'; }
function table(parent, columns, rows) {
    if (!rows.length) {
        parent.append(node('p', messages.empty, 'empty'));
        return;
    }
    const wrap = node('div', '', 'table-wrap');
    const t = node('table');
    const head = node('thead');
    const tr = node('tr');
    columns.forEach(c => { const th = node('th', c); th.setAttribute('scope', 'col'); tr.append(th); });
    head.append(tr);
    t.append(head);
    const body = node('tbody');
    rows.forEach(cells => {
        const row = node('tr');
        cells.forEach(value => {
            const td = node('td');
            if (value instanceof HTMLElement)
                td.append(value);
            else
                td.textContent = value;
            row.append(td);
        });
        body.append(row);
    });
    t.append(body);
    wrap.append(t);
    parent.append(wrap);
}
const list = (d) => (Array.isArray(d.items) ? d.items : []);
function time(s) { const d = new Date(String(s)); return Number.isNaN(d.getTime()) ? '时间尚未确认' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(d); }
function button(label, action) { const b = node('button', label); b.type = 'button'; b.addEventListener('click', () => { b.disabled = true; action().catch(e => notice(String(e.message))).finally(() => { b.disabled = false; }); }); return b; }
async function download(path) {
    const r = await fetch('/api/v1/' + path, { credentials: 'same-origin' });
    if (!r.ok)
        throw new Error('此原件当前不可读取，请核对权限或保留期。');
    const blob = await r.blob();
    const a = document.createElement('a');
    const url = URL.createObjectURL(blob);
    a.href = url;
    a.download = path.startsWith('export') ? '运维中文摘要.json' : '来源原件.json';
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 3000);
}
async function render() {
    const current = ++generation;
    controller?.abort();
    controller = new AbortController();
    view.replaceChildren();
    notice(messages.loading);
    get('page-title').textContent = messages[page];
    document.title = messages[page] + ' · 统一运维观测平台';
    document.querySelectorAll('nav button').forEach(b => b.setAttribute('aria-current', b.dataset.page === page ? 'page' : 'false'));
    try {
        if (page === 'platform') {
            if (!platformAccess)
                throw new Error('当前组织角色没有平台域读取权限。');
            await platformView(current);
        }
        else if (page === 'overview') {
            const data = await api('sources');
            if (generation !== current)
                return;
            const grid = node('div', '', 'grid');
            for (const [title, state, explain] of [
                ['原请求结果', messages.unsupported, '通用观察来源不提供原业务操作账本。'],
                ['资源效果', '待核验', '当前快照不能证明与原请求绑定的效果。'],
                ['服务体验', '以实际探测为准', '请读取指标与链路，核对具体时间和请求样本。'],
                ['证据覆盖', messages.partial, '普通遥测可能采样，尚未证明完整历史。'],
            ]) {
                const c = node('section', '', 'card');
                c.append(node('h3', title), node('strong', state), node('p', explain));
                grid.append(c);
            }
            view.append(grid);
            const p = panel('已登记的真实来源');
            table(p, ['来源标识', '服务', '组织范围'], list(data).map(s => [String(s.id), String(s.service), String(s.tenant)]));
            const info = panel('当前建设范围');
            info.append(node('p', '公共平台独立部署，使用标准指标、日志与调用链后端。'), node('p', 'Neon：未接入 · 未启用 · 运行资格尚未执行。'), button('导出中文摘要', () => download('export')));
        }
        else if (page === 'catalog') {
            const data = await api('catalog');
            if (generation !== current)
                return;
            const parent = panel('资源当前观察');
            table(parent, ['资源名称', '原始身份', '来源', '最后观测', '证据质量', '历史观察'], list(data).map(s => [String(s.name), String(s.uid), String(s.source), time(s.observedAt), s.stale ? '数据已过期，当前待确认' : '当前快照 · 部分覆盖', button('查看历史', async () => { const result = await api('catalog/history?' + new URLSearchParams({ source: String(s.source), uid: String(s.uid) })); const history = panel('历史观察 · ' + String(s.name)); history.append(node('p', String(result.message)), node('pre', JSON.stringify(result.items, null, 2))); })]));
        }
        else if (page === 'signals') {
            const data = await api('sources');
            if (generation !== current)
                return;
            for (const source of list(data).filter(s => s.kind !== 'kubernetes_cluster')) {
                await signalView(source, current);
                if (generation !== current)
                    return;
            }
        }
        else if (page === 'events') {
            const data = await api('events');
            if (generation !== current)
                return;
            table(panel('事件与来源原件'), ['事件标识', '来源', '平台接收时刻', '内容摘要', '原件'], list(data).map(s => [String(s.id), String(s.source), time(s.receivedAt), String(s.sha256), button(messages.original, () => download('events/' + encodeURIComponent(String(s.id)) + '?source=' + encodeURIComponent(String(s.source))))]));
        }
        else if (page === 'audits') {
            const data = await api('audits');
            if (generation !== current)
                return;
            const actions = { 'event.ingested': '保存来源事件', 'event.conflict': '发现事件内容冲突', 'evidence.download': '下载来源原件', 'attachment.saved': '保存证据附件', 'attachment.download': '下载证据附件', 'summary.export': '导出中文摘要', 'incident.acknowledged': '确认告警处理责任' };
            table(panel('平台自身事务审计'), ['审计序号', '操作者', '操作', '对象', '发生时间'], list(data).map(s => [String(s.sequence), String(s.actor), actions[String(s.action)] || '平台管理操作（原始代码：' + String(s.action) + '）', String(s.object), time(s.at)]));
        }
        else if (page === 'notifications') {
            const data = await api('notifications');
            if (generation !== current)
                return;
            table(panel('已保存的中文站内通知'), ['通知标题', '说明与核验建议', '生成时间'], list(data).map(s => [String(s.title), String(s.body), time(s.at)]));
        }
        else if (page === 'incidents') {
            const data = await api('incidents');
            if (generation !== current)
                return;
            table(panel('告警与处理责任'), ['告警说明', '当前规则状态', '最后读取', '处理'], list(data).map(s => [String(s.title), messages[s.state] || messages.unknown, time(s.at), s.state === 'firing' && canManage ? button('确认处理责任', async () => { await api('incidents/ack', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ fingerprint: s.fingerprint }) }); await render(); }) : '当前只读或无需重复确认；服务恢复仍需核验']));
        }
        else {
            const data = await api('sources');
            if (generation !== current)
                return;
            table(panel('接入与资格分别核验'), ['来源', '当前配置', '观测类别', '覆盖承诺'], list(data).map(s => [String(s.id), '已登记并启用', '普通遥测 / 轮询快照', '部分覆盖，不承诺原生操作完整性']));
            panel('后续领域接入').append(node('p', 'Neon 来源保持关闭，等待原基线验收及用户后续指令。'));
        }
        notice('');
    }
    catch (e) {
        if (e.name !== 'AbortError')
            notice(e.message);
    }
}
for (const key of ['overview', 'platform', 'catalog', 'signals', 'events', 'incidents', 'notifications', 'audits', 'sources']) {
    const b = node('button', messages[key]);
    b.type = 'button';
    b.dataset.page = key;
    b.hidden = key === 'platform';
    b.addEventListener('click', () => { page = key; location.hash = key; void render(); });
    get('navigation').append(b);
}
get('login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const input = get('token');
    const token = input.value;
    input.value = '';
    try {
        await api('login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
        await start();
    }
    catch (err) {
        notice(err.message);
    }
});
get('refresh').addEventListener('click', () => void render());
get('logout').addEventListener('click', async () => {
    try {
        await api('logout', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
        showLogin();
    }
    catch (e) {
        notice(e.message);
    }
});
async function start() {
    try {
        const me = await api('me');
        platformAccess = me.platformAccess === true;
        canManage = me.role === 'admin' || me.role === 'platform_admin';
        const platformButton = document.querySelector('button[data-page="platform"]');
        if (platformButton)
            platformButton.hidden = !platformAccess;
        get('scope').textContent = (platformAccess ? '平台域：' : '租户范围：') + String(me.tenant) + ' · 操作者：' + String(me.subject);
        get('login').hidden = true;
        get('content').hidden = false;
        const initial = location.hash.slice(1);
        if (['overview', 'platform', 'catalog', 'signals', 'events', 'incidents', 'notifications', 'audits', 'sources'].includes(initial))
            page = initial;
        else
            page = platformAccess ? 'platform' : 'overview';
        await render();
    }
    catch (e) {
        showLogin();
        notice(e.message);
    }
}
async function discoverIdentity() {
    try {
        const response = await fetch('/api/v1/auth/options', { credentials: 'same-origin' });
        const options = await response.json();
        if (options.oidcEnabled) {
            const link = document.createElement('a');
            link.href = '/api/v1/auth/start';
            link.textContent = '使用统一身份登录';
            link.className = 'identity-login';
            const paragraph = node('p');
            paragraph.append(link);
            get('login-form').before(paragraph, node('p', '也可以使用已授权的应急访问凭据。', 'muted'));
        }
    }
    catch { /* The emergency entry remains available when the identity discovery is unavailable. */ }
}
void discoverIdentity();
get('clock').textContent = time(new Date().toISOString());
void start();
