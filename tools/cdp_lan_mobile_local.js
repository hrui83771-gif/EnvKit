// CDP（手机视口）：手机页三块新卡片 + 终端全屏层，**按键真的点得到**。
//
// 为什么单独写这一个：接口层面的用例（cdp_lan_local_test.js）全绿，
// 也完全可能在手机上"点了没反应" —— 这类问题只出在 DOM/层级/命中测试上。
// 本项目踩过：CDP 的 element.click() 会绕过命中测试，
// 所以这里一律用 elementFromPoint(中心点) 再点，
// 点不到就说明真有东西压在上面（那正是"看起来没反应"的成因）。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const LAN_PORT = 18797;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN = 'http://127.0.0.1:' + LAN_PORT;
const DBG = 9347;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2', 'EnvKit_local.exe');
const DIR = path.dirname(EXE);

let TOKEN = '';
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
async function getJSON(url, headers) {
  const r = await req('GET', url, null, headers);
  try { return JSON.parse(r.body.toString('utf8')); } catch (e) { return r.body.toString('utf8'); }
}
async function saveLan(patch) {
  const r = await req('POST', URL_ + 'api/lan/config', JSON.stringify(patch), authH());
  try { return JSON.parse(r.body.toString('utf8')); } catch (e) { return null; }
}

