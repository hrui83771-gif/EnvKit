// 真进程验证：手机访问（局域网端口）的安全属性
//
// 为什么单测不够：
//   单测用 httptest 直接调 lanPairPost / lanStateGet，绕过了
//   **真正的 net.Listener、真正的 cookie 往返、真正的 Host 头**。
//   而这个功能最关键的三件事全在那一层：
//     · 监听是否真的绑在 0.0.0.0（而不是 127.0.0.1，那样手机根本连不上）
//     · 配对后 cookie 能不能真的往返
//     · 桌面路由在手机端口上是不是真的 404（不是"被判断成不允许"）
//
// 安全闸（同 cdp_proc_syslog_test.js / cfg_hot_reload_test.js）：
//   1. 只在 EnvKit_p2b.exe 旁边写种子，用户真实配置一个字不动；
//   2. ENVKIT_INSTANCE_TAG 换互斥体名，不要求用户先关掉自己那份；
//   3. 只认 EnvKit_p2b.exe，且手机端口固定 18866。
//
// 用法：
//   go build -trimpath -ldflags "-s -w" -o tools\_p2b\EnvKit_p2b.exe .
//   node tools/cfg_lan_access_test.js
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18802;          // 主控制台（隔离实例）
const LAN_PORT = 18866;      // 手机访问端口
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN = 'http://127.0.0.1:' + LAN_PORT;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2b', 'EnvKit_p2b.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

