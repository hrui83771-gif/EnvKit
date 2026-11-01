// CDP/HTTP 端到端（v3.6.2 第二批）：手机端三件本地能力 + 配对二次验证。
//
// 验的是**手机端**这条链，不是桌面端页面：
//   1. 能力开关的缺省值（只读开、写/执行关）—— 出厂默认不能把终端给出去；
//   2. /api/lan/shot 真能出一张合法 PNG，且宽度受限；
//   3. /api/lan/clip 关着时 403、开着时能往返；
//   4. /api/lan/term 关着时不握手、开着时握手成功并真的开始吐字节；
//   5. 二次验证：不开时配对直接过；开了之后不带验证码 → need_otp，带对了 → 过。
//
// 三条装置铁律沿用 cdp_lan_test.js：
//   - 不碰用户真实配置（exe 放 tools/_p2/，config.json 就落在那里）；
//   - 不要求用户先关 EnvKit（ENVKIT_INSTANCE_TAG 换互斥体名）；
//   - 开工先查哨兵，测到旧构建就 exit 2 说清楚（别把"装置在测旧的"报成"产品坏了"）。
const { spawn } = require('child_process');
const crypto = require('crypto');
const fs = require('fs');
const http = require('http');
const net = require('net');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const LAN_PORT = 18797;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN = 'http://127.0.0.1:' + LAN_PORT;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2', 'EnvKit_local.exe');
const DIR = path.dirname(EXE);

let TOKEN = '';
let COOKIE = '';
const failures = [];
function check(name, ok, detail) {
  if (ok) console.log('  PASS  ' + name);
  else { console.log('  FAIL  ' + name + (detail ? '  → ' + detail : '')); failures.push(name); }
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
function authH() { return { 'X-EnvKit-Token': TOKEN }; }

function req(method, url, body, headers) {
  return new Promise((res, rej) => {
    const u = new URL(url);
    const opts = {
      method, hostname: u.hostname, port: u.port,
      path: u.pathname + u.search, headers: Object.assign({}, headers || {}),
    };
    if (body !== undefined && body !== null) {
      opts.headers['Content-Type'] = 'application/json';
      opts.headers['Content-Length'] = Buffer.byteLength(body);
    }
    const r = http.request(opts, resp => {
      // **按 Buffer 收**：PNG 是二进制，用 utf8 拼字符串会把 magic 字节毁掉
      const chunks = [];
      resp.on('data', c => chunks.push(c));
      resp.on('end', () => res({ code: resp.statusCode, body: Buffer.concat(chunks), headers: resp.headers }));
    });
    r.on('error', rej);
    if (body !== undefined && body !== null) r.write(body);
    r.end();
  });
}
async function getJSON(url, headers) {
  const r = await req('GET', url, null, headers);
  try { return JSON.parse(r.body.toString('utf8')); } catch (e) { return r.body.toString('utf8'); }
}
async function lanGet(p) { return req('GET', LAN + p, null, { Host: '127.0.0.1:' + LAN_PORT, Cookie: COOKIE }); }
async function lanPost(p, obj) {
  return req('POST', LAN + p, JSON.stringify(obj), { Host: '127.0.0.1:' + LAN_PORT, Cookie: COOKIE });
}
async function saveLan(patch) {
  const r = await req('POST', URL_ + 'api/lan/config', JSON.stringify(patch), authH());
  try { return JSON.parse(r.body.toString('utf8')); } catch (e) { return null; }
}

// ---------- TOTP（只为端到端用，不引依赖） ----------
function b32d(s) {
  const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = 0, val = 0;
  const out = [];
  for (const ch of String(s).toUpperCase().replace(/=+$/, '')) {
    const i = A.indexOf(ch);
    if (i < 0) continue;
    val = (val << 5) | i; bits += 5;
    if (bits >= 8) { out.push((val >>> (bits - 8)) & 0xff); bits -= 8; }
  }
  return Buffer.from(out);
}
function totpNow(secret) {
  const step = Math.floor(Date.now() / 1000 / 30);
  const buf = Buffer.alloc(8);
  buf.writeUInt32BE(0, 0); buf.writeUInt32BE(step, 4);
  const h = crypto.createHmac('sha1', b32d(secret)).update(buf).digest();
  const o = h[h.length - 1] & 0x0f;
  const n = ((h[o] & 0x7f) << 24) | (h[o + 1] << 16) | (h[o + 2] << 8) | h[o + 3];
  return String(n % 1000000).padStart(6, '0');
}

// 裸 WS 握手：Node 的 WebSocket 客户端改不了 Cookie 头，
// 这里直接发 HTTP Upgrade，看服务端是 403 还是 101。
//
// 101 之后**不能立刻关连接**：ConPTY 起 shell 要几十毫秒，
// 关得太快就永远看不到第一帧输出 —— 那样"终端真的活着"这条断言会空转成 false。
// 所以 101 之后再多收 1.5 秒，把收到的字节一起带出去。
function wsHandshake(host, port, p, cookie) {
  return new Promise((res) => {
    const sock = net.connect(port, host, () => {
      sock.write(
        'GET ' + p + ' HTTP/1.1\r\n' +
        'Host: ' + host + ':' + port + '\r\n' +
        'Origin: http://' + host + ':' + port + '\r\n' +
        'Connection: Upgrade\r\n' +
        'Upgrade: websocket\r\n' +
        'Sec-WebSocket-Version: 13\r\n' +
        'Sec-WebSocket-Key: ' + crypto.randomBytes(16).toString('base64') + '\r\n' +
        'Cookie: ' + cookie + '\r\n\r\n');
    });
    let buf = Buffer.alloc(0);
    let status = 0;
    let headLen = 0;
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      try { sock.destroy(); } catch (e) {}
      res({ status, head: buf.slice(0, headLen).toString('latin1'), buf: buf.slice(headLen) });
    };
    sock.on('data', (d) => {
      buf = Buffer.concat([buf, d]);
      if (status) return;
      const m = /^HTTP\/1\.1 (\d{3})/.exec(buf.toString('latin1'));
      if (!m) return;
      status = parseInt(m[1], 10);
      const idx = buf.indexOf('\r\n\r\n');
      headLen = idx >= 0 ? idx + 4 : buf.length;
      if (status !== 101) return finish();
      setTimeout(finish, 1500);
    });
    sock.on('error', (e) => {
      if (!status) { status = 0; buf = Buffer.alloc(0); done = true; res({ status: 0, err: e.message, buf }); }
    });
    setTimeout(() => { if (!status) { done = true; try { sock.destroy(); } catch (e) {} res({ status: 0, err: 'timeout', buf }); } }, 5000);
  });
}