(async function main() {
  if (!fs.existsSync(EXE)) {
    console.log('找不到测试 exe：' + EXE);
    process.exit(2);
  }
  const seedPath = path.join(DIR, 'config.json');
  let base = {};
  try { base = JSON.parse(fs.readFileSync(seedPath, 'utf8')); } catch (e) { base = {}; }
  base.lan = { enabled: true, port: LAN_PORT, devices: [] };
  base.dev = Object.assign({}, base.dev || {}, { server_port: PORT });
  fs.writeFileSync(seedPath, JSON.stringify(base, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdplmb' }),
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

  let chrome = null;
  try {
    const cfg = await getJSON(URL_ + 'api/lan/config', authH());
    const pair = await req('POST', LAN + '/api/lan/pair', JSON.stringify({ code: cfg.pair_code }), { Host: '127.0.0.1:' + LAN_PORT });
    const cookieRaw = ((pair.headers['set-cookie'] || [])[0] || '');
    const lanTok = cookieRaw.split(';')[0].split('=')[1] || '';
    check('配对拿到设备令牌', !!lanTok, cookieRaw.slice(0, 40));

    chrome = spawn(CHROME, [
      '--headless=new', '--remote-debugging-port=' + DBG, '--no-sandbox',
      '--disable-gpu', '--window-size=390,844', 'about:blank',
    ], { stdio: ['ignore', 'pipe', 'pipe'] });
    chrome.stdout.on('data', () => {});
    chrome.stderr.on('data', () => {});

    let wsUrl = '';
    for (let i = 0; i < 30; i++) {
      await sleep(500);
      try {
        const list = JSON.parse(await new Promise((res, rej) => {
          http.get('http://127.0.0.1:' + DBG + '/json/list', resp => {
            let d = ''; resp.on('data', c => d += c); resp.on('end', () => res(d));
          }).on('error', rej);
        }));
        const pg = list.find(x => x.type === 'page');
        if (pg && pg.webSocketDebuggerUrl) { wsUrl = pg.webSocketDebuggerUrl; break; }
      } catch (e) { /* 还没起 */ }
    }
    if (!wsUrl) { check('CDP 连上 Chrome', false, '拿不到 wsUrl'); throw new Error('no cdp'); }
    check('CDP 连上 Chrome', true);

    const ws = new global.WebSocket(wsUrl);
    let id = 0;
    const pend = new Map();
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
      if (r.result && r.result.exceptionDetails) return { err: JSON.stringify(r.result.exceptionDetails).slice(0, 300) };
      return { val: r.result && r.result.result ? r.result.result.value : undefined };
    };
    // **按命中测试点**：elementFromPoint 拿到的元素才是用户真能点到的那个
    const clickAt = (sel) => evalJS(`(async()=>{
      const e = document.querySelector(${JSON.stringify(sel)});
      if(!e) return 'no-el';
      e.scrollIntoView({block:'center', inline:'center'});
      await new Promise(r=>requestAnimationFrame(()=>requestAnimationFrame(r)));
      const r = e.getBoundingClientRect();
      if(!r.width || !r.height) return 'zero-size';
      const x = r.left + r.width/2, y = r.top + r.height/2;
      const top = document.elementFromPoint(x, y);
      if(!top) return 'nothing-at-point(' + Math.round(x) + ',' + Math.round(y) + ')';
      if(top !== e && !e.contains(top)) return 'covered-by:' + (top.id || top.className || top.nodeName);
      e.click();
      return 'ok';
    })()`);

    await send('Page.enable');
    await send('Runtime.enable');
    await send('Network.enable');
    await send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
    await send('Network.setCookie', { name: 'ek_lan', value: lanTok, domain: '127.0.0.1', path: '/' });
    await send('Page.navigate', { url: LAN + '/' });
    await sleep(3000);

    const inApp = await evalJS(`!document.getElementById('app').classList.contains('hidden')`);
    check('手机页进了主界面（cookie 被认）', inApp.val === true, JSON.stringify(inApp));

    // ---------- 屏幕卡片（默认开） ----------
    console.log('\n[屏幕]');
    const shotVisible = await evalJS(`!document.getElementById('shotCard').classList.contains('hidden')`);
    check('默认就显示「电脑屏幕」卡片', shotVisible.val === true, JSON.stringify(shotVisible));
    const c1 = await clickAt('#shotBtn');
    check('「刷新」按钮点得到', c1.val === 'ok', c1.val);
    await sleep(2500);
    const imgOk = await evalJS(`(()=>{const i=document.getElementById('shotImg');
      return i ? (i.naturalWidth||0) + 'x' + (i.naturalHeight||0) : 'no-img';})()`);
    check('截图真的解码出来了（naturalWidth > 0）', /^\d+x\d+$/.test(String(imgOk.val)) && parseInt(String(imgOk.val), 10) > 0, String(imgOk.val));

    // ---------- 截图放大（v3.6.2：点一下就能看细节） ----------
    console.log('\n[截图放大]');
    const zHint = await evalJS(`!!document.querySelector('#shotWrap .zoomhint')`);
    check('缩略图上有「点击放大」提示', zHint.val === true, JSON.stringify(zHint));

    const cZoom = await clickAt('#shotImg');
    check('点画面能放大', cZoom.val === 'ok', cZoom.val);
    await sleep(3500);
    const svOn = await evalJS(`!document.getElementById('shotView').classList.contains('hidden')`);
    check('放大层打开了', svOn.val === true, JSON.stringify(svOn));
    /* 放大层必须取**更高分辨率**那张：拿 1280 的缩略图放大等于放大马赛克，
       用户点开放大就是为了看清小字。 */
    const svSize = await evalJS(`(()=>{const i=document.getElementById('svgImg');
      return (i.naturalWidth||0)+'x'+(i.naturalHeight||0);})()`);
    check('放大层用的是高分辨率图（宽 > 1280）', parseInt(String(svSize.val), 10) > 1280, String(svSize.val));

    /* 「保存到手机」：手机上要能把这张图弄进相册。
       桌面 Chrome 没有可用的分享目标，这里把 canShare/share 换成桩，
       **真的走一遍** svSave 的「取像素 → 编码 PNG → 交给系统」路径，
       再用桩拿到的 File 校验 PNG magic 和 IHDR 里的宽高。
       只断言"按钮存在"等于没测：编码写坏了、画布尺寸取错了，那样也全绿。 */
    const stub = await evalJS(`(()=>{
      window.__saved = null;
      try{
        Object.defineProperty(navigator,'canShare',{configurable:true,
          value:function(o){ return !!(o && o.files && o.files.length); }});
        Object.defineProperty(navigator,'share',{configurable:true,
          value:function(o){
            var f = o.files[0];
            return f.slice(0,33).arrayBuffer().then(function(b){
              var u = new Uint8Array(b), hx = '', i;
              for(i = 0; i < 8; i++) hx += ('0' + u[i].toString(16)).slice(-2);
              var dv = new DataView(b);
              window.__saved = {name:f.name, size:f.size, type:f.type, magic:hx,
                w:dv.getUint32(16), h:dv.getUint32(20)};
            });
          }});
      }catch(e){ return 'stub-fail:' + e.message; }
      return 'ok';
    })()`);
    check('分享接口打桩成功', stub.val === 'ok', String(stub.val));

    const cSave = await clickAt('#svgSave');
    check('「保存」点得到（没被裁掉/没被遮住）', cSave.val === 'ok', cSave.val);
    await sleep(2500);
    const saved = await evalJS(`window.__saved`);
    const sv = saved.val || {};
    check('保存出来的是 PNG（magic 89504e470d0a1a0a）', String(sv.magic) === '89504e470d0a1a0a', JSON.stringify(sv));
    check('文件名带时间戳 envkit-shot-YYYYMMDD-HHMMSS.png',
      /^envkit-shot-\d{8}-\d{6}\.png$/.test(String(sv.name)), String(sv.name));
    check('PNG 的 IHDR 宽高 == 放大层那张图（不是空白/错尺寸）',
      String(sv.w) + 'x' + String(sv.h) === String(svSize.val), JSON.stringify(sv) + ' vs ' + svSize.val);
    check('保存的内容不是空文件', Number(sv.size) > 5000, String(sv.size));

    const dbl = await evalJS(`(()=>{
      const im=document.getElementById('svgImg');
      const before=im.style.transform;
      const st=document.getElementById('svgStage');
      st.click(); st.click();
      return before+'|'+im.style.transform;
    })()`);
    const dblParts = String(dbl.val).split('|');
    check('双击切换缩放倍数', dblParts.length === 2 && dblParts[0] !== dblParts[1], String(dbl.val));

    const cFit = await clickAt('#svgFit');
    check('「适应」点得到', cFit.val === 'ok', cFit.val);
    const cSvc = await clickAt('#svgClose');
    check('放大层「关闭」点得到', cSvc.val === 'ok', cSvc.val);
    await sleep(400);
    const svOff = await evalJS(`document.getElementById('shotView').classList.contains('hidden')`);
    check('关闭后放大层收起', svOff.val === true, JSON.stringify(svOff));

    // ---------- 右上角折叠栏（三区） ----------
    console.log('\n[折叠栏]');
    const cOpen = await clickAt('#drawerOpen');
    check('右上角折叠栏按钮点得到', cOpen.val === 'ok', cOpen.val);
    await sleep(500);
    const drawerOn = await evalJS(`document.getElementById('drawer').classList.contains('on')`);
    check('折叠栏打开了', drawerOn.val === true, JSON.stringify(drawerOn));
    const grpN = await evalJS(`document.querySelectorAll('#drawerBody details.grp').length`);
    check('折叠栏分三个区域', grpN.val === 3, String(grpN.val));
    const grpState = await evalJS(`[...document.querySelectorAll('#drawerBody details.grp')].map(d=>d.open).join(',')`);
    check('默认只展开第一个区域（终端 & 剪贴板）', grpState.val === 'true,false,false', String(grpState.val));
    const grpTitles = await evalJS(`[...document.querySelectorAll('#drawerBody details.grp > summary')]
      .map(s=>s.textContent.trim()).join(' / ')`);
    check('三个区域标题对得上', /终端 & 剪贴板.*主操作.*日志报告/.test(String(grpTitles.val)), String(grpTitles.val));

    // ---------- 剪贴板（默认关） ----------
    console.log('\n[剪贴板]');
    const clipHidden = await evalJS(`document.getElementById('clipCard').classList.contains('hidden')`);
    check('默认不显示「剪贴板」卡片（没打开的能力不该出现）', clipHidden.val === true, JSON.stringify(clipHidden));

    await saveLan({ allow_clip: true });
    await sleep(4000); // 等一轮 3 秒轮询
    const clipShown = await evalJS(`!document.getElementById('clipCard').classList.contains('hidden')`);
    check('电脑上打开后，手机跟着出现（轮询生效）', clipShown.val === true, JSON.stringify(clipShown));

    const mark = 'from-phone-' + Date.now();
    await evalJS(`document.getElementById('clipText').value = ${JSON.stringify(mark)}; 'set'`);
    const c2 = await clickAt('#clipPush');
    check('「发到电脑」点得到', c2.val === 'ok', c2.val);
    await sleep(900);
    const remote = await getJSON(LAN + '/api/lan/clip', { Host: '127.0.0.1:' + LAN_PORT, Cookie: 'ek_lan=' + lanTok });
    check('电脑剪贴板里就是手机上发的那段', remote.text === mark, JSON.stringify(remote).slice(0, 140));

    // ---------- 终端（默认关） ----------
    console.log('\n[终端]');
    const termHidden = await evalJS(`document.getElementById('termCard').classList.contains('hidden')`);
    check('默认不显示「电脑终端」卡片', termHidden.val === true, JSON.stringify(termHidden));

    await saveLan({ allow_term: true });
    await sleep(4000);
    const termShown = await evalJS(`!document.getElementById('termCard').classList.contains('hidden')`);
    check('打开后卡片出现', termShown.val === true, JSON.stringify(termShown));
    /* 链端终端入口：没配 SSH 主机时必须**整块隐藏**。
       摆一个点进去必然报错的按钮不算功能，只会让人以为坏了。 */
    const sshHidden = await evalJS(`document.getElementById('sshCard').classList.contains('hidden')`);
    check('未配置链端 SSH 时不显示「链端终端」入口', sshHidden.val === true, JSON.stringify(sshHidden));

    // 抽屉可能被前面的步骤关掉了，点之前先确保它是开的
    await evalJS(`(()=>{const d=document.getElementById('drawer');
      if(!d.classList.contains('on')) document.getElementById('drawerOpen').click(); return 'ok';})()`);
    await sleep(400);
    const c3 = await clickAt('#termOpen');
    check('「打开 PowerShell」点得到', c3.val === 'ok', c3.val);
    await sleep(2600);
    const viewOn = await evalJS(`!document.getElementById('termView').classList.contains('hidden')`);
    check('终端全屏层显示了', viewOn.val === true, JSON.stringify(viewOn));
    const termTitle = await evalJS(`document.getElementById('termTitle').textContent`);
    check('本地终端的标题是「电脑终端」', /电脑终端/.test(String(termTitle.val)), String(termTitle.val));
    /* 打开终端时抽屉应当自动收起：终端要占满整屏，底下留一层抽屉很容易误触 */
    const drawerClosed = await evalJS(`document.getElementById('drawer').classList.contains('on')`);
    check('开终端时抽屉自动收起', drawerClosed.val === false, JSON.stringify(drawerClosed));
    /* xterm 5.x 的渲染后端是可替换的（canvas / DOM / webgl），
       断言不能钉在某一种上 —— 钉错了会在"产品其实好好的"时候变红。
       这里只要求：.xterm 挂上了，且屏幕元素真的有尺寸（尺寸为 0 = 终端根本看不见）。 */
    const xt = await evalJS(`(()=>{const h=document.getElementById('termHost');
      const x=h.querySelector('.xterm');
      if(!x) return 'no-xterm';
      const scr=x.querySelector('.xterm-screen') || x;
      const r=scr.getBoundingClientRect();
      const cv=h.querySelector('canvas');
      return Math.round(r.width)+'x'+Math.round(r.height)+(cv?'/canvas':'/dom');})()`);
    check('xterm 挂上了且屏幕有尺寸', /^\d+x\d+/.test(String(xt.val)) && parseInt(String(xt.val), 10) > 0, String(xt.val));
    // 软键盘上没有的键：按钮必须真的在可视区内、且没被终端层遮住
    const cEsc = await clickAt('.termkeys .btn[data-k="esc"]');
    check('Esc 键点得到（没被遮挡/没被裁掉）', cEsc.val === 'ok', cEsc.val);
    const cClose = await clickAt('#termClose');
    check('「关闭」点得到', cClose.val === 'ok', cClose.val);
    await sleep(500);
    const viewOff = await evalJS(`document.getElementById('termView').classList.contains('hidden')`);
    check('关闭后终端层收起', viewOff.val === true, JSON.stringify(viewOff));

    // 关掉开关后，即便终端层还开着也必须被收掉（不留"电脑上关了、手机上还能敲"的窗口）
    const c4 = await clickAt('#termOpen');
    await sleep(1500);
    await saveLan({ allow_term: false });
    await sleep(4000);
    const forced = await evalJS(`document.getElementById('termView').classList.contains('hidden')`);
    check('电脑上关掉后，手机上的终端层自动收起', forced.val === true, JSON.stringify(forced));

    // ---------- 日志报告（统计 / 晨报 / 实时日志） ----------
    console.log('\n[日志报告]');
    await evalJS(`(()=>{const d=document.getElementById('drawer');
      if(!d.classList.contains('on')) document.getElementById('drawerOpen').click(); return 'ok';})()`);
    await sleep(400);
    await evalJS(`(()=>{const g=document.querySelectorAll('#drawerBody details.grp')[2];
      if(!g.open) g.querySelector('summary').click(); return 'ok';})()`);
    await sleep(300);
    const logGrpOpen = await evalJS(`document.querySelectorAll('#drawerBody details.grp')[2].open`);
    check('「日志报告」区域能展开', logGrpOpen.val === true, JSON.stringify(logGrpOpen));

    const cRep = await clickAt('#repLog');
    check('「实时日志」点得到', cRep.val === 'ok', cRep.val);
    await sleep(1800);
    const logN = await evalJS(`(window.__lastLogs || []).length`);
    check('实时日志拉到了内容', Number(logN.val) > 0, String(logN.val));
    const logPaneOn = await evalJS(`!document.getElementById('paneLog').classList.contains('hidden')`);
    check('日志面板切过去了', logPaneOn.val === true, JSON.stringify(logPaneOn));

    const cStat = await clickAt('#repStat');
    check('「统计」点得到', cStat.val === 'ok', cStat.val);
    await sleep(1800);
    const stOps = await evalJS(`document.getElementById('stOps').textContent`);
    check('统计里的操作数是一个数（不是占位符）', /^\d+$/.test(String(stOps.val)), String(stOps.val));

    const cBrief = await clickAt('#repBrief');
    check('「晨报」点得到', cBrief.val === 'ok', cBrief.val);
    await sleep(2200);
    const briefInfo = await evalJS(`(()=>({
      report: (window.__lastBrief||{}).report || '',
      hasText: !!((window.__lastBrief||{}).hasText),
      htmlLen: document.getElementById('briefBox').innerHTML.length
    }))()`);
    const bi = briefInfo.val || {};
    /* 有报告就该渲染出来；没有报告也要有一句说得清的话 ——
       两者都算通过，"空白一块"不算。 */
    check('晨报区有内容（有报告就渲染，没有就明说）', Number(bi.htmlLen || 0) > 20, JSON.stringify(bi).slice(0, 160));

    await saveLan({ allow_clip: false });
  } catch (e) {
    console.log('异常：' + (e && e.stack || e));
    failures.push('exception');
  } finally {
    try { if (chrome) chrome.kill(); } catch (e) {}
    try { proc.kill(); } catch (e) {}
  }

  console.log('\n==== ' + (failures.length === 0 ? '全部通过' : failures.length + ' 项失败') + ' ====');
  if (failures.length) { failures.forEach(f => console.log('  - ' + f)); process.exit(1); }
})();
