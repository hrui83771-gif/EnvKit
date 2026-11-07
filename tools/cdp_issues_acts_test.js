// CDP 实测：问题清单的可点动作 + 配置改动后的「待复检」 + 个人中心的新手指引
//
// 为什么要有这个脚本：这三件事都是"看得见才叫做完"的交互，
// 静态检查（i18n 缺口 / 语法 / 编译）一条都覆盖不到。
//
// 隔离：把 EnvKit.exe 复制到 .gotmp/cdp-issues/ 下跑，配置从零开始，
// **绝不碰 exe 同目录那份真实 config.json**。跑完整个目录删掉。
//
// 断言（全部走真实鼠标/真实请求，不用 DOM .click() 绕过命中测试）：
//   1  空配置下 #issues-list 里出现了可点按钮
//   2  点「去填写」→ 切到 panel-config 且焦点真的落在 #fe_dir
//   3  改一个设置 → #stale-banner 出现，顶部芯片带 stale
//   4  点「重新检测」→ stale 条消失
//   5  个人中心 #btn-guide 存在，点了能开引导层
const { spawn, spawnSync } = require('child_process');
const http = require('http');
const fs = require('fs');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const ROOT = 'D:\\BCGD\\FarmTrace\\envkit';
const SANDBOX = path.join(ROOT, '.gotmp', 'cdp-issues');
const DBG = 9345;

const results = [];
function ok(name, cond, detail) {
  results.push({ name, pass: !!cond, detail: detail || '' });
  console.log((cond ? 'PASS  ' : 'FAIL  ') + name + (detail ? '  [' + detail + ']' : ''));
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}

