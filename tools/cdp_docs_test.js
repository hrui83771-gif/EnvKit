// CDP 回归：独立文档页 /docs + 顶栏搜索入口。
//
// 守的是几个能被用户撞见的事实：
//   1) 侧栏「使用文档」在新标签打开 /docs，不是主界面里切 panel；
//   2) 这个新页面真正独立：没有主控制台的 DOM、没有注入令牌、纯静态；
//   3) 三栏文档站：目录过滤 / 正文表格与步骤 / 右目录联动 / 复制 / 阅读进度；
//   4) 中英双语共享 ek-lang，英文模式文档正文无中文残留；
//   5) 顶栏搜索框打开的是已有 Ctrl+K 面板，搜「文档」能找到入口。
//
// 变异：--mut=no-entry 把侧栏按钮换成没有 data-open-docs 的新节点（点它不开页）；
//       --mut=embed-docs 需配合临时去掉 main.go 的 /docs 路由并重建 exe——
//       此时 /docs 落回主界面 serveIndex，独立页断言必红。
// 用法：node tools/cdp_docs_test.js [--mut=no-entry|--mut=embed-docs]
// 隔离：独立端口 18831 + ENVKIT_INSTANCE_TAG + 独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18831;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9371;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_docs', 'EnvKit_docs.exe');
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
  // 每轮强制覆盖：前端 embed 在 exe 里，只在不存在时复制会一直测旧副本。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
}

async function attach(target) {
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let mid = 0;
  const pending = new Map();
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  };
  const send = (method, params) => new Promise(res => {
    const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params }));
  });
  const evalJS = async expr => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.result && r.result.exceptionDetails) throw new Error('页面异常：' + JSON.stringify(r.result.exceptionDetails));
    return r.result && r.result.result ? r.result.result.value : undefined;
  };
  return { ws, send, evalJS };
}

