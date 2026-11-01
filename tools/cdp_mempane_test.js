// CDP 回归：记忆与经验面板的布局（v3.7.1 重排）
//
// ## 这条测试要防的是什么
//
// 用户报："记忆这里面板有点拥挤，核心的记忆输入就那么一点大，试算我感觉没啥必要"。
// 原布局把「输入框 + 标签 + 两个勾选 + 添加」全塞进一行 flex，输入框只剩下
// 一指宽；下面还压着「批量导入/刷新」一行和「试算」一行，真正要写的那句话
// 又小又挤。改法是：输入改成整块多行 textarea，选项降到它下面一行，
// 批量导入/刷新上移到标题右侧，试算整行删除。
//
// 布局问题靠"读源码"是测不出来的：DOM 结构全对、CSS 被别的规则挤扁，
// 断言照样全绿。所以这里量**真实盒子**（getBoundingClientRect）：
//   - 输入框必须横跨整张卡（吃掉面板宽度），而不是被后面的控件挤成一条
//   - 选项行必须在输入框**下方**，且四个控件在同一行（y 相近）
//   - 添加按钮右对齐到输入框右边缘（不然看着像漏在中间的孤儿按钮）
//   - 试算的输入框/按钮/提示三件套必须**都不存在**
//
// 另外补一条功能断言：多行框里带换行的内容，落库时必须压成一行
// （记忆是按 "- 内容" 单行拼进提示词的，换行会把一条记忆炸成几行碎句）。
//
// 用法：
//   1) go build -o tools\_mem\EnvKit_mem.exe .
//   2) node tools/cdp_mempane_test.js
//
// 隔离：独立端口 18798 + ENVKIT_INSTANCE_TAG + exe 旁独立数据目录，
// 不碰用户自己的 config / memories.json。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9348;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_mem', 'EnvKit_mem.exe');
const DIR = path.dirname(EXE);

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 隔离目录跑完就删（见文件末尾的收尾）：exe 不预置，缺了就从仓库根那份复制一次，
  // 这样"删干净"之后下一次跑不必再手动 go build。
  if (!fs.existsSync(EXE)) { try { fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE); } catch (e) {} }
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    ai: { memory_on: true },
  }, null, 2), 'utf8');
  // 从空记忆开始：断言里要数条数，残留的旧条目会让期望值飘
  try { fs.unlinkSync(path.join(DIR, 'memories.json')); } catch (e) {}
}

