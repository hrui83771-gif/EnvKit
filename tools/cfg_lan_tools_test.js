// 真进程验证：手机端发给上游的**工具清单**里不能有写工具
//
// ## 为什么不能用"问模型你有哪些工具"来验
//
// 那是**间接证据**：模型可能记错、可能漏答、可能只说一半。
// 而"手机端只读"这条承诺的硬定义是**请求体里的 tools 数组**。
// 模型说什么都不算数，只有请求体能算数。
//
// 所以这里起一个**假上游**（本地 HTTP 服务），把 AI 的 base_url 指过去，
// 把 EnvKit 真实发出的请求体抓下来，直接读 tools 数组。
// 假上游回一个固定的流式响应，让这一轮能正常收尾。
//
// ## 假上游同时承担第二个角色
//
// 它可以**主动返回一个写工具的 tool_call**，模拟"模型硬报一个写工具名"
// （清单里没有它，模型凭记忆或幻觉报了）。真上游几乎不会这么做，
// 但"模型看不到 ≠ 服务端调不动"这条必须有人验。
//
// 用法：
//   go build -trimpath -ldflags "-s -w" -o tools\_p2b\EnvKit_p2b.exe .
//   node tools/cfg_lan_tools_test.js
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18802;
const LAN_PORT = 18866;
const FAKE_PORT = 18911;      // 假上游
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const LAN = 'http://127.0.0.1:' + LAN_PORT;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2b', 'EnvKit_p2b.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

const KNOWN_WRITE = ['start_service', 'stop_service', 'restart_service', 'run_detection',
  'db_backup', 'cleanup_processes', 'apply_whitelist', 'manage_memories', 'sync_chain_ip'];

