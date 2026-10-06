// CDP 回归（v2.0.0-rc）：验证层前端集成
// 1) 新构建的 exe 必须 serve 出带复验工具名的新前端（embed 是否真的重编）
// 2) AI_TOOL_CN 与 I18N_EN 都要有 verify_environment 的中/英文案
// 3) 页面上的版本号必须是 2.0.0-rc（防止"代码改了、包没重编"）
// 4) 真实页面加载零运行时异常
// 说明：复验的业务行为由 verify_test.go 覆盖（备份产物被删、校验和被改、端口被占、块高不涨）。
const { spawn } = require('child_process');
const http = require('http');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const CWD = 'D:\\BCGD\\FarmTrace\\envkit';
const URL_ = 'http://127.0.0.1:18765/';
const DBG = 9342;
const PROF = 'D:\\BCGD\\FarmTrace\\envkit\\tools\\_cdptmp13';

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

    // ===== 1) 服务端确实 serve 出新前端 =====
    const html = await get(URL_);
    check('served-html-has-verify_tool', html.includes('verify_environment'), html.length + ' bytes');

    // ===== 2) 前端工具名映射齐全 =====
    const toolMap = await evalJS(`JSON.stringify(AI_TOOL_CN)`);
    check('tool-cn-verify_environment', String(toolMap).includes('verify_environment'), '');
    check('tool-cn-still-has-explore', String(toolMap).includes('list_project') && String(toolMap).includes('read_file'), '');

    // ===== 3) i18n 英译已注入 =====
    const i18nProbe = await evalJS(`JSON.stringify({
      verify: (typeof I18N_EN !== 'undefined') ? (I18N_EN['复验环境状态'] || '') : 'NO-I18N'
    })`);
    check('i18n-has-verify', /Verify environment state/.test(i18nProbe), i18nProbe);

    // ===== 4) 页面上的版本号与源码一致（防止"代码改了、包没重编"）=====
    //
    // **不要硬编码版本号，也不要从 exe 二进制里猜。** 两版都试过：
    //   ·硬编码 `2.0.0-rc` —— 那是 v2.0 的值，版本一往前走断言永久失效。
    //     实测它从 v2.0 之后一直红到现在（最后修改于8e782c5），
    //     而所有人都在忽略它。**一个长期红的断言等于没有断言。**
    //   · 从 exe 二进制里正则捞 —— 实测里面有 23 处 `2.x.y`、6 个不同版本
    //     （含依赖里的 2.0.2 / 2.2.1），猜必然取错。
    //
    // 现在以 `main.go` 的 appVersion 常量为唯一真值来源 ——
    // 它就是编译进二进制的那份，两边不一致就说明包没重编。
    const fsMod = require('fs');
    const srcVer = (() => {
      try {
        const m = fsMod.readFileSync(require('path').join(CWD, 'main.go'), 'utf8')
          .match(/appVersion\s*=\s*"(\d+\.\d+\.\d+)"/);
        return m ? m[1] : '';
      } catch (e) { return ''; }
    })();
    // 直接看 **serve 出来的 HTML** 而不是 innerText ——
    // 实测 innerText 里混着 `127.0.0` 这类串，用版本号正则去捞容易误匹配；
    // 而 serve 出来的 HTML 里那行是确定的 `EnvKit</b> v2.6.0 · 2026-10-06`。
    // 这也正是本断言真正要验的东西：**占位符有没有被替换成当前版本**。
    const pageVer = (html.match(/EnvKit<\/b>\s*v([0-9]+\.[0-9]+\.[0-9]+)/) || [])[1] || '';
    check('page-version-matches-source', !!srcVer && pageVer === srcVer,
          'src=' + srcVer + ' page=' + pageVer);
    check('no-unreplaced-version-placeholder', !html.includes('__APP_VERSION__'),
          'serve 出的 HTML 里不该还有 __APP_VERSION__ 占位符');

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