(async () => {
  if (!fs.existsSync(EXE)) {
    // 隔离目录跑完就删（见文件末尾）：exe 不预置，缺了就从仓库根那份复制一次，
    // 这样「删干净」之后下一次跑不必再手动 go build。
    fs.mkdirSync(DIR, { recursive: true });
    fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  }
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'mem' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_mem'),
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
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

    // 打开 AI 面板 → 记忆与经验（这就是用户实际点的两步）
    await evalJS(`goto('ai')`);
    await sleep(400);
    await evalJS(`document.getElementById('btn-ai-memory').click()`);
    await sleep(900);

    const m = JSON.parse(await evalJS(`(function(){
      var q = function(s){ return document.querySelector(s); };
      var r = function(el){ if(!el) return null; var b = el.getBoundingClientRect();
        return {x:Math.round(b.left), y:Math.round(b.top), w:Math.round(b.width), h:Math.round(b.height), right:Math.round(b.right)}; };
      var box = q('#ai-memory-box'), ta = q('#mem-text');
      return JSON.stringify({
        visible: !!box && box.getBoundingClientRect().height > 40,
        tag: ta ? ta.tagName : null,
        boxW: box ? Math.round(box.getBoundingClientRect().width) : 0,
        ta: r(ta), tags: r(q('#mem-tags')), alw: r(q('#mem-always')),
        glb: r(q('#mem-global')), addb: r(q('#mem-add')),
        sum: (q('#mem-sum')||{}).textContent || '',
        legacy: [!!q('#mem-preview'), !!q('#mem-preview-task'), !!q('#mem-preview-hint')],
        hintText: (document.querySelector('.mp-tip')||{}).textContent || ''
      });
    })()`));

    ok(m.visible, '点「记忆与经验」后面板展开');
    if (!m.visible || !m.ta) throw new Error('面板没打开，后面的盒子断言无意义');

    // ---- 布局：输入框是主角 ----
    ok(m.tag === 'TEXTAREA', '输入框是多行 textarea（不是单行 input）→ ' + m.tag);
    ok(m.ta.w >= m.boxW - 80, '输入框横跨整张卡：宽 ' + m.ta.w + ' / 卡 ' + m.boxW + '（旧版被挤到一指宽）');
    ok(m.ta.h >= 56, '输入框高度够写一句话：' + m.ta.h + 'px（旧版约 30px）');
    ok(m.ta.h > m.tags.h * 1.5, '输入框明显高于选项控件：' + m.ta.h + ' vs ' + m.tags.h);

    // ---- 布局：选项行在输入框下方、同一行、右对齐 ----
    ok(m.tags.y >= m.ta.y + m.ta.h - 2, '标签框在输入框下方（y ' + m.tags.y + ' ≥ ' + (m.ta.y + m.ta.h) + '）');
    ok(Math.abs(m.alw.y - m.tags.y) <= 12 && Math.abs(m.glb.y - m.tags.y) <= 12 && Math.abs(m.addb.y - m.tags.y) <= 12,
      '标签 / 每轮都注入 / 全局适用 / 添加 四个控件在同一行（y=' + m.tags.y + '/' + m.alw.y + '/' + m.glb.y + '/' + m.addb.y + '）');
    ok(Math.abs(m.addb.right - m.ta.right) <= 8, '添加按钮右对齐输入框右边缘（' + m.addb.right + ' vs ' + m.ta.right + '）');
    ok(m.tags.x <= m.ta.x + 2, '标签框贴着左边起始（' + m.tags.x + ' vs ' + m.ta.x + '）');
    ok(m.addb.w >= 64, '添加按钮写全「添加记忆」而不是两个字：宽 ' + m.addb.w);

    // ---- 试算三件套必须都没了 ----
    ok(!m.legacy[0] && !m.legacy[1] && !m.legacy[2], '试算的输入框 / 按钮 / 提示都不存在');
    ok(m.hintText.indexOf('批量导入') >= 0, '原来挤在按钮行的那句导入格式说明改挂在输入区下方');

    // ---- 功能：多行内容必须压成一行落库 ----
    // 行数不能直接相减：空表时有一行「还没有记忆条目」的占位行，加一条后
    // 占位行被真实行替换，总行数不变（0 行差），相减得到的"新增 0 行"是假信号。
    const before = JSON.parse(await evalJS(`document.querySelectorAll('#mem-body tr').length`));
    await evalJS(`(function(){
      var t = document.getElementById('mem-text');
      t.value = 'CDP布局探针\\n第二行不该独立成条';
      document.getElementById('mem-add').click();
    })()`);
    await sleep(1200);
    const after = JSON.parse(await evalJS(`(function(){
      var tr = [].slice.call(document.querySelectorAll('#mem-body tr'))
        .filter(function(x){ return x.textContent.indexOf('CDP布局探针') >= 0; });
      var opTd = tr.length ? tr[0].lastElementChild : null;
      return JSON.stringify({
        n: document.querySelectorAll('#mem-body tr').length,
        found: tr.length,
        text: tr.length ? tr[0].children[1].textContent : '',
        sum: (document.getElementById('mem-sum')||{}).textContent || '',
        op: opTd ? {c: opTd.clientWidth, s: opTd.scrollWidth} : null
      });
    })()`));
    ok(after.found === 1, '点「添加记忆」后表格里出现这条，且只有一条（找到 ' + after.found + ' 行）');
    // 操作列塞着「改为按需注入 + 删除」两个 nowrap 按钮。列宽不够时 td 有
    // overflow:hidden，删除按钮会被切掉半个（看着像没法点）——量 scrollWidth 才算数。
    ok(after.op && after.op.s <= after.op.c + 1,
      '操作列不切按钮：内容 ' + (after.op && after.op.s) + ' / 列宽 ' + (after.op && after.op.c));
    ok(after.text.indexOf('第二行不该独立成条') >= 0 && after.text.indexOf('\n') < 0,
      '换行被压成空格，一条记忆没被炸成两行 → ' + JSON.stringify(after.text));
    ok(/共\s*1\s*条/.test(after.sum) || after.sum.indexOf('1') >= 0, '统计条跟着更新 → ' + JSON.stringify(after.sum));

    // ---- 收尾：删掉探针条目（隔离实例也要留干净的现场） ----
    await evalJS(`(function(){
      window.confirm = function(){ return true; };
      var tr = [].slice.call(document.querySelectorAll('#mem-body tr'))
        .filter(function(x){ return x.textContent.indexOf('CDP布局探针') >= 0; })[0];
      if(!tr) return;
      var btns = tr.querySelectorAll('button');
      btns[btns.length - 1].click();   // 最后一个按钮 = 删除
    })()`);
    await sleep(1000);
    const left = JSON.parse(await evalJS(`(function(){
      return JSON.stringify({
        n: document.querySelectorAll('#mem-body tr').length,
        probe: [].slice.call(document.querySelectorAll('#mem-body tr'))
          .filter(function(x){ return x.textContent.indexOf('CDP布局探针') >= 0; }).length
      });
    })()`));
    ok(left.probe === 0 && left.n === before, '探针条目已删除，表格回到初始状态（行数 ' + left.n + '，探针残留 ' + left.probe + '）');
  } catch (e) {
    fails.push('异常：' + e.message);
    console.log('  FAIL 异常：' + e.message);
  } finally {
    try { ws && ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log('');
  console.log(fails.length ? ('失败 ' + fails.length + ' 条') : '全部通过');
  // 成功即删隔离目录：每个测试都在 DIR 里复制一份 exe 加一个 Chrome profile，
  // 不删就会累积（2026-11-01 实测 tools/ 下 17 个目录共 255MB）。
  // 失败时保留现场 —— 那时 exe / config / 日志本身就是证据。CDP_KEEP=1 强制保留。
  if (fails.length === 0 && !process.env.CDP_KEEP) {
    try { fs.rmSync(DIR, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch (e) {}
  }
  /* cdpCleanupMarker */
  process.exit(fails.length ? 1 : 0);
})();
