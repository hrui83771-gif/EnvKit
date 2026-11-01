// CDP 回归：日志收集器不许把 WARN 当成错误（v3.7.1）
//
// ## 这条测试要复现的真实事故
//
// 启动日志里有一条「未以管理员身份运行，MySQL 服务注册可能失败」。
// 旧的 collectErrLines 用 /失败|错误/ 做字面兜底、**不看级别标签**，
// 于是这条警告被收成"错误"；又因为 sys 级日志被广播到 5 个分区
// （appendLog: scope==='sys' → install/config/web/backend/chain），
// 同一条被复制成 5 行、分别冠上 [install]/[config]/[web]/[backend]/[chain]，
// 发给 AI 时标题还写着"以下是最近的错误日志"。
// 结果：AI 认真分析了一个根本不存在的故障，还反复让用户"查看错误日志"。
//
// ## 为什么喂构造数据而不是读真实日志
//
// 只用真实日志测，本机恰好没有某种坏值时断言会**空转** ——
// 把过滤条件改坏了测试照样绿。所以把规则抽成纯函数
// （window.__ekErrProbe），用构造的行直接打。
//
// 用法：
//   1) go build -o tools\_errc\EnvKit_errc.exe .
//   2) node tools/cdp_err_collect_test.js
//
// 隔离：独立端口 18798 + ENVKIT_INSTANCE_TAG + 独立数据目录（exe 旁边），
// 不碰用户自己的 config/日志；也不用先关掉用户那份 EnvKit。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9346;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_errc', 'EnvKit_errc.exe');
const DIR = path.dirname(EXE);   // 数据目录 = exe 所在目录（exeDir()），不是 cwd

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

// 用例：既有本 bug 的原样文本，也有必须继续命中的真错误，
// 还有"没有级别标签"的两侧（兜底不能因此失效，也不能什么都说成错误）。
const TS = '2026-11-01 12:45:45';
const CASES = [
  { name: 'WARN 文案里带"失败" → 不算错误（就是这次的事故）',
    line: TS + ' [WARN] [权限] 未以管理员身份运行，MySQL 服务注册可能失败',
    want: false },
  { name: 'HINT 文案里带"失败" → 不算错误',
    line: TS + ' [HINT] [权限] 安装 MySQL 服务需要管理员权限，否则会注册失败',
    want: false },
  { name: 'WARN 里带"错误" → 不算错误',
    line: TS + ' [WARN] [启动] 端口被占用，错误码 10048', want: false },
  { name: 'FAIL → 算错误',
    line: TS + ' [FAIL] [启动] 后端启动失败：exit status 1', want: true },
  { name: 'ERROR → 算错误',
    line: TS + ' [ERROR] [安装] 解压失败', want: true },
  { name: '无级别 + panic → 算错误（字面兜底仍在）',
    line: 'panic: runtime error: invalid memory address', want: true },
  { name: '无级别 + 失败 → 算错误（字面兜底仍在）',
    line: 'npm ERR! 构建失败', want: true },
  { name: '无级别 + 普通输出 → 不算错误',
    line: 'go: downloading github.com/pkg/errors v0.9.1', want: false },
];

// sys 级日志广播到 5 个分区的样子：同一条文本被存了 5 份。
const ALL5 = ['install', 'config', 'web', 'backend', 'chain'];
function broadcast(text) { return ALL5.map(k => ({ key: k, text })); }

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
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'errc' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_err'),
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
    const hasProbe = await evalJS('typeof window.__ekErrProbe === "object" && window.__ekErrProbe !== null');
    ok(hasProbe === true, '页面暴露了 __ekErrProbe（说明测的是含本次改动的构建）');
    if (!hasProbe) throw new Error('没有 __ekErrProbe：web/index.html 没重新构建，或测的是旧 exe');

    // ---- 单行判定 ----
    console.log('单行判定：');
    const got = await evalJS(`JSON.stringify(${JSON.stringify(CASES.map(c => c.line))}.map(function(l){
      return window.__ekErrProbe.isErrLine(l);
    }))`);
    const gotArr = JSON.parse(got);
    CASES.forEach((c, i) => ok(gotArr[i] === c.want, c.name));

    // ---- 广播合并 ----
    console.log('广播合并（同一条文本在 5 个分区各存一份）：');
    const warnMerged = await evalJS(`(function(){
      const p = window.__ekErrProbe;
      return JSON.stringify(p.merge(${JSON.stringify(broadcast(TS + ' [WARN] [权限] 未以管理员身份运行，MySQL 服务注册可能失败'))}));
    })()`);
    const wm = JSON.parse(warnMerged);
    ok(wm.length === 0, '同一条 WARN 广播 5 份 → 一条都不收（旧版会收成 5 条"错误"）');

    const failMerged = await evalJS(`(function(){
      const p = window.__ekErrProbe;
      return JSON.stringify(p.merge(${JSON.stringify(broadcast(TS + ' [FAIL] [启动] 后端启动失败'))}));
    })()`);
    const fm = JSON.parse(failMerged);
    ok(fm.length === 1, '同一条 FAIL 广播 5 份 → 合并成 1 条（旧版会报 5 条）');
    ok(fm.length === 1 && fm[0].tag === '[sys]', '合并后的标签是 [sys]，不冒充某个组件');
    ok(fm.length === 1 && fm[0].keys && fm[0].keys.length === 5, '合并后仍记得它出现在 5 个分区');

    // ---- 不同分区的不同错误不能被合成一条 ----
    const two = await evalJS(`(function(){
      const p = window.__ekErrProbe;
      return JSON.stringify(p.merge([
        {key:'web', text:'${TS} [FAIL] [启动] 前端 vite 退出'},
        {key:'backend', text:'${TS} [FAIL] [启动] 后端编译失败'}
      ]));
    })()`);
    const tw = JSON.parse(two);
    ok(tw.length === 2, '两个分区的两条不同错误 → 保持 2 条');
    ok(tw.every(r => r.tag !== '[sys]'), '各自标自己的分区，没被误标成 [sys]');

    // ---- 顺序与截断 ----
    const many = await evalJS(`(function(){
      const p = window.__ekErrProbe;
      const arr = [];
      for(let i=0;i<20;i++) arr.push({key:'web', text:'${TS} [FAIL] 错误 ' + i});
      const rows = p.merge(arr).slice(-15);
      return JSON.stringify({n: rows.length, first: rows[0].text.slice(-3), last: rows[rows.length-1].text.slice(-3)});
    })()`);
    const mj = JSON.parse(many);
    ok(mj.n === 15, '上限 15 条（截的是最早的，不是随机砍）');
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
