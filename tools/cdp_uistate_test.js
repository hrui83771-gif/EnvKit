// v3.9 UI 状态门禁：结论跨刷新/重启、ESC 统一、配置备份恢复、进度回放。
//
// 断言落在出问题的那一层：
//   · 「待复检」不是看后端字段，是真的刷新页面后界面还挂着；
//   · 报告留存不是看磁盘文件，是真的重启进程后 GET 还回 done 且自报 restored；
//   · ESC 用真键盘事件，不是 DOM .click()；
//   · 恢复配置的 428 由服务端强制，前端点不点都绕不过；
//   · 进度放慢用 MutationObserver 记录真实文本变化与时间间隔，不靠肉眼看截图。
//
// 沙箱：复制 exe 到 .gotmp/cdp-ui/ 从零配置，不碰真实 config.json / state.json。
// 端口：启动前先记已占用端口并跳过（用户自己开着的实例常在 18765）。
const { spawn, spawnSync } = require('child_process');
const http = require('http');
const fs = require('fs');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const ROOT = 'D:\\BCGD\\FarmTrace\\envkit';
const SANDBOX = path.join(ROOT, '.gotmp', 'cdp-ui');
const DBG = 9352;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const getJSON = url => new Promise((res, rej) => {
  http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
});
const postJSON = (url, body, token) => new Promise((res, rej) => {
  const u = new URL(url);
  const data = JSON.stringify(body || {});
  const req = http.request({ hostname: u.hostname, port: u.port, path: u.pathname + (u.search || ''),
    method: 'POST', headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data),
      'X-EnvKit-Token': token || '' } },
    r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { let j = {}; try { j = JSON.parse(d); } catch (e) {} res({ code: r.statusCode, body: d, json: j }); }); });
  req.on('error', rej); req.write(data); req.end();
});

let pass = 0, fail = 0;
const ok = (name, cond, detail) => {
  if (cond) { pass++; console.log('PASS  ' + name + (detail ? '  [' + detail + ']' : '')); }
  else { fail++; console.log('FAIL  ' + name + '  [' + (detail || '') + ']'); }
};

