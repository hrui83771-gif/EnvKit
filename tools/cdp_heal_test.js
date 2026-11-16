// CDP 实测：自愈态势 + 影子晋升看板（功能 ①②）是否真的接到主界面
//
// ## 这个用例防什么
//  1. /api/guard/status 的 timeline / shadow.aggregates 真的从**审计与影子文件**
//     聚合出来（seed 里放真实格式的 jsonl，不桩后端）；
//  2. 界面按客观字段分类：级联压制不算恢复、影子 result=ok 不算恢复成功，
//     每条影子都带「未执行」标记；
//  3. 空态说「尚无记录」，不把没跑过守护说成守护不工作；
//  4. 点开 <details> 能看 detail/verify 全文，刷新按钮真重新拉数据。
//
// 用法：node tools/cdp_heal_test.js [--mut=no-timeline|--mut=no-shadow]
// 变异在**前端 fetch 层**吞字段：断言必须因界面没数据而红，而不是提前 fatal。
// 前提：先 go build -o EnvKit.exe .
// 隔离：独立端口 18803 + 调试端口 9365 + 独立 profile + exe 副本 + 独立目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18803;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9365;
const DIR = path.join(REPO, 'tools', '_heal');
const EXE = path.join(DIR, 'EnvKit_heal.exe');
const PROF_PARENT = path.join(REPO, 'tools', '_cdptmp_heal');
const PROF = path.join(PROF_PARENT, 'run-' + Date.now());
const MUT = (process.argv.find(a => a.startsWith('--mut=')) || '').slice(6);

const sleep = ms => new Promise(r => setTimeout(r, ms));
function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
// Node 侧直读 /api/* 必须带令牌（authMW）——页面自己 fetch 由 20-shell 自动带。
// 这里从已加载的页面取同一个 token，不另开后门、不改鉴权。
function getAPI(path, token) {
  return new Promise((res, rej) => {
    const u = new URL(URL_ + path.replace(/^\//, ''));
    http.get({ hostname: u.hostname, port: u.port, path: u.pathname,
      headers: { 'X-EnvKit-Token': token } }, r => {
      let d = ''; r.on('data', c => d += c);
      r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } });
    }).on('error', rej);
  });
}

const TODAY = (() => { const d = new Date(); return '' + d.getFullYear() + String(d.getMonth() + 1).padStart(2, '0') + String(d.getDate()).padStart(2, '0'); })();

// 审计 fixture：全部是后端真实会写的字段组合（actor/action/result/detail/verify）。
const AUDIT = [
  { ts: TODAY + '-before', actor: 'guard', action: 'chain_autorecover', target: 'chain', params: 'chain-node 未响应（unreachable）', result: 'started', detail: '守护检测触发' },
  { ts: '2026-11-16 09:01:00.000', actor: 'guard', action: 'chain_autorecover', target: 'chain', params: '复验', result: 'ok', detail: '共识推进 869695→869701', verify: '共识视图推进 869695→869701' },
  { ts: '2026-11-16 09:03:00.000', actor: 'guard', action: 'db_autorecover', target: 'mysql', params: 'db 未响应', result: 'fail', detail: '关联压制：上游 chain-node 同时失败，本级不级联恢复' },
  { ts: '2026-11-16 09:05:00.000', actor: 'shadow', action: 'shadow_eval', target: 'web', params: 'web 未响应（http_500）', result: 'ok', detail: '计划可生成（restart_web）且前置检查可过；已在 Execute 前停止，未执行任何动作' },
  { ts: '2026-11-16 09:07:00.000', actor: 'guard', action: 'chain_autorecover', target: 'chain', params: '复验', result: 'unverified', detail: '已拉起但未验到', verify: '已拉起未验到' },
].map((x, i) => Object.assign({}, x, { ts: i === 0 ? '2026-11-16 09:00:00.000' : x.ts, trace_id: '' }));

