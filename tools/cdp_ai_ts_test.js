// CDP 回归（v1.10.2）：AI 消息时间戳
// 1) 历史存档带 ts → 刷新恢复后气泡渲染 HH:MM；无 ts 的旧存档不显示时间
// 2) 非法 ts（格式不符）必须被拒绝渲染
// 3) 真实发送路径：AI 未配置报错，但用户气泡带时间且入档；ai-stop 按钮存在且回合后隐藏
// 4) AI 气泡 dataset.raw 不含时间戳前缀（复制干净）
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9337;

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
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp7',
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

    await evalJS(`localStorage.removeItem('ek-ai-hist')`);

    // ===== 1) 带 ts 的历史恢复后渲染时间 =====
    await evalJS(`(function(){
      aiMessages = [];
      aiMessages.push({role:'user', content:'测试：历史里的用户消息', ts:'10:05'},
                      {role:'assistant', content:'测试：历史里的助手回复', ts:'10:06'});
      aiHistSave();
      return 'ok';
    })()`);
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);
    const restored = JSON.parse(await evalJS(`(function(){
      const ut = document.querySelector('#ai-msgs .ai-msg.user .ai-time');
      const at = document.querySelector('#ai-msgs .ai-msg.ai .ai-time');
      return JSON.stringify({
        m0ts: aiMessages[0] && aiMessages[0].ts,
        userTime: ut ? ut.textContent : null,
        aiTime: at ? at.textContent : null
      });
    })()`));
    console.log('RESTORED:', JSON.stringify(restored));

    // ===== 2) 非法 ts 拒绝 + 复制原文不含时间 =====
    const invalid = JSON.parse(await evalJS(`(function(){
      const d = aiAdd('ai', 'hello-world', '99:99');
      const noTime = !d.querySelector('.ai-time');
      const d2 = aiAdd('ai', 'raw-check', '23:59');
      const hasTime = !!d2.querySelector('.ai-time');
      const rawClean = d2.dataset.raw === 'raw-check';
      const textHasTime = d2.textContent.indexOf('23:59') >= 0;
      d.remove(); d2.remove();
      return JSON.stringify({noTime, hasTime, rawClean, textHasTime});
    })()`));
    console.log('INVALID_AND_COPY:', JSON.stringify(invalid));

    // ===== 3) 真实发送（AI 未配置 → 走错误路径，用户气泡仍应带时间并入档）=====
    const stopHiddenBefore = await evalJS(`document.getElementById('ai-stop').style.display === 'none'`);
    await evalJS(`(function(){
      localStorage.removeItem('ek-ai-hist');
      aiMessages = [];
      document.querySelector('#ai-msgs').innerHTML = '';
      return 'ok';
    })()`);
    await evalJS(`(function(){ document.getElementById('ai-in').value = '几点了测试'; return 'ok'; })()`);
    await evalJS(`document.getElementById('ai-send').click()`);
    await sleep(2000);
    const sent = JSON.parse(await evalJS(`(function(){
      const ut = document.querySelector('#ai-msgs .ai-msg.user .ai-time');
      const saved = JSON.parse(localStorage.getItem('ek-ai-hist')||'[]');
      const um = saved.find(m => m.role === 'user');
      return JSON.stringify({
        timeShown: ut ? ut.textContent : null,
        timeOk: ut ? /^([01]\\d|2[0-3]):[0-5]\\d$/.test(ut.textContent) : false,
        savedTs: um ? (um.ts || null) : 'NO-MSG',
        stopBtnBack: document.getElementById('ai-stop').style.display === 'none',
        sendEnabled: !document.getElementById('ai-send').disabled
      });
    })()`));
    console.log('SENT:', JSON.stringify(sent), 'stopHiddenBefore:', stopHiddenBefore);

    const runtimeClean = consoleErrs.length === 0;
    console.log('RUNTIME_ERRS:', consoleErrs.length, JSON.stringify(consoleErrs));
    const pass =
      restored.m0ts === '10:05' && restored.userTime === '10:05' && restored.aiTime === '10:06' &&
      invalid.noTime && invalid.hasTime && invalid.rawClean && invalid.textHasTime &&
      stopHiddenBefore === true &&
      sent.timeOk && sent.savedTs && /^([01]\d|2[0-3]):[0-5]\d$/.test(sent.savedTs) &&
      sent.stopBtnBack && sent.sendEnabled &&
      runtimeClean;
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
