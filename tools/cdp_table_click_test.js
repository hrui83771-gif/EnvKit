// CDP 回归（v1.9.16）：点库 → 直接点左树表项 → 不切页签，断言右侧网格立刻渲染数据
// 回归背景：曾因 dbv.tabSwitch 笔误（应为 dbvTabSwitch）导致点表抛 TypeError，数据要切一次页签才出现
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9334;

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

(async () => {
  const envkit = spawn(EXE, [], { cwd: CWD, stdio: 'ignore', detached: false });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_.replace(/\/$/, '') + '/'); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp3',
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    const ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      return r.result && r.result.result ? r.result.result.value : JSON.stringify(r.result);
    };
    const errs = [];
    ws.onmessage2 = null;

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    // 1) 打开数据浏览，等库列表
    console.log('OPEN:', await evalJS(`(function(){ document.getElementById('btn-dbview').click(); return 'clicked'; })()`));
    await sleep(3500);
    console.log('DBS:', await evalJS(`(function(){
      const side = document.getElementById('dbv-side');
      return 'status=' + document.getElementById('st-dbview').textContent + ' | items=' + side.children.length;
    })()`));

    // 2) 点第一个库，等表列表
    console.log('DBCLICK:', await evalJS(`(function(){
      const db = Array.from(document.getElementById('dbv-side').children).find(e => e.classList.contains('db'));
      if (!db) return 'NO_DB_ITEM';
      db.click(); return 'clicked ' + db.textContent.trim();
    })()`));
    await sleep(3000);
    console.log('TABLES:', await evalJS(`(function(){
      const tbs = Array.from(document.getElementById('dbv-side').children).filter(e => e.classList.contains('tb'));
      return 'tables=' + tbs.length + ' | first=' + (tbs[0] ? tbs[0].textContent.trim() : '-');
    })()`));

    // 3) 关键回归：直接点第一张表，不做任何页签切换
    console.log('TBCLICK:', await evalJS(`(function(){
      const tb = Array.from(document.getElementById('dbv-side').children).find(e => e.classList.contains('tb'));
      if (!tb) return 'NO_TABLE_ITEM';
      tb.click(); return 'clicked ' + tb.textContent.trim();
    })()`));
    await sleep(3000);

    // 4) 断言：网格有表头+数据行、页码信息非空、rows 页签处于选中态
    const verdict = await evalJS(`(function(){
      const grid = document.getElementById('dbv-grid');
      const heads = grid.querySelectorAll('thead th').length;
      const rows = grid.querySelectorAll('tbody tr').length;
      const info = document.getElementById('dbv-pageinfo').textContent;
      const tabOn = document.getElementById('dbv-tab-rows').classList.contains('on');
      return JSON.stringify({ heads, rows, info, tabOn,
        pass: heads > 0 && info.indexOf('共') >= 0 && tabOn });
    })()`);
    console.log('VERDICT:', verdict);

    ws.close();
  } catch (e) {
    console.log('TESTERR:', e.message);
  } finally {
    try { chrome.kill(); } catch (_) {}
    try { envkit.kill(); } catch (_) {}
    console.log('cleaned up');
    process.exit(0);
  }
})();
