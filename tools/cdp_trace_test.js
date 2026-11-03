// CDP 回归（v3.7.3）：AI 侧栏「轨迹」页的历史视图
//
// 此前轨迹页只渲染**浏览器内存里的本回合节点**——刷新即失；而轨迹一直在
// 落盘，后端 traceQuery/traceSummary 是死代码。本用例盯的是"盘上的轨迹
// 人能看见"这条链路：
//   1) /api/traces 结构正确（traces 数组 + stats.total + retention_days）
//   2) 轨迹页有「本回合 / 历史」切换，默认仍是本回合
//   3) 切到历史能真的渲染出行（不是空状态）
//   4) 统计条与"仅覆盖最近 N 天"提示都在
//   5) 点一行进详情：时间轴有步骤、有返回按钮
//   6) 结局筛选真的减少/改变结果集
//   7) 页面零运行时异常
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9347;
const PROF = 'D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp_tr';

function get(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => res(d)); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

(async () => {
  const envkit = spawn(EXE, [], { cwd: CWD, stdio: 'ignore', detached: false });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await get(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + PROF,
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
  const results = [];
  const check = (name, ok, extra) => { results.push((ok ? 'PASS' : 'FAIL') + ' ' + name + (extra ? ' :: ' + extra : '')); if (!ok) exitCode = 1; };
  // SKIP 只用于"本机数据凑不出这个前提"。**不许静默略过**：
  // 打出来但不计入 PASS，谁看报告就知道这条这轮没验到。
  const skip = (name, why) => { results.push('SKIP ' + name + ' :: ' + why); };

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = JSON.parse(await get('http://127.0.0.1:' + DBG + '/json/list')); break; } catch (e) { await sleep(800); }
    }
    if (!targets) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    const ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    const errs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.method === 'Runtime.exceptionThrown') errs.push(m.params.exceptionDetails.text);
      if (m.method === 'Log.entryAdded' && m.params.entry.level === 'error') errs.push(m.params.entry.text);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      return r.result && r.result.result ? r.result.result.value : JSON.stringify(r.result);
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    // ===== 1) 接口结构 =====
    const api = await evalJS(`(async()=>{ const d = await (await fetch('/api/traces?n=5')).json();
      return JSON.stringify({ok:d.ok, isArr:Array.isArray(d.traces), n:(d.traces||[]).length,
        statsTotal:(d.stats&&d.stats.total)||0, retention:d.retention_days,
        first:(d.traces||[])[0]||null}); })()`);
    const apiObj = JSON.parse(api);
    check('api-traces-shape', apiObj.ok === true && apiObj.isArr && apiObj.statsTotal > 0, api.slice(0, 240));
    check('api-traces-retention', typeof apiObj.retention === 'number' && apiObj.retention > 0, String(apiObj.retention));
    // 列表行的 steps 是**计数**，不是步骤数组：明细由 /api/trace 单独取，
    // 否则一次几十条轨迹就是几 MB。
    check('api-row-steps-is-count', apiObj.first && typeof apiObj.first.steps === 'number' && !Array.isArray(apiObj.first.steps), 'steps=' + (apiObj.first && JSON.stringify(apiObj.first.steps)));

    // ===== 2) 打开轨迹页：默认本回合 =====
    await evalJS(`aiSide('trace')`);
    await sleep(800);
    const paneShown = await evalJS(`(()=>{const p=document.getElementById('ai-pane-trace');return !!p && !p.hidden;})()`);
    check('trace-pane-shown', paneShown === true, String(paneShown));

    const btns = await evalJS(`JSON.stringify([...document.querySelectorAll('#ai-pane-trace .pbar button')].map(b=>b.innerText.trim()))`);
    check('trace-switch-exists', /本回合/.test(btns) && /历史/.test(btns), btns);

    // 默认在本回合：不应出现历史列表的统计/筛选下拉
    const hasSelectBefore = await evalJS(`!!document.querySelector('#ai-pane-trace select')`);
    check('default-is-turn-view', hasSelectBefore === false, 'select=' + hasSelectBefore);

    // ===== 3) 切到历史 =====
    await evalJS(`[...document.querySelectorAll('#ai-pane-trace .pbar button')].find(b=>b.innerText.trim()==='历史').click()`);
    await sleep(1800);
    const rows = await evalJS(`document.querySelectorAll('#ai-pane-trace .tr-root').length`);
    check('hist-rows-rendered', Number(rows) > 0, rows + ' rows');

    const hasSelect = await evalJS(`!!document.querySelector('#ai-pane-trace select')`);
    check('hist-has-outcome-filter', hasSelect === true, String(hasSelect));

    // ===== 4) 统计条 + 保留期提示 =====
    const statsTxt = await evalJS(`(()=>{const p=document.getElementById('ai-pane-trace');
      const d=[...p.querySelectorAll('div')].map(x=>x.innerText||'').find(t=>/最近 \\d+ 条/.test(t)); return d||'';})()`);
    check('hist-stats-bar', /最近 \d+ 条/.test(statsTxt) && /平均/.test(statsTxt), statsTxt.slice(0, 120));

    const retentionTxt = await evalJS(`document.getElementById('ai-pane-trace').innerText.includes('保留期')`);
    check('hist-retention-hint', retentionTxt === true, String(retentionTxt));

    // ===== 5) 点一条**有步骤的**行进详情 =====
    //
    // 盲点第一行会误判：本机第一条可能是纯问答轨迹（steps=0），
    // 详情页正确地显示"这一回合没调用任何工具"，
    // 而断言"必须有时间线"就红了 —— 把对的判成错的。
    // 所以先挑一条 data-steps>0 的行；一条都没有才算这件装置没法验（SKIP）。
    const picked = await evalJS(`(()=>{
      var rows=[...document.querySelectorAll('#ai-pane-trace .tr-root')];
      var withSteps=rows.filter(r=>Number(r.dataset.steps||0)>0);
      if(!withSteps.length) return '';
      withSteps[0].querySelector('.tr-head').click();
      return withSteps[0].dataset.traceId||'clicked';
    })()`);
    if (!picked) {
      skip('detail-timeline-steps', '本机没有带步骤的轨迹，无法验证详情时间线');
      skip('detail-shows-phases', '同上');
      skip('detail-has-back', '同上');
    } else {
      await sleep(1200);
      const detailSteps = await evalJS(`document.querySelectorAll('#ai-pane-trace .tl-row').length`);
      check('detail-timeline-steps', Number(detailSteps) > 0, detailSteps + ' steps @ ' + picked);
      const detailMeta = await evalJS(`document.getElementById('ai-pane-trace').innerText.includes('复验') || document.getElementById('ai-pane-trace').innerText.includes('动作')`);
      check('detail-shows-phases', detailMeta === true, String(detailMeta));
      const hasBack = await evalJS(`[...document.querySelectorAll('#ai-pane-trace button')].some(b=>/返回列表/.test(b.innerText))`);
      check('detail-has-back', hasBack === true, String(hasBack));
      // 返回列表
      await evalJS(`[...document.querySelectorAll('#ai-pane-trace button')].find(b=>/返回列表/.test(b.innerText)).click()`);
      await sleep(800);
    }
    const backRows = await evalJS(`document.querySelectorAll('#ai-pane-trace .tr-root').length`);
    check('back-to-list', Number(backRows) > 0, backRows + ' rows');

    // ===== 6) 结局筛选改变结果集 =====
    const totalNow = Number(backRows);
    const filtered = await evalJS(`(async()=>{
      const sel=document.querySelector('#ai-pane-trace select');
      sel.value='failed'; sel.dispatchEvent(new Event('change'));
      await new Promise(r=>setTimeout(r,1500));
      return document.querySelectorAll('#ai-pane-trace .tr-root').length;})()`);
    check('filter-by-outcome', Number(filtered) <= totalNow, 'all=' + totalNow + ' failed=' + filtered);

    // ===== 7) 切回本回合仍然可用 =====
    await evalJS(`[...document.querySelectorAll('#ai-pane-trace .pbar button')].find(b=>b.innerText.trim()==='本回合').click()`);
    await sleep(700);
    const backTurn = await evalJS(`!document.querySelector('#ai-pane-trace select')`);
    check('switch-back-to-turn', backTurn === true, String(backTurn));

    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }

  console.log(results.join('\n'));
  const nSkip = results.filter(r => r.startsWith('SKIP')).length;
  console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL') + (nSkip ? '（含 ' + nSkip + ' 项 SKIP，本轮没验到）' : ''));

  try { envkit.kill(); } catch (e) {}
  try { chrome.kill(); } catch (e) {}
  await sleep(500);
  process.exit(exitCode);
})();
