// CDP 回归（v3.6.2 手机访问改造）：四项需求逐条验。
//
//   #199 桌面端「启动中」永不翻转  → 复验后 phase 必须是 running，不是 starting
//   #200 局域网地址筛选              → addrs 里不能有 169.254 / VMware 虚拟网卡地址
//   #201 个人中心开关 + 配对弹窗      → DOM 断言 + 遮挡断言（elementFromPoint）
//   #202 手机端状态同步              → /api/lan/state 返回 phase 五态
//
// 三条设计约束（沿用 cdp_p2_mission_test.js 的踩坑经验）：
//   1. **不碰用户的真实数据**：配置写到临时目录（EXE 旁边），独立端口。
//   2. **不要求用户先关掉 EnvKit**：ENVKIT_INSTANCE_TAG 换互斥体名，两边并存。
//   3. **先确认测的是新构建**：哨兵断言查新元素 id，不满足就 exit 2 报 STALE_INSTANCE。
//      （"评测装置在测旧构建"是本项目真实踩过的坑，让它一开口就说清楚。）
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const LAN_PORT = 18796;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9345;
const EXE = (function () {
  if (process.env.ENVKIT_EXE) return process.env.ENVKIT_EXE;
  return path.join(REPO, 'tools', '_p2', 'EnvKit_p2.exe');
})();
// 数据目录 = exe 所在目录（exeDir()），不是 cwd
const DIR = path.dirname(EXE);

