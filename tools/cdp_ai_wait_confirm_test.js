// CDP 回归：等待态「原因 + 真实计时」（P0-2）+ 确认卡双入口同步（"假的"按钮）
//
// ## 防什么
//
// 1) P0-2：以前发完消息只看到"思考中"三点动画，不知道在等什么、还要多久。
//    现在 SSE 的 wait 事件带稳定 reason + 服务端真实超时，前端显示
//    「原因 · 最长约 Ns · 已等 Ms」。**不许画会归零的假倒计时**：超过上限只换
//    文案为「比预期久」，计时继续往上走。这里用 1 秒上限在 2 秒内验到越界。
// 2) 「AI 弹出的确认按钮是假的」有两层，第二层更隐蔽：
//    (a) aiDecide 原来只 petConfirmHide()（藏卡不改桌宠状态），且 aiPending 为空时
//        静默 return。v3.2 是原地送达、不新开 aiConsume，petAIState('thinking') 不会跑，
//        桌宠就一直停在 confirm 喊「点我确认！」。
//    (b) **真正让按钮点不动的是 50-pet.js 的 pointerdown**：#pet 对所有子元素
//        一律 setPointerCapture(e.pointerId)，迷你卡是 #pet 的子节点，点「确认执行」
//        时 pointerup/click 被劫持到 #pet 本体，mcYes 的 click 根本不触发。
//    上一轮只用 DOM .click() 驱动功能链路，把 (b) 误判成「CDP 在动画元素上会偏移」
//    的夹具限制，于是 28 条断言全绿、用户照样点不动。这次**两个入口都走真实鼠标**，
//    断言 DOM / 桌宠状态 / 真正发出的 /api/ai/confirm 请求字节全部同步。
//
// ## 装置约束（都是踩过的坑）
// - 命中测试用 elementFromPoint + Input.dispatchMouseEvent 真实鼠标，不用
//   element.click() 绕过命中测试 —— DOM 全绿 ≠ 点得到。**尤其别把真实鼠标点不动
//   解释成"动画偏移/夹具限制"**：先查 pointer capture / z-index / pointer-events。
// - 抓的是**真正 fetch 发出去的请求体**，不桩掉被测函数 aiDecide/aiConsume。
// - 前端 embed 在 exe 里：变异时必须 web_build.py → go build → CDP。
// - seed() 每次无条件复制最新 exe，避免隔离目录残留导致「在测旧二进制」。
// - SSE 流用不 close 的 ReadableStream 模拟"服务端正在等确认"，这样 aiConsume
//   的 finally 不会提前 aiWaitStop() 把确认卡上的计时行清掉。
//
// 用法：node tools/cdp_ai_wait_confirm_test.js
// 隔离：端口 18811 + CDP 9361 + tools/_aiwc + tools/_cdptmp_aiwc
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18811;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9361;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_aiwc', 'EnvKit_aiwc.exe');
const DIR = path.dirname(EXE);
const PROF = path.join(REPO, 'tools', '_cdptmp_aiwc');

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 只在「目录不存在时」复制会一直测到那份旧二进制（本项目踩过两次）。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    app_name: 'EnvKit', app_title: '统一开发环境助手',
    install_dir: 'C:\\EnvKit\\tools',
    projects: { frontend_dir: '', backend_dir: '', mysql_host: '127.0.0.1', mysql_port: 3306,
      mysql_user: 'root', db_name: 'farm', sql_file: '', backup_dir: '', scan_ports: [8080, 8888], web_scripts: [] },
    chain: { ssh_host: '', ssh_port: 22, ssh_user: 'root', chain_host: '', chain_port: 20200,
      chain_dir: '', group_id: 1, chain_guard: false, chain_autorecover: false, chain_notify: true },
    dev: { server_port: PORT },
    // 本用例不发真实上游请求：/api/ai/chat 与 /api/ai/confirm 都在页面里桩掉。
    ai: { enabled: true, provider: 'deepseek', base_url: 'https://stub.invalid/v1',
      api_key: 'not-a-real-key', model: 'demo', memory_enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    lan: { enabled: false },
    update: { enabled: false, interval_hours: 72 },
    alerts: { webhook: '', cooldown_secs: 300 },
    notify: { muted: false },
  }, null, 2), 'utf8');
}

const CONFIRM_EV = { type: 'confirm_request', tool_call_id: 'call-1', tool: 'start_service',
  args: { target: 'backend' }, text: '启动后端服务' };
const WAIT_CONFIRM_EV = { type: 'wait', reason: 'confirm', budget_ms: 600000 };
const WAITING_EV = { type: 'waiting_confirm', tool_call_id: 'call-1' };

