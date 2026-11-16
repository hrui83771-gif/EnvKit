// CDP 回归（v3.9）：桌宠右键控制菜单重做——分组 / 图标 / 分段选择器。
//
// 这条测试守的不是"看起来好看"，而是重做后三个可验证的事实：
//   1) 四组小标题 + 每项 Lucide 图标真的注入了 DOM（ekIconFill 跑过）；
//   2) 模式 / 大小 / 穿透是"三选一"分段：当前项 aria-pressed=true 且带 .cur，
//      点另一项后状态必须整体迁移（不能两个都亮）；
//   3) 切换鼠标穿透时图标 span 不能被抹掉——旧代码 b.textContent=... 会把
//      整个按钮内容覆盖，是本轮修的真实缺陷；变异 no-icons/no-pressed 各红自己那组。
//
// 断言全部落在 DOM 事实与真实 localStorage 写入上，不看气泡措辞。
// 用法：node tools/cdp_pet_menu_test.js [--mut=no-icons|--mut=no-pressed]
// 隔离：独立端口 18821 + ENVKIT_INSTANCE_TAG + 独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18821;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9361;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_petmenu', 'EnvKit_menu.exe');
const DIR = path.dirname(EXE);
const MUT = process.argv.find(a => a.startsWith('--mut=')) || '--mut=none';

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 每轮强制覆盖 exe：embed 在二进制里，只在"不存在时复制"会让旧副本
  // 一直被测下去（本轮实测踩坑：重建了根 exe，副本还是上一版，假红 3 轮）。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
}

