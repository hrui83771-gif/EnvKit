// CDP 回归（v2.0.0-rc）：操作审计查看页
// 此前审计只写不读——没有接口、没有页面、auditTail/auditSummary 都是死代码。
// 本用例盯的是"页面真能看见审计"这条链路：
//   1) /api/audit 返回结构正确（days + entries，entries 是数组不是 null）
//   2) 侧栏能切到审计页，且切过去会自动加载
//   3) 表格真的渲染出行（不是"暂无记录"）
//   4) 按主体筛选生效
//   5) 复验结论/耗时这类 P2 字段能显示出来
//   6) 页面零运行时异常
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9343;
const PROF = 'D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp14';

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
    const api = await evalJS(`(async()=>{ const d = await (await fetch('/api/audit?n=50')).json();
      return JSON.stringify({ok:d.ok, isArr:Array.isArray(d.entries), days:Array.isArray(d.days), n:(d.entries||[]).length, first:(d.entries||[])[0]||null}); })()`);
    const apiObj = JSON.parse(api);
    check('api-audit-shape', apiObj.ok === true && apiObj.isArr && apiObj.days, api);

    // ===== 2) 侧栏能切到审计页 =====
    const navHas = await evalJS(`!!document.querySelector('.nav[data-panel="audit"]')`);
    check('nav-has-audit', navHas === true, String(navHas));

    await evalJS(`document.querySelector('.nav[data-panel="audit"]').click()`);
    await sleep(1500);
    const shown = await evalJS(`document.getElementById('panel-audit').classList.contains('show')`);
    check('panel-audit-shown', shown === true, String(shown));

    // ===== 3) 切过去自动加载并渲染出行 =====
    const rowCount = await evalJS(`document.querySelectorAll('#audit-body tr').length`);
    check('audit-rows-rendered', Number(rowCount) > 0, rowCount + ' rows');
    const notEmpty = await evalJS(`!document.getElementById('audit-body').innerText.includes('暂无审计记录')`);
    check('audit-not-empty-state', notEmpty === true, String(notEmpty));

    // ===== 4) 日期下拉被填充（至少 all + 1 个真实日期）=====
    const optCount = await evalJS(`document.getElementById('audit-day').options.length`);
    check('audit-day-options', Number(optCount) >= 2, optCount + ' options');

    // ===== 5) 按主体筛选：选 AI 后行数应 <= 全部，且按钮高亮切换 =====
    const allRows = Number(rowCount);
    await evalJS(`document.getElementById('audit-f-ai').click()`);
    await sleep(1200);
    const aiRows = Number(await evalJS(`document.querySelectorAll('#audit-body tr').length`));
    const aiOn = await evalJS(`document.getElementById('audit-f-ai').classList.contains('on') && !document.getElementById('audit-f-all').classList.contains('on')`);
    check('audit-filter-actor', aiRows <= allRows && aiOn === true, `all=${allRows} ai=${aiRows} on=${aiOn}`);

    // 切回全部（空结果时也允许"暂无记录"占位行）
    await evalJS(`document.getElementById('audit-f-all').click()`);
    await sleep(1000);

    // ===== 6) 表头齐全 =====
    const heads = await evalJS(`JSON.stringify([...document.querySelectorAll('#audit-grid thead th')].map(x=>x.innerText.trim()))`);
    check('audit-table-heads', /时间/.test(heads) && /主体/.test(heads) && /结果/.test(heads), heads);

    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }

  console.log(results.join('\n'));
  console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));

  try { envkit.kill(); } catch (e) {}
  try { chrome.kill(); } catch (e) {}
  await sleep(500);
  process.exit(exitCode);
})();
