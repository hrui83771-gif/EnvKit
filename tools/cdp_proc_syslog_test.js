// CDP 回归：进程资源行 + 系统事件弹窗（gopsutil / wevtutil 这两块新能力）
//
// 用法：
//   1) go build -trimpath -ldflags "-s -w" -o tools\_p2b\EnvKit_p2b.exe .
//   2) node tools/cdp_proc_syslog_test.js
//
// 三条约束，和 cdp_p2_mission_test.js 同一套理由：
//   1. 只在 EnvKit_p2b.exe 旁边写种子（数据目录=exe 目录），用户真实配置一个字不动；
//   2. ENVKIT_INSTANCE_TAG 换互斥体名，不要求用户先关掉自己那份；
//   3. 先确认 /api/proc/stats 存在——否则后面所有断言都在测旧构建。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18802;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9352;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2b', 'EnvKit_p2b.exe');
const DIR = path.dirname(EXE);

function req(url, opts) {
  return new Promise((res, rej) => {
    const u = new URL(url);
    const r = http.request({ hostname: u.hostname, port: u.port, path: u.pathname + u.search, method: (opts && opts.method) || 'GET', headers: (opts && opts.headers) || {} },
      resp => { let d = ''; resp.on('data', c => d += c); resp.on('end', () => res({ code: resp.statusCode, body: d })); });
    r.on('error', rej);
    if (opts && opts.body) r.write(opts.body);
    r.end();
  });
}
function getText(url) { return req(url).then(r => r.body); }
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

let fails = 0;
function ok(cond, name, extra) {
  if (cond) { console.log('  PASS  ' + name); } else { fails++; console.log('  FAIL  ' + name + (extra ? '  → ' + extra : '')); }
}

