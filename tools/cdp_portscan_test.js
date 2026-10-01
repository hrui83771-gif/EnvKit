// CDP 回归（v1.9.17）：程序启动页 → 端口占用诊断 UI + 自定义脚本输入框
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9335;

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
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp4',
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

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    // 1) 切到程序启动页，检查自定义脚本输入框与默认选项
    console.log('NAV:', await evalJS(`(function(){ goto('start'); return 'ok'; })()`));
    await sleep(500);
    console.log('SCRIPT_UI:', await evalJS(`(function(){
      const inp = document.getElementById('web_script');
      const dl = document.getElementById('wl-scripts');
      return JSON.stringify({ hasInput: !!inp, inpVal: inp ? inp.value : null,
        options: dl ? Array.from(dl.options).map(o=>o.value) : null });
    })()`));

    // 2) 添加端口 chip
    console.log('ADD:', await evalJS(`(function(){
      document.getElementById('ps-new').value = '5173';
      document.getElementById('btn-ps-add').click();
      return document.getElementById('ps-chips').textContent.trim();
    })()`));

    // 3) 扫描（3306 上 MySQL 可能在跑，不强断言具体状态，只断言表格渲染 + 状态徽标非"扫描中"）
    console.log('SCAN:', await evalJS(`(function(){ document.getElementById('btn-ps-scan').click(); return 'clicked'; })()`));
    await sleep(3500);
    const verdict = await evalJS(`(function(){
      const rows = document.querySelectorAll('#ps-result tr').length;
      const st = document.getElementById('st-portscan').textContent;
      return JSON.stringify({ rows, st, done: st !== '扫描中…' });
    })()`);
    console.log('VERDICT:', verdict);

    // 4) 页面运行时错误检查
    console.log('ERRS:', await evalJS(`JSON.stringify((window.__errs||[]).slice(0,3))`));

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
