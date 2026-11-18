// CDP 回归：MCP 开关 + 复制连接配置（v3.8）
//
// 防什么：设置页「MCP 工具服务」卡的交互链路，验证的是**用户真实点到的路径**：
//   1. 默认关：老配置升级后 MCP 卡开关是关的，POST /api/mcp（握手）被 403
//   2. 真点开关 → 服务端 enabled=true，状态行就地写明"已开启"与工具数
//      （开着的 403 消失 —— 同一个端点两态都断，防止"断言在读到 null 时也成立"）
//   3. 点「复制连接配置」→ 状态行出现"令牌…重新复制"提示（复制成功路径的可见后果）；
//      GET /api/mcp/config 的 snippet 是合法 JSON 且含 X-EnvKit-Token 头
//   4. 再点开关 → 服务端 enabled=false，/api/mcp 恢复 403
//
// 铁律：元素点击一律 Input.dispatchMouseEvent 真鼠标（el.click() 绕过命中测试）。
// 隔离：端口 18798 + 独立 exe 副本（每轮强制覆盖）+ 独立 user-data-dir。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9353;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_mcpcfg', 'EnvKit_mcpcfg.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

function httpReq(url, opts, body) {
  return new Promise((res, rej) => {
    const req = http.request(url, opts, r => {
      let d = ''; r.on('data', c => d += c); r.on('end', () => res({ code: r.statusCode, body: d }));
    });
    req.on('error', rej);
    if (body) req.write(body);
    req.end();
  });
}
// getJSONStr：只负责拿到正文（/ 返回的是 HTML 页面，不是 JSON —— ready 探测
// 不能 JSON.parse，第一次跑就栽在这里：exe 正常起来，ready 却永远 false）
const getJSONStr = async url => (await httpReq(url, {})).body;
const sleep = ms => new Promise(r => setTimeout(r, ms));

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // **每轮强制覆盖 exe**（"在测旧二进制"的老坑）
  try { fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE); } catch (e) {}
  ['', '.bak', '.bak2', '.bak3'].forEach(sfx => { try { fs.rmSync(CFG + sfx, { force: true }); } catch (e) {} });
  // 不写 mcp 段 = 老配置升级场景：默认必须关
  fs.writeFileSync(CFG, JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    ai: { memory_enabled: false },
    lan: { enabled: false },
    projects: { frontend_dir: '', backend_dir: '', backup_dir: '', scan_ports: [8080] },
    chain: { chain_host: '', chain_port: 20200, ssh_host: '', ssh_user: '', chain_guard: false },
  }, null, 2), 'utf8');
}