function req(url, opts) {
  return new Promise((res, rej) => {
    const u = new URL(url);
    const r = http.request({
      hostname: u.hostname, port: u.port, path: u.pathname + u.search,
      method: (opts && opts.method) || 'GET', headers: (opts && opts.headers) || {},
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

// 假上游：记录请求体，按 mode 决定回什么
const upstream = [];
let mode = 'plain';

function startFake() {
  const srv = http.createServer((rq, rs) => {
    let body = '';
    rq.on('data', c => body += c);
    rq.on('end', () => {
      let parsed = null;
      try { parsed = JSON.parse(body); } catch (e) {}
      upstream.push(parsed);
      rs.writeHead(200, { 'Content-Type': 'text/event-stream' });
      if (mode === 'forgeToolCall') {
        // 模拟"模型硬报一个写工具名"：清单里没有它，模型凭记忆报了。
        rs.write('data: ' + JSON.stringify({
          choices: [{ delta: { tool_calls: [{ index: 0, id: 'call_forged', type: 'function', function: { name: 'start_service', arguments: '{"service":"web"}' } }] } }],
        }) + '\n\n');
        rs.write('data: ' + JSON.stringify({ choices: [{ delta: {}, finish_reason: 'tool_calls' }] }) + '\n\n');
      } else {
        rs.write('data: ' + JSON.stringify({
          choices: [{ delta: { content: '好的，这是假上游的固定回答。' } }],
        }) + '\n\n');
        rs.write('data: ' + JSON.stringify({ choices: [{ delta: {}, finish_reason: 'stop' }] }) + '\n\n');
      }
      rs.write('data: [DONE]\n\n');
      rs.end();
    });
  });
  return new Promise(res => srv.listen(FAKE_PORT, '127.0.0.1', () => res(srv)));
}

(async () => {
  if (!fs.existsSync(EXE)) { console.log('FAIL 找不到测试构建：' + EXE); process.exit(1); }
  if (path.basename(EXE).toLowerCase() !== 'envkit_p2b.exe') {
    console.log('拒绝在真实数据目录跑：' + EXE); process.exit(1);
  }

  const fake = await startFake();
  console.log('fake upstream on', FAKE_PORT);

  // 种子：AI 指向假上游（这样能抓请求体），开关打开
  fs.writeFileSync(CFG, JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    app_name: 'LAN-TOOLS-TEST',
    ai: {
      enabled: true,
      api_key: 'fake-key-for-capture',
      base_url: 'http://127.0.0.1:' + FAKE_PORT + '/v1',
      model: 'fake-model',
    },
    lan: { enabled: true, port: LAN_PORT },
  }, null, 2), 'utf8');

  const envkit = spawn(EXE, [], {
    cwd: DIR, env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdp2b' }), stdio: 'ignore',
  });

  let ready = false;
  for (let i = 0; i < 60; i++) { try { await req(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} try { fake.close(); } catch (e) {} process.exit(1); }

  const home = (await req(URL_)).body;
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  const H = { 'X-EnvKit-Token': tm ? tm[1] : '' };

  try {
    // 配对
    const lr = await req(URL_ + 'api/lan/config', { headers: H });
    const code = (JSON.parse(lr.body).pair_code || '');
    ok(/^\d{6}$/.test(code), '拿到配对码', code);
    const pr = await req(LAN + '/api/lan/pair', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: code }),
    });
    ok(pr.code === 200, '配对成功', 'code=' + pr.code);
    const ck = (pr.headers['set-cookie'] || []).join(';');
    const cm = /ek_lan=([^;]+)/.exec(ck);
    const CH = { Cookie: 'ek_lan=' + (cm ? cm[1] : '') };

    async function chat(msgs, timeoutMs) {
      return new Promise((resolve) => {
        const body = JSON.stringify({ messages: msgs, lang: 'zh' });
        const u = new URL(LAN + '/api/lan/chat');
        const r = http.request({
          hostname: u.hostname, port: u.port, path: u.pathname, method: 'POST',
          headers: Object.assign({ 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) }, CH),
        }, resp => {
          let acc = '', buf = '';
          resp.on('data', c => {
            buf += c.toString('utf8');
            let i;
            while ((i = buf.indexOf('\n\n')) >= 0) {
              const line = buf.slice(0, i).trim(); buf = buf.slice(i + 2);
              if (line.indexOf('data: ') !== 0) continue;
              let ev; try { ev = JSON.parse(line.slice(6)); } catch (e) { continue; }
              if (ev.type === 'delta') acc += (ev.text || '');
              if (ev.type === 'error') acc += '\n[ERR]' + (ev.text || '');
            }
          });
          resp.on('end', () => resolve({ acc }));
        });
        r.on('error', e => resolve({ acc: '', err: e.message }));
        r.write(body); r.end();
        setTimeout(() => { try { r.destroy(); } catch (e) {} }, timeoutMs || 60000);
      });
    }

    console.log('\n[1] 手机端发给上游的 tools 数组里不能有写工具');
    upstream.length = 0;
    mode = 'plain';
    await chat([{ role: 'user', content: '现在环境怎么样？' }], 60000);
    await sleep(1500);
    ok(upstream.length > 0, '假上游收到了请求', 'count=' + upstream.length);
    const mobileTools = ((upstream[0] || {}).tools || []).map(t => (t.function || {}).name);
    console.log('  INFO  手机端 tools（' + mobileTools.length + ' 个）: ' + mobileTools.join(', '));
    const leaked = mobileTools.filter(n => KNOWN_WRITE.indexOf(n) >= 0);
    ok(leaked.length === 0, '手机端 tools 数组里没有写工具', leaked.join(','));
    ok(mobileTools.length > 0, '手机端仍能拿到只读工具（不能把工具全去掉）', 'len=' + mobileTools.length);

    console.log('\n[2] 桌面端发给上游的 tools 数组里**应当有**写工具（对照）');
    upstream.length = 0;
    mode = 'plain';
    await new Promise((resolve) => {
      const body = JSON.stringify({ messages: [{ role: 'user', content: '你好' }], lang: 'zh' });
      const u = new URL(URL_ + 'api/ai/chat');
      const r = http.request({
        hostname: u.hostname, port: u.port, path: u.pathname, method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body), 'X-EnvKit-Token': H['X-EnvKit-Token'] },
      }, resp => { resp.resume(); resp.on('end', () => resolve()); });
      r.on('error', () => resolve());
      r.write(body); r.end();
      setTimeout(() => { try { r.destroy(); } catch (e) {} resolve(); }, 60000);
    });
    await sleep(1500);
    const deskTools = ((upstream[0] || {}).tools || []).map(t => (t.function || {}).name);
    console.log('  INFO  桌面端 tools（' + deskTools.length + ' 个）');
    const deskLeaked = deskTools.filter(n => KNOWN_WRITE.indexOf(n) >= 0);
    ok(deskLeaked.length > 0, '桌面端仍看得到写工具（只读没有误伤主控制台）', deskLeaked.join(','));

    console.log('\n[3] 模型硬报一个写工具 → 服务端必须挡住（执行闸）');
    upstream.length = 0;
    mode = 'forgeToolCall';
    const before = upstream.length;
    const c3 = await chat([{ role: 'user', content: '调用 start_service' }], 60000);
    await sleep(1500);
    ok(true, '这一轮已发出（INFO）', (c3.acc || '').slice(0, 120));

    // 判据：审计里不能有 start_service 的执行记录，但可以有 lan_write_blocked
    const ar = await req(URL_ + 'api/audit?n=200', { headers: H });
    let acts = [];
    try { acts = (JSON.parse(ar.body).entries || []).map(x => String(x.action || '')); } catch (e) {}
    const executed = acts.filter(a => /^start_service$|^stop_service$|^restart_service$/.test(a));
    const blocked = acts.filter(a => a.indexOf('lan_write_blocked') >= 0);
    ok(executed.length === 0, '写工具没有被执行', executed.join(','));
    ok(blocked.length > 0, '执行闸留下了拦截记录', 'blocked=' + blocked.length);
    console.log('  INFO  模型回复: ' + (c3.acc || '').slice(0, 200).replace(/\n/g, ' '));
  } finally {
    try { envkit.kill(); } catch (e) {}
    try { fake.close(); } catch (e) {}
  }

  console.log('\n' + (fails ? ('FAILED ' + fails + ' 项') : 'ALL PASS'));
  process.exit(fails ? 1 : 0);
})();
