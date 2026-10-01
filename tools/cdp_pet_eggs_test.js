// CDP 回归（v1.9.18）：桌宠彩蛋三件套
// 1) 连击 5 次 → 围裙换色；再 5 次 → 头发/勋章换色
// 2) 快速甩动拖拽 → dizzy 态 + 松手后恢复
// 3) 按住 1.5s 摸头 → pat 标记 + 后续 click 被吞
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
    try { await getJSON('http://127.0.0.1:18765/'); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp5',
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
    await sleep(4000);

    // 清掉持久化皮肤，从默认造型开始
    console.log('RESET:', await evalJS(`(function(){ localStorage.removeItem('ek-pet-skin'); localStorage.removeItem('ek-pet-pos'); return 'ok'; })()`));
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    const apron0 = await evalJS(`document.getElementById('pet-apronP').getAttribute('fill')`);
    const hair0 = await evalJS(`document.querySelector('#petHairG stop').getAttribute('stop-color')`);
    console.log('INITIAL:', JSON.stringify({ apron0, hair0 }));

    // 1) 连击 5 次 → 围裙换色；期间抽屉只应被第 1 次点击打开
    for (let i = 0; i < 5; i++) { await evalJS(`document.getElementById('pet').click()`); await sleep(120); }
    await sleep(300);
    const apron1 = await evalJS(`document.getElementById('pet-apronP').getAttribute('fill')`);
    const drawerOpen = await evalJS(`document.getElementById('ai-drawer').classList.contains('open')`);
    console.log('CLOTHES:', JSON.stringify({ apron0, apron1, changed: apron0 !== apron1, drawerOpenAfterCombo: drawerOpen }));

    // 2) 再连击 5 次 → 头发/勋章换色
    for (let i = 0; i < 5; i++) { await evalJS(`document.getElementById('pet').click()`); await sleep(120); }
    await sleep(300);
    const hair1 = await evalJS(`document.querySelector('#petHairG stop').getAttribute('stop-color')`);
    const medal1 = await evalJS(`document.querySelector('#petMedalG stop').getAttribute('stop-color')`);
    console.log('HAIR:', JSON.stringify({ hair0, hair1, changed: hair0 !== hair1, medal1 }));

    // 3) 快速甩动 → dizzy；松手静置 → 恢复
    const dizzy = await evalJS(`(function(){
      const pet = document.getElementById('pet');
      const r = pet.getBoundingClientRect();
      const opt = {bubbles: true, button: 0};
      let x = r.left + 70, y = r.top + 70;
      pet.dispatchEvent(new PointerEvent('pointerdown', Object.assign({clientX:x, clientY:y, pointerId:7}, opt)));
      let lastX = x;
      for (let i = 0; i < 40; i++) {
        x = r.left + 70 + (i % 2 === 0 ? 90 : -60);
        pet.dispatchEvent(new PointerEvent('pointermove', Object.assign({clientX:x, clientY:y, pointerId:7}, opt)));
      }
      pet.dispatchEvent(new PointerEvent('pointerup', Object.assign({clientX:x, clientY:y, pointerId:7}, opt)));
      return pet.classList.contains('dizzy') ? 'dizzy' : 'not-dizzy';
    })()`);
    console.log('DIZZY:', dizzy);
    await sleep(3000);
    const recovered = await evalJS(`!document.getElementById('pet').classList.contains('dizzy')`);
    console.log('RECOVERED:', recovered);

    // 4) 摸头：按住 1.6s → pat 标记；随后 click 被吞（抽屉状态不变）
    //    先补一次 click 消耗掉上一步拖拽残留的 dragged 标记
    await evalJS(`document.getElementById('pet').click()`);
    await sleep(200);
    const drawerBefore = await evalJS(`document.getElementById('ai-drawer').classList.contains('open')`);
    const pat = await evalJS(`(function(){
      const pet = document.getElementById('pet');
      const r = pet.getBoundingClientRect();
      const opt = {bubbles: true, button: 0};
      pet.dispatchEvent(new PointerEvent('pointerdown', Object.assign({clientX:r.left+70, clientY:r.top+70, pointerId:8}, opt)));
      return 'down';
    })()`);
    await sleep(1700);
    const patFlag = await evalJS(`document.getElementById('pet').dataset.pat || ''`);
    await evalJS(`document.getElementById('pet').dispatchEvent(new PointerEvent('pointerup', {bubbles:true, button:0, pointerId:8}))`);
    await evalJS(`document.getElementById('pet').click()`);
    await sleep(200);
    const drawerAfterPat = await evalJS(`document.getElementById('ai-drawer').classList.contains('open')`);
    const patConsumed = await evalJS(`document.getElementById('pet').dataset.pat || ''`);
    console.log('PAT:', JSON.stringify({ pat, patFlag, drawerBefore, drawerAfterPat, patConsumed }));

    const runtimeClean = consoleErrs.length === 0;
    console.log('RUNTIME_ERRS:', consoleErrs.length, JSON.stringify(consoleErrs));
    console.log('PASS:', JSON.stringify({
      clothes: apron0 !== apron1,
      hair: hair0 !== hair1,
      dizzy: dizzy === 'dizzy',
      recovered: recovered === true,
      patBlockedClick: patFlag === '1' && drawerAfterPat === drawerBefore && patConsumed === '',
      runtimeClean
    }));

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
