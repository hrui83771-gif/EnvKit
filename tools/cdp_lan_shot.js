// 配对弹窗截图取证（v3.6.2）
// 只做两件事：把弹窗打开、截图。不做断言（断言在 cdp_lan_test.js 里）。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9346;
const EXE = path.join(REPO, 'tools', '_p2', 'EnvKit_p2.exe');
const DIR = path.dirname(EXE);
const OUT = path.join(REPO, 'tools', '_shots');

let TOKEN = '';
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
  base.lan = { enabled: true, port: 18796, devices: [] };
  base.dev = Object.assign({}, base.dev || {}, { server_port: PORT });
  fs.writeFileSync(seedPath, JSON.stringify(base, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdshot' }),
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

  const chrome = spawn(CHROME, [
    '--headless=new', '--remote-debugging-port=' + DBG, '--no-sandbox',
    '--disable-gpu', '--window-size=1440,900', 'about:blank',
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
  let id = 0; const pend = new Map();
  await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
  ws.onmessage = ev => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pend.has(msg.id)) { pend.get(msg.id)(msg); pend.delete(msg.id); }
  };
  const send = (method, params) => new Promise(res => {
    const i = ++id; pend.set(i, res);
    ws.send(JSON.stringify({ id: i, method, params: params || {} }));
  });
  const evalJS = async (expr) => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    return r.result && r.result.result ? r.result.result.value : undefined;
  };
  const shot = async (file) => {
    const r = await send('Page.captureScreenshot', { format: 'png' });
    fs.writeFileSync(path.join(OUT, file), Buffer.from(r.result.data, 'base64'));
    console.log('已保存 ' + file);
  };

  await send('Page.enable');
  await send('Runtime.enable');
  await send('Page.navigate', { url: URL_ });
  await sleep(4000);

  // 1) 个人中心（展开）
  await evalJS(`document.getElementById('btn-profile').click()`);
  await sleep(1200);
  await shot('lan_profile.png');

  // 2) 配对弹窗
  await evalJS(`document.getElementById('btn-lan-open').click()`);
  await sleep(1500);
  await shot('lan_pair_modal.png');

  // 3) 窄屏（手机宽度看弹窗布局）
  await send('Emulation.setDeviceMetricsOverride', { width: 430, height: 900, deviceScaleFactor: 1, mobile: true });
  await sleep(1200);
  await shot('lan_pair_mobile.png');

  ws.close();
  try { chrome.kill(); } catch (e) {}
  try { proc.kill(); } catch (e) {}
  console.log('完成');
  process.exit(0);
})().catch(e => { console.error('异常：', e); process.exit(2); });
