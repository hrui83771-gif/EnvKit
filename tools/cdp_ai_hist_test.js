// CDP 回归（v1.10.1）：AI 历史持久化 + error 态单击「AI 分析/看日志」分流
// 1) 塞两条消息 → aiHistSave → 刷新 → aiMessages 与聊天 DOM 恢复；悬挂 tool_calls 不入档
// 2) 清空按钮同时清 localStorage
// 3) error 态点桌宠 → 弹「让 AI 分析 / 查看日志」二选一，点查看日志正常关闭
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

    // 清掉旧持久化数据，从干净状态开始
    console.log('RESET:', await evalJS(`(function(){ localStorage.removeItem('ek-ai-hist'); return 'ok'; })()`));

    // ===== 1) 历史持久化 =====
    // 1a. 正常两条 + 尾部悬挂 tool_calls（应被 aiHistSave 剥掉）
    const seed = await evalJS(`(function(){
      aiMessages = [];
      aiMessages.push({role:'user', content:'测试问题：数据库起不来怎么办'},
                      {role:'assistant', content:'**这是测试回答**'});
      aiHistSave();
      const saved = JSON.parse(localStorage.getItem('ek-ai-hist')||'[]');
      const r1 = {savedLen: saved.length, savedRoles: saved.map(m=>m.role), hasDangling: saved.some(m=>m.tool_calls)};
      // 再补一条悬挂的 tool_calls 存进去试试
      aiMessages.push({role:'assistant', content:'', tool_calls:[{id:'x', type:'function', function:{name:'t', arguments:'{}'}}]});
      aiHistSave();
      const saved2 = JSON.parse(localStorage.getItem('ek-ai-hist')||'[]');
      r1.hasDangling2 = saved2.some(m=>m.tool_calls);
      r1.savedLen2 = saved2.length;
      return JSON.stringify(r1);
    })()`);
    console.log('SEED:', seed);

    // 1b. 刷新页面 → 应自动恢复
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);
    const restored = await evalJS(`(function(){
      const msgs = document.querySelectorAll('#ai-msgs .ai-msg');
      return JSON.stringify({
        aiLen: aiMessages.length,
        firstRole: aiMessages.length ? aiMessages[0].role : null,
        firstContent: aiMessages.length ? aiMessages[0].content : null,
        domUser: document.querySelectorAll('#ai-msgs .ai-msg.user').length,
        domAi: document.querySelectorAll('#ai-msgs .ai-msg.ai').length,
        domSys: document.querySelectorAll('#ai-msgs .ai-msg.sys').length,
        domTotal: msgs.length
      });
    })()`);
    console.log('RESTORED:', restored);

    // 1c. 清空按钮 → localStorage 也清掉
    const cleared = await evalJS(`(function(){
      document.getElementById('ai-clear').click();
      return JSON.stringify({aiLen: aiMessages.length, stored: localStorage.getItem('ek-ai-hist')});
    })()`);
    console.log('CLEARED:', cleared);

    // ===== 2) error 态单击桌宠 → 二选一弹窗 =====
    const errClick = await evalJS(`(function(){
      petAIState('error');
      document.getElementById('pet').click();
      const modal = document.getElementById('confirm');
      return JSON.stringify({
        shown: modal.classList.contains('show'),
        title: document.getElementById('cf-title').textContent,
        okText: document.getElementById('cf-ok').textContent,
        cancelText: document.getElementById('cf-cancel').textContent
      });
    })()`);
    console.log('ERR_CLICK:', errClick);

    // 点「查看日志」→ 弹窗应关闭（走 deepLink 分支，日志无错误时给气泡提示，不报错）
    const afterCancel = await evalJS(`(function(){
      document.getElementById('cf-cancel').click();
      return JSON.stringify({modalClosed: !document.getElementById('confirm').classList.contains('show')});
    })()`);
    console.log('AFTER_CANCEL:', afterCancel);

    // 复位状态
    await evalJS(`(function(){ petAIState('idle'); return 'ok'; })()`);

    const runtimeClean = consoleErrs.length === 0;
    console.log('RUNTIME_ERRS:', consoleErrs.length, JSON.stringify(consoleErrs));
    const r1 = JSON.parse(seed), r2 = JSON.parse(restored), r3 = JSON.parse(cleared),
          r4 = JSON.parse(errClick), r5 = JSON.parse(afterCancel);
    const pass =
      r1.savedLen === 2 && !r1.hasDangling && r1.savedLen2 === 2 && !r1.hasDangling2 &&
      r2.aiLen === 2 && r2.firstRole === 'user' && r2.domUser === 1 && r2.domAi === 1 && r2.domSys >= 1 &&
      r3.aiLen === 0 && r3.stored === null &&
      r4.shown === true && r4.title === '最近有任务失败' && r4.okText === '让 AI 分析' && r4.cancelText === '查看日志' &&
      r5.modalClosed === true &&
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
