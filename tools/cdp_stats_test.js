/**
 * CDP 验收：统计看板（一次性，跑完即删）
 *
 * 验的不只是「元素存在」，而是**数字与 /api/stats 返回的一致**——
 * 画得好看但数字对不上，等于输出一条与事实相反的信号。
 */
const { spawn } = require('child_process');
const http = require('http');
const fs = require('fs');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9351;
const PROF = 'D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdpstats';

function get(u) {
  return new Promise((res, rej) => {
    http.get(u, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => res(d)); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = [];
let exitCode = 0;
const check = (name, ok, extra) => {
  out.push((ok ? 'PASS ' : 'FAIL ') + name + (extra ? ' :: ' + extra : ''));
  if (!ok) exitCode = 1;
};

(async () => {
  // 单实例互斥：先停掉可能存在的。
  // **必须用 spawnSync 而非 execSync** —— 没有进程时 taskkill 返回非 0，
  // 而 execSync 会把它抛成异常，第一版就死在这儿。
  require('child_process').spawnSync('taskkill', ['/F', '/IM', 'EnvKit.exe'], { stdio: 'ignore' });
  await sleep(800);
  const envkit = spawn(EXE, [], { cwd: CWD, stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await get(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  out.push('envkit ready: ' + ready);
  if (!ready) { console.log(out.join('\n')); process.exit(1); }

  const html = await get(URL_);
  const tok = (html.match(/\b([a-f0-9]{32})\b/) || [])[1] || '';
  check('token-extracted', !!tok, tok.slice(0, 8));

  // 先直接从 API 取真值，后面与 DOM 比对
  const stats = JSON.parse(await new Promise((res, rej) => {
    http.get(URL_ + 'api/stats?days=30', { headers: { 'X-EnvKit-Token': tok } },
      r => { let d = ''; r.on('data', c => d += c); r.on('end', () => res(d)); }).on('error', rej);
  }));
  out.push('api totals: ops=' + stats.totals.ops + ' fail=' + stats.totals.fail +
    ' denied=' + stats.totals.denied + ' traces=' + stats.totals.traces +
    ' token=' + stats.totals.token_total + ' samples=' + stats.totals.token_samples);

  // 浏览器
  // Node 22 自带全局 WebSocket，**不要 require('ws')**——
  // 本仓库没装那个包，require 会直接抛 MODULE_NOT_FOUND（第一版踩了）。
  // 既有 CDP 脚本也是用全局 WebSocket，保持一致。
  const chrome = spawn(CHROME, [
    '--headless=new', '--remote-debugging-port=' + DBG,
    '--user-data-dir=' + PROF, '--no-first-run', '--disable-gpu',
    URL_,
  ], { stdio: 'ignore', detached: false });

  let ws;
  try {
    // **先取 /json/list 拿 page target**（与既有 CDP 脚本一致）。
    // 直接连 ws://127.0.0.1:PORT 会在浏览器还没准备好时抛
    // "Sent before connected" —— 第一版踩了。
    let targets = null;
    for (let i = 0; i < 12; i++) {
      try { targets = JSON.parse(await get('http://127.0.0.1:' + DBG + '/json/list')); break; }
      catch (e) { await sleep(800); }
    }
    if (!targets) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map(); const errs = [];
    // Node 内置 WebSocket 用 onmessage 赋值，**没有 .on()**（那是浏览器版的）。
    ws.onmessage = ev => {
      const mm = JSON.parse(ev.data);
      if (mm.method === 'Runtime.exceptionThrown') errs.push(mm.params.exceptionDetails.text);
      if (mm.method === 'Log.entryAdded' && mm.params.entry.level === 'error') errs.push(mm.params.entry.text);
      if (mm.id && pending.has(mm.id)) { pending.get(mm.id)(mm); pending.delete(mm.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const ev = async e => {
      const r = await send('Runtime.evaluate', { expression: e, returnByValue: true, awaitPromise: true });
      return r.result && r.result.result ? r.result.result.value : JSON.stringify(r.result);
    };
    await send('Page.enable', {});
    await send('Runtime.enable', {});
    await send('Log.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(3500);

    // 切到审计面板
    await ev("document.querySelector('[data-panel=\"audit\"]').click()");
    await sleep(2500);

    check('kpis-rendered', (await ev("document.querySelectorAll('#st-kpis .kpi').length")) === 4,
      'count=' + await ev("document.querySelectorAll('#st-kpis .kpi').length") +
      '（v2.7.1 精简为 4 张：总数/复验率/延迟/Token）');

    // **KPI 必须一行排完** —— 卡片数与列数不整除时最后一张会掉到第二行
    const kpiRows = await ev("(function(){const ks=[...document.querySelectorAll('#st-kpis .kpi')];if(!ks.length)return 0;const top=ks[0].getBoundingClientRect().top;return new Set(ks.map(k=>Math.round(k.getBoundingClientRect().top))).size})()");
    check('kpis-single-row', kpiRows === 1, 'distinct rows = ' + kpiRows);

    // **数字一致性**：KPI 里的数字必须与 API 一致。
    //
    // 注意：**不能断言严格相等** —— EnvKit 自己在运行时不断写审计
    // （env_check 轮询、AI 快照等），页面渲染和这次 API 请求之间
    // 相差几秒，数字会自然增长。第一版断言 `===` 于是报
    // dom=1,105 vs api=1101 —— 那是**断言写错了，不是代码错了**。
    //
    // 放宽成「差异在合理范围内」：既能抓住真错（差几百= 口径坏了），
    // 又不会因为几秒的时间差误报。
    const numEq = (a, b, tol) => {
      const x = Number(String(a).replace(/,/g, '')), y = Number(b);
      return Number.isFinite(x) && Number.isFinite(y) && Math.abs(x - y) <= (tol || 30);
    };
    const kpiOps = await ev("(function(){const k=[...document.querySelectorAll('#st-kpis .kpi')].find(x=>/操作总数|Total operations/.test(x.querySelector('.k').textContent));return k?k.querySelector('.v').textContent:''})()");
    check('kpi-ops-matches-api', numEq(kpiOps, stats.totals.ops, 30),
      'dom=' + kpiOps + ' api=' + stats.totals.ops + '（允许 ±30，实测差 ' +
      Math.abs(Number(String(kpiOps).replace(/,/g, '')) - stats.totals.ops) + '）');

    // v2.7.1 删掉了「失败」KPI（信息在下方结果分布的图例数字里），
    // 所以**不再断言它** —— 第一版留着这条断言，于是报 dom= 空。
    // 失败数改由下面的 stack 图例断言覆盖。

    // 图表 SVG 真的画出来了
    check('ops-chart-has-svg', (await ev("!!document.querySelector('#st-ops-chart svg')")));
    check('ops-chart-has-rects',
      (await ev("document.querySelectorAll('#st-ops-chart rect').length")) > 0,
      'rects=' + await ev("document.querySelectorAll('#st-ops-chart rect').length"));
    check('actor-chart-has-bars',
      (await ev("document.querySelectorAll('#st-actor-chart i').length")) > 0,
      'bars=' + await ev("document.querySelectorAll('#st-actor-chart i').length"));
    check('action-chart-has-bars',
      (await ev("document.querySelectorAll('#st-action-chart i').length")) > 0,
      'bars=' + await ev("document.querySelectorAll('#st-action-chart i').length"));

    // v2.7.1 移除的两张图：**DOM 与渲染代码都要真的删掉**。
    // 只删 DOM 不删代码的话，渲染时 getElementById 返回 null、
    // `.innerHTML=` 会抛 TypeError —— 所以「元素不存在」本身就是断言。
    check('trace-chart-removed', (await ev("!document.getElementById('st-trace-chart')")));
    check('token-chart-removed', (await ev("!document.getElementById('st-token-chart')")));
    // 被删的 KPI 也不该再有（它们的信息在下方图表的结果分布里）
    const kpiKeys = await ev("JSON.stringify([...document.querySelectorAll('#st-kpis .kpi .k')].map(e=>e.textContent))");
    out.push('kpi keys = ' + kpiKeys);
    check('no-redundant-kpis',
      !/任务数/.test(kpiKeys) && !/被拦下/.test(kpiKeys), kpiKeys);

    // 堆叠条四段 + **图例数字必须与 API 一致**（v2.7.1 删掉「失败」KPI 后，
    // 失败数唯一的呈现位置就是这里的图例数字，所以这条断言顶替了原来那条）
    check('stack-four-segments',
      (await ev("document.querySelectorAll('#st-ops-stack i').length")) === 4,
      'n=' + await ev("document.querySelectorAll('#st-ops-stack i').length"));
    const lgn = await ev("JSON.stringify([...document.querySelectorAll('#panel-audit .lgnv')].map(e=>e.textContent))");
    out.push('legend numbers = ' + lgn);
    const nums = JSON.parse(lgn).map(s => Number(String(s).replace(/,/g, '')));
    check('legend-matches-api',
      nums.length === 4 && numEq(nums[0], stats.totals.ok, 30) &&
      numEq(nums[1], stats.totals.fail, 5) && numEq(nums[2], stats.totals.denied, 1),
      lgn + '  api ok/fail/denied=' +
      [stats.totals.ok, stats.totals.fail, stats.totals.denied].join('/'));

    // **无样本必须显式说明，不能画成 0**
    const vrText = await ev("(function(){const k=[...document.querySelectorAll('#st-kpis .kpi')].find(x=>/复验|Verification/.test(x.querySelector('.k').textContent));return k?k.className+'|'+k.querySelector('.v').textContent:''})()");
    out.push('verify-rate card: ' + vrText);
    if (stats.totals.verify_rate_has_sample) {
      check('verify-rate-shows-percent', /%/.test(vrText), vrText);
    } else {
      check('verify-rate-shows-nosample', /none/.test(vrText) && /无样本|No data/.test(vrText), vrText);
    }

    // token 无样本时要有说明文案（在 KPI 副标题里）
    const tkSub = await ev("(function(){const k=[...document.querySelectorAll('#st-kpis .kpi')].find(x=>/Token/.test(x.querySelector('.k').textContent));return k?k.className+'|'+k.querySelector('.v').textContent+'|'+k.querySelector('.s').textContent:''})()");
    out.push('token card: ' + tkSub);
    if (!stats.totals.token_samples) {
      check('token-nosample-explained', /none/.test(tkSub) && /v2\.7/.test(tkSub), tkSub);
    } else {
      check('token-card-has-value', /[\d]/.test(tkSub), tkSub);
    }

    // 轨迹保留期提醒（覆盖长度不同，必须说出来）—— 在 notes 里
    const notesTxt = await ev("(document.getElementById('st-notes')||{}).textContent||''");
    check('retention-noted-in-notes', /14|30|轨迹/.test(notesTxt), notesTxt.slice(0, 90));

    // 区间切换（v2.7.1 后是 4 张卡，断言同步改）
    await ev("(function(){const s=document.getElementById('st-days');s.value='7';s.dispatchEvent(new Event('change'));return 1})()");
    await sleep(1200);
    check('days-switch-works',
      (await ev("document.querySelectorAll('#st-kpis .kpi').length")) === 4,
      'count=' + await ev("document.querySelectorAll('#st-kpis .kpi').length"));
    out.push('after switch to 7 days, ops label = ' + await ev("(function(){const k=[...document.querySelectorAll('#st-kpis .kpi')].find(x=>/操作总数|Total operations/.test(x.querySelector('.k').textContent));return k?k.querySelector('.s').textContent:''})()"));

    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    out.push('ERROR ' + e.message);
    exitCode = 1;
  }

  out.push('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));
  console.log(out.join('\n'));
  try { envkit.kill(); } catch (e) {}
  try { require('child_process').spawnSync('taskkill', ['/F', '/IM', 'chrome.exe'], { stdio: 'ignore' }); } catch (e) {}
  process.exit(exitCode);
})();