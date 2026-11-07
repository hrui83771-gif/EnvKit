// 全链路体检的浏览器门禁：真实鼠标 + 真实请求字节 + 独立沙箱。
//
// 断言落在出问题的那一层：不只看「页面上有结论」，还要抓到前端真的发了
// POST 启动、真的轮询 GET、导出的 Markdown 真的含结论与只读声明。
//
// 沙箱：复制 exe 到 .gotmp/cdp-hc/ 从零配置，不碰真实 config.json。
// 端口：启动前先记已占用端口并跳过（用户自己开着的实例常在 18765）。
const { spawn, spawnSync } = require('child_process');
const http = require('http');
const fs = require('fs');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const ROOT = 'D:\\BCGD\\FarmTrace\\envkit';
const SANDBOX = path.join(ROOT, '.gotmp', 'cdp-hc');
const DBG = 9351;
const sleep = ms => new Promise(r => setTimeout(r, ms));
const getJSON = url => new Promise((res, rej) => {
  http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
});
const getText = url => new Promise((res, rej) => {
  http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => res({ code: r.statusCode, body: d })); }).on('error', rej);
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

  const exe = spawn(path.join(SANDBOX, 'EnvKit.exe'), [], { cwd: SANDBOX, stdio: 'ignore' });
  let base = '';
  for (let i = 0; i < 60 && !base; i++) {
    for (let p = 18765; p <= 18815; p++) {
      if (occupied.has(p)) continue;
      try { await getJSON('http://127.0.0.1:' + p + '/api/health'); base = 'http://127.0.0.1:' + p + '/'; break; } catch (e) {}
    }
    if (!base) await sleep(500);
  }
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
    let mid = 0; const pending = new Map(); const reqs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.method === 'Network.requestWillBeSent') {
        const r = m.params.request;
        reqs.push({ url: r.url, method: r.method, headers: r.headers || {} });
      }
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

    await send('Page.enable');
    await send('Network.enable');
    await send('Page.navigate', { url: base });
    await sleep(3600);
    // Node 侧直接读后端要用令牌（/api/* 全走 authMW）；页面自己 fetch 由 20-shell 自动带
    const TOKEN = await ev(`window.__EK_TOKEN__`);
    // 首次引导会盖住主界面，关掉（只动它自己的 class，不改业务状态）
    await ev(`(()=>{const o=document.getElementById('onboard'); if(o) o.classList.remove('show'); return 1;})()`);
    await sleep(300);

    // ---- 1. 真实鼠标点按钮，面板打开 ----
    const clicked = await click('#btn-healthcheck');
    await sleep(500);
    const opened = await ev(`(()=>{const m=document.getElementById('hc'); return !!m && m.classList.contains('show');})()`);
    ok('1  真实点击后面板打开', clicked && opened, 'clicked=' + clicked + ' opened=' + opened);

    // ---- 2. 抓真实请求字节：POST 启动 + 带令牌 ----
    await sleep(600);
    const posts = reqs.filter(r => /\/api\/healthcheck$/.test(r.url) && r.method === 'POST');
    const withToken = posts.some(r => Object.keys(r.headers).some(k => k.toLowerCase() === 'x-envkit-token'));
    ok('2  真实发出 POST /api/healthcheck 且带令牌', posts.length === 1 && withToken,
       'posts=' + posts.length + ' token=' + withToken);

    // ---- 3. 等报告跑完（最多 120 秒）----
    let rep = null;
    for (let i = 0; i < 120; i++) {
      try { rep = await getJSON(base + 'api/healthcheck?t=' + encodeURIComponent(TOKEN)); } catch (e) {}
      if (rep && rep.status === 'done') break;
      await sleep(1000);
    }
    ok('3  后端报告跑到 done', !!(rep && rep.status === 'done'),
       'status=' + (rep && rep.status) + ' done=' + (rep && rep.done));

    // ---- 4. 前端真的轮询，且渲染出结论 ----
    // 后端 done ≠ 界面立刻画好：前端会按真实阶段留痕逐条回放（总补时 ≤2.6s），
    // 结论卡要等回放播完才出现。用显式观测点等，不睡固定毫秒。
    let shown = {visible:false, text:''};
    for (let i = 0; i < 48; i++) {
      shown = await ev(`(()=>{const v=document.getElementById('hc-verdict');
        return {visible: !!v && v.offsetHeight>0, text: v ? v.textContent.trim() : ''};})()`);
      const ready = await ev(`!!document.querySelector('#hc-items .hc-group')`);
      if (shown.visible && shown.text && ready) break;
      await sleep(250);
    }
    const gets = reqs.filter(r => /\/api\/healthcheck\?/.test(r.url) || (/\/api\/healthcheck$/.test(r.url) && r.method === 'GET'));
    ok('4  前端轮询并把结论显示出来', gets.length >= 2 && shown.visible &&
       (rep.deliverable ? /可以交付/.test(shown.text) : /不能交付/.test(shown.text)),
       'gets=' + gets.length + ' verdict=' + shown.text.slice(0, 60));

    // ---- 5. 22 项、计数自洽、跳过必须带原因 ----
    const n = (rep.items || []).length;
    const cnt = rep.pass + rep.fail + rep.skip;
    const skips = (rep.items || []).filter(i => i.status === 'skip');
    const noReason = skips.filter(i => !(i.detail || '').trim());
    ok('5  22 项且计数自洽、跳过项都有原因',
       n === 22 && cnt === 22 && noReason.length === 0,
       'items=' + n + ' pass=' + rep.pass + ' fail=' + rep.fail + ' skip=' + rep.skip + ' noReason=' + noReason.length);

    // ---- 6. 面板里失败项有「去处理」按钮，证据区免翻译 ----
    const dom = await ev(`(()=>{
      const box=document.getElementById('hc-items');
      return {acts: box.querySelectorAll('[data-hc-act]').length,
              noI18n: box.hasAttribute('data-no-i18n'),
              skipBadges: box.querySelectorAll('.hc-badge.skip').length,
              groups: box.querySelectorAll('.hc-group').length};
    })()`);
    ok('6  失败项有动作按钮 / 跳过徽标 / 分段', dom.acts > 0 && dom.skipBadges > 0 &&
       dom.groups >= 5 && dom.noI18n, JSON.stringify(dom));

    // ---- 7. 真实点击一个动作按钮：弹窗收起且焦点落到目标输入框 ----
    const actInfo = await ev(`(()=>{const b=document.querySelector('[data-hc-act="focus:fe_dir"]');
      return b ? b.getAttribute('data-hc-act') : (document.querySelector('[data-hc-act]')||{}).getAttribute?.('data-hc-act') || '';})()`);
    let acted = false, focusId = '';
    if (actInfo) {
      await click('[data-hc-act="' + actInfo + '"]');
      await sleep(700);
      const st = await ev(`(()=>({closed: !document.getElementById('hc').classList.contains('show'),
        focus: (document.activeElement||{}).id || '', panel: (document.querySelector('.panel.show')||{}).id || ''}))()`);
      acted = st.closed;
      focusId = st.focus + '/' + st.panel;
    }
    ok('7  点动作按钮后面板收起并跳到对应控件', acted, 'act=' + actInfo + ' ' + focusId);

    // ---- 8. 首页留一句结论，不是跑完就消失 ----
    const summary = await ev(`(document.getElementById('hc-summary')||{}).textContent || ''`);
    ok('8  首页保留最近一次结论', /项通过/.test(summary) && /\d{2}:\d{2}:\d{2}/.test(summary), summary.slice(0, 70));

    // ---- 9. 导出是真实 HTTP 200 + 结论在最上面 + 写明只读 ----
    const exp = await getText(base + 'api/healthcheck/export?t=' +
      encodeURIComponent(await ev(`window.__EK_TOKEN__`)));
    const md = exp.body || '';
    ok('9  导出 Markdown 含结论与只读声明',
       exp.code === 200 && /只读/.test(md) && (/可以交付/.test(md) || /不能交付/.test(md)),
       'code=' + exp.code + ' len=' + md.length + ' head=' + md.slice(0, 60).replace(/\n/g, ' '));

    // ---- 10. 英文模式：界面标签无中文（证据原文是后端中文，属存量）----
    await ev(`ekSetLang('en', false)`);
    await sleep(700);
    const en = await ev(`(()=>{const b=document.getElementById('hc-items');
      const txt=[...b.querySelectorAll('.hc-group,.hc-badge')].map(e=>e.textContent).join(' | ');
      const foot=[...document.querySelectorAll('#hc .mfoot button')].map(e=>e.textContent).join(' | ');
      return {txt, foot};})()`);
    ok('10 英文模式标签无中文残留', !/[\u4e00-\u9fff]/.test(en.txt + '|' + en.foot),
       (en.txt + ' | ' + en.foot).slice(0, 110));
    await ev(`ekSetLang('zh', false)`);

    // 截图：打开已有报告（不要点按钮——那会启动新一轮体检，截到 0/22 的进度态）
    await ev(`(()=>{window.scrollTo(0,0); if(document.activeElement) document.activeElement.blur();
      hcOpen(); return 1;})()`);
    await sleep(500);
    const domState = await ev(`(()=>{const v=document.getElementById('hc-verdict'), p=document.getElementById('hc-prog'), b=document.getElementById('hc-body');
      return {vDisplay:getComputedStyle(v).display, vHeight:v.offsetHeight, vText:(v.textContent||'').slice(0,40),
              pDisplay:getComputedStyle(p).display, scrollTop:b.scrollTop, last:(typeof hcLast!=='undefined'&&hcLast)?hcLast.status:'n/a'};})()`);
    console.log('verdict state:', JSON.stringify(domState));
    const shot = await send('Page.captureScreenshot', { format: 'png', clip: { x: 0, y: 0, width: 1280, height: 900, scale: 2 } });
    fs.writeFileSync(path.join(ROOT, 'docs', 'shot-healthcheck.png'), Buffer.from(shot.result.data, 'base64'));
    console.log('wrote docs/shot-healthcheck.png');
  } catch (e) {
    console.log('ERROR', e.stack || e.message || e);
    fail++;
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    if (exe.pid) spawnSync('taskkill', ['/F', '/T', '/PID', String(exe.pid)], { stdio: 'ignore' });
    await sleep(700);
    try { fs.rmSync(SANDBOX, { recursive: true, force: true }); } catch (e) {}
  }
  console.log('\n== ' + pass + '/' + (pass + fail) + ' passed ==');
  if (fail) process.exitCode = 1;
})();
