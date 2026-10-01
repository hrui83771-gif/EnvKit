// CDP 回归（v1.10.3）：网页端「退出程序」
// 1) 关于/免责按钮下方存在退出按钮，点击弹对话框，勾选框默认勾选（连带停服务）
// 2) 取消按钮 / 点击遮罩可关闭对话框，服务不受影响（页面仍可访问）
// 3) 取消勾选后确认退出 → 出现全屏告别层，EnvKit 进程真实退出（assistant_only 路径）
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9339;

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function alive(url) {
  return new Promise(res => { http.get(url, r => res(r.statusCode === 200)).on('error', () => res(false)); });
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
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp9',
    '--no-first-run', 'about:blank',
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

    // ===== 1) 按钮存在 + 弹窗默认态 =====
    const ui = JSON.parse(await evalJS(`(function(){
      const btn = document.getElementById('btn-exit-app');
      if(!btn) return JSON.stringify({btn:false});
      btn.click();
      const modal = document.getElementById('exit-app');
      return JSON.stringify({
        btn: true,
        afterAboutBtn: !!document.getElementById('btn-about'),
        shown: modal.classList.contains('show'),
        checked: document.getElementById('exit-stop-svc').checked
      });
    })()`));
    console.log('UI:', JSON.stringify(ui));

    // ===== 2) 取消关闭，服务不受影响 =====
    const cancel = JSON.parse(await evalJS(`(function(){
      document.getElementById('exit-cancel').click();
      const closed = !document.getElementById('exit-app').classList.contains('show');
      document.getElementById('btn-exit-app').click();
      document.getElementById('exit-app').click(); // 点遮罩
      const closed2 = !document.getElementById('exit-app').classList.contains('show');
      return JSON.stringify({closed, closed2});
    })()`));
    await sleep(500);
    const stillAlive = await alive(URL_);
    console.log('CANCEL:', JSON.stringify(cancel), 'stillAlive:', stillAlive);

    // ===== 3) 真实退出（只退助手） =====
    await evalJS(`(function(){
      document.getElementById('exit-stop-svc').checked = false;
      document.getElementById('exit-confirm').click();
      return 'ok';
    })()`);
    await sleep(1200);
    const overlay = JSON.parse(await evalJS(`(function(){
      const ovs = [...document.querySelectorAll('body > div')].filter(d => d.style.zIndex === '9999');
      return JSON.stringify({overlay: ovs.length === 1, text: ovs[0] ? ovs[0].textContent : ''});
    })()`));
    let gone = false;
    for (let i = 0; i < 10; i++) { if (!(await alive(URL_))) { gone = true; break; } await sleep(400); }
    console.log('OVERLAY:', JSON.stringify(overlay), 'serverGone:', gone);

    const runtimeClean = consoleErrs.length === 0;
    console.log('RUNTIME_ERRS:', consoleErrs.length, JSON.stringify(consoleErrs));
    const pass =
      ui.btn && ui.afterAboutBtn !== undefined && ui.shown && ui.checked === true &&
      cancel.closed && cancel.closed2 && stillAlive &&
      overlay.overlay && overlay.text.includes('EnvKit 已退出') &&
      gone && runtimeClean;
    console.log('PASS:', pass);
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
