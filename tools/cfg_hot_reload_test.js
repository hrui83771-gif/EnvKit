// 真进程验证：改 config.json 后配置热重载生效（fsnotify 这条链路）
//
// 为什么不能用单测代替：
//   单测直接调 cfgReloadIfChanged，绕过了"watcher 真的在监听目录"这一环。
//   而这恰恰是 fsnotify 最容易失效的地方——监听文件本身、监听错目录、
//   或者原子保存后 inode 变了就不再触发。只有跑真进程、真写文件才能证伪。
//
// 三条约束（同 cdp_proc_syslog_test.js）：
//   1. 只在 EnvKit_p2b.exe 旁边写种子，用户真实配置一个字不动；
//   2. ENVKIT_INSTANCE_TAG 换互斥体名，不要求用户先关掉自己那份；
//   3. 端口 18802 + 只认 EnvKit_p2b.exe，防止误连到真实实例。
//
// 用法：
//   go build -trimpath -ldflags "-s -w" -o tools\_p2b\EnvKit_p2b.exe .
//   node tools/cfg_hot_reload_test.js
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const REPO = path.resolve(__dirname, '..');
const PORT = 18802;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_p2b', 'EnvKit_p2b.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

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
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

let fails = 0;
function ok(cond, name, extra) {
  if (cond) { console.log('  PASS  ' + name); } else { fails++; console.log('  FAIL  ' + name + (extra ? '  → ' + extra : '')); }
}

// 模拟 saveExternalConfig 的写法：先写 .tmp 再 rename 覆盖。
// 这一条很关键——如果哪天有人把监听改成盯文件本身，这里就会立刻失效。
function atomicWrite(p, text) {
  const tmp = p + '.tmp';
  fs.writeFileSync(tmp, text, 'utf8');
  fs.renameSync(tmp, p);
}

function seedCfg(appName, ai) {
  const c = {
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    app_name: appName,
  };
  if (ai) c.ai = ai;
  return c;
}

(async () => {
  if (!fs.existsSync(EXE)) {
    console.log('FAIL 找不到测试构建：' + EXE);
    process.exit(1);
  }
  if (path.basename(EXE).toLowerCase() !== 'envkit_p2b.exe') {
    console.log('拒绝在真实数据目录跑：' + EXE);
    process.exit(1);
  }

  // 基线：磁盘上是 BEFORE，AI 段是 OLD-KEY
  const oldAI = { enabled: true, api_key: 'OLD-KEY-SHOULD-SURVIVE', base_url: 'https://old.example', model: 'old-model' };
  atomicWrite(CFG, JSON.stringify(seedCfg('BEFORE', oldAI), null, 2));

  const envkit = spawn(EXE, [], { cwd: DIR, env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdp2b' }), stdio: 'ignore' });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await req(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('exe:', EXE);
  console.log('envkit ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(1); }

  const home = (await req(URL_)).body;
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  ok(!!tm, '首页里能拿到本机令牌');
  const tok = tm ? tm[1] : '';
  const H = { 'X-EnvKit-Token': tok };

  const getCfg = async () => {
    const r = await req(URL_ + 'api/config', { headers: H });
    try { return { code: r.code, cfg: JSON.parse(r.body) }; } catch (e) { return { code: r.code, cfg: null }; }
  };

  try {
    console.log('\n[1] 启动后读到的是磁盘上那份');
    let c = await getCfg();
    ok(c.code === 200 && c.cfg, 'GET /api/config 可用');
    ok(c.cfg && c.cfg.app_name === 'BEFORE', 'app_name = BEFORE', c.cfg && c.cfg.app_name);

    console.log('\n[2] 外部改文件 → 内存跟着变（核心断言）');
    const next = seedCfg('AFTER-HOT', { enabled: true, api_key: 'NEW-KEY-FROM-FILE', base_url: 'https://new.example', model: 'new-model' });
    atomicWrite(CFG, JSON.stringify(next, null, 2));
    // 去抖动 600ms + 解析，留 3s 足够；不给足时间会得到假失败
    await sleep(3000);
    c = await getCfg();
    ok(c.cfg && c.cfg.app_name === 'AFTER-HOT', 'app_name 已热重载为 AFTER-HOT', c.cfg && c.cfg.app_name);

    console.log('\n[3] AI 段不参与热重载（必须明确保留旧值）');
    ok(c.cfg && c.cfg.ai && c.cfg.ai.api_key === 'OLD-KEY-SHOULD-SURVIVE',
      'API Key 保持运行值，没被文件里的值覆盖', c.cfg && c.cfg.ai && c.cfg.ai.api_key);
    ok(c.cfg && c.cfg.ai && c.cfg.ai.model === 'old-model', 'model 同样保持运行值', c.cfg && c.cfg.ai && c.cfg.ai.model);

    console.log('\n[4] 自写回环抑制：自己保存配置不该再触发一次重载');
    // POST 一次内容不变的配置（内容与磁盘当前一致）→ 不应产生额外 reload。
    // 判据用"改 A 再改回"这种笨办法会被去抖动合并，所以直接数日志里的 reload 条数。
    const countReload = async () => {
      const r = await req(URL_ + 'api/audit?n=200', { headers: H });
      const j = JSON.parse(r.body);
      if (!j || !Array.isArray(j.entries)) return -1;
      return j.entries.filter(x => String(x.action || '').indexOf('config_reload') >= 0).length;
    };
    let beforeN = -1;
    try { beforeN = await countReload(); } catch (e) {}
    // /api/audit 不可用就不判这一条（不能把"接口不存在"写成"回环没检出"）
    if (beforeN >= 0) {
      await req(URL_ + 'api/config', { method: 'POST', headers: Object.assign({ 'Content-Type': 'application/json' }, H), body: JSON.stringify({ app_name: 'AFTER-HOT' }) });
      await sleep(2500);
      let afterN = beforeN;
      try { afterN = await countReload(); } catch (e) {}
      ok(afterN === beforeN, '自己保存没有引发 config_reload', 'before=' + beforeN + ' after=' + afterN);
      // 自写之后内存仍是保存后的值（没被自己写的内容反向覆盖）
      c = await getCfg();
      ok(c.cfg && c.cfg.app_name === 'AFTER-HOT', '自写后内存值未被反向覆盖', c.cfg && c.cfg.app_name);
    } else {
      console.log('  SKIP  /api/audit 不可用，跳过自写回环的真机断言（单测已覆盖）');
    }

    console.log('\n[5] 坏 JSON 不把配置搞崩');
    atomicWrite(CFG, '{ "app_name": "BROKEN", '); // 半截 JSON
    await sleep(3000);
    c = await getCfg();
    ok(c.code === 200 && c.cfg && c.cfg.app_name === 'AFTER-HOT', '解析失败时继续用内存里的配置', c.cfg && c.cfg.app_name);

    // 收尾：恢复成合法配置，避免留下坏文件影响下一次
    atomicWrite(CFG, JSON.stringify(seedCfg('AFTER-HOT'), null, 2));
    await sleep(1200);
  } finally {
    try { envkit.kill(); } catch (e) {}
  }

  console.log('\n' + (fails ? ('FAILED ' + fails + ' 项') : 'ALL PASS'));
  process.exit(fails ? 1 : 0);
})();