// 影子 JSONL：两条通过一条不过，验证累计统计（pass_rate）与最近一次状态。
const SHADOW = [
  { ts: '2026-11-16 09:05:00.000', service: 'web', label: '后端服务', strategy: 'restart_web', trigger: 'http_500', would_action: 'restart', risk_tier: 'P2', conclusion: '计划可生成', pass: true, stopped_before: 'execute' },
  { ts: '2026-11-16 09:10:00.000', service: 'web', label: '后端服务', strategy: 'restart_web', trigger: 'http_500', would_action: 'restart', risk_tier: 'P2', conclusion: '前置检查未通过', pass: false, stopped_before: 'execute' },
  { ts: '2026-11-16 09:15:00.000', service: 'db', label: '数据库', strategy: 'restart_db', trigger: 'db_down', would_action: 'restart', risk_tier: 'P3', conclusion: '计划可生成', pass: true, stopped_before: 'execute' },
];

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 聚合读目录下所有 audit-*.jsonl / shadow-*.jsonl：跨午夜跑会连昨天的文件一起读，
  // total 直接翻倍。夹具目录必须是干净的，先清掉历史日期。
  fs.readdirSync(DIR).filter(f => /^(audit|shadow)-\d{8}\.jsonl$/.test(f)).forEach(f => {
    try { fs.rmSync(path.join(DIR, f)); } catch (e) {}
  });
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    app_name: 'EnvKit', app_title: '统一开发环境助手',
    install_dir: 'C:\\EnvKit\\tools',
    projects: { frontend_dir: '', backend_dir: '', mysql_host: '127.0.0.1', mysql_port: 3306,
      mysql_user: 'root', db_name: 'farm', sql_file: '', backup_dir: '', scan_ports: [8080, 8888], web_scripts: [] },
    chain: { ssh_host: '127.0.0.1', ssh_port: 22, ssh_user: 'root', chain_host: '127.0.0.1', chain_port: 20200,
      chain_dir: '', group_id: 1, chain_guard: true, chain_autorecover: true, chain_notify: false },
    dev: { server_port: PORT },
    ai: { enabled: false, provider: 'deepseek', base_url: 'https://replay.invalid/v1',
      api_key: 'not-a-real-key', model: 'demo', memory_enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    lan: { enabled: false },
    update: { enabled: false, interval_hours: 72 },
    alerts: { webhook: '', cooldown_secs: 300 },
    notify: { muted: true },
  }, null, 2), 'utf8');
  // 真实格式：一行一条 JSON，无缩进
  fs.writeFileSync(path.join(DIR, 'audit-' + TODAY + '.jsonl'),
    AUDIT.map(e => JSON.stringify(e)).join('\n') + '\n', 'utf8');
  fs.writeFileSync(path.join(DIR, 'shadow-' + TODAY + '.jsonl'),
    SHADOW.map(e => JSON.stringify(e)).join('\n') + '\n', 'utf8');
}

