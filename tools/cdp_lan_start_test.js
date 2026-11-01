// 复现「手机上点启动，电脑没反应」
//
// 关键：走**真实的 LAN mux**（不是主控制台），用配对后的 cookie 发 POST /api/lan/start，
// 然后看：桌面端到底有没有跑起来（看日志 + 看 /state 的 phase 变化）。
//
// 这是"手机点了没反应"的唯一硬证据 —— 不看审计猜不出来。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18791;
const LAN_PORT = 18792;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN_ = 'http://127.0.0.1:' + LAN_PORT + '/';
const EXE = path.join(REPO, 'tools', '_fwtest', 'EnvKit_fw.exe');
const DIR = path.dirname(EXE);

let TOKEN = '';
const failures = [];
function check(name, ok, detail) {
  if (ok) console.log('  PASS  ' + name);
  else { console.log('  FAIL  ' + name + (detail ? '  → ' + detail : '')); failures.push(name); }
}
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
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

(async function main() {
  // 用真实的项目目录（前端有 serve 脚本），否则测的是"配置缺失"这条分支
  const FRONTEND = 'D:\\BCGD\\Addition\\BloodLine\\web';
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    lan: { enabled: true, port: LAN_PORT, devices: [] },
    dev: { server_port: PORT },
    projects: { frontend_dir: FRONTEND, backend_dir: '' },
  }, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'lanstart' }),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout.on('data', () => {});
  proc.stderr.on('data', () => {});

  for (let i = 0; i < 40; i++) {
    await sleep(1000);
    try {
      const html = await req('GET', URL_, null, null);
      if (html.code === 200) {
        const m = /__EK_TOKEN__="([0-9a-f]+)"/.exec(html.body.toString('utf8'));
        if (m) { TOKEN = m[1]; break; }
      }
    } catch (e) {}
  }
  if (!TOKEN) { console.log('实例没起来'); try { proc.kill(); } catch (e) {} process.exit(2); }
  console.log('实例就绪');

  try {
    const H = { 'X-EnvKit-Token': TOKEN };
    const cfg = JSON.parse((await req('GET', URL_ + 'api/lan/config', null, H)).body.toString('utf8'));
    const code = cfg.pair_code;
    console.log('配对码 = ' + code);

    // 配对
    const pair = await req('POST', LAN_ + 'api/lan/pair', JSON.stringify({ code }), { Host: '127.0.0.1:' + LAN_PORT });
    if (pair.code !== 200) { console.log('配对失败：' + pair.code + ' ' + pair.body.toString('utf8')); process.exit(2); }
    const ck = ((pair.headers['set-cookie'] || [])[0] || '').split(';')[0];
    const CH = { Host: '127.0.0.1:' + LAN_PORT, Cookie: ck };
    check('配对成功', !!ck, ck);

    // 启动前状态
    const before = JSON.parse((await req('GET', LAN_ + 'api/lan/state', null, CH)).body.toString('utf8'));
    console.log('  启动前 web = ' + JSON.stringify((before.services || []).find(s => s.key === 'web')));

    console.log('\n[发启动请求] POST /api/lan/start {key:"web"}');
    const t0 = Date.now();
    const start = await req('POST', LAN_ + 'api/lan/start', JSON.stringify({ key: 'web' }), CH);
    const dt = Date.now() - t0;
    console.log('  响应 ' + start.code + '（' + dt + 'ms）: ' + start.body.toString('utf8').slice(0, 200));
    check('启动请求有响应', start.code === 200, String(start.code));
    check('启动请求**没有阻塞**（应远小于 45s 复验窗口）', dt < 5000, dt + 'ms');

    // 轮询状态，看 phase 有没有变成 starting → running
    console.log('\n[轮询状态]');
    let sawStarting = false, sawRunning = false, last = null;
    for (let i = 0; i < 40; i++) {
      await sleep(1000);
      const st = JSON.parse((await req('GET', LAN_ + 'api/lan/state', null, CH)).body.toString('utf8'));
      const web = (st.services || []).find(s => s.key === 'web');
      last = web;
      if (web && web.phase === 'starting') sawStarting = true;
      if (web && (web.phase === 'running' || web.phase === 'degraded')) { sawRunning = true; break; }
    }
    console.log('  末态 web = ' + JSON.stringify(last));
    check('状态出现过 starting（说明服务端确实在启动）', sawStarting, JSON.stringify(last));
    check('状态变成 running/degraded（服务真起来了）', sawRunning, JSON.stringify(last));

    // 桌面端 /api/runtime/state 也该同步
    const rt = JSON.parse((await req('GET', URL_ + 'api/runtime/state', null, H)).body.toString('utf8'));
    const rweb = ((rt.state && rt.state.services && rt.state.services.web) || {});
    console.log('  桌面端 web = ' + JSON.stringify({ phase: rweb.phase, running: rweb.running, verified: rweb.verified }));
    check('桌面端状态与手机端一致（同一口径）',
      rweb.phase === (last && last.phase), JSON.stringify(rweb) + ' vs ' + JSON.stringify(last));

    // 日志：后台任务到底跑了什么
    console.log('\n[日志]');
    const logs = fs.readdirSync(DIR).filter(f => /^envkit-\d{8}\.log$/.test(f));
    if (logs.length) {
      const lf = path.join(DIR, logs.sort().pop());
      const lines = fs.readFileSync(lf, 'utf8').split('\n').filter(l => /启动|前端|手机|npm|web/i.test(l));
      lines.slice(-18).forEach(l => console.log('  ' + l.trim()));
    } else {
      console.log('  (没有日志文件)');
    }
  } finally {
    try { proc.kill(); } catch (e) {}
  }

  console.log('\n==== 结果 ====');
  if (failures.length) {
    console.log('FAIL ' + failures.length + ' 项：');
    failures.forEach(f => console.log('  - ' + f));
    process.exit(1);
  }
  console.log('全部通过');
  process.exit(0);
})().catch(e => { console.error('异常：', e); process.exit(2); });