(async () => {
  // ---- 0. 沙箱 ----
  fs.mkdirSync(SANDBOX, { recursive: true });
  for (const f of fs.readdirSync(SANDBOX)) {
    try { fs.rmSync(path.join(SANDBOX, f), { recursive: true, force: true }); } catch (e) {}
  }
  fs.copyFileSync(path.join(ROOT, 'EnvKit.exe'), path.join(SANDBOX, 'EnvKit.exe'));

  // 控制台端口默认 18765，被占用会顺延到 port_scan_limit（18815）。
  // 不写死单个端口：沙箱里没有 config.json，正常就是默认值，但别的前提不该让脚本假红。
  // ★ 启动前先记下已被占用的端口：用户自己开着的 EnvKit 常常就挂在 18765，
  //   不排除的话会连到那个旧实例，跑出来的全是旧代码的结果（假绿）。
  const occupied = new Set();
  for (let p = 18765; p <= 18815; p++) {
    try { await getJSON('http://127.0.0.1:' + p + '/api/health'); occupied.add(p); } catch (e) {}
  }
  if (occupied.size) console.log('已占用端口（跳过）:', [...occupied].join(','));

  const envkit = spawn(path.join(SANDBOX, 'EnvKit.exe'), [], { cwd: SANDBOX, stdio: 'ignore' });
  let base = '';
  for (let i = 0; i < 60 && !base; i++) {
    for (let p = 18765; p <= 18815; p++) {
      if (occupied.has(p)) continue;
      try { await getJSON('http://127.0.0.1:' + p + '/api/health'); base = 'http://127.0.0.1:' + p + '/'; break; } catch (e) {}
    }
    if (!base) await sleep(500);
  }
  const ready = !!base;
  console.log('envkit ready:', ready, base);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(2); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--window-size=1440,1000',
    '--remote-debugging-port=' + DBG,
    '--user-data-dir=' + path.join(SANDBOX, '_chrome'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let ws = null;
  let failed = false;
  try {
    let targets = null;
    for (let i = 0; i < 15; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); if (targets && targets.length) break; } catch (e) { await sleep(800); }
    }
    if (!targets) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) {
        const d = r.result.exceptionDetails;
        const msg = (d.exception && (d.exception.description || d.exception.value)) || d.text;
        throw new Error('eval: ' + String(msg).split('\n')[0]);
      }
      return r.result && r.result.result ? r.result.result.value : undefined;
    };
    // 真实鼠标点击（走命中测试，能抓到「元素被盖住点不到」这类问题）
    const realClick = async (selector) => {
      const box = await evalJS(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});
        if(!e) return null; e.scrollIntoView({block:'center'});
        const r=e.getBoundingClientRect(); return {x:r.left+r.width/2, y:r.top+r.height/2, w:r.width, h:r.height};})()`);
      if (!box || box.w < 1 || box.h < 1) return false;
      for (const type of ['mousePressed', 'mouseReleased']) {
        await send('Input.dispatchMouseEvent', { type, x: box.x, y: box.y, button: 'left', clickCount: 1 });
      }
      return true;
    };

    await send('Page.enable', {});
    await send('Runtime.enable', {});
    await send('Page.navigate', { url: base });
    await sleep(3000);

    // 启动期会自己 POST /api/check；等它把 issues 算出来
    for (let i = 0; i < 20; i++) {
      const n = await evalJS(`document.querySelectorAll('#issues-list [data-issue-act]').length`);
      if (n > 0) break;
      await sleep(700);
    }

    // ---- 1. 空配置下问题清单里有可点按钮 ----
    const acts = await evalJS(`(()=>{
      const bs=[...document.querySelectorAll('#issues-list [data-issue-act]')];
      return {count:bs.length, labels:bs.map(b=>b.textContent.trim()).slice(0,8),
              acts:[...new Set(bs.map(b=>b.getAttribute('data-issue-act')))].slice(0,8)};
    })()`);
    ok('1  问题清单出现可点按钮', acts && acts.count > 0, acts ? acts.count + ' 个 ' + JSON.stringify(acts.acts) : 'none');

    // ---- 2. 点「去填写」→ 切面板 + 聚焦 ----
    const focusAct = await evalJS(`(()=>{
      const b=[...document.querySelectorAll('#issues-list [data-issue-act^="focus:"]')][0];
      return b ? b.getAttribute('data-issue-act') : '';
    })()`);
    let focusOK = false, focusDetail = '';
    if (focusAct) {
      const clicked = await realClick(`#issues-list [data-issue-act="${focusAct}"]`);
      await sleep(600);
      const st = await evalJS(`(()=>{
        const active=document.activeElement;
        const panel=document.querySelector('section.panel.show');
        return {activeId: active?active.id:'', panel: panel?panel.id:''};
      })()`);
      const wantId = focusAct.split(':')[1];
      focusOK = clicked && st && st.activeId === wantId && st.panel.indexOf('panel-') === 0;
      focusDetail = focusAct + ' → 焦点=' + (st && st.activeId) + ' 面板=' + (st && st.panel);
    } else {
      focusDetail = '没有 focus: 类动作（配置可能不空）';
    }
    ok('2  点「去填写」切面板并聚焦', focusOK, focusDetail);

    // ---- 3. 改一个设置 → 待复检标记 ----
    // 真输入：先切到「程序配置」，点中库名字段，再用 Input.insertText 打一个字符
    //（走的是真实 input 事件链，不是直接改 value 再手动派发）
    await evalJS("goto('config')");
    await sleep(500);
    const focused = await realClick('#db_name');
    if (focused) await send('Input.insertText', { text: '1' });
    await sleep(2400);   // saveNow 有 800ms 防抖，再加一点让它真写完
    const stale = await evalJS(`(()=>{
      const b=document.getElementById('stale-banner');
      const chipStale=document.querySelectorAll('.chip.stale').length;
      const chipText=(document.querySelector('#chip-env')||{}).textContent||'';
      return {shown: !!b && b.offsetHeight>0, chipStale, chipText};
    })()`);
    ok('3  保存后出现「待复检」', focused && stale && stale.shown && stale.chipStale > 0,
       JSON.stringify(stale));

    // ---- 4. 点「重新检测」→ stale 消失 ----
    let cleared = false, clearDetail = '';
    if (stale && stale.shown) {
      const c = await realClick('#btn-recheck');
      await sleep(1200);
      const after = await evalJS(`(()=>{
        const b=document.getElementById('stale-banner');
        return {shown: !!b && b.offsetHeight>0, chipStale: document.querySelectorAll('.chip.stale').length};
      })()`);
      cleared = c && after && !after.shown && after.chipStale === 0;
      clearDetail = JSON.stringify(after);
    } else {
      clearDetail = 'stale 条没出现，跳过';
    }
    ok('4  点「重新检测」清掉待复检', cleared, clearDetail);

    // ---- 5. 英文模式下，这一轮新加的三处文案不能漏中文 ----
    // 只看本轮新增的：问题正文（i.what / i.action）是后端中文原文，
    // 存量就没做翻译（UX 文档 P2-11「i18n 存量收口」），不在本轮范围内。
    await evalJS("ekSetLang('en', false)");
    await sleep(700);
    const en = await evalJS(`(()=>{
      const btns=[...document.querySelectorAll('#issues-list [data-issue-act]')].map(b=>b.textContent.trim());
      const stale=(document.getElementById('stale-banner')||{}).textContent||'';
      const chips=[...document.querySelectorAll('.chip')].map(c=>c.textContent.trim());
      const txt=[...btns, stale, ...chips].join(' | ');
      return {txt, cjk: /[\\u4e00-\\u9fff]/.test(txt)};
    })()`);
    ok('5  英文模式下新增文案无中文', en && !en.cjk, (en && en.txt || '').slice(0, 140));
    await evalJS("ekSetLang('zh', false)");
    await sleep(500);

    // ---- 6. 个人中心的新手指引 ----
    const guideBtn = await evalJS(`!!document.getElementById('btn-guide')`);
    // 按钮旁边不许再有说明文字（用户明确要求删掉；加回来要能看见红）
    await realClick('#btn-profile');
    await sleep(400);
    const rowNow = await evalJS(`(()=>{const b=document.getElementById('btn-guide'); if(!b) return null;
      const row=b.closest('.pp-row'); return {txt:(row?row.textContent:'').replace(/\\s+/g,''), hints:row?row.querySelectorAll('.hint').length:-1};})()`);
    let guideOpened = false;
    if (guideBtn) {
      await realClick('#btn-guide');
      await sleep(1200);
      guideOpened = await evalJS(`(()=>{const o=document.getElementById('onboard'); return !!o && o.classList.contains('show');})()`);
    }
    ok('6  个人中心能重新打开引导，且只留按钮本身',
       guideBtn && guideOpened && rowNow && rowNow.hints === 0 && rowNow.txt === '新手指引',
       'btn=' + guideBtn + ' opened=' + guideOpened + ' row=' + JSON.stringify(rowNow));

    // ---- 7. 白名单覆盖 + 「问 AI」真的把问题填进输入框 ----
    // 沙箱里触发不到「服务崩溃 / 熔断 / 建库」这些分支，所以直接验解释器：
    // 认识的 kind 都要有 label，不认识的必须返回 null（**不能猜**，否则后端
    // 换成什么字符串前端都会去执行）。
    const kinds = await evalJS(`(()=>{
      const ks=['restart:web','breaker:backend','start:web','ai','ports','createdb','dbtest',
                'deps:web','deps:backend','install','check','goto:chain','focus:fe_dir','evil:rm-rf'];
      return ks.map(k=>{ const i=issueActInfo(k); return k+'='+(i?i.label:'NULL'); });
    })()`);
    const known = (kinds || []).filter(x => !x.endsWith('=NULL'));
    const unknownOK = (kinds || []).indexOf('evil:rm-rf=NULL') >= 0;
    ok('7a 动作白名单全部可解释、未知动作拒绝', known.length === 13 && unknownOK, (kinds || []).join(' '));

    await evalJS(`askAIAboutIssue({what:'前端依赖未安装（node_modules 缺失）', action:'先执行 npm install'})`);
    await sleep(500);
    const aiVal = await evalJS(`(()=>{
      const b=document.getElementById('ai-in');
      const d=document.getElementById('ai-drawer');
      return {v: b?b.value:'', open: !!d && d.classList.contains('open'), focused: document.activeElement===b};
    })()`);
    const aiOK = aiVal && aiVal.v.indexOf('前端依赖未安装') >= 0 && aiVal.v.indexOf('npm install') >= 0 && aiVal.open;
    ok('7b 「问 AI」把问题与建议填进输入框（不自动发送）', aiOK, JSON.stringify(aiVal).slice(0, 160));

  } catch (e) {
    console.log('ERROR', e && e.message ? e.message : String(e));
    failed = true;
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    // 只杀沙箱这个进程树：/T 连带它的子进程，绝不按映像名通杀
    //（按名字 taskkill 会顺手杀掉用户正在用的那个 EnvKit）
    if (envkit && envkit.pid) {
      spawnSync('taskkill', ['/F', '/T', '/PID', String(envkit.pid)], { stdio: 'ignore' });
    }
    await sleep(600);
    try { fs.rmSync(SANDBOX, { recursive: true, force: true }); } catch (e) {}
  }

  const bad = results.filter(r => !r.pass);
  console.log('\n== ' + (results.length - bad.length) + '/' + results.length + ' passed ==');
  if (bad.length || failed) process.exit(1);
})();