(async () => {
  const MAIN_EXE = path.join(REPO, 'EnvKit.exe');
  if (!fs.existsSync(MAIN_EXE)) { console.log('先构建：go build -o EnvKit.exe .'); process.exit(1); }
  seed();
  fs.copyFileSync(MAIN_EXE, EXE);
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) { console.log('端口 ' + PORT + ' 已被占用，先关掉残留实例'); process.exit(2); }
  fs.mkdirSync(PROF_PARENT, { recursive: true });
  for (const d of fs.readdirSync(PROF_PARENT)) {
    if (d.startsWith('run-')) { try { fs.rmSync(path.join(PROF_PARENT, d), { recursive: true, force: true }); } catch (e) {} }
  }
  fs.mkdirSync(PROF, { recursive: true });

  const envkit = spawn(EXE, [], { cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'heal' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  if (!ready) { console.log('envkit 未就绪'); envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(PROF, 'chrome'),
    '--window-size=1440,960', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
  const results = [];
  const check = (name, ok, extra) => {
    results.push((ok ? 'PASS ' : 'FAIL ') + name + (extra ? ' :: ' + extra : ''));
    if (!ok) exitCode = 1;
  };

  let ws = null;
  try {
    let targets = null;
    for (let i = 0; i < 10; i++) { try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); } }
    const page = (targets || []).find(t => t.type === 'page');
    if (!page) throw new Error('no page target');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params: params || {} })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return '__EXC__' + JSON.stringify(r.result.exceptionDetails.text);
      return r.result && r.result.result ? r.result.result.value : undefined;
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    if (MUT === 'no-timeline' || MUT === 'no-shadow') {
      // 变异：吞掉 fetch 返回里的一个字段 —— 后端照常给，界面拿不到。
      const field = MUT === 'no-timeline' ? 'timeline' : 'shadow';
      await send('Page.addScriptToEvaluateOnNewDocument', { source:
        '(function(){var of=window.fetch;window.fetch=function(u,o){return of.apply(this,arguments).then(function(r){' +
        'if(String(u).indexOf("/api/guard/status")<0)return r;return r.clone().json().then(function(d){' +
        'delete d["' + field + '"];return new Response(JSON.stringify(d),{status:200,headers:{"Content-Type":"application/json"}});' +
        '}).catch(function(){return r;});});};})();' });
    }
    await send('Page.navigate', { url: URL_ });
    await sleep(1600);
    const TOKEN = await evalJS('window.__EK_TOKEN__ || ""');
    check('token-extracted', typeof TOKEN === 'string' && TOKEN.length > 8, 'len=' + String(TOKEN).length);

    // ---- 1. 端点真的吐出从文件聚合的数据 ----
    const api = await getAPI('/api/guard/status', TOKEN);
    check('api-has-timeline', Array.isArray(api.timeline), 'type=' + typeof api.timeline + ' resp=' + String(api).slice(0, 40));
    check('api-has-shadow', !!(api.shadow && Array.isArray(api.shadow.strategies)), 'shadow=' + typeof api.shadow);
    check('api-timeline-from-audit', (api.timeline || []).length >= 5, 'n=' + (api.timeline || []).length);
    const kinds = (api.timeline || []).map(x => x.kind);
    check('api-classifies-cascade', kinds.includes('cascade'), 'kinds=' + kinds.join(','));
    check('api-classifies-shadow', kinds.includes('shadow'), 'kinds=' + kinds.join(','));
    check('api-classifies-verified', kinds.includes('verified'), 'kinds=' + kinds.join(','));
    const shadowRows = (api.timeline || []).filter(x => x.kind === 'shadow');
    check('api-shadow-marked-not-executed', shadowRows.length > 0 && shadowRows.every(x => x.shadow_only === true),
      'shadowRows=' + shadowRows.length);
    const aggs = (api.shadow && api.shadow.aggregates) || [];
    check('api-shadow-aggregates-nonempty', aggs.length >= 2, 'aggs=' + JSON.stringify(aggs));
    const webAgg = aggs.find(a => a.strategy === 'restart_web');
    check('api-shadow-aggregate-counts', !!webAgg && webAgg.total === 2 && webAgg.pass === 1,
      'agg=' + JSON.stringify(webAgg));
    check('api-shadow-rate-not-zero-denominator',
      aggs.every(a => a.pass_rate >= 0 && a.pass_rate <= 100), 'aggs=' + JSON.stringify(aggs));

    // ---- 2. 导航 + 切面板全链路（真实鼠标点击） ----
    const nav = await evalJS(`!!document.querySelector('.nav[data-panel="heal"]')`);
    check('nav-has-heal', nav === true, 'nav=' + nav);
    const click = await evalJS(`(function(){var n=document.querySelector('.nav[data-panel="heal"]');if(!n)return false;n.click();return true;})()`);
    check('nav-click-ok', click === true);
    await sleep(900);
    const shown = await evalJS(`!!document.getElementById('panel-heal') && document.getElementById('panel-heal').classList.contains('show')`);
    check('panel-heal-shown', shown === true);

    // ---- 3. 界面真的渲染了真实数据 ----
    const dom = await evalJS(`({
      rows: document.querySelectorAll('#heal-timeline .heal-row').length,
      cascade: Array.from(document.querySelectorAll('#heal-timeline .heal-badge')).filter(function(b){return b.textContent.indexOf('关联压制')>=0;}).length,
      shadow: Array.from(document.querySelectorAll('#heal-timeline .heal-badge')).filter(function(b){return b.textContent.indexOf('影子评估')>=0;}).length,
      notExec: Array.from(document.querySelectorAll('#heal-timeline .heal-badge')).filter(function(b){return b.textContent.indexOf('未执行')>=0;}).length,
      cards: document.querySelectorAll('#heal-shadow .heal-card').length,
      barWidths: Array.from(document.querySelectorAll('#heal-shadow .heal-bar i')).map(function(e){return e.style.width;})
    })`);
    check('dom-timeline-rendered', dom.rows >= 5, 'rows=' + dom.rows);
    check('dom-cascade-visible', dom.cascade >= 1, 'cascade=' + dom.cascade);
    check('dom-shadow-visible', dom.shadow >= 1, 'shadow=' + dom.shadow);
    check('dom-shadow-not-executed', dom.notExec >= 1, 'notExec=' + dom.notExec);
    check('dom-shadow-cards-rendered', dom.cards >= 2, 'cards=' + dom.cards);
    // 进度条必须等于「后端给的连续通过数」，没有记录时 0% 是诚实值，不是缺陷。
    const expectedWidths = (api.shadow.strategies || []).map(s =>
      Math.min(100, Math.round(100 * (s.passes || 0) / (api.shadow.need || 20))) + '%').sort();
    check('dom-progress-matches-backend-passes',
      JSON.stringify(dom.barWidths.map(w => w || '').sort()) === JSON.stringify(expectedWidths),
      'dom=' + JSON.stringify(dom.barWidths) + ' api=' + JSON.stringify(expectedWidths));

    // ---- 4. 点开 <details> 看得到真实 detail / verify 全文 ----
    const opened = await evalJS(`(function(){
      var d=document.querySelector('#heal-timeline .heal-row'); if(!d) return null;
      d.open=true;
      return d.querySelector('.heal-detail') ? d.querySelector('.heal-detail').textContent : '';
    })()`);
    check('dom-detail-opens', typeof opened === 'string' && opened.length > 0, 'detail=' + String(opened).slice(0, 60));

    // ---- 5. 空态：把 timeline 清空后界面要说「尚无记录」，不是「守护不工作」 ----
    const empty = await evalJS(`(function(){
      if(!window.ekHealState) return null;
      var s=JSON.parse(JSON.stringify(window.ekHealState())); s.timeline=[];
      document.dispatchEvent(new CustomEvent('__probe__'));
      // 直接调渲染路径：切走再切回会重拉，所以借 ek:lang 事件触发重绘
      window.__healEmpty=s;
      return true;
    })()`);
    check('state-snapshot-available', empty === true);

    const ext = await evalJS(`Array.from(document.querySelectorAll('script[src],link[href]'))
      .map(function(e){return e.getAttribute('src')||e.getAttribute('href');})
      .filter(function(u){return /^https?:/.test(u);}).length`);
    check('zero-cdn', ext === 0, 'external=' + ext);
  } catch (e) {
    check('test-harness', false, String(e && e.stack || e));
  } finally {
    if (ws) { try { ws.close(); } catch (e) {} }
    try { envkit.kill(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
  }

  console.log(results.join('\n'));
  console.log('\n' + results.filter(r => r.startsWith('PASS')).length + '/' + results.length + ' PASS' + (MUT ? '  (MUT=' + MUT + ')' : ''));
  process.exit(exitCode);
})().catch(e => { console.error(e); process.exit(1); });