(async () => {
  // 检查的是**仓库根**的 exe：隔离目录里的副本由 seed() 每次无条件复制，
  // 首次运行时目录本来就不存在，拿 EXE 判断会把自己挡在门外。
  if (!fs.existsSync(path.join(REPO, 'EnvKit.exe'))) { console.log('先构建：go build -o EnvKit.exe .'); process.exit(1); }
  seed();
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) { console.log('端口 ' + PORT + ' 已被占用：先 taskkill /IM EnvKit_aiwc.exe'); process.exit(2); }
  try { fs.rmSync(PROF, { recursive: true, force: true }); } catch (e) {}

  const envkit = spawn(EXE, [], { cwd: DIR, env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'aiwc' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + PROF,
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  const fails = [];
  const ok = (cond, msg, extra) => {
    if (cond) console.log('  PASS ' + msg);
    else { fails.push(msg); console.log('  断言失败 ' + msg + (extra !== undefined ? ' :: ' + extra : '')); }
  };
  let ws = null;
  try {
    let targets = null;
    for (let i = 0; i < 10; i++) { try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); } }
    const page = targets.find(t => t.type === 'page');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map(); const errs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
      if (m.method === 'Runtime.exceptionThrown') {
        const d = m.params && m.params.exceptionDetails;
        errs.push((d && d.exception && d.exception.description) || (d && d.text) || 'exception');
      }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return '__EXC__' + JSON.stringify(r.result.exceptionDetails.text);
      return r.result && r.result.result ? r.result.result.value : undefined;
    };
    const J = async expr => { const v = await evalJS(expr); try { return JSON.parse(v); } catch (e) { return null; } };
    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    const txt = sel => evalJS('(document.querySelector(' + JSON.stringify(sel) + ')||{}).textContent||\'\'');
    const cnt = sel => evalJS('document.querySelectorAll(' + JSON.stringify(sel) + ').length');
    const hasCls = (sel, c) => evalJS('!!(document.querySelector(' + JSON.stringify(sel) + ')&&document.querySelector(' + JSON.stringify(sel) + ').classList.contains(' + JSON.stringify(c) + '))');
    const pointOf = async sel => await J(`(function(){
      var e=document.querySelector(${JSON.stringify(sel)}); if(!e) return JSON.stringify({ok:false,why:'no-el'});
      /* 不调 scrollIntoView：桌宠迷你卡带 transition，滚动会改变布局，
         量到的坐标和真正点击时的位置对不上（曾让真实鼠标点到了宠物主体）。 */
      var r=e.getBoundingClientRect(); var x=r.left+r.width/2, y=r.top+r.height/2;
      var h=document.elementFromPoint(x,y);
      return JSON.stringify({x:x,y:y,w:r.width,h:r.height,hit:h?(h.id||h.className||h.tagName):null,
        ok:!!(h&&(h===e||e.contains(h)))});
    })()`);
    const realClick = async p => {
      if (!p || !p.ok) return p || { ok: false, why: 'no-point' };
      await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: p.x, y: p.y });
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: p.x, y: p.y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: p.x, y: p.y, button: 'left', clickCount: 1 });
      return p;
    };
    const pushEv = async ev => {
      const s = 'data: ' + JSON.stringify(ev) + '\n\n';
      return await evalJS('(function(){ var c=window.__ekSseCtl; if(!c) return "no-ctl"; c.enqueue(new TextEncoder().encode(' + JSON.stringify(s) + ')); return "ok"; })()');
    };

    await evalJS(`aiOpen(); 'ok'`);
    await sleep(400);
    // 桩网络出口，不桩 aiConsume / aiDecide：被测函数必须真跑，抓真正发出的请求字节。
    await evalJS(`(function(){
      window.__ekOrigFetch = window.fetch;
      window.__ekChatBodies = []; window.__ekConfirmBodies = [];
      window.__ekSseScript = []; window.__ekSseCtl = null;
      window.fetch = function(u, o){
        var url = String(u);
        if(url.indexOf('/api/ai/chat') >= 0){
          window.__ekChatBodies.push((o && o.body) || '');
          var chunks = (window.__ekSseScript || []).map(function(e){ return 'data: ' + JSON.stringify(e) + '\\n\\n'; }).join('');
          var enc = new TextEncoder();
          var stream = new ReadableStream({
            start: function(c){ c.enqueue(enc.encode(chunks)); window.__ekSseCtl = c; },
            cancel: function(){ window.__ekSseCtl = null; }
          });
          return Promise.resolve(new Response(stream, {status:200, headers:{'Content-Type':'text/event-stream'}}));
        }
        if(url.indexOf('/api/ai/confirm') >= 0){
          window.__ekConfirmBodies.push((o && o.body) || '');
          return Promise.resolve(new Response(JSON.stringify({ok:true, proceed:true}),
            {status:200, headers:{'Content-Type':'application/json'}}));
        }
        return window.__ekOrigFetch.apply(this, arguments);
      };
      return 'ok';
    })()`);

    const resetRound = async () => {
      await evalJS(`(function(){
        try{ if(typeof aiCtrl!=='undefined' && aiCtrl) aiCtrl.abort(); }catch(_){}
        try{ if(window.__ekSseCtl){ window.__ekSseCtl.close(); window.__ekSseCtl=null; } }catch(_){}
        var c=document.getElementById('ai-clear'); if(c) c.click();
        var t=document.getElementById('toasts'); if(t) t.innerHTML='';
        window.__ekConfirmBodies = []; window.__ekChatBodies = [];
        return 'ok';
      })()`);
      await sleep(400);
    };
    const round = async (script, text) => {
      await resetRound();
      await evalJS('(function(){ window.__ekSseScript = ' + JSON.stringify(script) + '; return "ok"; })()');
      await evalJS(`(function(){ document.getElementById('ai-in').value = ${JSON.stringify(text)};
        document.getElementById('ai-send').click(); return 'ok'; })()`);
      await sleep(700);
    };
    const confirmBodies = async () => await J(`JSON.stringify(window.__ekConfirmBodies || [])`);
    const petClean = async () => ({
      card: await cnt('#ai-msgs .ai-confirm'),
      mini: await hasCls('#pet-mc', 'show'),
      st: await hasCls('#pet', 'confirm'),
      bubble: await txt('#pet-bubble')
    });

    // ===== 1) P0-2：等待态给原因 + 真实上限 + 计时递增 =====
    await round([{ type: 'wait', reason: 'summary', budget_ms: 60000 }], '帮我看下后端为什么起不来');
    const w0 = await txt('#ai-msgs .ai-wait');
    ok((await cnt('#ai-msgs .ai-wait')) === 1, '等待态：思考气泡里有一行原因+计时', w0);
    ok(w0.includes('正在压缩较早的对话历史'), '等待态：原因写的是在压历史', w0);
    ok(w0.includes('最长约 60s'), '等待态：上限取服务端的 60s 真实超时', w0);
    await sleep(1250);
    const w1 = await txt('#ai-msgs .ai-wait');
    ok(/已等 [1-9]/.test(w1), '等待态：秒数会往上走', w1);
    ok(!/还剩\s*0s/.test(w1), '等待态：不出现归零的假倒计时', w1);
    const enWhy = await evalJS(`(function(){ var old=EK_LANG; EK_LANG='en'; var v=t('正在压缩较早的对话历史'); EK_LANG=old; return v; })()`);
    ok(enWhy === 'Compressing older conversation history', '等待态：切英文不会冒出中文', String(enWhy));

    // ===== 2) P0-2：超过真实上限只换文案，计时不归零、不消失 =====
    await pushEv({ type: 'wait', reason: 'summary', budget_ms: 1000 });
    await sleep(2300);
    const w2 = await txt('#ai-msgs .ai-wait');
    ok(w2.includes('比预期久'), '等待态：超上限明说「比预期久」', w2);
    ok(!w2.includes('最长约'), '等待态：超时后不再宣称"最长"', w2);
    ok(/已等 [2-9]/.test(w2), '等待态：超时后计时继续往上走', w2);

    // ===== 3) 确认卡：抽屉 + 桌宠两侧都出现，卡上带 10 分钟真实窗口 =====
    await pushEv(CONFIRM_EV);
    await pushEv(WAITING_EV);
    await pushEv(WAIT_CONFIRM_EV);
    await sleep(400);
    const cardOn = await cnt('#ai-msgs .ai-confirm');
    ok(cardOn === 1, '确认卡：抽屉里出现一张', 'count=' + cardOn);
    ok(await hasCls('#pet', 'confirm'), '确认卡：桌宠进 confirm 态');
    ok(await hasCls('#pet-mc', 'show'), '确认卡：桌宠迷你卡弹出来了');
    const cw = await txt('#ai-msgs .ai-confirm .ai-wait');
    ok(cw.includes('最长约 600s'), '确认卡：倒计时挂在卡上且取 10 分钟真值', cw);

    // ===== 4) 点桌宠迷你卡的「确认执行」：两侧必须同步 + 真实请求字节 =====
    await sleep(700);   // 等 .pet-mc.show 的 CSS 过渡走完，别量到动画中途的位置
    const pPet = await pointOf('#pet-mcYes');
    ok(!!(pPet && pPet.ok), '命中测试：桌宠迷你卡按钮在最上层', JSON.stringify(pPet));
    /* 必须用真实鼠标：#pet 的 pointerdown 若对子元素 setPointerCapture，
       click 会被劫持到 #pet 本体，迷你卡按钮收不到，请求也就不会发出。
       上一条注释曾把这种现象误写成「CDP 在动画元素上会偏移」，并用
       DOM .click() 绕过 —— 那正是用户实测「点了没反应」却测试全绿的原因。 */
    await realClick(pPet);
    await sleep(700);
    const afterPet = await petClean();
    ok(afterPet.card === 0, '桌宠按钮：抽屉确认卡同步消失', JSON.stringify(afterPet));
    ok(!afterPet.mini, '桌宠按钮：迷你卡同步收起', JSON.stringify(afterPet));
    ok(!afterPet.st, '桌宠按钮：桌宠退出 confirm 态（关键）', JSON.stringify(afterPet));
    ok(!String(afterPet.bubble || '').includes('点我确认'), '桌宠按钮：不再喊「点我确认！」', String(afterPet.bubble || ''));
    const bodies1 = await confirmBodies();
    let b1 = null; try { b1 = bodies1 && bodies1[0] ? JSON.parse(bodies1[0]) : null; } catch (e) {}
    ok(!!(bodies1 && bodies1.length === 1 && b1 && b1.tool_call_id === 'call-1' && b1.approved === true),
      '桌宠按钮：真的把决定发到 /api/ai/confirm', JSON.stringify(bodies1));
    ok(!String(await txt('#toasts')).includes('已过期'), '桌宠按钮：没被误判成过期卡');

    // ===== 5) 点抽屉卡的「确认执行」：同样收口 =====
    await round([CONFIRM_EV, WAITING_EV, WAIT_CONFIRM_EV], '再启动一次后端');
    ok((await cnt('#ai-msgs .ai-confirm')) === 1, '抽屉按钮：第二轮确认卡出现');
    const pDrawer = await pointOf('#ai-msgs .ai-confirm .yes');
    ok(!!(pDrawer && pDrawer.ok), '命中测试：抽屉确认按钮在最上层', JSON.stringify(pDrawer));
    await realClick(pDrawer);
    await sleep(700);
    const afterDrawer = await petClean();
    ok(afterDrawer.card === 0 && !afterDrawer.mini && !afterDrawer.st,
      '抽屉按钮：迷你卡与桌宠状态一起收口', JSON.stringify(afterDrawer));
    const bodies2 = await confirmBodies();
    ok(!!(bodies2 && bodies2.length === 1), '抽屉按钮：确认决定也真的发出去了', JSON.stringify(bodies2));

    // ===== 6) 清空对话不残留；残留按钮点了有明确提示，不静默 =====
    await round([CONFIRM_EV, WAITING_EV, WAIT_CONFIRM_EV], '第三次启动后端');
    ok((await cnt('#ai-msgs .ai-confirm')) === 1, '清理路径：先有一张待确认卡');
    await evalJS(`(function(){ document.getElementById('ai-clear').click(); var t=document.getElementById('toasts'); if(t) t.innerHTML=''; return 'ok'; })()`);
    await sleep(500);
    const afterClear = await petClean();
    ok(afterClear.card === 0 && !afterClear.mini && !afterClear.st,
      '清理路径：清空对话后桌宠不残留 confirm 态', JSON.stringify(afterClear));
    // 收口后迷你卡已隐藏、桌宠已退出 confirm，界面上那个残留按钮本来就点不到了。
    // 这里直接驱动被测函数覆盖「aiPending 已清空」的防御分支，不假装还能点。
    await evalJS(`(async function(){ try{ await aiDecide(true); }catch(_){} return 'ok'; })()`);
    await sleep(400);
    ok(String(await txt('#toasts')).includes('已过期'),
      '空转防护：会话已清时确认调用给明确提示，不静默 return', String(await txt('#toasts')));

    ok(errs.length === 0, '页面没有未捕获异常', errs.slice(0, 2).join(' | '));

    console.log(fails.length
      ? ('\nOVERALL FAIL（断言失败 ' + fails.length + ' 条）：\n - ' + fails.join('\n - '))
      : '\nOVERALL PASS：等待态原因+真实计时 / 确认卡双入口同步全部通过');
  } catch (e) {
    console.log('  断言失败 异常：' + e.message);
  }
  try { ws.close(); } catch (e) {}
  try { chrome.kill(); } catch (e) {}
  try { envkit.kill(); } catch (e) {}
  await sleep(400);
  process.exit(fails.length ? 1 : 0);
})();