(async () => {
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'petmenu' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_petmenu'),
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
    await sleep(4000);

    // 变异：数据层拦截——把图标 SVG 抹掉 / 把 aria-pressed 拿掉。
    // 故意放在页面加载后、断言前，红的是界面渲染断言而非提前 fatal。
    if (MUT === '--mut=no-icons') {
      await evalJS(`document.querySelectorAll('#pet-ctx .ek svg').forEach(function(s){ s.remove(); }); true`);
    }
    if (MUT === '--mut=no-pressed') {
      await evalJS(`document.querySelectorAll('#pet-ctx [data-seg-item]').forEach(function(b){ b.removeAttribute('aria-pressed'); }); true`);
    }

    // ---- 1) 四组小标题齐全（分组是重做的骨架）----
    // 标题文本会被 i18n 按浏览器语言替换（动作→Action），所以不断言字面中文；
    // 断言「四组 + 每组一个标题 + 标题带图标」，这些是语言无关的 DOM 事实。
    console.log('分组结构：');
    const groups = JSON.parse(await evalJS(`JSON.stringify((function(){
      return Array.from(document.querySelectorAll('#pet-ctx .pet-ctx-grp')).map(function(g){
        var h = g.querySelector('.pet-ctx-h');
        return { hasH: !!h, hasIco: !!(h && h.querySelector('svg')), n: h ? h.textContent.trim().length : 0 };
      });
    })())`));
    ok(groups.length === 4, '菜单分成四组（实际 ' + groups.length + '）');
    ok(groups.every(g => g.hasH && g.n > 0), '每组都有非空小标题');
    ok(groups.every(g => !g.hasIco), '小标题是纯文字（不与图标占位共用 DOM，防文字被抹）');

    // ---- 2) 图标注入：每项按钮都有 SVG，且 data-ek-icon 占位已被消费 ----
    console.log('图标注入：');
    const ico = JSON.parse(await evalJS(`JSON.stringify((function(){
      var bs = Array.from(document.querySelectorAll('#pet-ctx button'));
      return { total: bs.length,
        withSvg: bs.filter(function(b){ return b.querySelector('.ek svg'); }).length,
        leftovers: document.querySelectorAll('#pet-ctx [data-ek-icon]').length };
    })())`));
    // 4 动作（含隐藏的天气）+ 3 模式 + 3 大小 + 1 穿透 + 3 系统 + 1 玩法 = 15
    ok(ico.total === 15, '菜单共 15 个动作项（实际 ' + ico.total + '）');
    ok(ico.withSvg === ico.total, '每项都注入了 Lucide SVG（' + ico.withSvg + '/' + ico.total + '）');
    ok(ico.leftovers === 0, 'data-ek-icon 占位已全部消费（残留 ' + ico.leftovers + '）');

    // ---- 3) 分段选择器：模式三选一，当前项 aria-pressed=true ----
    console.log('分段选择器（模式）：');
    const seg0 = JSON.parse(await evalJS(`JSON.stringify((function(){
      var bs = Array.from(document.querySelectorAll('#pet-ctx [data-seg-item="mode"]'));
      return bs.map(function(b){ return {act:b.dataset.act, p:b.getAttribute('aria-pressed'), cur:b.classList.contains('cur')}; });
    })())`));
    ok(seg0.length === 3, '模式组三项（实际 ' + seg0.length + '）');
    ok(seg0.filter(x => x.p === 'true').length === 1, '有且仅有一项 aria-pressed=true');
    ok(seg0.filter(x => x.p === 'true' && x.cur).length === 1, 'aria-pressed 与 .cur 同向（当前值一眼可见）');

    // 真点击"溜达"：状态必须整体迁移，且写进 localStorage（真实副作用）
    const moved = JSON.parse(await evalJS(`(function(){
      var b = document.querySelector('#pet-ctx [data-act="mode-wander"]');
      b.click();
      var bs = Array.from(document.querySelectorAll('#pet-ctx [data-seg-item="mode"]'));
      return JSON.stringify({
        pressed: bs.map(function(x){ return x.getAttribute('aria-pressed'); }),
        mode: localStorage.getItem('ek-pet-mode')
      });
    })()`));
    ok(moved.pressed.join(',') === 'false,true,false', '点溜达后 pressed 整体迁移（' + moved.pressed.join(',') + '）');
    ok(moved.mode === 'wander', '模式写入 localStorage（实际 ' + moved.mode + '）');

    // ---- 4) 穿透切换：图标不能被抹掉（本轮修的真实缺陷）----
    console.log('穿透切换保图标：');
    const pass = JSON.parse(await evalJS(`(function(){
      var b = document.querySelector('#pet-ctx [data-act="passthrough"]');
      var before = !!b.querySelector('.ek svg');
      b.click();                       // 开穿透 → 文案改成"解除鼠标穿透"
      var after = !!b.querySelector('.ek svg');
      var label = b.querySelector('.pet-ctx-t');
      var txt = label ? label.textContent : '';
      b.click();                       // 关穿透 → 复原
      return JSON.stringify({before:before, after:after,
        txt:txt, restored:b.querySelector('.pet-ctx-t').textContent,
        pressed:b.getAttribute('aria-pressed')});
    })()`));
    ok(pass.before && pass.after, '切换穿透后图标仍在（缺陷回归：旧版 textContent 会抹掉）');
    ok(pass.txt.indexOf('解除') >= 0, '穿透开启时文案变为"解除鼠标穿透"');
    ok(pass.restored.indexOf('鼠标穿透') >= 0, '关闭后文案复原');
    ok(pass.pressed === 'false', '关闭后 aria-pressed=false');

    // ---- 5) 原有分发未破：立即检测仍是真动作（data-act 一个没改）----
    console.log('动作分发：');
    const acts = JSON.parse(await evalJS(`JSON.stringify(
      Array.from(document.querySelectorAll('#pet-ctx button')).map(function(b){ return b.dataset.act; }))`));
    for (const a of ['home', 'hide', 'say', 'weather', 'aierr', 'drawer', 'check', 'tricks', 'passthrough']) {
      ok(acts.indexOf(a) >= 0, '保留 data-act=' + a);
    }
  } catch (e) {
    fails.push('EXCEPTION: ' + e.message);
    console.log('EXCEPTION', e.message);
  } finally {
    if (ws) { try { ws.close(); } catch (e) {} }
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log('\n==== ' + (fails.length ? 'FAIL ' + fails.length : 'ALL PASS') + ' (' + MUT + ') ====');
  process.exit(fails.length ? 1 : 0);
})().catch(e => { console.error('FATAL', e); process.exit(2); });
