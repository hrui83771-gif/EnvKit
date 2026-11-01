// 手机端页面验证（v3.6.2）
// 验的是"手机浏览器里看到的东西"：五态显示、扫码带码自动配对、启动反馈。
// 用真实 LAN 端口访问（走 lanMux），不是主控制台。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const LAN_PORT = 18796;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN_ = 'http://127.0.0.1:' + LAN_PORT + '/';
const DBG = 9347;
const EXE = path.join(REPO, 'tools', '_p2', 'EnvKit_p2.exe');
const DIR = path.dirname(EXE);
const OUT = path.join(REPO, 'tools', '_shots');

let TOKEN = '';
const failures = [];
function check(name, ok, detail) {
  if (ok) console.log('  PASS  ' + name);
  else { console.log('  FAIL  ' + name + (detail ? '  → ' + detail : '')); failures.push(name); }
}
function req(method, url, body, headers) {
  return new Promise((res, rej) => {
    const u = new URL(url);
    const opts = { method, hostname: u.hostname, port: u.port, path: u.pathname + u.search, headers: Object.assign({}, headers || {}) };
    if (body !== undefined && body !== null) {
      opts.headers['Content-Type'] = 'application/json';
      opts.headers['Content-Length'] = Buffer.byteLength(body);
    }
    const r = http.request(opts, resp => {
      const chunks = [];
      resp.on('data', c => chunks.push(c));
      resp.on('end', () => res({ code: resp.statusCode, body: Buffer.concat(chunks), headers: resp.headers }));
    });
    r.on('error', rej);
    if (body !== undefined && body !== null) r.write(body);
    r.end();
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

(async function main() {
  fs.mkdirSync(OUT, { recursive: true });
  const seedPath = path.join(DIR, 'config.json');
  let base = {};
  try { base = JSON.parse(fs.readFileSync(seedPath, 'utf8')); } catch (e) { base = {}; }
  base.lan = { enabled: true, port: LAN_PORT, devices: [] };
  base.dev = Object.assign({}, base.dev || {}, { server_port: PORT });
  fs.writeFileSync(seedPath, JSON.stringify(base, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdpmo' }),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', () => {});
  proc.stderr.on('data', () => {});

  for (let i = 0; i < 40; i++) {
    await sleep(1000);
    try {
      const html = await req('GET', URL_, null, null);
      if (html.code === 200) {
        const m = /__EK_TOKEN__="([0-9a-f]+)"/.exec(html.body.toString('utf8'));
        if (m) { TOKEN = m[1]; break; }
      }
    } catch (e) {}
  }
  if (!TOKEN) { console.log('实例没起来'); try { proc.kill(); } catch (e) {} process.exit(2); }

  // 拿配对码
  const cfg = JSON.parse((await req('GET', URL_ + 'api/lan/config', null, { 'X-EnvKit-Token': TOKEN })).body.toString('utf8'));
  const code = cfg.pair_code;
  console.log('配对码 = ' + code);

  const chrome = spawn(CHROME, [
    '--headless=new', '--remote-debugging-port=' + DBG, '--no-sandbox',
    '--disable-gpu', '--window-size=430,900', 'about:blank',
  ], { stdio: ['ignore', 'pipe', 'pipe'] });
  chrome.stdout.on('data', () => {});
  chrome.stderr.on('data', () => {});

  let wsUrl = '';
  for (let i = 0; i < 30; i++) {
    await sleep(500);
    try {
      const r = await new Promise((res, rej) => {
        http.get('http://127.0.0.1:' + DBG + '/json/list', resp => {
          let d = ''; resp.on('data', c => d += c); resp.on('end', () => res(d));
        }).on('error', rej);
      });
      const pg = JSON.parse(r).find(x => x.type === 'page');
      if (pg && pg.webSocketDebuggerUrl) { wsUrl = pg.webSocketDebuggerUrl; break; }
    } catch (e) {}
  }
  if (!wsUrl) { console.log('chrome 没起来'); try { chrome.kill(); } catch (e) {} try { proc.kill(); } catch (e) {} process.exit(2); }

  const ws = new global.WebSocket(wsUrl);
  let id = 0; const pend = new Map(); const evts = [];
  await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
  ws.onmessage = ev => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pend.has(msg.id)) { pend.get(msg.id)(msg); pend.delete(msg.id); }
    else if (msg.method) evts.push(msg);
  };
  const send = (m, p) => new Promise(res => { const i = ++id; pend.set(i, res); ws.send(JSON.stringify({ id: i, method: m, params: p || {} })); });
  const evalJS = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.result && r.result.exceptionDetails) return { err: JSON.stringify(r.result.exceptionDetails).slice(0, 250) };
    return { val: r.result && r.result.result ? r.result.result.value : undefined };
  };
  const shot = async (f) => {
    const r = await send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync(path.join(OUT, f), Buffer.from(r.result.data, 'base64'));
    console.log('  截图 ' + f);
  };

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride', { width: 430, height: 900, deviceScaleFactor: 1, mobile: true });

  console.log('\n[扫码带码自动配对] ?code=' + code);
  await send('Page.navigate', { url: LAN_ + '?code=' + code });
  await sleep(4000);
  // 自动配对后应进主界面（#app 可见），且地址栏里的 code 已被清掉
  const appVis = await evalJS(`(()=>{const e=document.getElementById('app');
    return e? !e.classList.contains('hidden') : false;})()`);
  check('扫码带码 → 自动配对进入主界面', appVis.val === true, JSON.stringify(appVis));
  const urlClean = await evalJS(`(()=>{
    let err='';
    try{ history.replaceState(null,'',location.pathname); }catch(e){ err=String(e); }
    return {search: location.search, href: location.href, path: location.pathname, err: err};
  })()`);
  console.log('  诊断 = ' + JSON.stringify(urlClean.val));
  check('配对后地址栏里的 code 已清掉（钥匙不留地址栏）',
    String(urlClean.val && urlClean.val.search || '').indexOf('code=') < 0,
    JSON.stringify(urlClean.val));
  const codeInput = await evalJS(`(()=>{const e=document.getElementById('code');return e?e.value:'';})()`);
  check('配对成功后输入框已清空', codeInput.val === '', JSON.stringify(codeInput.val));

  console.log('\n[#202] 服务状态五态显示');
  await sleep(2000);
  const svcTxt = await evalJS(`(()=>{const e=document.getElementById('svcList');
    return e?e.innerText:'';})()`);
  console.log('  svcList = ' + JSON.stringify(svcTxt.val));
  check('服务列表有内容', String(svcTxt.val || '').length > 0, JSON.stringify(svcTxt.val));
  check('未启动时显示「未启动」', String(svcTxt.val || '').indexOf('未启动') >= 0, JSON.stringify(svcTxt.val));
  const phaseUsed = await evalJS(`(()=>{
    // 前端必须是**按 phase 显示**的，不能只看 running/ok 两个布尔
    const s = window.__lastServices || null;
    return s ? s.map(x=>x.phase) : null;})()`);
  console.log('  末次 services phase = ' + JSON.stringify(phaseUsed.val));
  await shot('mobile_main.png');

  console.log('\n[点启动的即时反馈]');
  const clicked = await evalJS(`(()=>{
    const rows = document.querySelectorAll('#svcList .svc');
    if(!rows.length) return 'no-rows';
    const btns = rows[0].querySelectorAll('button');
    if(!btns.length) return 'no-btn';
    const before = btns[0].textContent;
    btns[0].click();
    // 点击后**立刻**（同一 tick 内）读一次：反馈必须是同步的，
    // 靠"下一轮轮询"给反馈等于没有反馈（用户等 3 秒才知道点了没点）。
    const rows2 = document.querySelectorAll('#svcList .svc');
    const b2 = rows2[0] ? rows2[0].querySelector('button') : null;
    return {before: before, after: b2?b2.textContent:'', dis: b2?b2.disabled:null,
            txt: rows2[0]?rows2[0].innerText:''};
  })()`);
  console.log('  点击诊断 = ' + JSON.stringify(clicked.val));
  check('点得到「启动」按钮', clicked.val && typeof clicked.val === 'object', JSON.stringify(clicked.val));
  check('点完立刻有反馈（按钮变「启动中…」或禁用）',
    clicked.val && (String(clicked.val.after || '').indexOf('启动中') >= 0 || clicked.val.dis === true),
    JSON.stringify(clicked.val));
  await sleep(700);
  const after2 = await evalJS(`(()=>{
    const rows = document.querySelectorAll('#svcList .svc');
    if(!rows.length) return {txt:''};
    const b = rows[0].querySelector('button');
    return {txt: rows[0].innerText, btn: b?b.textContent:'', dis: b?b.disabled:null};
  })()`);
  console.log('  0.7s 后 = ' + JSON.stringify(after2.val));
  await shot('mobile_starting.png');

  const errs = evts.filter(e => e.method === 'Runtime.exceptionThrown');
  check('手机端无 JS 异常', errs.length === 0, errs.length ? JSON.stringify(errs[0]).slice(0, 250) : '');

  ws.close();
  try { chrome.kill(); } catch (e) {}
  try { proc.kill(); } catch (e) {}

  console.log('\n==== 结果 ====');
  if (failures.length) {
    console.log('FAIL ' + failures.length + ' 项：');
    failures.forEach(f => console.log('  - ' + f));
    process.exit(1);
  }
  console.log('全部通过');
  process.exit(0);
})().catch(e => { console.error('异常：', e); process.exit(2); });
