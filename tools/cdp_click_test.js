// CDP 实测：真实 Chromium 渲染 EnvKit 页面 → 检查/点击 #btn-dbview → 回报结果
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9333;

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

(async () => {
  // 1. 起 EnvKit
  const envkit = spawn(EXE, [], { cwd: CWD, stdio: 'ignore', detached: false });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_.replace(/\/$/, '') + '/'); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  // 2. 起 Chrome（headless + CDP）
  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp2',
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

    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    const report = await evalJS(`(function(){
      const out = [];
      const b = document.getElementById('btn-dbview');
      b.click();
      const d = document.getElementById('dbview');
      out.push('opened=' + (d.style.display !== 'none'));
      out.push('btnText=' + b.textContent);
      return out.join(' | ');
    })()`);
    console.log('REPORT:', report);
    await sleep(3500); // 等 /api/db/list + 库列表渲染
    const report2 = await evalJS(`(function(){
      const side = document.getElementById('dbv-side');
      const st = document.getElementById('st-dbview');
      return 'status=' + st.textContent + ' | sideItems=' + side.children.length + ' | first3=' + Array.from(side.children).slice(0,3).map(e=>e.textContent.trim()).join(',');
    })()`);
    console.log('REPORT2:', report2);
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
