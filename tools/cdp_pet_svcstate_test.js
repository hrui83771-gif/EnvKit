// CDP 回归：桌宠不许把"启动成功"画成红灯（v3.7.1）
//
// ## 这条测试要复现的真实事故
//
// 用户报："启动没有错误了，但桌宠还是弹出（红感叹号）"。
//
// 根因是**两个口径被混成一个**：
//   - Go 侧 progState["web-start"].Running 的语义是「这一轮启动还在进行」，
//     收尾一律写 running=false（verify.go endStartFlag:143、program.go progSetSkip:737）；
//   - 前端 petTasksSync 却把它当「服务在不在跑」，只看 !s.running && p.running
//     就认定服务掉了，然后 status 只认 stopped / warn 两档，**ok 落进 else 亮红灯**。
// 于是每一次启动成功（true → false + status=ok）都弹"失败了，点红标看日志"，
// 而日志里一条错误都没有。启动越快越不容易被 2s 轮询抓到，所以是"偶发"的。
//
// ## 为什么喂构造快照而不是真启动服务
//
// 真启动要跑 npm/go build 一分多钟，还会占用户的端口；而且本机状态一变测试就飘。
// 规则已抽成纯函数（window.__ekSvcProbe），用构造的快照直接打；
// 另外再用 petTasksSync 真喂两轮快照，确认**表情**也跟着对（纯函数绿≠界面不弹红）。
//
// 用法：
//   1) go build -o tools\_svc\EnvKit_svc.exe .
//   2) node tools/cdp_pet_svcstate_test.js
//
// 隔离：独立端口 18797 + ENVKIT_INSTANCE_TAG + 独立数据目录（exe 旁边），
// 不碰用户自己的 config/日志；也不用先关掉用户那份 EnvKit。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18797;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9347;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_svc', 'EnvKit_svc.exe');
const DIR = path.dirname(EXE);   // 数据目录 = exe 所在目录（exeDir()），不是 cwd

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// 快照形状照抄 /api/program/state 的真实返回（handleProgState → ProgStatus）
const CONCL = [
  { name: '启动成功：running=false + ok=true + status=ok → ok（不是 fail）',
    cur: { running: false, ok: true, status: 'ok', msg: '后端服务已启动并复验通过' }, want: 'ok' },
  { name: '幂等跳过且验过：status=ok → ok',
    cur: { running: false, ok: true, status: 'ok', msg: '前端已在运行且复验通过，未重复启动' }, want: 'ok' },
  { name: '幂等跳过未复验：status=warn → warn（不是 fail）',
    cur: { running: false, ok: true, status: 'warn', msg: '前端进程已在运行，未重复启动' }, want: 'warn' },
  { name: '真失败：status=fail → fail',
    cur: { running: false, ok: false, status: 'fail', msg: '后端启动失败：exit status 1' }, want: 'fail' },
  { name: '用户停止：status=stopped → stopped',
    cur: { running: false, ok: false, status: 'stopped', msg: '已停止' }, want: 'stopped' },
  { name: '旧存档没有 status，msg=已停止 → stopped（按 msg 兜底）',
    cur: { running: false, ok: false, msg: '已停止' }, want: 'stopped' },
  { name: '旧存档没有 status，ok=true → ok（state.go 注释同口径）',
    cur: { running: false, ok: true, msg: '成功' }, want: 'ok' },
  { name: '键不存在（空对象）→ fail（缺 key 不当成功）',
    cur: {}, want: 'fail' },
];

// [prev, cur] → 该做什么
const EVENTS = [
  { name: '首轮（prev 为空）→ 不动作（别把历史结论当成刚发生的事）',
    prev: null, cur: { running: false, ok: false, status: 'fail', msg: '上一轮失败' }, want: null },
  { name: '启动开始 false→true → starting',
    prev: { running: false }, cur: { running: true, status: 'running', msg: '构建并启动中...' }, want: 'starting' },
  { name: '启动成功 true→false+ok → ok（**本次事故**：旧版给 fail）',
    prev: { running: true }, cur: { running: false, ok: true, status: 'ok', msg: '已启动并复验通过' }, want: 'ok' },
  { name: '启动未复验 true→false+warn → warn',
    prev: { running: true }, cur: { running: false, ok: true, status: 'warn', msg: '已派生未复验' }, want: 'warn' },
  { name: '启动真失败 true→false+fail → fail',
    prev: { running: true }, cur: { running: false, ok: false, status: 'fail', msg: 'exit status 1' }, want: 'fail' },
  { name: '用户停止 true→false+stopped → stopped（安静）',
    prev: { running: true }, cur: { running: false, ok: false, status: 'stopped', msg: '已停止' }, want: 'stopped' },
  { name: '旧存档 true→false 无 status、ok=true → ok',
    prev: { running: true }, cur: { running: false, ok: true }, want: 'ok' },
  { name: '稳态一直 true → 不动作（服务跑着不该反复庆祝/报警）',
    prev: { running: true }, cur: { running: true, status: 'ok' }, want: null },
  { name: '幂等跳过 26ms 抓不到（false→false）→ 不动作',
    prev: { running: false }, cur: { running: false, ok: true, status: 'ok', msg: '幂等跳过' }, want: null },
  { name: '一直没在动（false→false 历史失败）→ 不动作（不重复弹同一个红标）',
    prev: { running: false }, cur: { running: false, ok: false, status: 'fail' }, want: null },
];

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 隔离目录跑完就删（见文件末尾的收尾）：exe 不预置，缺了就从仓库根那份复制一次，
  // 这样"删干净"之后下一次跑不必再手动 go build。
  if (!fs.existsSync(EXE)) { try { fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE); } catch (e) {} }
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
}

