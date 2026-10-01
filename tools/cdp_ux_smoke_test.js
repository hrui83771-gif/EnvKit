// CDP 冒烟（v1.10.0 UX 批量改进）：四步指引 / 检测卡片 / 启动面板预设 / 桌宠新菜单
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9336;

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
    try { await getJSON('http://127.0.0.1:18765/'); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp6',
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
    await sleep(4500);

    const r1 = await evalJS(`(function(){
      const g = ['gs-1','gs-2','gs-3','gs-4'].map(id => {
        const el = document.getElementById(id);
        return el ? (el.classList.contains('done') ? 'done' : (el.classList.contains('cur') ? 'cur' : 'off')) : 'MISSING';
      });
      return JSON.stringify(g);
    })()`);
    console.log('GUIDE:', r1);

    const r2 = await evalJS(`(function(){
      return JSON.stringify({
        quickWeb: !!document.getElementById('btn-quick-web'),
        quickBoth: !!document.getElementById('btn-quick-both'),
        advDetails: !!document.querySelector('#panel-start details'),
        scriptInDetails: !!(document.querySelector('#panel-start details') || {}).contains === false ? true : !!document.querySelector('#panel-start details #web_script')
      });
    })()`);
    console.log('STARTPANEL:', r2);

    // 检测卡片：触发检测后看有没有渲染（本机组件可能全装齐，无失败项时 data-fix=0 属正常）
    const r3 = await evalJS(`(function(){
      document.getElementById('btn-check') && document.getElementById('btn-check').click();
      return 'check-clicked';
    })()`);
    console.log('CHECK:', r3);
    await sleep(4000);
    const r4 = await evalJS(`(function(){
      const cards = document.querySelectorAll('#detect-cards .comp').length;
      const fixes = document.querySelectorAll('#detect-cards [data-fix]').length;
      return JSON.stringify({ cards, fixes });
    })()`);
    console.log('CARDS:', r4);

    // 桌宠：右键菜单项、眼睛开关、小圆点
    const r5 = await evalJS(`(function(){
      const ctx = document.getElementById('pet-ctx');
      const acts = Array.from(ctx.querySelectorAll('button')).map(b => b.dataset.act);
      const eyeBtn = ctx.querySelector('[data-act="eyes"]');
      eyeBtn.click();
      const eyeAfter1 = eyeBtn.textContent;
      eyeBtn.click();
      const eyeAfter2 = eyeBtn.textContent;
      ctx.querySelector('[data-act="dot"]').click();
      const pet = document.getElementById('pet');
      const dotOn = pet.classList.contains('dot');
      pet.click();
      const dotOff = !pet.classList.contains('dot');
      ctx.querySelector('[data-act="tricks"]').click();
      const bubbleShown = pet.classList.contains('show-bubble');
      return JSON.stringify({ acts, eyeAfter1, eyeAfter2, dotOn, dotOff, bubbleShown });
    })()`);
    console.log('PET:', r5);

    // 确认弹窗 danger 样式
    const r6 = await evalJS(`(function(){
      return new Promise(res => {
        askConfirm('test', 'msg', 'ok', true).then(()=>res('resolved'));
        setTimeout(()=>{
          const danger = document.querySelector('#confirm .mbox').classList.contains('danger');
          document.getElementById('cf-cancel').click();
          setTimeout(()=>res(JSON.stringify({ danger })), 100);
        }, 100);
      });
    })()`);
    console.log('CONFIRM:', r6);

    console.log('RUNTIME_ERRS:', consoleErrs.length, JSON.stringify(consoleErrs));
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