(async () => {
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'mcpcfg' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: ['ignore', 'pipe', 'pipe'] });
  envkit.on('error', e => console.log('SPAWN-ERROR:', e.message));
  envkit.stderr.on('data', d => process.stderr.write('EXE-ERR: ' + d));
  envkit.stdout.on('data', d => { const s = d.toString(); if (/启动|监听|error|panic|ERROR/.test(s)) process.stdout.write('EXE: ' + s); });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSONStr(URL_); ready = true; break; } catch (e) { if (i === 59) console.log('LAST-ERR:', e.message); await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_mcpcfg'),
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  FAIL ' + msg); } };
  let ws = null;

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = JSON.parse(await getJSONStr('http://127.0.0.1:' + DBG + '/json/list')); break; } catch (e) { await sleep(800); }
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
    // 真鼠标点击：先 scrollIntoView（视口外坐标 dispatchMouseEvent 无效 ——
    // MCP 卡在页面 ~1800px 处，窗口只有 1000 高，不滚就点），再取视口内坐标
    const realClick = async (sel) => {
      await evalJS(`(function(){var el=document.querySelector('${sel}');if(el)el.scrollIntoView({block:'center',behavior:'instant'});})()`);
      await sleep(150);
      const box = JSON.parse(await evalJS(`(function(){var el=document.querySelector('${sel}');if(!el)return 'null';var b=el.getBoundingClientRect();return JSON.stringify({x:Math.round(b.x+b.width/2),y:Math.round(b.y+b.height/2),vis:b.width>0&&b.height>0,inVp:b.y>=0&&b.y+b.height<=innerHeight});})()`));
      if (box === 'null' || !box.vis || !box.inVp) throw new Error('点不到元素（不在视口内）：' + sel + ' ' + JSON.stringify(box));
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: box.x, y: box.y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: box.x, y: box.y, button: 'left', clickCount: 1 });
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4500);
    // 令牌：Node 侧直读 /api/* 必须带 X-EnvKit-Token（fetch 包装在 20-shell 才装）
    const TOKEN = await evalJS(`window.__EK_TOKEN__`);
    ok(typeof TOKEN === 'string' && TOKEN.length >= 16, '页面拿到令牌（len=' + (TOKEN || '').length + '）');
    const H = { 'Content-Type': 'application/json', 'X-EnvKit-Token': TOKEN };

    // ---- 1. 默认关：老配置升级场景 ----
    await evalJS(`goto('settings')`);
    await sleep(400);
    await evalJS(`(function(){var b=document.querySelector('[data-set-tab="base"]'); if(b) b.click();})()`);
    await sleep(900);
    ok(await evalJS(`!!document.getElementById('s_mcp_on')`), 'MCP 卡渲染（开关存在）');
    ok(await evalJS(`!document.getElementById('s_mcp_on').checked`), '默认关：开关初始为未勾选');
    ok(await evalJS(`(document.getElementById('h-mcp')||{}).textContent.indexOf('未开启') >= 0`),
      '状态行写明「未开启」，不是空白或「已开启」');

    const init = await httpReq(URL_ + 'api/mcp/config', { headers: H });
    ok(init.code === 200 && JSON.parse(init.body).enabled === false, 'GET config enabled=false（老配置升级）');
    const blocked = await httpReq(URL_ + 'api/mcp', { method: 'POST', headers: H },
      JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize' }));
    ok(blocked.code === 403, '开关关时握手 403 → ' + blocked.code);
    ok(blocked.body.indexOf('设置页') >= 0, '403 文案指路设置页（不静默）');

    // ---- 2. 真点开关：开 ----
    await realClick('#s_mcp_on');
    // 等 POST + 回显落定（switch 是异步保存，点了立即读读到的是自己 —— "测试读早了=恒真"的老坑）
    let opened = false;
    for (let i = 0; i < 20; i++) {
      await sleep(400);
      const j = JSON.parse((await httpReq(URL_ + 'api/mcp/config', { headers: H })).body);
      if (j.enabled === true) { opened = true; break; }
    }
    ok(opened, '真点开关后服务端 enabled=true');
    await sleep(600);
    const hintOn = await evalJS(`(document.getElementById('h-mcp')||{}).textContent`);
    ok(hintOn && hintOn.indexOf('已开启') >= 0, '状态行就地变「已开启」→ ' + hintOn);
    const allow = await httpReq(URL_ + 'api/mcp', { method: 'POST', headers: H },
      JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize' }));
    ok(allow.code !== 403, '开开关后同一端点不再 403 → ' + allow.code);

    // ---- 3. 复制连接配置 ----
    const cfgj = JSON.parse((await httpReq(URL_ + 'api/mcp/config', { headers: H })).body);
    ok(typeof cfgj.snippet === 'string' && cfgj.snippet.length > 20, 'GET 返回连接配置块');
    const snip = JSON.parse(cfgj.snippet);
    const srv = snip.mcpServers && snip.mcpServers.envkit;
    ok(!!srv && srv.type === 'http' && srv.url === cfgj.url, 'snippet 形状：mcpServers.envkit.http + url 一致');
    ok(!!(srv && srv.headers && srv.headers['X-EnvKit-Token']), 'snippet 带令牌头');
    await realClick('#btn-mcp-copy');
    await sleep(700);
    const snipHint = await evalJS(`(document.getElementById('h-mcp-snippet')||{}).textContent`);
    ok(snipHint && snipHint.indexOf('重新复制') >= 0, '复制后状态行提示令牌有效期 → ' + (snipHint || '').slice(0, 40));

    // ---- 4. 再点开关：关 ----
    await realClick('#s_mcp_on');
    let closed = false;
    for (let i = 0; i < 20; i++) {
      await sleep(400);
      const j = JSON.parse((await httpReq(URL_ + 'api/mcp/config', { headers: H })).body);
      if (j.enabled === false) { closed = true; break; }
    }
    ok(closed, '再点开关后服务端 enabled=false');
    // 关闭断言只在"确认开过"时才算数：开着都失败的话，false→false 是恒真假绿
    ok(!opened || closed, '关闭路径有效（前提：开启曾真实发生）');
    const blocked2 = await httpReq(URL_ + 'api/mcp', { method: 'POST', headers: H },
      JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize' }));
    ok(blocked2.code === 403 && opened, '关掉后握手恢复 403（开过才有资格断言恢复）→ ' + blocked2.code);
    // 开关状态落盘：config.json 里出现 mcp.enabled=false
    const saved = JSON.parse(fs.readFileSync(CFG, 'utf8'));
    ok(saved.mcp && saved.mcp.enabled === false, '开关已落盘 config.json mcp.enabled=false');

  } catch (e) {
    fails.push('探针异常：' + e.message);
  } finally {
    try { ws && ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }
  console.log(fails.length ? ('\n失败 ' + fails.length + ' 条：\n - ' + fails.join('\n - ')) : '\n全部通过');
  process.exit(fails.length ? 1 : 0);
})();