(async () => {
  if (!fs.existsSync(EXE)) {
    // 隔离目录跑完就删（见文件末尾）：exe 不预置，缺了就从仓库根那份复制一次，
    // 这样「删干净」之后下一次跑不必再手动 go build。
    fs.mkdirSync(DIR, { recursive: true });
    fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  }
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'svc' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_svc'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let ws = null;
  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  FAIL ' + msg); } };

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
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) throw new Error('页面异常：' + JSON.stringify(r.result.exceptionDetails));
      return r.result && r.result.result ? r.result.result.value : undefined;
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4500);

    // 探针不在 = 测的是旧构建（本项目踩过的坑：装置在测上一版还以为是产品的锅）
    const hasProbe = await evalJS('typeof window.__ekSvcProbe === "object" && window.__ekSvcProbe !== null');
    ok(hasProbe === true, '页面暴露了 __ekSvcProbe（说明测的是含本次改动的构建）');
    if (!hasProbe) throw new Error('没有 __ekSvcProbe：web/index.html 没重新构建，或测的是旧 exe');

    // ---- 结论判定 ----
    console.log('结论判定（快照 → 四态）：');
    const concGot = JSON.parse(await evalJS(`JSON.stringify(${JSON.stringify(CONCL.map(c => c.cur))}.map(function(s){
      return window.__ekSvcProbe.concl(s);
    }))`));
    CONCL.forEach((c, i) => ok(concGot[i] === c.want, c.name + ' → ' + JSON.stringify(concGot[i])));

    // ---- 相邻两轮 → 动作 ----
    console.log('相邻两轮（prev, cur）→ 动作：');
    const evGot = JSON.parse(await evalJS(`JSON.stringify(${JSON.stringify(EVENTS.map(e => [e.prev, e.cur]))}.map(function(p){
      return window.__ekSvcProbe.event(p[0], p[1]);
    }))`));
    EVENTS.forEach((e, i) => ok(evGot[i] === e.want, e.name + ' → ' + JSON.stringify(evGot[i])));

    // ---- 表情（纯函数对了，界面也不许弹红）----
    // 注意：必须在一个 evaluate 同步块里喂完再读 —— 页面自己每 2s 轮询一次
    // /api/program/state 也会调 petTasksSync（喂的是真实 prog），异步读会被它顶掉。
    console.log('实际表情（真喂 petTasksSync）：');
    const seq = JSON.parse(await evalJS(`(function(){
      const snap = function(){
        const p = document.getElementById('pet'), b = document.getElementById('pet-badge');
        return {cls: p.className, badge: b.className, text: b.textContent};
      };
      const feed = function(o){ petTasksSync({'web-start': o}); };
      const STOP = {running:false, ok:false, status:'stopped', msg:'已停止'};
      const RUN  = {running:true,  ok:false, status:'running', msg:'启动中...'};
      const out = {};
      // 先把桌宠归位到待机：页面自己的轮询/自动检测可能让它停在 busy/success/confirm，
      // 那几种状态下"启动开始"按设计不抢状态（怕清掉确认卡），会把这条断言测成假红。
      try{ petConfirmHide(); }catch(e){}
      try{ petAIIdle(); }catch(e){}
      feed(STOP); feed(RUN); out.busy = snap();
      feed({running:false, ok:true, status:'ok', msg:'已启动并复验通过'}); out.ok = snap();
      feed(STOP); feed(RUN); feed({running:false, ok:false, status:'fail', msg:'后端启动失败：exit status 1'}); out.fail = snap();
      feed(STOP); feed(RUN); feed({running:false, ok:true, status:'warn', msg:'已派生未复验'}); out.warn = snap();
      return JSON.stringify(out);
    })()`));

    ok(/busy/.test(seq.busy.cls), '启动开始 → 进忙碌态（旧版此刻就说"已启动"） -> ' + seq.busy.cls);
    ok(!/error/.test(seq.ok.cls), '启动成功 → **不亮红灯**（本次事故的正面断言）');
    ok(/success/.test(seq.ok.cls) && /green/.test(seq.ok.badge), '启动成功 → 绿勾庆祝');
    ok(/error/.test(seq.fail.cls), '真失败 → 仍然亮红灯（回归：没把报警关掉）');
    ok(/red/.test(seq.fail.badge), '真失败 → 徽标是红的');
    ok(!/error/.test(seq.warn.cls), '"已启动但还没复验" → 不亮红灯');
  } catch (e) {
    fails.push('异常：' + e.message);
    console.log('ERROR ' + e.message);
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log('');
  if (fails.length) {
    console.log('FAILED ' + fails.length + ' 项');
    fails.forEach(f => console.log('  - ' + f));
    process.exit(1);
  }
  // 成功即删隔离目录：每个测试都在 DIR 里复制一份 exe 加一个 Chrome profile，
  // 不删就会累积（2026-11-01 实测 tools/ 下 17 个目录共 255MB）。
  // 失败时保留现场 —— 那时 exe / config / 日志本身就是证据。CDP_KEEP=1 强制保留。
  if (true && !process.env.CDP_KEEP) {
    try { fs.rmSync(DIR, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch (e) {}
  }
  /* cdpCleanupMarker */
  console.log('ALL PASS');
  process.exit(0);
})();
