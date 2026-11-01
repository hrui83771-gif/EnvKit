// 防火墙诊断接口的真机验证（v3.6.2 追加）
//
// 验的是"手机连不上时，能不能说清是哪一边挡的"：
//   · 服务端在监听 → listening=true
//   · 防火墙没放行 → allowed=false
//   · 网络类别（Public/Private）能读出来
//   · 建议文案指向"该改哪一边"
//
// 隔离实例 + ENVKIT_INSTANCE_TAG，不碰用户正开着的托盘 EnvKit。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18793;
const LAN_PORT = 18794;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
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
  if (!fs.existsSync(EXE)) { console.log('缺少测试 exe：' + EXE); process.exit(2); }
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    lan: { enabled: true, port: LAN_PORT, devices: [] },
    dev: { server_port: PORT },
  }, null, 2));

  const proc = spawn(EXE, [], {
    cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'fwtest' }),
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

    console.log('\n[诊断接口] GET /api/lan/fw');
    const r = await req('GET', URL_ + 'api/lan/fw', null, H);
    check('返回 200', r.code === 200, String(r.code));
    const j = JSON.parse(r.body.toString('utf8'));
    const d = j.diag || {};
    console.log('  diag = ' + JSON.stringify(d));
    check('有 diag 段', !!j.diag, JSON.stringify(j));
    check('diag.allowed 是布尔', typeof d.allowed === 'boolean', JSON.stringify(d));
    check('diag.listening 是布尔', typeof d.listening === 'boolean', JSON.stringify(d));
    check('diag 有建议（advice）', typeof d.advice === 'string' && d.advice.length > 0, JSON.stringify(d));
    check('diag 有原因（hint）', typeof d.hint === 'string' && d.hint.length > 0, JSON.stringify(d));
    check('端口号正确', d.port === LAN_PORT, String(d.port));

    console.log('\n[config 不再带 fw 段]');
    // 防火墙告警块已按用户要求从配对弹窗移除，config 也不再带 fw 段
    // （省掉每次开设置页跑两次 PowerShell 的开销）。
    const c2 = JSON.parse((await req('GET', URL_ + 'api/lan/config', null, H)).body.toString('utf8'));
    check('config 里没有 fw 段（告警已移除）', !c2.fw, JSON.stringify(c2).slice(0, 200));

    console.log('\n[诊断结论必须指出"该改哪一边"]');
    // 服务端在监听 → 结论不能是"端口没在监听"
    if (d.listening) {
      check('在监听时 hint 不是"端口没在监听"', (d.hint || '').indexOf('端口没在监听') < 0, d.hint);
    }
    // 防火墙没放行 → advice 必须提到放行
    if (d.listening && d.allowed === false) {
      check('被拦时建议指向放行/防火墙', (d.advice || '').indexOf('防火墙') >= 0 || (d.hint || '').indexOf('防火墙') >= 0,
        JSON.stringify(d));
    }
    // 关键：不能把"我不知道"说成"我知道"
    check('读不到防火墙状态时不谎报（available=false 时不判定为被拦）',
      d.available !== false || d.allowed === true,
      JSON.stringify(d));

    console.log('\n[network category]');
    if (d.network_category) {
      console.log('  当前网络 = ' + d.network_name + ' / ' + d.network_category);
      check('网络类别是 Windows 的三种之一',
        ['Public', 'Private', 'DomainAuthenticated'].indexOf(d.network_category) >= 0, d.network_category);
    } else {
      console.log('  （读不到网络类别 —— 非管理员上下文，允许）');
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