(async () => {
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'docs' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_docs'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let main = null, docs = null;
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

    main = await attach(page);
    const { send, evalJS } = main;
    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);
    await evalJS(`ekSetLang('zh', true); localStorage.setItem('ek-lang','zh'); true`);
    await sleep(300);

    // ---- 1) 顶栏搜索把手 ----
    console.log('顶栏搜索：');
    const hdr = JSON.parse(await evalJS(`JSON.stringify((function(){
      var b=document.getElementById('btn-search'), p=document.getElementById('btn-profile');
      return { exists:!!b, before: !!(b && p && (b.compareDocumentPosition(p) & 4)),
        kbd: !!(b && b.querySelector('kbd')), icon: !!(b && b.querySelector('.ek svg')),
        kbdText: b && b.querySelector('kbd') ? b.querySelector('kbd').textContent : '' };
    })())`));
    ok(hdr.exists, '个人中心旁存在搜索入口');
    ok(hdr.before, '搜索入口在个人中心左侧（DOM 顺序）');
    ok(hdr.icon && hdr.kbd && /Ctrl/.test(hdr.kbdText), '放大镜图标 + Ctrl+K 键帽都在');

    await evalJS(`document.getElementById('btn-search').click(); true`);
    await sleep(200);
    const cp = JSON.parse(await evalJS(`JSON.stringify((function(){
      var box=document.getElementById('ek-cmd');
      return { open: !!(box && box.classList.contains('show')),
        focus: document.activeElement ? document.activeElement.id : '',
        total: window.EKCmd ? window.EKCmd.state().total : 0 };
    })())`));
    ok(cp.open && cp.focus === 'ekcp-q', '点搜索框打开 Ctrl+K 面板并聚焦搜索框');
    ok(cp.total > 0, '面板索引来自真实控件（' + cp.total + ' 项）');
    const hit = JSON.parse(await evalJS(`(function(){
      var i=document.getElementById('ekcp-q');
      i.value='文档'; i.dispatchEvent(new Event('input',{bubbles:true}));
      return JSON.stringify(Array.from(document.querySelectorAll('#ekcp-list .ekcp-row b')).map(function(x){return x.textContent;}));
    })()`));
    ok(hit.some(x => x.indexOf('使用文档') >= 0), '搜「文档」能找到独立文档入口（' + hit.join('/') + '）');
    await evalJS(`window.EKCmd.close(); true`);

    // ---- 2) 侧栏入口在新标签打开独立页 ----
    console.log('独立页面入口：');
    const entry = JSON.parse(await evalJS(`JSON.stringify((function(){
      var b=document.getElementById('btn-about');
      return { attr: !!(b && b.hasAttribute('data-open-docs')), text: b ? b.textContent.trim() : '',
        panel: !!document.getElementById('panel-docs') };
    })())`));
    ok(entry.attr, '侧栏按钮带 data-open-docs（新标签打开）');
    ok(!entry.panel, '主界面不再内嵌 panel-docs');

    if (MUT === '--mut=no-entry') {
      await evalJS(`(function(){
        var b=document.getElementById('btn-about'); if(!b) return true;
        var n=b.cloneNode(true); n.removeAttribute('data-open-docs');
        b.parentNode.replaceChild(n,b); return true;
      })()`);
    }
    const before = (await getJSON('http://127.0.0.1:' + DBG + '/json/list')).filter(t => t.type === 'page').length;
    await evalJS(`window.__opened=[]; var _o=window.open; window.open=function(u){ window.__opened.push(String(u)); return _o.apply(window, arguments); }; true`);
    // 真鼠标点击：脚本 .click() 没有用户手势，headless Chrome 会拦下弹窗。
    const rect = JSON.parse(await evalJS(`(function(){
      var b=document.getElementById('btn-about'), r=b.getBoundingClientRect();
      return JSON.stringify({x:r.left+r.width/2, y:r.top+r.height/2, w:r.width, h:r.height});
    })()`));
    await send('Input.dispatchMouseEvent', { type:'mouseMoved', x:rect.x, y:rect.y });
    await send('Input.dispatchMouseEvent', { type:'mousePressed', x:rect.x, y:rect.y, button:'left', clickCount:1 });
    await send('Input.dispatchMouseEvent', { type:'mouseReleased', x:rect.x, y:rect.y, button:'left', clickCount:1 });
    await sleep(500);
    const opened = JSON.parse(await evalJS(`JSON.stringify(window.__opened || [])`));
    ok(opened.some(u => u === '/docs'), '侧栏入口调用 window.open("/docs")');
    let docsTarget = null;
    for (let i = 0; i < 30; i++) {
      const list = (await getJSON('http://127.0.0.1:' + DBG + '/json/list')).filter(t => t.type === 'page');
      if (list.length > before) { docsTarget = list.find(t => /\/docs(\.html)?$/.test(t.url)); }
      if (docsTarget) break;
      await sleep(250);
    }
    ok(!!docsTarget, '点侧栏入口真的新开了 /docs 标签页' + (docsTarget ? '' : '（未新增 page target）'));

    // 入口变异时后续几组没有前置条件，明确 SKIP 而不是抛异常提前退出：
    // 红必须来自断言失败，不能来自脚本自己断掉。
    if (docsTarget) {
    docs = await attach(docsTarget);
    await docs.send('Runtime.enable', {});
    await docs.send('Page.enable', {});
    await docs.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    await sleep(1200);
    const djs = docs.evalJS;

    const iso = JSON.parse(await djs(`JSON.stringify((function(){
      return { path: location.pathname,
        title: document.title,
        mainApp: !!document.getElementById('btn-profile'),
        panel: !!document.getElementById('panel-docs'),
        token: typeof window.__EK_TOKEN__,
        sections: document.querySelectorAll('#body .sec').length,
        tabs: document.querySelectorAll('.tbl').length,
        steps: document.querySelectorAll('.steps').length };
    })())`));
    ok(iso.path === '/docs', '新标签地址是 /docs（实际 ' + iso.path + '）');
    ok(!iso.mainApp && !iso.panel, '独立页没有主控制台 DOM（不是切 panel）');
    ok(iso.token === 'undefined', '独立页不注入 API 令牌（纯静态）');
    ok(iso.sections >= 20, '文档正文有 ' + iso.sections + ' 节（丰富度）');
    ok(iso.tabs >= 8 && iso.steps >= 1, '正文含真实表格与步骤卡片（' + iso.tabs + '/' + iso.steps + '）');

    // ---- 3) 三栏结构 ----
    console.log('文档站结构：');
    const st = JSON.parse(await djs(`JSON.stringify({
      tree: document.querySelectorAll('#tree button[data-sec]').length,
      groups: document.querySelectorAll('#tree .grp').length,
      gcounts: Array.from(document.querySelectorAll('#tree .grp .gc')).map(function(x){return x.textContent;}),
      toc: document.querySelectorAll('#toc a[data-anchor]').length,
      tocSub: document.querySelectorAll('#toc a[data-anchor].sub').length,
      tocGrp: document.querySelectorAll('#toc .toc-grp').length,
      quick: document.querySelectorAll('#quick a').length,
      cols: getComputedStyle(document.querySelector('.layout')).gridTemplateColumns.split(' ').length,
      lead: document.getElementById('lead-t').textContent
    })`));
    ok(st.cols === 3, '宽屏是三栏布局（实际 ' + st.cols + '）');
    ok(st.tree >= 20 && st.tree === iso.sections, '左目录与正文小节一一对应（' + st.tree + '/' + iso.sections + '）');
    ok(st.groups >= 7, '目录按 7 组分类（实际 ' + st.groups + ' 组）');
    ok(st.gcounts.length === st.groups && st.gcounts.every(function(n){return parseInt(n,10) >= 1;}),
       '每个分组都显示本章节数（' + st.gcounts.join('/') + '）');
    ok(st.toc >= st.tree, '右目录来自正文真实锚点（' + st.toc + '）');
    ok(st.tocSub >= iso.sections, '每节都有子标题进入右目录（' + st.tocSub + ' 个 H3 ≥ ' + iso.sections + ' 节）');
    ok(st.tocGrp === st.groups, '右目录按组分节（' + st.tocGrp + ' 组）');
    ok(st.quick === st.groups, '快速跳转按分组取代表节（' + st.quick + ' 项 = ' + st.groups + ' 组）');
    ok(!!st.lead, '封面有引导语（' + st.lead.slice(0, 20) + '）');

    try {
      fs.mkdirSync(path.join(REPO, '.gotmp', 'ux-preview'), { recursive: true });
      const shot = await docs.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      fs.writeFileSync(path.join(REPO, '.gotmp', 'ux-preview', '06-docs-standalone.png'),
        Buffer.from(shot.result.data, 'base64'));
    } catch (e) {}

    // ---- 4) 过滤 / 跳转 / 复制 / 阅读进度 ----
    console.log('文档交互：');
    const filt = JSON.parse(await djs(`(function(){
      var i=document.getElementById('q');
      i.value='TOFU'; i.dispatchEvent(new Event('input',{bubbles:true}));
      var bs=Array.from(document.querySelectorAll('#tree button[data-sec]')).filter(function(b){return !b.hidden;});
      return JSON.stringify({n:bs.length, txt:bs.map(function(b){return b.textContent;})});
    })()`));
    ok(filt.n > 0 && filt.n < st.tree, '搜索真能收窄目录（' + filt.n + '/' + st.tree + '）');
    ok(filt.txt.some(x => x.indexOf('链端') >= 0), '按正文命中而不是只按标题（TOFU 只在正文里）');
    await djs(`(function(){var i=document.getElementById('q'); i.value=''; i.dispatchEvent(new Event('input',{bubbles:true})); return true;})()`);

    const sc = JSON.parse(await djs(`(function(){
      var b=document.querySelectorAll('#tree button[data-sec]')[4], id=b.dataset.sec;
      b.click();
      var h=document.getElementById('doc-'+id);
      return JSON.stringify({id:id, on:b.classList.contains('on'), top:h.getBoundingClientRect().top});
    })()`));
    await sleep(700);
    const scTop = await djs(`(function(){var h=document.getElementById('doc-${sc.id}');return h?h.getBoundingClientRect().top:9999;})()`);
    ok(sc.on && Math.abs(scTop) < 90, '点左目录滚到对应正文（' + sc.id + '，偏移 ' + Math.round(scTop) + '）');

    await djs(`(function(){var a=document.querySelector('#toc a[data-anchor]'); a.click(); return true;})()`);
    await sleep(700);
    const tocOn = await djs(`!!document.querySelector('#toc a[data-anchor].on')`);
    ok(tocOn, '点右目录有高亮反馈');

    await djs(`document.querySelector('#body .cp').click(); true`);
    await sleep(300);
    const copied = await djs(`document.querySelector('#body .cp').textContent`);
    ok(copied !== '复制' && copied.length > 0, '代码块复制按钮给出可见反馈（' + copied + '）');

    await djs(`window.scrollTo({top:document.documentElement.scrollHeight, behavior:'instant'}); true`);
    await sleep(400);
    const prog = await djs(`parseFloat(document.getElementById('prog').style.width) || 0`);
    ok(prog > 90, '滚动到底阅读进度接近满（' + Math.round(prog) + '%）');
    const totop = await djs(`document.getElementById('totop').classList.contains('show')`);
    ok(totop, '滚动后出现返回顶部按钮');

    // ---- 5) 双语：与主控制台共享 ek-lang ----
    console.log('英文模式：');
    await djs(`document.getElementById('lang').click(); true`);
    await sleep(500);
    const en = JSON.parse(await djs(`JSON.stringify((function(){
      var s=document.getElementById('body').textContent + document.getElementById('tree').textContent +
            document.getElementById('toc').textContent;
      // 安装.bat 是仓库里真实的文件名，英文文案里也必须照写，不算漏翻译。
      s = s.replace(/安装\.bat/g, '');
      return { cjk: /[\\u4e00-\\u9fa5]/.test(s), sample:(s.match(/[\\u4e00-\\u9fa5]+/g)||[]).slice(0,6).join('|'),
        stored: localStorage.getItem('ek-lang'), path:location.pathname };
    })())`));
    ok(!en.cjk, '切英文后文档正文 / 目录无中文（' + en.sample + '）');
    ok(en.stored === 'en', '语言选择写入 ek-lang，与主控制台共享');
    await djs(`document.getElementById('lang').click(); localStorage.setItem('ek-lang','zh'); true`);
    await sleep(300);

    } else {
      console.log('  SKIP 独立页未打开，结构 / 交互 / 英文三组跳过（前置条件不满足）');
    }

    // ---- 6) 关于弹窗与重看引导仍在 ----
    console.log('关于入口：');
    await evalJS(`document.getElementById('about').classList.add('show'); true`);
    await sleep(150);
    const ab = JSON.parse(await evalJS(`JSON.stringify({
      shown: document.getElementById('about').classList.contains('show'),
      replay: !!document.getElementById('about-replay'),
      docs: !!document.getElementById('about-docs')
    })`));
    ok(ab.shown && ab.replay && ab.docs, '主界面关于弹窗保留，重看引导 / 文档按钮都在');
    await evalJS(`document.getElementById('about-close').click(); true`);
  } catch (e) {
    fails.push('EXCEPTION: ' + e.message);
    console.log('EXCEPTION', e.message);
  } finally {
    if (docs) { try { docs.ws.close(); } catch (e) {} }
    if (main) { try { main.ws.close(); } catch (e) {} }
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log('\n==== ' + (fails.length ? 'FAIL ' + fails.length : 'ALL PASS') + ' (' + MUT + ') ====');
  process.exit(fails.length ? 1 : 0);
})().catch(e => { console.error('FATAL', e); process.exit(2); });
