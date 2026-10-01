// CDP 布局验证：退出程序对话框（勾选框不再被全局 input 样式撑爆）
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9341;

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
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp11',
    '--no-first-run', '--window-size=1200,800', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
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
    let consoleErrs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.method === 'Runtime.exceptionThrown') consoleErrs.push(m.params.exceptionDetails.text);
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

    await evalJS(`document.getElementById('btn-exit-app').click()`);
    await sleep(300);

    const m = JSON.parse(await evalJS(`(function(){
      const cb = document.getElementById('exit-stop-svc').getBoundingClientRect();
      const span = document.querySelector('#exit-app label span').getBoundingClientRect();
      const noHint = !document.getElementById('exit-app').textContent.includes('取消勾选');
      return JSON.stringify({
        cbW: Math.round(cb.width), cbH: Math.round(cb.height),
        gap: Math.round(span.left - cb.right),
        sameRow: Math.abs(cb.top - span.top) < 10,
        noHint
      });
    })()`));
    console.log('LAYOUT:', JSON.stringify(m));

    const shot = await send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync('D:\\BCGD\\FarmTrace\\envkit\\tools\\_dlg.png', Buffer.from(shot.result.data, 'base64'));

    const pass = m.cbW < 25 && m.cbH < 25 && m.gap >= 0 && m.gap < 40 && m.sameRow && m.noHint && consoleErrs.length === 0;
    console.log('RUNTIME_ERRS:', consoleErrs.length, 'PASS:', pass);
    if (!pass) exitCode = 1;
  } catch (e) {
    console.log('FATAL:', e && e.message);
    exitCode = 1;
  } finally {
    try { chrome.kill(); } catch (_) {}
    try { envkit.kill(); } catch (_) {}
    process.exit(exitCode);
  }
})();