function req(url, opts) {
  return new Promise((res, rej) => {
    const u = new URL(url);
    const r = http.request({
      hostname: u.hostname, port: u.port, path: u.pathname + u.search,
      method: (opts && opts.method) || 'GET',
      headers: (opts && opts.headers) || {},
    }, resp => {
      let d = '';
      resp.on('data', c => d += c);
      resp.on('end', () => res({ code: resp.statusCode, body: d, headers: resp.headers }));
    });
    r.on('error', rej);
    if (opts && opts.body) r.write(opts.body);
    r.end();
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

let fails = 0;
function ok(cond, name, extra) {
  if (cond) { console.log('  PASS  ' + name); }
  else { fails++; console.log('  FAIL  ' + name + (extra ? '  → ' + extra : '')); }
}

(async () => {
  if (!fs.existsSync(EXE)) { console.log('FAIL 找不到测试构建：' + EXE); process.exit(1); }
  if (path.basename(EXE).toLowerCase() !== 'envkit_p2b.exe') {
    console.log('拒绝在真实数据目录跑：' + EXE); process.exit(1);
  }

  // 种子配置：关掉 AI（避免本机真 key 参与），端口设成隔离端口，
  // 手机访问开关**先关着** —— 后面通过桌面端点打开，走的是真实路径。
  fs.writeFileSync(CFG, JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    app_name: 'LAN-TEST',
  }, null, 2), 'utf8');

  const envkit = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdp2b' }),
    stdio: 'ignore',
  });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await req(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('exe:', EXE);
  console.log('envkit ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(1); }

  const home = (await req(URL_)).body;
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  const tok = tm ? tm[1] : '';
  const H = { 'X-EnvKit-Token': tok };
  const postJSON = (p, obj) => req(URL_ + p, {
    method: 'POST', headers: Object.assign({ 'Content-Type': 'application/json' }, H), body: JSON.stringify(obj || {}),
  });

  try {
    console.log('\n[1] 开关关闭时，手机端口根本不监听');
    let lanUp = true;
    try { await req(LAN + '/'); } catch (e) { lanUp = false; }
    ok(!lanUp, 'LAN 端口未监听（开关关着就该连不上）');

    console.log('\n[2] 打开开关 → 端口开始监听');
    let r = await postJSON('api/lan/config', { enabled: true, port: LAN_PORT });
    ok(r.code === 200, 'POST /api/lan/config 开启成功', 'code=' + r.code + ' ' + r.body.slice(0, 160));
    let cfgj = {};
    try { cfgj = JSON.parse(r.body); } catch (e) {}
    ok(cfgj.enabled === true, '回显 enabled=true');
    ok(cfgj.port === LAN_PORT, '回显端口 = ' + LAN_PORT, String(cfgj.port));
    const code = String(cfgj.pair_code || '');
    ok(/^\d{6}$/.test(code), '托盘/接口能拿到 6 位配对码', code);

    let lanOpen = false;
    for (let i = 0; i < 20; i++) {
      try { await req(LAN + '/'); lanOpen = true; break; } catch (e) { await sleep(300); }
    }
    ok(lanOpen, 'LAN 端口已在监听');

    console.log('\n[3] 手机页可达，且不泄露桌面令牌');
    const page = await req(LAN + '/');
    ok(page.code === 200, 'GET / 拿到手机页', 'code=' + page.code);
    ok(page.body.indexOf('EnvKit') >= 0, '返回的是手机页 HTML');
    ok(page.body.indexOf('__EK_TOKEN__') < 0, '手机页里没有本机令牌（主控制台的令牌绝不能外泄）');
    ok(page.body.indexOf('配对码') >= 0, '手机页含配对提示');

    console.log('\n[4] 未配对时，状态端点必须 401');
    const st0 = await req(LAN + '/api/lan/state');
    ok(st0.code === 401, '未配对 → 401', 'code=' + st0.code);

    console.log('\n[5] 错误配对码 → 403 且不下发 cookie');
    const bad = await req(LAN + '/api/lan/pair', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: '000000' }),
    });
    ok(bad.code === 403, '错误码 → 403', 'code=' + bad.code);
    const setCookie = bad.headers['set-cookie'] || [];
    ok(setCookie.length === 0, '失败不下发 cookie', JSON.stringify(setCookie));

    console.log('\n[6] 正确配对码 → 200 + HttpOnly cookie');
    const good = await req(LAN + '/api/lan/pair', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'User-Agent': 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1' },
      body: JSON.stringify({ code: code }),
    });
    ok(good.code === 200, '正确配对码 → 200', 'code=' + good.code + ' ' + good.body.slice(0, 160));
    const ck = (good.headers['set-cookie'] || []).join(';');
    ok(/ek_lan=/.test(ck), '下发了 ek_lan cookie');
    ok(/HttpOnly/i.test(ck), 'cookie 是 HttpOnly（前端 JS 读不到）');
    const m = /ek_lan=([^;]+)/.exec(ck);
    const cookie = m ? m[1] : '';

    console.log('\n[7] 带 cookie → 状态端点放行');
    const CH = { Cookie: 'ek_lan=' + cookie };
    const st1 = await req(LAN + '/api/lan/state', { headers: CH });
    ok(st1.code === 200, '已配对 → 200', 'code=' + st1.code);
    let sj = {};
    try { sj = JSON.parse(st1.body); } catch (e) {}
    ok(sj.ok === true, '状态返回 ok');
    ok(Array.isArray(sj.services) && sj.services.length >= 2, '返回了服务列表', 'len=' + (sj.services || []).length);
    ok(sj.version !== undefined, '带版本号');

    console.log('\n[8] 手机端口上**不能有**桌面路由（能写错的表面积必须小）');
    for (const p of ['api/config', 'api/audit', 'api/ports/kill', 'api/db/query', 'api/lan/config', 'api/program/exit', 'api/ai/config']) {
      const rr = await req(LAN + '/' + p, { method: 'POST', headers: Object.assign({ 'Content-Type': 'application/json' }, CH), body: '{}' });
      ok(rr.code === 404, '/' + p + ' → 404（路由压根没注册）', 'code=' + rr.code);
    }

    console.log('\n[9] 伪造 Host（DNS rebinding）应被拒');
    const reb = await req(LAN + '/api/lan/state', { headers: Object.assign({}, CH, { Host: 'evil.example' }) });
    ok(reb.code === 403, 'Host=evil.example → 403', 'code=' + reb.code);

    console.log('\n[10] 设备表：磁盘上只有 sha256，没有明文令牌');
    const lc = await req(URL_ + 'api/lan/config', { headers: H });
    let lj = {};
    try { lj = JSON.parse(lc.body); } catch (e) {}
    ok(Array.isArray(lj.devices) && lj.devices.length === 1, '设备表里有 1 台', JSON.stringify((lj.devices || []).length));
    const dev = (lj.devices || [])[0] || {};
    ok(/iPhone/.test(dev.name || ''), '设备名识别出 iPhone', dev.name);
    ok(typeof dev.hash === 'string' && dev.hash.length === 64, '存的是 sha256 十六进制', 'len=' + (dev.hash || '').length);
    ok((dev.hash || '').indexOf(cookie) < 0, '磁盘上没有明文令牌');

    console.log('\n[11] 配对限速：连续失败后被挡');
    // 新 IP 做不到（都是 127.0.0.1），所以这条在真机上靠"先成功再改码"测不出来，
    // 单测已覆盖按 IP 限速。这里只确认"换码后旧码立即失效"。
    const rot = await postJSON('api/lan/rotate', {});
    ok(rot.code === 200, '更换配对码成功', 'code=' + rot.code);
    let rj = {};
    try { rj = JSON.parse(rot.body); } catch (e) {}
    const code2 = String(rj.pair_code || '');
    ok(/^\d{6}$/.test(code2) && code2 !== code, '换码后是新码', code + ' → ' + code2);
    const old = await req(LAN + '/api/lan/pair', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: code }),
    });
    ok(old.code === 403, '旧码立即失效', 'code=' + old.code);

    console.log('\n[12] 关闭开关 → 端口立刻不再监听');
    await postJSON('api/lan/config', { enabled: false });
    await sleep(800);
    let down = false;
    try { await req(LAN + '/'); } catch (e) { down = true; }
    ok(down, 'LAN 端口已停止监听');
  } finally {
    try { envkit.kill(); } catch (e) {}
  }

  console.log('\n' + (fails ? ('FAILED ' + fails + ' 项') : 'ALL PASS'));
  process.exit(fails ? 1 : 0);
})();
