// CDP 回归：清空对话 10 秒撤销 + 开发者配置的因果说明（v3.7.3）
//
// ## 这条测试防的是什么
//
// 用户体感清单 P0-1 / P0-3：开发者 tab 是一排裸参数、用户不敢动；
// 「清空」点一下就抹掉整段排障过程且不可撤销。这轮给 5 组参数补了因果说明，
// 并给清空加了 10 秒撤销窗口（快照存 DOM 节点，不重放 aiMessages）。
//
// **判据必须落在真正出问题的那一层**：
//   - 只断言 DOM 里多了个「撤销」按钮是空转 —— #toasts 是 pointer-events:none，
//     按钮看得见点不到照样全绿。所以这里用 elementFromPoint 做命中测试，
//     再用真实鼠标事件点击，不用 element.click() 绕过命中测试。
//   - 只断言 aiMessages 恢复也是空转 —— 工具卡、确认卡、时间戳都不在 aiMessages 里。
//     所以清空前塞一个**不在 aiMessages 里的自定义 DOM 节点**，撤销后它必须还在，
//     证明是整段 DOM 搬回来，而不是重放消息（重放会把它丢掉）。
//   - 每组断言都先校验「前提真的成立」（清空确实清掉了、按钮确实在），
//     否则 FAIL 而不是让后面的断言碰巧绿。
//
// 用法：
//   1) 先重建 EnvKit.exe（前端 embed 在 exe 里，只跑 web_build 会测到旧二进制）
//   2) node tools/cdp_ai_clear_undo_test.js
// 隔离：独立端口 18798 + ENVKIT_INSTANCE_TAG + exe 旁独立数据目录。
// 变异验证见 tools/clear_undo_mutate.py（改坏必红 / 复原必绿）。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9353;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_clearundo', 'EnvKit_clearundo.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

const M_USER = 'UNDO_MARKER_USER_7742';
const M_AI = 'UNDO_MARKER_AI_7742';
const M_DOM = 'UNDO_DOM_NODE_7742';

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 每次都用仓库根的最新 exe 覆盖：失败轮会保留隔离目录（留现场），
  // 只在「目录不存在时才复制」会让后续轮次一直测到那份旧二进制
  // —— 正是本项目踩过的「在测旧二进制」坑。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(CFG, JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    ai: { memory_enabled: false },
    lan: { enabled: false },
    projects: {
      frontend_dir: 'D:\\BCGD\\FarmTrace\\web',
      backend_dir: 'D:\\BCGD\\FarmTrace\\server',
      backup_dir: '', scan_ports: [8080, 8888],
    },
  }, null, 2), 'utf8');
}

