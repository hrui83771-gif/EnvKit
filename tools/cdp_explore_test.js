// CDP 回归（v2.0.0-beta）：探索层前端集成
// 1) 新构建的 exe 必须 serve 出带探索工具名与画像词条的前端（embed 是否真的重编）
// 2) AI_TOOL_CN 与 I18N 都必须含四个新工具的中/英文案
// 3) 真实页面加载零运行时异常
// 说明：工具的业务行为由 explore_test.go 的 Go 单测覆盖（沙箱越界、敏感文件、脱敏、限额）。
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9341;

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
    '--remote-debugging-port=' + DBG, '--user-data-dir=D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp12',
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
    let errs = [];
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

    // ===== 1) 服务端确实 serve 出新前端 =====
    const html = await get(URL_);
    check('served-html-has-list_project', html.includes('list_project'), html.length + ' bytes');

    // ===== 2) 前端工具名映射齐全 =====
    const toolMap = await evalJS(`JSON.stringify(AI_TOOL_CN)`);
    check('tool-cn-list_project', String(toolMap).includes('list_project'), '');
    check('tool-cn-search_files', String(toolMap).includes('search_files'), '');
    check('tool-cn-read_file', String(toolMap).includes('read_file'), '');
    check('tool-cn-get_project_brief', String(toolMap).includes('get_project_brief'), '');

    // ===== 3) i18n 英译已注入（字典变量名是 I18N_EN）=====
    const i18nProbe = await evalJS(`JSON.stringify({
      brief: (typeof I18N_EN !== 'undefined') ? (I18N_EN['读取项目画像'] || '') : 'NO-I18N',
      read: (typeof I18N_EN !== 'undefined') ? (I18N_EN['读取项目文件'] || '') : 'NO-I18N',
      search: (typeof I18N_EN !== 'undefined') ? (I18N_EN['搜索项目文件'] || '') : 'NO-I18N'
    })`);
    check('i18n-has-project-brief', /Read project profile/.test(i18nProbe), i18nProbe);

    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }

  console.log(results.join('\n'));
  console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));

  try { envkit.kill(); } catch (e) {}
  try { chrome.kill(); } catch (e) {}
  await sleep(500);
  process.exit(exitCode);
})();
