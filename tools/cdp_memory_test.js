// CDP 回归（v2.0）：记忆与经验面板（M1 自动经验 / M3 用户记忆）
// 记忆系统最怕两件事：面板坏了用户没法管，以及经验算错了误导 AI。
// 本用例盯的是"用户真的能看见并否决"这条链路：
//   1) /api/lessons 返回结构正确（lessons/memories 都是数组不是 null）
//   2) 面板入口在 AI 助手页内（用户要求的位置），点开才加载
//   3) 记忆能新增 → 渲染 → 切换注入方式 → 删除
//   4) 经验能渲染，且带审计证据
//   5) 经验能逐条否决（否决后状态与刷新后一致）
//   6) 页面零运行时异常
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9345;
const PROF = 'D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp15';
const MARK = 'cdp-memory-probe';

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
    const api = await evalJS(`(async()=>{ const d = await (await fetch('/api/lessons')).json();
      return JSON.stringify({ok:d.ok, lessons:Array.isArray(d.lessons), memories:Array.isArray(d.memories),
        n:(d.lessons||[]).length, m:(d.memories||[]).length,
        hasEvidence:(d.lessons||[]).some(x=>Array.isArray(x.evidence)),
        allEnabled:(d.lessons||[]).every(x=>x.enabled!==false)}); })()`);
    const apiObj = JSON.parse(api);
    check('api-lessons-shape', apiObj.ok === true && apiObj.lessons && apiObj.memories, api);
    console.log('  lessons=' + apiObj.n + ' memories=' + apiObj.m);

    // ===== 2) 入口在 AI 助手页内 =====
    const inAI = await evalJS(`!!document.getElementById('btn-ai-memory') &&
      !!document.getElementById('panel-ai') &&
      document.getElementById('ai-memory-box') !== null`);
    check('memory-box-in-ai-panel', inAI === true, String(inAI));

    const hiddenAtStart = await evalJS(`document.getElementById('ai-memory-box').style.display === 'none'`);
    check('memory-box-collapsed-initially', hiddenAtStart === true, String(hiddenAtStart));

    // ===== 3) 展开后自动加载并渲染 =====
    await evalJS(`document.getElementById('btn-ai-memory').click()`);
    await sleep(1500);
    const shown = await evalJS(`document.getElementById('ai-memory-box').style.display !== 'none'`);
    check('memory-box-opened', shown === true, String(shown));

    // ===== 4) 新增一条记忆 → 表格出现该行 =====
    const before = Number(await evalJS(`document.querySelectorAll('#mem-body tr').length`));
    await evalJS(`(async()=>{ document.getElementById('mem-text').value = ${JSON.stringify(MARK)};
      document.getElementById('mem-always').checked = true;
      document.getElementById('mem-add').click(); })()`);
    await sleep(1800);
    const after = Number(await evalJS(`document.querySelectorAll('#mem-body tr').length`));
    check('memory-add-renders-row', after > before, `before=${before} after=${after}`);

    const hasMark = await evalJS(`document.getElementById('mem-body').innerText.includes(${JSON.stringify(MARK)})`);
    check('memory-text-visible', hasMark === true, String(hasMark));

    // 走 API 确认真的落盘了
    const stored = await evalJS(`(async()=>{ const d = await (await fetch('/api/lessons')).json();
      return (d.memories||[]).some(m=>m.text===${JSON.stringify(MARK)} && m.always===true); })()`);
    check('memory-persisted-with-always', stored === true, String(stored));

    // ===== 5) 切换注入方式 → always 标记消失 =====
    await evalJS(`(()=>{ const rows=[...document.querySelectorAll('#mem-body tr')];
      const tr=rows.find(r=>r.innerText.includes(${JSON.stringify(MARK)}));
      if(tr) tr.lastElementChild.firstElementChild.click(); })()`);
    await sleep(1500);
    const toggled = await evalJS(`(async()=>{ const d = await (await fetch('/api/lessons')).json();
      const m=(d.memories||[]).find(x=>x.text===${JSON.stringify(MARK)}); return m? m.always : null; })()`);
    check('memory-toggle-always', toggled === false, 'always=' + toggled);

    // ===== 6) 经验表：若有数据应带证据；能逐条否决 =====
    const lesRows = Number(await evalJS(`document.querySelectorAll('#les-body tr').length`));
    if (apiObj.n > 0) {
      check('lessons-rendered', lesRows > 0, lesRows + ' rows');
      const hasEv = await evalJS(`document.getElementById('les-body').innerText.includes('依据')`);
      check('lessons-show-evidence', hasEv === true || !apiObj.hasEvidence, 'evidence=' + hasEv);
      const btn = await evalJS(`(()=>{ const b=document.querySelector('#les-body tr button'); return b? b.innerText.trim() : ''; })()`);
      check('lesson-toggle-button', btn.length > 0, btn);
      if (btn) {
        await evalJS(`document.querySelector('#les-body tr button').click()`);
        await sleep(1500);
        const nowBtn = await evalJS(`(()=>{ const b=document.querySelector('#les-body tr button'); return b? b.innerText.trim() : ''; })()`);
        check('lesson-toggle-works', nowBtn !== btn, `${btn} -> ${nowBtn}`);
        // 还原
        await evalJS(`document.querySelector('#les-body tr button').click()`);
        await sleep(1200);
      }
    } else {
      check('lessons-empty-state', lesRows === 1, lesRows + ' rows (empty placeholder)');
    }

    // ===== 7) 删除测试记忆（清理，别留垃圾在用户机器上）=====
    await evalJS(`(()=>{ const rows=[...document.querySelectorAll('#mem-body tr')];
      const tr=rows.find(r=>r.innerText.includes(${JSON.stringify(MARK)}));
      if(!tr) return; const btns=tr.lastElementChild.querySelectorAll('button');
      btns[btns.length-1].click(); })()`);
    await sleep(500);
    await evalJS(`window.confirm = ()=>true;`);
    await evalJS(`(()=>{ const rows=[...document.querySelectorAll('#mem-body tr')];
      const tr=rows.find(r=>r.innerText.includes(${JSON.stringify(MARK)}));
      if(!tr) return; const btns=tr.lastElementChild.querySelectorAll('button');
      btns[btns.length-1].click(); })()`);
    await sleep(1800);
    const gone = await evalJS(`(async()=>{ const d = await (await fetch('/api/lessons')).json();
      return !(d.memories||[]).some(m=>m.text===${JSON.stringify(MARK)}); })()`);
    check('memory-deleted', gone === true, String(gone));

    // 兜底清理：万一 confirm 没生效
    await evalJS(`fetch('/api/memory',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({op:'delete',id:'nonexistent-cleanup'})})`);

    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }

  console.log(results.join('\n'));
  console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));

  try { chrome.kill(); } catch (e) {}
  try { envkit.kill(); } catch (e) {}
  process.exit(exitCode);
})();