(async () => {
  fs.mkdirSync(SANDBOX, { recursive: true });
  for (const f of fs.readdirSync(SANDBOX)) { try { fs.rmSync(path.join(SANDBOX, f), { recursive: true, force: true }); } catch (e) {} }
  fs.copyFileSync(path.join(ROOT, 'EnvKit.exe'), path.join(SANDBOX, 'EnvKit.exe'));

  const occupied = new Set();
  for (let p = 18765; p <= 18815; p++) {
    try { await getJSON('http://127.0.0.1:' + p + '/api/health'); occupied.add(p); } catch (e) {}
  }
  if (occupied.size) console.log('已占用端口（跳过）:', [...occupied].join(','));

  let exe = spawn(path.join(SANDBOX, 'EnvKit.exe'), [], { cwd: SANDBOX, stdio: 'ignore' });
  const boot = async () => {
    for (let i = 0; i < 60; i++) {
      for (let p = 18765; p <= 18815; p++) {
        if (occupied.has(p)) continue;
        try { await getJSON('http://127.0.0.1:' + p + '/api/health'); return 'http://127.0.0.1:' + p + '/'; } catch (e) {}
      }
      await sleep(500);
    }
    return '';
  };
  let base = await boot();
  if (!base) { console.log('envkit not ready'); try { exe.kill(); } catch (e) {} process.exit(2); }

  const chrome = spawn(CHROME, ['--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--window-size=1280,900', '--force-device-scale-factor=2',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(SANDBOX, '_chrome'), '--no-first-run', 'about:blank'], { stdio: 'ignore' });
  await sleep(2500);

  let ws = null;
  try {
    const targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list');
    const page = targets.find(t => t.type === 'page');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (m, p) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method: m, params: p })); });
    const ev = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) throw new Error('eval: ' + JSON.stringify(r.result.exceptionDetails.text));
      return r.result.result.value;
    };
    const click = async sel => {
      const box = await ev(`(()=>{const e=document.querySelector(${JSON.stringify(sel)}); if(!e) return null;
        e.scrollIntoView({block:'center'}); const r=e.getBoundingClientRect();
        return {x:r.left+r.width/2, y:r.top+r.height/2};})()`);
      if (!box) return false;
      for (const type of ['mousePressed', 'mouseReleased'])
        await send('Input.dispatchMouseEvent', { type, x: box.x, y: box.y, button: 'left', clickCount: 1 });
      return true;
    };
    const escape = async () => {
      for (const type of ['keyDown', 'keyUp'])
        await send('Input.dispatchKeyEvent', { type, key: 'Escape', code: 'Escape',
          windowsVirtualKeyCode: 27, nativeVirtualKeyCode: 27 });
      await sleep(250);
    };
    const navigate = async () => {
      await send('Page.navigate', { url: base });
      for (let i = 0; i < 60; i++) {
        const t = await ev(`(document.readyState==='complete' && window.__EK_TOKEN__) ? window.__EK_TOKEN__ : ''`).catch(() => '');
        if (t) return t;
        await sleep(200);
      }
      return '';
    };
    const uiState = async token => getJSON(base + 'api/uistate?t=' + encodeURIComponent(token));
    const hcState = async token => getJSON(base + 'api/healthcheck?t=' + encodeURIComponent(token));

    await send('Page.enable');
    let TOKEN = await navigate();
    await sleep(1800);
    await ev(`(()=>{const o=document.getElementById('onboard'); if(o) o.classList.remove('show'); return 1;})()`);
    ok('0  页面与令牌就绪', !!TOKEN, 'token=' + (TOKEN ? TOKEN.slice(0, 6) + '…' : 'missing'));

    // ---- 1. uistate 只读端点结构 ----
    const u0 = await uiState(TOKEN);
    ok('1  /api/uistate 暴露待复检与历史', u0 && typeof u0.cfg_stale === 'boolean' && Array.isArray(u0.hc_history),
       'stale=' + (u0 && u0.cfg_stale) + ' hist=' + (u0 && u0.hc_history && u0.hc_history.length));

    // ---- 2. 真输入改配置 → 前后端都标「待复检」----
    await ev(`goto('config')`);
    await sleep(400);
    const typed = await click('#db_name');
    if (typed) await send('Input.insertText', { text: '9' });
    await sleep(2600);   // saveNow 800ms 防抖 + 落盘
    const u1 = await uiState(TOKEN);
    const banner1 = await ev(`(()=>{const b=document.getElementById('stale-banner'); return !!b && b.offsetHeight>0;})()`);
    ok('2  改配置后后端落盘 stale 且横幅可见', typed && !!(u1 && u1.cfg_stale) && banner1,
       'backend=' + (u1 && u1.cfg_stale) + ' banner=' + banner1);

    // 第二次保存：沙箱从零配置，第一次只是创建正本，没有旧正本可轮转，
    // 不会生成 .bak。备份列表要用第二次保存才有东西可列。
    await ev(`goto('config')`);
    await sleep(300);
    await click('#db_name');
    await send('Input.insertText', { text: '8' });
    await sleep(2600);

    // ---- 3. 刷新页面：这是本轮修的核心（原来 cfgStale 是前端 let，一刷就没）----
    TOKEN = await navigate();
    await sleep(2000);
    await ev(`(()=>{const o=document.getElementById('onboard'); if(o) o.classList.remove('show'); return 1;})()`);
    const banner2 = await ev(`(()=>{const b=document.getElementById('stale-banner');
      return {shown: !!b && b.offsetHeight>0, chips: document.querySelectorAll('.chip.stale').length};})()`);
    const u2 = await uiState(TOKEN);
    ok('3  刷新页面后「待复检」仍在', !!(u2 && u2.cfg_stale) && banner2.shown && banner2.chips > 0,
       JSON.stringify(banner2) + ' backend=' + (u2 && u2.cfg_stale));

    // ---- 4. ESC 真键盘关掉三种弹窗（原来只关 about/confirm/browser）----
    const escRes = [];
    for (const id of ['about', 'syslog', 'hc']) {
      await ev(`document.getElementById(${JSON.stringify(id)}).classList.add('show')`);
      const before = await ev(`document.getElementById(${JSON.stringify(id)}).classList.contains('show')`);
      await escape();
      const after = await ev(`document.getElementById(${JSON.stringify(id)}).classList.contains('show')`);
      escRes.push(id + '=' + (before && !after));
    }
    ok('4  ESC 真键盘关掉 about / syslog / hc', escRes.every(x => x.endsWith('=true')), escRes.join(' '));

    // ---- 5. 备份列表 + 服务端强制 428 + 槽位白名单 ----
    const baks = await getJSON(base + 'api/config/backups?t=' + encodeURIComponent(TOKEN));
    const list = (baks && baks.backups) || [];
    const noConfirm = await postJSON(base + 'api/config/restore', { slot: 1 }, TOKEN);
    const badSlot = await postJSON(base + 'api/config/restore', { slot: 0, confirm: true }, TOKEN);
    const evilPath = await postJSON(base + 'api/config/restore',
      { slot: 1, path: 'C:/Windows/win.ini' }, TOKEN);
    ok('5  备份列表 + 428 闸门 + 非法槽位拒绝',
       list.length >= 1 && list.every(b => [1, 2, 3].includes(b.slot)) &&
       noConfirm.code === 428 && noConfirm.json.needConfirm === true && badSlot.code === 400 &&
       evilPath.code === 428 && !/win\.ini/.test(evilPath.body),
       'slots=' + list.map(b => b.slot).join(',') + ' 428=' + noConfirm.code +
       ' bad=' + badSlot.code + ' evil=' + evilPath.code);

    // ---- 6. 确认后真恢复：配置回到备份值，并重新标待复检 ----
    const beforeVal = await ev(`document.getElementById('db_name').value`);
    const restored = await ev(`(async()=>{const r=await fetch('/api/config/restore',{method:'POST',
      headers:{'Content-Type':'application/json','X-EnvKit-Token':window.__EK_TOKEN__},
      body:JSON.stringify({slot:1,confirm:true})}); return {code:r.status, body:await r.text()};})()`);
    const afterCfg = await ev(`(async()=>{const r=await fetch('/api/config',{headers:{'X-EnvKit-Token':window.__EK_TOKEN__}});
      const j=await r.json(); return (j.projects||{}).db_name||'';})().catch(()=>'')`);
    const u3 = await uiState(TOKEN);
    ok('6  带 confirm 真恢复且重新标待复检', restored.code === 200 && /"ok":true/.test(restored.body) &&
       !!(u3 && u3.cfg_stale), 'code=' + restored.code + ' stale=' + (u3 && u3.cfg_stale) +
       ' before=' + beforeVal + ' after=' + afterCfg);

    // ---- 7. 体检：真实阶段留痕 + 前端回放不是一闪而过 ----
    TOKEN = await navigate();
    await sleep(1600);
    await ev(`(()=>{const o=document.getElementById('onboard'); if(o) o.classList.remove('show');
      window.__hcSeen=[]; const t=document.getElementById('hc-prog-text');
      if(t) new MutationObserver(()=>window.__hcSeen.push({s:t.textContent,at:Date.now()}))
        .observe(t,{childList:true,subtree:true,characterData:true}); return 1;})()`);
    await click('#btn-healthcheck');
    let rep = null;
    for (let i = 0; i < 150; i++) {
      try { rep = await hcState(TOKEN); } catch (e) {}
      if (rep && rep.status === 'done') break;
      await sleep(700);
    }
    // 等回放队列播完（结论卡出现）
    for (let i = 0; i < 48; i++) {
      const ready = await ev(`(()=>{const v=document.getElementById('hc-verdict');
        return !!v && v.offsetHeight>0 && !!document.querySelector('#hc-items .hc-group');})()`);
      if (ready) break;
      await sleep(250);
    }
    const seen = await ev(`window.__hcSeen || []`);
    const gaps = [];
    for (let i = 1; i < seen.length; i++) gaps.push(seen[i].at - seen[i - 1].at);
    const slowGaps = gaps.filter(g => g >= 200).length;
    // ★ 判据必须落在用户抱怨的那一层：「前面几项太快都看不到」= 一闪而过。
    // 只数 slowGaps 不够：后端真实步骤间隔本来就慢，把前端最小驻留删成 0
    // 后 slowGaps 仍有 8 个（来自后端 gap），旧断言会假绿。真正能区分的是
    // 快间隔（<200ms）的数量——最小驻留归零后瞬间多出 20+ 个。
    const fastGaps = gaps.filter(g => g < 200).length;
    const slowRatio = gaps.length ? slowGaps / gaps.length : 0;
    ok('7  阶段逐条可见（留痕 + 无「一闪而过」）',
       !!(rep && rep.status === 'done') && (rep.steps || []).length >= 10 &&
       seen.length >= 3 && slowGaps >= 2 && fastGaps <= 3 && slowRatio >= 0.6,
       'steps=' + ((rep && rep.steps) || []).length + ' textChanges=' + seen.length +
       ' slowGaps=' + slowGaps + ' fastGaps=' + fastGaps + ' slowRatio=' + slowRatio.toFixed(2));

    // ---- 8. 历史入盘 ----
    const u4 = await uiState(TOKEN);
    ok('8  体检结束写入历史摘要', !!(u4 && u4.hc_history && u4.hc_history.length >= 1) &&
       u4.hc_history[0].fail === rep.fail,
       'hist=' + ((u4 && u4.hc_history) || []).length + ' fail=' + (u4 && u4.hc_history[0] && u4.hc_history[0].fail));

    // ---- 9. 重启进程：报告还在，且自报「上次运行留下的」----
    try { spawnSync('taskkill', ['/F', '/T', '/PID', String(exe.pid)], { stdio: 'ignore' }); } catch (e) {}
    await sleep(1200);
    exe = spawn(path.join(SANDBOX, 'EnvKit.exe'), [], { cwd: SANDBOX, stdio: 'ignore' });
    // 旧端口可能处在 TIME_WAIT，新实例会顺延一位；重新发现，别写死端口
    base = (await boot()) || base;
    let alive = false;
    for (let i = 0; i < 60 && !alive; i++) {
      try { const h = await getJSON(base + 'api/health'); alive = !!h; } catch (e) { await sleep(400); }
    }
    TOKEN = await navigate();
    await sleep(1800);
    const rep2 = await hcState(TOKEN);
    const restoredTxt = await ev(`(()=>{const e=document.querySelector('.hc-restored'); return e?e.textContent:'';})()`);
    ok('9  重启后报告仍在且标明是上次运行留下的',
       alive && rep2 && rep2.status === 'done' && rep2.restored === true &&
       /上次运行留下的报告/.test(restoredTxt),
       'alive=' + alive + ' status=' + (rep2 && rep2.status) + ' restored=' + (rep2 && rep2.restored) +
       ' tip=' + (restoredTxt || '').slice(0, 30));

    // ---- 10. 英文模式下新文案无中文残留 ----
    await ev(`(()=>{ if(typeof ekSetLang==='function') ekSetLang('en'); return 1;})()`);
    // 备份卡片只在设置页加载时拉取；重启后停在首页，先显式加载一次，
    // 否则下面会拿一个空容器做"无中文"的假绿（正对照是 .bk-row 数量）
    await ev(`(async()=>{ if(typeof ekSetLoadBackups==='function') await ekSetLoadBackups(); return 1;})()`);
    await sleep(700);
    // 只查本轮新增的界面标签。证据区 #hc-items 是后端拼好的中文原文、
    // 明确标了 data-no-i18n，属于已知存量（上一轮已写明本轮不做）。
    const i18n = await ev(`(()=>{
      const sels=['#cfg_backups','.hc-hist','.hc-restored','#hc-summary'];
      const txt=sels.map(s=>{const e=document.querySelector(s);return e?e.textContent:'';}).join(' ');
      return {zh:(txt.match(/[\\u4e00-\\u9fa5]/g)||[]).length,
              cnt:document.querySelectorAll('.bk-row').length, len:txt.trim().length};
    })()`);
    ok('10 英文模式新增文案无中文残留', i18n.zh === 0 && i18n.cnt >= 1 && i18n.len > 0,
       JSON.stringify(i18n));

  } catch (e) {
    console.log('ERROR ' + (e && e.stack || e));
    fail++;
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { spawnSync('taskkill', ['/F', '/T', '/PID', String(exe.pid)], { stdio: 'ignore' }); } catch (e) {}
    await sleep(600);
    try { fs.rmSync(SANDBOX, { recursive: true, force: true }); } catch (e) {}
  }
  console.log('\n== ' + pass + '/' + (pass + fail) + ' passed ==');
  process.exit(fail ? 1 : 0);
})();