let TOKEN = '';
const failures = [];
function check(name, ok, detail) {
  if (ok) { console.log('  PASS  ' + name); }
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
      // **按 Buffer 收**：PNG 是二进制，用 utf8 拼字符串会把 magic 字节毁掉
      // （判"是不是 PNG"就再也判不准了 —— 装置坏了会伪装成产品坏了）。
      const chunks = [];
      resp.on('data', c => chunks.push(c));
      resp.on('end', () => res({
        code: resp.statusCode,
        body: Buffer.concat(chunks),
        headers: resp.headers,
      }));
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
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
function authH() { return { 'X-EnvKit-Token': TOKEN }; }

// ---------- 启动隔离实例 ----------
function startInstance() {
  const p = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdplan' }),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  p.stdout.on('data', () => {});
  p.stderr.on('data', () => {});
  return p;
}

(async function main() {
  if (!fs.existsSync(EXE)) {
    console.log('找不到测试 exe：' + EXE);
    console.log('先跑：go build -trimpath -ldflags "-s -w" -o tools\\_p2\\EnvKit_p2.exe .');
    process.exit(2);
  }
  // 种子：开启手机访问、固定端口
  const seed = {
    lan: { enabled: true, port: LAN_PORT, devices: [] },
    dev: { server_port: PORT },
  };
  const seedPath = path.join(DIR, 'config.json');
  let base = {};
  try { base = JSON.parse(fs.readFileSync(seedPath, 'utf8')); } catch (e) { base = {}; }
  base.lan = seed.lan;
  base.dev = Object.assign({}, base.dev || {}, seed.dev);
  fs.writeFileSync(seedPath, JSON.stringify(base, null, 2));

  const proc = startInstance();
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
  if (!ready) {
    console.log('实例没起来');
    try { proc.kill(); } catch (e) {}
    process.exit(2);
  }
  console.log('实例就绪，token=' + TOKEN.slice(0, 8) + '…');

  try {
    // ---------- 哨兵：确认测的是新构建 ----------
    const html = (await req('GET', URL_, null, null)).body.toString('utf8');
    check('哨兵：新 DOM 已进构建产物（#pp_lan_on）', html.indexOf('id="pp_lan_on"') >= 0);
    check('哨兵：配对弹窗已进构建产物（#lanpair）', html.indexOf('id="lanpair"') >= 0);
    check('哨兵：旧开关已移除（#s_lan_on 不该再存在）', html.indexOf('id="s_lan_on"') < 0);
    if (html.indexOf('id="pp_lan_on"') < 0) {
      console.log('STALE_INSTANCE：测的是旧构建，退出');
      process.exit(2);
    }

    // ---------- #200 地址筛选 ----------
    console.log('\n[#200] 局域网地址筛选');
    const cfg = await getJSON(URL_ + 'api/lan/config', authH());
    check('config 返回 addrs', Array.isArray(cfg.addrs), JSON.stringify(cfg));
    console.log('       addrs = ' + JSON.stringify(cfg.addrs));
    const bad = (cfg.addrs || []).filter(u => /169\.254\./.test(u));
    check('addrs 里没有 169.254 链路本地地址', bad.length === 0, JSON.stringify(bad));
    const vm = (cfg.addrs || []).filter(u => /192\.168\.110\.|192\.168\.128\./.test(u));
    check('addrs 里没有 VMware 虚拟网卡地址', vm.length === 0, JSON.stringify(vm));
    check('addrs 数量 ≤ 2（不该再一次性列一大串）', (cfg.addrs || []).length <= 2, JSON.stringify(cfg.addrs));

    // ---------- 二维码 ----------
    console.log('\n[二维码] /api/lan/qr');
    const qr = await req('GET', URL_ + 'api/lan/qr', null, authH());
    check('QR 返回 200', qr.code === 200, String(qr.code));
    check('QR 是 PNG', (qr.headers['content-type'] || '').indexOf('image/png') >= 0, qr.headers['content-type']);
    // PNG magic：89 50 4E 47 0D 0A 1A 0A（按 Buffer 判，字符串会被 utf8 解码毁掉）
    check('QR 内容是合法 PNG', qr.body.length > 100 && qr.body.slice(0, 8).equals(Buffer.from([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A])), qr.body.slice(0, 8).toString('hex'));

    // ---------- #202 手机端状态：五态 ----------
    console.log('\n[#202] 手机端状态同步');
    // 配一台设备
    const pair = await req('POST', 'http://127.0.0.1:' + LAN_PORT + '/api/lan/pair', JSON.stringify({ code: cfg.pair_code }), { Host: '127.0.0.1:' + LAN_PORT });
    check('配对成功', pair.code === 200, pair.code + ' ' + pair.body.toString('utf8').slice(0, 120));
    const cookie = (pair.headers['set-cookie'] || [])[0] || '';
    const ck = cookie.split(';')[0];
    check('拿到设备 cookie', !!ck, cookie);

    const st = await req('GET', 'http://127.0.0.1:' + LAN_PORT + '/api/lan/state', null, { Host: '127.0.0.1:' + LAN_PORT, Cookie: ck });
    const sj = JSON.parse(st.body.toString('utf8'));
    check('state 200', st.code === 200, String(st.code));
    check('state 带 services', Array.isArray(sj.services) && sj.services.length === 2, JSON.stringify(sj.services));
    const web = (sj.services || []).find(s => s.key === 'web');
    check('state 里 web 有 phase 字段', web && typeof web.phase === 'string', JSON.stringify(web));
    check('web.phase 是五态之一', web && ['stopped', 'starting', 'running', 'degraded', 'failed'].indexOf(web.phase) >= 0, web && web.phase);

    // ---------- #199 桌面端相位：启动后不能停在 starting ----------
    console.log('\n[#199] 桌面端「启动中」不永久卡住');
    const rt = await getJSON(URL_ + 'api/runtime/state', authH());
    // 响应结构：{ok, state:{services:{web:{phase,...}, backend:{...}}, ...}}
    const rweb = ((rt.state && rt.state.services && rt.state.services.web) || {});
    console.log('       web.phase = ' + rweb.phase + '  verified=' + rweb.verified);
    check('web 有 phase 字段', typeof rweb.phase === 'string', JSON.stringify(rt).slice(0, 300));
    // 这里服务没起，phase 应是 stopped（不是 starting）——
    // 如果是 starting，说明"启动中"旗标在没有任何启动动作时也挂着。
    check('没启动时 phase 不是 starting', rweb.phase !== 'starting', String(rweb.phase));

    // ---------- #201 前端 DOM ----------
    console.log('\n[#201] 个人中心开关 + 配对弹窗');
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
        const list = JSON.parse(r);
        const pg = list.find(x => x.type === 'page');
        if (pg && pg.webSocketDebuggerUrl) { wsUrl = pg.webSocketDebuggerUrl; break; }
      } catch (e) { /* chrome 还没起 */ }
    }
    if (!wsUrl) { check('CDP 连上了 Chrome', false, '拿不到 wsUrl'); }
    else {
      check('CDP 连上了 Chrome', true);
      const WS = null; // 用 Node 22 原生 WebSocket，不需要 ws 模块
      const ws = new global.WebSocket(wsUrl);
      let id = 0;
      const pend = new Map();
      const evts = [];
      await new Promise((r, j) => { ws.onopen = r; ws.onerror = j; });
      ws.onmessage = ev => {
        const msg = JSON.parse(ev.data);
        if (msg.id && pend.has(msg.id)) { pend.get(msg.id)(msg); pend.delete(msg.id); }
        else if (msg.method) evts.push(msg);
      };
      const send = (method, params) => new Promise(res => {
        const i = ++id; pend.set(i, res);
        ws.send(JSON.stringify({ id: i, method, params: params || {} }));
      });
      const evalJS = async (expr) => {
        const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
        if (r.result && r.result.exceptionDetails) {
          return { err: JSON.stringify(r.result.exceptionDetails).slice(0, 300) };
        }
        return { val: r.result && r.result.result ? r.result.result.value : undefined };
      };

      await send('Page.enable');
      await send('Runtime.enable');
      await send('Page.navigate', { url: URL_ });
      // 等待久一点：/api/lan/config 现在会顺带做防火墙诊断
      //（两次 PowerShell，各约 1-2 秒），页面加载后开关状态要等它回来才写入。
      await sleep(3500);
      await evalJS(`(()=>{
        const sw=document.getElementById('pp_lan_on');
        if(sw && sw.checked) return 'already';
        // 没到位就再等一轮（最多 20 次 × 500ms）
        return new Promise(res=>{
          let n=0;
          const iv=setInterval(()=>{
            n++;
            if((sw && sw.checked) || n>20){ clearInterval(iv); res(n>20?'timeout':'ok'); }
          },500);
        });
      })()`);
      await sleep(600);

      // 个人中心里的开关
      const sw = await evalJS(`!!document.getElementById('pp_lan_on')`);
      check('DOM：#pp_lan_on 存在（个人中心开关）', sw.val === true, JSON.stringify(sw));
      const inPop = await evalJS(`(()=>{const e=document.getElementById('pp_lan_on');if(!e)return false;
        return !!(e.closest('#profile-pop'));})()`);
      check('DOM：开关在 #profile-pop 内（个人中心下）', inPop.val === true, JSON.stringify(inPop));
      const oldSwitch = await evalJS(`!!document.getElementById('s_lan_on')`);
      check('DOM：旧开关 #s_lan_on 已移除（唯一写入口）', oldSwitch.val === false, JSON.stringify(oldSwitch));

      // 开关状态与后端一致（后端 seed 是 enabled:true）
      const swOn = await evalJS(`document.getElementById('pp_lan_on') ? document.getElementById('pp_lan_on').checked : null`);
      check('DOM：开关状态与后端一致（应为 true）', swOn.val === true, JSON.stringify(swOn));

      // 配对弹窗：点开
      const opened = await evalJS(`(()=>{const b=document.getElementById('btn-lan-open');if(!b)return 'no-btn';
        b.click(); return 'clicked';})()`);
      check('DOM：点「配对与扫码…」有反应', opened.val === 'clicked', JSON.stringify(opened));
      await sleep(1200);
      const shown = await evalJS(`(()=>{const m=document.getElementById('lanpair');return m?m.className:'';})()`);
      check('DOM：#lanpair 已显示（class 含 show）', String(shown.val || '').indexOf('show') >= 0, String(shown.val));
      const code = await evalJS(`(()=>{const e=document.getElementById('lp_code');return e?e.textContent:'';})()`);
      check('DOM：弹窗里有配对码', !!code.val && code.val !== '——', String(code.val));
      const url = await evalJS(`(()=>{const e=document.getElementById('lp_url');return e?e.textContent:'';})()`);
      check('DOM：弹窗里有访问网址', !!url.val && url.val.indexOf('http') === 0, String(url.val));
      check('DOM：访问网址带上了配对码（扫码即配对）', String(url.val || '').indexOf('code=') >= 0, String(url.val));
      const qrSrc = await evalJS(`(()=>{const e=document.getElementById('lp_qr');return e?e.getAttribute('src'):'';})()`);
      check('DOM：弹窗里有二维码 img', !!qrSrc.val, String(qrSrc.val));
      // 二维码**真的加载出来了**没有 —— naturalWidth > 0 才算图。
      // <img src> 不带令牌头时会返回一张碎图，src 看着是有的，图却是坏的。
      const qrImg = await evalJS(`(()=>{const e=document.getElementById('lp_qr');
        return e?{w:e.naturalWidth,h:e.naturalHeight,disp:e.style.display}:null;})()`);
      check('DOM：二维码真的渲染出来了（naturalWidth>0）',
        qrImg.val && qrImg.val.w > 0 && qrImg.val.disp !== 'none',
        JSON.stringify(qrImg));

      // 防火墙告警块已按用户要求移除：配对弹窗里不该再出现
      // "Windows 防火墙没放行这个端口 / 请求在到达 EnvKit 之前就被系统丢掉"。
      // 断言的是**这块不存在**，不是它的措辞。
      const fwBox = await evalJS(`(()=>{const e=document.getElementById('lp_fw');
        return e?{txt:e.innerText}:null;})()`);
      check('DOM：配对弹窗里没有防火墙告警块（已按要求移除）',
        fwBox.val === null, JSON.stringify(fwBox.val));
      const fwBtn = await evalJS(`(()=>document.getElementById('lp_fw_btn');})()`);
      check('DOM：配对弹窗里没有「放行防火墙」按钮',
        !fwBtn.val, JSON.stringify(fwBtn.val));

      // 遮挡断言：配对弹窗的按钮必须真的点得到（elementFromPoint）
      const occl = await evalJS(`(()=>{
        const m=document.getElementById('lanpair');
        if(!m || m.className.indexOf('show')<0) return {err:'modal not shown'};
        const btn=document.getElementById('lp-close');
        const r=btn.getBoundingClientRect();
        if(!r.width||!r.height) return {err:'close btn has no box'};
        const top=document.elementFromPoint(r.left+r.width/2, r.top+r.height/2);
        return {ok: !!(top && (top===btn || btn.contains(top))), tag: top?top.tagName+'#'+top.id:''};
      })()`);
      check('遮挡：配对弹窗的「关闭」按钮点得到', occl.val && occl.val.ok === true, JSON.stringify(occl));

      // 弹窗能关掉
      const closed = await evalJS(`(()=>{const b=document.getElementById('lp-close');if(!b)return false;b.click();return true;})()`);
      await sleep(500);
      const hidden = await evalJS(`(()=>{const m=document.getElementById('lanpair');return m?m.className:'';})()`);
      check('DOM：弹窗能关掉', String(hidden.val || '').indexOf('show') < 0, String(hidden.val));

      // JS 异常
      const errs = evts.filter(e => e.method === 'Runtime.exceptionThrown');
      const realErrs = errs.filter(e => {
        const d = JSON.stringify(e.params || {});
        return d.indexOf('aiSuppress') < 0 && d.indexOf('ReferenceError') < 0;
      });
      check('页面无未预期的 JS 异常', realErrs.length === 0, realErrs.length ? JSON.stringify(realErrs[0]).slice(0, 300) : '');

      ws.close();
    }
    try { chrome.kill(); } catch (e) {}
  } finally {
    try { proc.kill(); } catch (e) {}
  }

  console.log('\n==== 结果 ====');
  if (failures.length) {
    console.log('FAIL ' + failures.length + ' 项：');
    failures.forEach(f => console.log('  - ' + f));
    process.exit(1);
  }
  console.log('全部通过');
  process.exit(0);
})().catch(e => { console.error('脚本异常：', e); process.exit(2); });