(async function main() {
  if (!fs.existsSync(EXE)) {
    console.log('找不到测试 exe：' + EXE);
    console.log('先跑：go build -trimpath -ldflags "-s -w" -o tools\\_p2\\EnvKit_local.exe .');
    process.exit(2);
  }
  // 种子：开手机访问、固定端口、**能力开关全部显式写死**（不依赖缺省，缺省另有断言）
  const seedPath = path.join(DIR, 'config.json');
  let base = {};
  try { base = JSON.parse(fs.readFileSync(seedPath, 'utf8')); } catch (e) { base = {}; }
  base.lan = { enabled: true, port: LAN_PORT, devices: [] };
  base.dev = Object.assign({}, base.dev || {}, { server_port: PORT });
  fs.writeFileSync(seedPath, JSON.stringify(base, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdploc' }),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', () => {});
  proc.stderr.on('data', () => {});

  let ready = false;
  for (let i = 0; i < 40; i++) {
    await sleep(1000);
    try {
      const html = await req('GET', URL_, null, null);
      if (html.code === 200) {
        const m = /__EK_TOKEN__="([0-9a-f]+)"/.exec(html.body.toString('utf8'));
        if (m) { TOKEN = m[1]; ready = true; break; }
      }
    } catch (e) { /* 还没起来 */ }
  }
  if (!ready) { console.log('实例没起来'); try { proc.kill(); } catch (e) {} process.exit(2); }
  console.log('实例就绪 token=' + TOKEN.slice(0, 8) + '…');

  try {
    // ---------- 哨兵 ----------
    const html = (await req('GET', URL_, null, null)).body.toString('utf8');
    const mobile = (await req('GET', LAN + '/', null, { Host: '127.0.0.1:' + LAN_PORT })).body.toString('utf8');
    check('哨兵：桌面端新开关已进产物（#lp_shot）', html.indexOf('id="lp_shot"') >= 0);
    check('哨兵：桌面端二次验证区块已进产物（#lp_otp_on）', html.indexOf('id="lp_otp_on"') >= 0);
    check('哨兵：手机页新卡片已进产物（#shotCard）', mobile.indexOf('id="shotCard"') >= 0);
    if (html.indexOf('id="lp_shot"') < 0 || mobile.indexOf('id="shotCard"') < 0) {
      console.log('STALE_INSTANCE：测的是旧构建，退出');
      process.exit(2);
    }

    // ---------- 1. 缺省值 ----------
    console.log('\n[1] 能力开关缺省值');
    let cfg = await getJSON(URL_ + 'api/lan/config', authH());
    // 种子里没写 allow_*，所以这里是"用户从没设过"→ 生效值走代码缺省
    check('看屏幕默认开', cfg.eff_shot === true, JSON.stringify(cfg.eff_shot));
    check('剪贴板默认关', cfg.eff_clip === false, JSON.stringify(cfg.eff_clip));
    check('终端默认关', cfg.eff_term === false, JSON.stringify(cfg.eff_term));
    check('raw 为 null（区分"没设过"和"设成 false"）', cfg.allow_shot === null, JSON.stringify(cfg.allow_shot));
    check('conpty 可用', cfg.conpty === true, String(cfg.conpty));

    // ---------- 配对 ----------
    const pair = await req('POST', LAN + '/api/lan/pair', JSON.stringify({ code: cfg.pair_code }), { Host: '127.0.0.1:' + LAN_PORT });
    check('配对成功', pair.code === 200, pair.code + ' ' + pair.body.toString('utf8').slice(0, 120));
    COOKIE = ((pair.headers['set-cookie'] || [])[0] || '').split(';')[0];
    check('拿到设备 cookie', !!COOKIE);

    // ---------- 2. 截屏 ----------
    console.log('\n[2] 截屏');
    const shot = await lanGet('/api/lan/shot?max=640');
    check('shot 200', shot.code === 200, String(shot.code));
    check('shot 是 PNG', (shot.headers['content-type'] || '').indexOf('image/png') >= 0, shot.headers['content-type']);
    const magic = shot.body.slice(0, 8).equals(Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]));
    check('PNG magic 正确（按 Buffer 判）', magic, shot.body.slice(0, 8).toString('hex'));
    // IHDR 宽高在固定偏移：8..16 是长度+类型，16..24 是 width/height
    let pw = 0, ph = 0;
    if (magic && shot.body.length > 24) { pw = shot.body.readUInt32BE(16); ph = shot.body.readUInt32BE(20); }
    console.log('       截图尺寸 = ' + pw + 'x' + ph + '，字节 ' + shot.body.length);
    check('宽度被限制在 max=640', pw > 0 && pw <= 640, pw + 'x' + ph);
    check('高度也算出来了（不是 0）', ph > 0, ph + '');
    check('长边没有超过 max', Math.max(pw, ph) <= 640 || pw === 640, pw + 'x' + ph);

    // ---------- 3. 剪贴板 ----------
    console.log('\n[3] 剪贴板');
    const clipOff = await lanGet('/api/lan/clip');
    check('关着时 GET 403', clipOff.code === 403, String(clipOff.code));
    let cj = {};
    try { cj = JSON.parse(clipOff.body.toString('utf8')); } catch (e) {}
    check('403 里带 disabled 标记（前端据此说明原因）', cj.disabled === true, JSON.stringify(cj).slice(0, 120));

    await saveLan({ allow_clip: true });
    const mark = 'envkit-clip-' + Date.now();
    const cp = await lanPost('/api/lan/clip', { text: mark });
    check('打开后写入 200', cp.code === 200, cp.code + ' ' + cp.body.toString('utf8').slice(0, 120));
    const cg = await lanGet('/api/lan/clip');
    const cgj = JSON.parse(cg.body.toString('utf8'));
    check('读回来的就是刚写进去的', cgj.text === mark, JSON.stringify(cgj).slice(0, 160));
    await saveLan({ allow_clip: false });

    // ---------- 4. 终端 ----------
    console.log('\n[4] 终端');
    const tOff = await lanGet('/api/lan/term');
    check('关着时 HTTP 403（不握手）', tOff.code === 403, String(tOff.code));
    const hOff = await wsHandshake('127.0.0.1', LAN_PORT, '/api/lan/term', COOKIE);
    check('关着时 WS 也握手不成功', hOff.status !== 101, String(hOff.status) + ' ' + (hOff.err || ''));

    await saveLan({ allow_term: true });
    const hOn = await wsHandshake('127.0.0.1', LAN_PORT, '/api/lan/term', COOKIE);
    check('打开后 WS 握手 101', hOn.status === 101, String(hOn.status) + ' ' + (hOn.err || ''));
    // 握手成功之后服务端会立刻把 shell 的输出推过来（首帧是 0x82 = FIN+binary）
    const tail = hOn.buf;
    check('握手后收到了终端输出帧（0x82 二进制）', !!(tail && tail.length > 0 && tail[0] === 0x82),
      tail && tail.length ? tail.slice(0, 4).toString('hex') : 'no data');
    await saveLan({ allow_term: false });

    // ---------- 5. 二次验证 ----------
    console.log('\n[5] 配对二次验证');
    let oc = await saveLan({ require_otp: true });
    check('打开二次验证时自动生成密钥', !!(oc && oc.otp_secret), oc && String(oc.otp_secret).slice(0, 8));
    check('otp_url 是 otpauth 地址', !!(oc && /^otpauth:\/\/totp\//.test(oc.otp_url || '')), oc && oc.otp_url);

    const noOtp = await req('POST', LAN + '/api/lan/pair', JSON.stringify({ code: oc.pair_code }), { Host: '127.0.0.1:' + LAN_PORT });
    const nj = JSON.parse(noOtp.body.toString('utf8'));
    check('不带验证码被拒', noOtp.code === 403, String(noOtp.code));
    check('回包里 need_otp=true（手机据此展开输入框）', nj.need_otp === true, JSON.stringify(nj));

    const withOtp = await req('POST', LAN + '/api/lan/pair',
      JSON.stringify({ code: oc.pair_code, totp: totpNow(oc.otp_secret) }),
      { Host: '127.0.0.1:' + LAN_PORT });
    check('带对口令码配对成功', withOtp.code === 200, withOtp.code + ' ' + withOtp.body.toString('utf8').slice(0, 120));

    // 同一个码再来一次必须被当成重放（防的是"码被截获后重放 30 秒"）
    const replay = await req('POST', LAN + '/api/lan/pair',
      JSON.stringify({ code: oc.pair_code, totp: totpNow(oc.otp_secret) }),
      { Host: '127.0.0.1:' + LAN_PORT });
    const rj = JSON.parse(replay.body.toString('utf8'));
    check('同一个验证码重放被拒', replay.code === 403, String(replay.code));
    check('拒绝理由是"刚用过"（不是"不正确"）', /刚用过/.test(rj.msg || ''), rj.msg);

    await saveLan({ clear_otp: true });
    const afterClear = await getJSON(URL_ + 'api/lan/config', authH());
    check('关掉后密钥被清空', !afterClear.otp_secret, JSON.stringify(afterClear.otp_secret));
    check('关掉后开关是 false', afterClear.require_otp === false, String(afterClear.require_otp));

    // ---------- 复位 ----------
    await saveLan({ allow_shot: null });
  } catch (e) {
    console.log('异常：' + (e && e.stack || e));
    failures.push('exception');
  } finally {
    try { proc.kill(); } catch (e) {}
  }

  console.log('\n==== ' + (failures.length === 0 ? '全部通过' : failures.length + ' 项失败') + ' ====');
  if (failures.length) { failures.forEach(f => console.log('  - ' + f)); process.exit(1); }
})();