(async () => {
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'clearundo' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_clearundo'),
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let ws = null;
  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  断言失败 ' + msg); } };

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets || !Array.isArray(targets)) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    const consoleErrs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.method === 'Runtime.exceptionThrown') consoleErrs.push(m.params.exceptionDetails.text);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      return r.result && r.result.result ? r.result.result.value : null;
    };
    const J = async expr => { const v = await evalJS(expr); try { return JSON.parse(v); } catch (e) { return null; } };
    const reload = async () => { await send('Page.navigate', { url: URL_ }); await sleep(3200); };
    // 真实鼠标点击：坐标来自 getBoundingClientRect，不用 element.click() 绕过命中测试
    const clickAt = async (x, y) => {
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: x, y: y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: x, y: y, button: 'left', clickCount: 1 });
      await sleep(250);
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await reload();

    // ---------- 共用夹具：写历史 → reload → 注入一个「不在 aiMessages 里」的 DOM 节点 ----------
    const plant = async () => {
      await evalJS(`(function(){
        localStorage.setItem('ek-ai-hist', JSON.stringify([
          {role:'user', content:'${M_USER}'},
          {role:'assistant', content:'${M_AI}'}
        ]));
        return 'ok';
      })()`);
      await reload();
      return J(`(function(){
        const box = document.getElementById('ai-msgs');
        const d = document.createElement('div');
        d.className = 'ai-msg sys'; d.id = 'ek-undo-dom-marker'; d.textContent = '${M_DOM}';
        box.appendChild(d);
        return JSON.stringify({
          len: aiMessages.length,
          roles: aiMessages.map(m=>m.role),
          first: aiMessages.length ? aiMessages[0].content : null,
          domNode: !!document.getElementById('ek-undo-dom-marker'),
          hasUser: box.textContent.indexOf('${M_USER}') >= 0,
          hasAi: box.textContent.indexOf('${M_AI}') >= 0
        });
      })()`);
    };

    // ===== 1) 前置：两条历史 + 自定义 DOM 节点都真的在 =====
    const p0 = await plant();
    ok(p0 && p0.len === 2 && p0.roles.join(',') === 'user,assistant' && p0.first === M_USER,
      '夹具前提：历史 2 条且顺序为 user→assistant → ' + JSON.stringify(p0));
    ok(p0 && p0.domNode && p0.hasUser && p0.hasAi,
      '夹具前提：自定义 DOM 节点与两条正文都渲染出来 → ' + JSON.stringify(p0));

    // ===== 2) 清空：立即生效 + 撤销卡片出现 + 命中测试 =====
    const cleared = await J(`(function(){
      document.getElementById('ai-clear').click();
      const box = document.getElementById('ai-msgs');
      const b = document.querySelector('#toasts .toast-undo .toast-act');
      let hit = null, isBtn = false, btnW = 0, toastW = 0, pe = '', x = 0, y = 0;
      if(b){
        const r = b.getBoundingClientRect(), t = b.closest('.toast').getBoundingClientRect();
        x = r.left + r.width/2; y = r.top + r.height/2;
        hit = document.elementFromPoint(x, y);
        isBtn = (hit === b || b.contains(hit));
        btnW = Math.round(r.width); toastW = Math.round(t.width); pe = getComputedStyle(b).pointerEvents;
      }
      return JSON.stringify({
        aiLen: aiMessages.length,
        stored: localStorage.getItem('ek-ai-hist'),
        domNode: !!document.getElementById('ek-undo-dom-marker'),
        hasUser: box.textContent.indexOf('${M_USER}') >= 0,
        hasAi: box.textContent.indexOf('${M_AI}') >= 0,
        sysText: box.textContent.trim().slice(0, 40),
        btn: !!b, btnText: b ? b.textContent : null,
        hitTag: hit ? hit.tagName : null, isBtn: isBtn, btnW: btnW, toastW: toastW, pe: pe,
        x: x, y: y
      });
    })()`);
    ok(cleared && cleared.aiLen === 0 && cleared.stored === null,
      '清空立即生效：aiMessages 归零且 localStorage 已删 → ' + JSON.stringify({ len: cleared && cleared.aiLen, stored: cleared && cleared.stored }));
    ok(cleared && !cleared.domNode && !cleared.hasUser && !cleared.hasAi,
      '清空前提：旧 DOM 节点与两条正文确实都没了（否则撤销断言会假绿）→ ' + JSON.stringify(cleared && cleared.sysText));
    ok(cleared && cleared.btn && cleared.btnText === '撤销',
      '清空后出现带「撤销」按钮的 toast → ' + JSON.stringify({ btn: cleared && cleared.btn, text: cleared && cleared.btnText }));
    ok(cleared && cleared.isBtn && cleared.pe === 'auto',
      '命中测试：按钮在最上层且 pointer-events 恢复（DOM 有按钮≠点得到）→ ' +
      JSON.stringify({ hit: cleared && cleared.hitTag, isBtn: cleared && cleared.isBtn, pe: cleared && cleared.pe }));
    ok(cleared && cleared.btnW > 20 && cleared.btnW < 120 && cleared.btnW < cleared.toastW,
      '按钮宽度逐项复位成 auto，没被全局 width:100% 撑满 → ' +
      JSON.stringify({ btnW: cleared && cleared.btnW, toastW: cleared && cleared.toastW }));

    // ===== 3) 真实鼠标点撤销：整段 DOM 原样回来 =====
    if (cleared && cleared.btn && cleared.isBtn) await clickAt(cleared.x, cleared.y);
    const undone = await J(`(function(){
      const box = document.getElementById('ai-msgs');
      return JSON.stringify({
        len: aiMessages.length,
        roles: aiMessages.map(m=>m.role),
        first: aiMessages.length ? aiMessages[0].content : null,
        second: aiMessages.length > 1 ? aiMessages[1].content : null,
        stored: localStorage.getItem('ek-ai-hist'),
        domNode: !!document.getElementById('ek-undo-dom-marker'),
        hasUser: box.textContent.indexOf('${M_USER}') >= 0,
        hasAi: box.textContent.indexOf('${M_AI}') >= 0,
        toastGone: !document.querySelector('#toasts .toast-undo')
      });
    })()`);
    ok(undone && undone.len === 2 && undone.roles.join(',') === 'user,assistant' &&
       undone.first === M_USER && undone.second === M_AI,
      '撤销恢复 aiMessages 两条且顺序/内容不变 → ' + JSON.stringify(undone));
    ok(undone && undone.stored && undone.stored.indexOf(M_USER) >= 0,
      '撤销恢复 localStorage（刷新也不会再丢）→ ' + JSON.stringify(undone && undone.stored && undone.stored.slice(0, 60)));
    ok(undone && undone.domNode && undone.hasUser && undone.hasAi,
      '撤销搬回**整段 DOM**：不在 aiMessages 里的工具卡/自定义节点也回来了 → ' +
      JSON.stringify({ domNode: undone && undone.domNode, hasUser: undone && undone.hasUser, hasAi: undone && undone.hasAi }));
    ok(undone && undone.toastGone, '点过撤销后卡片消失，不会重复撤销 → ' + JSON.stringify(undone && undone.toastGone));

    // ===== 4) 连点两次清空：撤销仍应恢复第一次的快照 =====
    const dbl = await J(`(function(){
      document.getElementById('ai-clear').click();
      document.getElementById('ai-clear').click();
      const b = document.querySelector('#toasts .toast-undo .toast-act');
      const r = b ? b.getBoundingClientRect() : null;
      return JSON.stringify({toasts: document.querySelectorAll('#toasts .toast-undo').length,
        x: r ? r.left + r.width/2 : 0, y: r ? r.top + r.height/2 : 0, after: aiMessages.length});
    })()`);
    ok(dbl && dbl.after === 0 && dbl.toasts === 1,
      '连点两次：只剩一张撤销卡（不会叠两张）→ ' + JSON.stringify(dbl));
    if (dbl && dbl.toasts === 1) await clickAt(dbl.x, dbl.y);
    const dbl2 = await J(`(function(){ return JSON.stringify({len:aiMessages.length,
      first: aiMessages.length ? aiMessages[0].content : null,
      domNode: !!document.getElementById('ek-undo-dom-marker')}); })()`);
    ok(dbl2 && dbl2.len === 2 && dbl2.first === M_USER && dbl2.domNode,
      '连点后撤销仍恢复第一次的内容（快照没被第二次覆盖）→ ' + JSON.stringify(dbl2));

    // ===== 5) 新对话边界：撤销窗口里发了新消息，不许覆盖 =====
    const fresh = await J(`(function(){
      document.getElementById('ai-clear').click();
      aiMessages.push({role:'user', content:'NEW_TURN_7742'});
      const b = document.querySelector('#toasts .toast-undo .toast-act');
      const r = b ? b.getBoundingClientRect() : null;
      return JSON.stringify({x: r ? r.left + r.width/2 : 0, y: r ? r.top + r.height/2 : 0,
        before: aiMessages.length});
    })()`);
    if (fresh && fresh.before === 1) await clickAt(fresh.x, fresh.y);
    const fresh2 = await J(`(function(){
      const box = document.getElementById('ai-msgs');
      return JSON.stringify({len:aiMessages.length,
        first: aiMessages.length ? aiMessages[0].content : null,
        hasOld: box.textContent.indexOf('${M_USER}') >= 0,
        guarded: Array.from(document.querySelectorAll('#toasts .toast')).some(e=>e.textContent.indexOf('无法撤销') >= 0)});
    })()`);
    ok(fresh2 && fresh2.len === 1 && fresh2.first === 'NEW_TURN_7742' && !fresh2.hasOld,
      '新对话边界：撤销不覆盖已开始的新对话 → ' + JSON.stringify(fresh2));
    ok(fresh2 && fresh2.guarded, '拒绝撤销时给用户一句明说，而不是静默无反应 → ' + JSON.stringify(fresh2 && fresh2.guarded));

    // ===== 6) 10 秒过期：撤销卡自动消失，不留幽灵按钮 =====
    await evalJS(`(function(){ aiMessages = []; document.getElementById('ai-clear').click(); return 'ok'; })()`);
    const beforeExpire = await J(`(function(){ return JSON.stringify({n:document.querySelectorAll('#toasts .toast-undo').length}); })()`);
    ok(beforeExpire && beforeExpire.n === 1, '过期判据前提：撤销卡确实出现过 → ' + JSON.stringify(beforeExpire));
    await sleep(10800);
    const afterExpire = await J(`(function(){ return JSON.stringify({n:document.querySelectorAll('#toasts .toast-undo').length}); })()`);
    ok(afterExpire && afterExpire.n === 0, '10 秒后撤销卡自动消失（不会留一个永远点不动的按钮）→ ' + JSON.stringify(afterExpire));

    // ===== 7) 开发者区因果说明（P0-1）：文案在位 + 布局没被撑破 =====
    // 前提：设置面板本身要先打开。只点 tab 的话 #panel-settings 仍是 display:none，
    // 量出来的盒子全是 0（第一次跑就红在这里）—— 这不是判据太严，是前提没成立。
    await evalJS(`goto('settings')`);
    await sleep(900);
    await evalJS(`(function(){ var b=document.querySelector('[data-set-tab="dev"]'); if(b) b.click(); })()`);
    await sleep(400);
    const dev = await J(`(function(){
      const pane = document.querySelector('[data-set-pane="dev"]');
      const ids = ['h-sd-intro','h-sd-summary','h-sd-retain','h-sd-log','h-sd-retry'];
      const hints = ids.map(id=>{ const e=document.getElementById(id);
        const r = e ? e.getBoundingClientRect() : null;
        return {id:id, text:e?e.textContent:'', w:e?e.scrollWidth:0, cw:e?e.clientWidth:0, bw:r?Math.round(r.width):0}; });
      const f = pane ? pane.querySelector('.set-field') : null;
      const gtc = f ? getComputedStyle(f).gridTemplateColumns : '';
      return JSON.stringify({
        panelShown: document.getElementById('panel-settings').classList.contains('show'),
        shown: pane ? pane.classList.contains('show') : false,
        hints: hints,
        pxCols: (gtc.match(/[\\d.]+px/g) || []).length,
        fieldChildren: f ? f.children.length : 0,
        clearTitle: document.getElementById('ai-clear').getAttribute('title')
      });
    })()`);
    ok(dev && dev.panelShown && dev.shown,
      '布局判据前提：设置面板与开发者 pane 都真的可见（display 不是 none）→ ' +
      JSON.stringify({ panel: dev && dev.panelShown, pane: dev && dev.shown }));
    ok(dev && dev.shown && dev.hints.every(h => h.text.length > 15),
      '开发者页 5 条因果说明都在且有实质内容（不是空壳 hint）→ ' +
      JSON.stringify(dev && dev.hints.map(h => [h.id, h.text.length])));
    ok(dev && dev.hints.some(h => h.id === 'h-sd-summary' && h.text.indexOf('调大反而压得更少') >= 0),
      '最关键那条写明了「摘要后保留量调大反而更频繁」这个反直觉方向 → ' +
      JSON.stringify(dev && dev.hints.find(h => h.id === 'h-sd-summary')));
    ok(dev && dev.hints.every(h => h.cw > 0 && h.bw > 0 && h.w <= h.cw + 2),
      '布局：说明文字不横向溢出（量盒子，clientWidth/rect.width>0 证明真量到了可见元素）→ ' +
      JSON.stringify(dev && dev.hints.map(h => [h.w, h.cw, h.bw])));
    ok(dev && dev.pxCols === 3 && dev.fieldChildren === 3,
      '布局：.set-field 仍是三列且三个子元素（说明没挤进滑块/数字框那一列）→ ' +
      JSON.stringify({ pxCols: dev && dev.pxCols, children: dev && dev.fieldChildren }));
    ok(dev && dev.clearTitle && dev.clearTitle.indexOf('可撤销') >= 0,
      '清空按钮 title 写明「10 秒内可撤销」→ ' + JSON.stringify(dev && dev.clearTitle));

    // ===== 8) i18n：新增文案补了英文，切语言不露中文 =====
    const i18n = await J(`(function(){
      ekSetLang('en', false);
      const e = document.getElementById('h-sd-summary');
      const s = e ? e.textContent : '';
      const title = document.getElementById('ai-clear').getAttribute('title');
      ekSetLang('zh', false);
      return JSON.stringify({en: s.toLowerCase().indexOf('compress') >= 0,
        titleEn: (title||'').toLowerCase().indexOf('undo') >= 0, sample: s.slice(0, 70)});
    })()`);
    ok(i18n && i18n.en && i18n.titleEn,
      '切英文后新增说明/按钮 title 都有译文 → ' + JSON.stringify(i18n));

    ok(consoleErrs.length === 0, '页面无未捕获异常 → ' + JSON.stringify(consoleErrs.slice(0, 3)));

  } catch (e) {
    fails.push('异常：' + e.message);
    console.log('  断言失败 异常：' + e.message);
  } finally {
    try { ws && ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log(fails.length
    ? ('\nOVERALL FAIL（断言失败 ' + fails.length + ' 条）：\n - ' + fails.join('\n - '))
    : '\nOVERALL PASS：清空撤销闭环 + 命中测试 + 开发者说明全部通过');
  if (fails.length === 0 && !process.env.CDP_KEEP) {
    try { fs.rmSync(DIR, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch (e) {}
  }
  process.exit(fails.length ? 1 : 0);
})();