(async () => {
  if (!fs.existsSync(EXE)) {
    console.log('FAIL 找不到测试构建：' + EXE);
    console.log('     go build -trimpath -ldflags "-s -w" -o tools\\_p2b\\EnvKit_p2b.exe .');
    process.exit(1);
  }
  if (path.basename(EXE).toLowerCase() !== 'envkit_p2b.exe') {
    console.log('拒绝在真实数据目录跑（会把种子写进用户配置）：' + EXE);
    process.exit(1);
  }
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');

  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdp2b' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getText(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('exe:', EXE);
  console.log('envkit ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(1); }

  const home = await getText(URL_);
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  ok(!!tm, '首页里能拿到本机令牌');
  const tok = tm ? tm[1] : '';

  // ---------- 后端：两个新端点 ----------
  console.log('\n[1] /api/proc/stats');
  const st = await req(URL_ + 'api/proc/stats', { headers: { 'X-EnvKit-Token': tok } });
  ok(st.code === 200, '端点存在（200）', 'code=' + st.code);
  let sj = {};
  try { sj = JSON.parse(st.body); } catch (e) {}
  ok(Array.isArray(sj.procs) && sj.procs.length > 0, 'procs 非空');
  const self = (sj.procs || []).find(p => p.key === 'self');
  ok(!!self, '列表里有 self 这一项');
  ok(!!self && self.rss_mb > 0, 'EnvKit 自身 RSS 是真实读数（>0）', self ? 'rss=' + self.rss_mb : '');
  ok(!!self && self.pid > 0 && self.alive === true, 'self 标记为存活且 PID>0');
  ok(!!sj.cpus && sj.cpus > 0, '带上了 CPU 核数（前端折算用）');

  console.log('\n[2] /api/win/events');
  const we = await req(URL_ + 'api/win/events', { headers: { 'X-EnvKit-Token': tok } });
  ok(we.code === 200, '端点存在（200）', 'code=' + we.code);
  let wj = {};
  try { wj = JSON.parse(we.body); } catch (e) {}
  ok(typeof wj.ok === 'boolean', '返回结构里有 ok 字段');
  ok(wj.events !== undefined && Array.isArray(wj.events), 'events 是数组');
  // 不要求一定有错误事件（干净机器就是 0 条），但必须"要么有事件，要么说清为什么没有"
  ok((wj.events || []).length > 0 || !!wj.err, '有事件，或明确给出读取失败原因', 'events=' + (wj.events || []).length + ' err=' + (wj.err || ''));
  console.log('       读到 ' + (wj.events || []).length + ' 条；ok=' + wj.ok + (wj.err ? ' err=' + wj.err : ''));
  if ((wj.events || []).length) {
    const e0 = wj.events[0];
    ok(typeof e0.id === 'number' && !!e0.time, '事件条目带 ID 与时间', JSON.stringify(e0).slice(0, 160));
  }

  // ---------- 前端 ----------
  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(DIR, '_cdptmp'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let targets = null;
  for (let i = 0; i < 10; i++) {
    try { targets = JSON.parse(await getText('http://127.0.0.1:' + DBG + '/json/list')); break; } catch (e) { await sleep(800); }
  }
  const page = targets && targets.find(t => t.type === 'page');
  if (!page) { console.log('FAIL 拿不到 CDP 页面目标'); try { chrome.kill(); } catch (e) {} try { envkit.kill(); } catch (e) {} process.exit(1); }

  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let mid = 0;
  const pending = new Map();
  ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
  const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
  const evalJS = async expr => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    return r.result && r.result.result ? r.result.result.value : undefined;
  };

  try {
    await send('Page.navigate', { url: URL_ });
    await sleep(2500);

    console.log('\n[3] 服务卡资源行');
    ok(await evalJS(`!!document.getElementById('sr-web')`) === true, '#sr-web 容器存在');
    ok(await evalJS(`!!document.getElementById('sr-backend')`) === true, '#sr-backend 容器存在');
    // 隔离实例里前端/后端都没启动 → 资源行必须空着，不能显示上一次的数字
    const emptyWeb = await evalJS(`document.getElementById('sr-web').textContent.trim()`);
    ok(emptyWeb === '', '服务未启动时资源行为空（不显示假数据）', JSON.stringify(emptyWeb));
    // 直接喂一条假数据，验证渲染路径（真模型之外的确定性检查）
    const painted = await evalJS(`(function(){
      paintRes('web', {key:'web', alive:true, rss_mb:123.4, cpu_pct:7.5, threads:11, handles:222, uptime_sec:90,
                        growth_mb:0, growing:false, hist:[100,110,120,123.4]});
      const el = document.getElementById('sr-web');
      return el.textContent + '||' + (el.querySelector('svg.spark') ? 'spark' : 'nospark');
    })()`);
    ok(/123\.4/.test(painted || ''), '资源行渲染出内存数值', painted);
    ok(/spark/.test(painted || ''), '渲染出 sparkline');
    const cleared = await evalJS(`(function(){ paintRes('web', null); return document.getElementById('sr-web').textContent; })()`);
    ok(cleared === '', '进程不存在时资源行被清空', JSON.stringify(cleared));

    console.log('\n[4] 系统事件弹窗');
    ok(await evalJS(`!!document.getElementById('btn-syslog')`) === true, '侧栏有"系统事件"入口');
    await evalJS(`document.getElementById('btn-syslog').click()`);
    await sleep(300);
    ok(await evalJS(`document.getElementById('syslog').classList.contains('show')`) === true, '点击后弹窗打开');
    await sleep(2200); // 等 wevtutil 读（首次要 0.5~2s）
    const cnt = await evalJS(`document.getElementById('syslog-list').children.length`);
    const txt = await evalJS(`document.getElementById('syslog-list').textContent`);
    ok(cnt > 0 || (txt || '').length > 0, '列表有内容（事件或明确的失败说明）', 'children=' + cnt + ' text=' + JSON.stringify((txt || '').slice(0, 80)));
    // 关键：页脚按钮必须真的在视口内（踩过"被内容撑出可视区、弹窗关不掉"）
    const vis = await evalJS(`(function(){
      const b = document.getElementById('syslog-close');
      const r = b.getBoundingClientRect();
      return JSON.stringify({w:r.width, h:r.height, top:r.top, bottom:r.bottom, vh:innerHeight});
    })()`);
    let v = {}; try { v = JSON.parse(vis); } catch (e) {}
    ok(v.w > 0 && v.h > 0 && v.bottom <= v.vh && v.top >= 0, '关闭按钮在可视区内', vis);
    await evalJS(`document.getElementById('syslog-close').click()`);
    await sleep(200);
    ok(await evalJS(`document.getElementById('syslog').classList.contains('show')`) === false, '点关闭能关掉');
    // 刷新按钮：强制绕过 30s 缓存，不能报错
    await evalJS(`document.getElementById('btn-syslog').click()`);
    await sleep(300);
    await evalJS(`document.getElementById('syslog-refresh').click()`);
    await sleep(2200);
    const txt2 = await evalJS(`document.getElementById('syslog-list').textContent`);
    ok(!/undefined|NaN/.test(txt2 || ''), '重新读取后没有 undefined/NaN', (txt2 || '').slice(0, 80));
    ok((await evalJS(`(window.__errs||[]).length`)) === 0, '页面没有 JS 报错', JSON.stringify(await evalJS(`window.__errs||[]`)));
  } finally {
    try { ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log('\n' + (fails ? ('FAILED ' + fails + ' 项') : 'ALL PASS'));
  process.exit(fails ? 1 : 0);
})();
