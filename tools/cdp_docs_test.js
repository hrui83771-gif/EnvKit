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
      // 文档页会自己补 #doc-<id> 锚点（地址栏可分享），匹配时要把 hash 算进去。
      if (list.length > before) { docsTarget = list.find(t => /\/docs(\.html)?(#.*)?$/.test(t.url)); }
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

    // 逐节切换统计：分页后正文只有一节，全量规模要从每个页面里累加。
    const iso = JSON.parse(await djs(`JSON.stringify((function(){
      var btns=[].slice.call(document.querySelectorAll('#tree button[data-sec]'));
      var ids=btns.map(function(b){ return b.dataset.sec; });
      var tabs=0, steps=0, xlinks=[], txt='';
      btns.forEach(function(b){
        b.click();
        tabs += document.querySelectorAll('#body .tbl').length;
        steps += document.querySelectorAll('#body .steps').length;
        txt += ' ' + document.getElementById('body').textContent;
        [].slice.call(document.querySelectorAll('#body .xlink a')).forEach(function(a){ xlinks.push(a.getAttribute('href')); });
      });
      return { path: location.pathname,
        title: document.title,
        mainApp: !!document.getElementById('btn-profile'),
        panel: !!document.getElementById('panel-docs'),
        token: typeof window.__EK_TOKEN__,
        sections: btns.length,
        ids: ids,
        glossary: ids.indexOf('glossary') >= 0 && txt.indexOf('共识视图') >= 0,
        leadNav: document.querySelectorAll('#lead-nav a, .lead-nav a').length,
        perPage: document.querySelectorAll('#body .sec').length,
        tabs: tabs, steps: steps, xlinks: xlinks.length,
        validLinks: xlinks.every(function(h){ return ids.indexOf(String(h).replace('#doc-','')) >= 0; }),
        tools: ['get_system_state','verify_environment','db_query','read_doc','sync_chain_ip']
                 .filter(function(n){ return txt.indexOf(n) >= 0; }).length,
        paths: ['/opt/fisco/nodes','start_all.sh','18766','MySQL80','18765']
                 .filter(function(n){ return txt.indexOf(n) >= 0; }).length,
        trouble: txt.indexOf('排障索引') >= 0,
        upgrade: ids.indexOf('upgrade') >= 0 &&
          /覆盖升级|回滚|卸载/.test(txt),
        stale26: /26 个(受控)?工具/.test(txt) };
    })())`));
    ok(iso.path === '/docs', '新标签地址是 /docs（实际 ' + iso.path + '）');
    ok(!iso.mainApp && !iso.panel, '独立页没有主控制台 DOM（不是切 panel）');
    ok(iso.token === 'undefined', '独立页不注入 API 令牌（纯静态）');
    ok(iso.sections >= 21, '文档共 ' + iso.sections + ' 节（含升级卸载节）');
    ok(iso.perPage === 1, '正文一次只渲染一节（实际 ' + iso.perPage + ' 节）');
    ok(iso.tabs >= 12 && iso.steps >= 5, '正文含真实表格与步骤卡片（' + iso.tabs + '/' + iso.steps + '）');
    ok(iso.trouble, '有按症状聚合的排障索引节');
    ok(iso.glossary, '有术语对照节，解释自造词');
    ok(iso.upgrade, '有升级/回滚/卸载节，覆盖覆盖升级、回滚与卸载');
    ok(iso.leadNav === 0,
       '页头不留快速跳转，篇幅还给导语（lead-nav 链接 ' + iso.leadNav + ' 条）');
    ok(iso.tools === 5, 'AI 节列出真实注册工具名（' + iso.tools + '/5）');
    ok(iso.paths === 5, '操作节给出真实字段值 / 端口 / 示例路径（' + iso.paths + '/5）');
    ok(iso.xlinks >= 7 && iso.validLinks, '节间交叉链接全部指向存在的节（' + iso.xlinks + ' 条）');
    ok(!iso.stale26, '没有与真实工具数漂移的旧「26 个工具」表述');

    // 升级/卸载节 + 日志导出表 + 版本提示：切到对应节实测渲染结果。
    const upd = JSON.parse(await djs(`JSON.stringify((function(){
      var out = {};
      document.querySelector('#tree button[data-sec="upgrade"]').click();
      var ut = document.getElementById('body').textContent;
      out.upgradeH3 = [].slice.call(document.querySelectorAll('#body .sec h3')).map(function(h){return h.textContent;});
      out.hasOverwrite = ut.indexOf('覆盖升级') >= 0;
      out.hasRollback = ut.indexOf('回滚') >= 0;
      out.hasUninstall = ut.indexOf('卸载') >= 0 && ut.indexOf('MySQL') >= 0;
      out.hasConfigSafe = ut.indexOf('config.json') >= 0;
      document.querySelector('#tree button[data-sec="start"]').click();
      var st = document.getElementById('body').textContent;
      out.logFiles = /envkit-<日期>|envkit-.*\\.log/.test(st) && st.indexOf('5 MB') >= 0;
      out.exportTable = st.indexOf('是否脱敏') >= 0 && st.indexOf('原样导出') >= 0;
      document.querySelector('#tree button[data-sec="about"]').click();
      out.versionNote = document.getElementById('body').textContent.indexOf('适用') >= 0;
      out.verText = document.getElementById('ver').textContent.trim();
      return out;
    })())`));
    ok(upd.hasOverwrite && upd.hasRollback && upd.hasUninstall && upd.hasConfigSafe,
       '升级节覆盖覆盖升级 / 回滚 / 卸载边界 / config 保留四项');
    ok(upd.upgradeH3.length >= 4, '升级节有小节导航（' + upd.upgradeH3.length + ' 个 h3）');
    ok(upd.logFiles && upd.exportTable, '日志节写明落盘文件名、配额与导出是否脱敏');
    ok(upd.versionNote && /\d+\.\d+\.\d+/.test(upd.verText),
       '关于节有版本适用提示，页头版本号是真实版本（' + upd.verText + '）');

    // 遍历停在最后一节，回到第一节再验布局。
    await djs(`document.querySelector('#tree button[data-sec]').click(); true`);
    await sleep(250);

    // ---- 3) 三栏结构 + 右栏只服务当前节 ----
    console.log('文档站结构：');
    const st = JSON.parse(await djs(`JSON.stringify({
      tree: document.querySelectorAll('#tree button[data-sec]').length,
      groups: document.querySelectorAll('#tree .grp').length,
      gcounts: Array.from(document.querySelectorAll('#tree .grp .gc')).map(function(x){return x.textContent;}),
      toc: document.querySelectorAll('#toc a[data-anchor]').length,
      pageH3: document.querySelectorAll('#body .sec h3').length,
      tocInPage: Array.prototype.every.call(document.querySelectorAll('#toc a[data-anchor]'), function(a){
        var el=document.getElementById(a.dataset.anchor); return !!el && !!el.closest('#body'); }),
      quick: document.querySelectorAll('#quick a').length,
      cols: getComputedStyle(document.querySelector('.layout')).gridTemplateColumns.split(' ').length,
      lead: document.getElementById('lead-t').textContent,
      leadShown: getComputedStyle(document.querySelector('.lead')).display !== 'none'
    })`));
    ok(st.cols === 3, '宽屏是三栏布局（实际 ' + st.cols + '）');
    ok(st.tree >= 21 && st.tree === iso.sections, '左目录与总节数一一对应（' + st.tree + '/' + iso.sections + '）');
    ok(st.groups >= 7, '目录按 7 组分类（实际 ' + st.groups + ' 组）');
    ok(st.gcounts.length === st.groups && st.gcounts.every(function(n){return parseInt(n,10) >= 1;}),
       '每个分组都显示本章节数（' + st.gcounts.join('/') + '）');
    ok(st.toc === st.pageH3 + 1 && st.tocInPage,
      '右栏「此页面」只列当前节（' + st.toc + ' 项 = ' + st.pageH3 + ' 个 H3 + 节标题）');
    ok(st.quick === st.groups, '快速跳转按分组取代表节（' + st.quick + ' 项 = ' + st.groups + ' 组）');
    ok(!!st.lead && st.leadShown, '第一节有引导语（' + st.lead.slice(0, 16) + '）');

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

    // 点左目录＝换页，不是滚到页面某处
    const sc = JSON.parse(await djs(`(function(){
      var b=document.querySelectorAll('#tree button[data-sec]')[4], id=b.dataset.sec;
      b.click();
      return JSON.stringify({id:id, on:b.classList.contains('on'),
        cur:document.querySelector('#body .sec h2').id, hash:location.hash,
        secs:document.querySelectorAll('#body .sec').length});
    })()`));
    await sleep(300);
    ok(sc.on && sc.cur === 'doc-' + sc.id && sc.hash === '#doc-' + sc.id && sc.secs === 1,
      '点左目录换到对应节并改写地址（' + sc.id + '）');

    await djs(`(function(){var a=document.querySelector('#toc a[data-anchor]'); a.click(); return true;})()`);
    await sleep(400);
    const tocOn = await djs(`!!document.querySelector('#toc a[data-anchor].on')`);
    ok(tocOn, '点右目录有高亮反馈');

    await djs(`document.querySelector('#body .cp').click(); true`);
    await sleep(300);
    const copied = await djs(`document.querySelector('#body .cp').textContent`);
    ok(copied !== '复制' && copied.length > 0, '代码块复制按钮给出可见反馈（' + copied + '）');

    // 进度条改成「读到第几节」：分页后滚动进度没有意义。
    await djs(`document.querySelector('#tree button[data-sec="chain"]').click(); true`);
    await sleep(300);
    await djs(`window.scrollTo({top:document.documentElement.scrollHeight, behavior:'instant'}); true`);
    await sleep(400);
    const prog = JSON.parse(await djs(`JSON.stringify((function(){
      var bs=[].slice.call(document.querySelectorAll('#tree button[data-sec]'));
      var i=bs.findIndex(function(b){ return b.classList.contains('on'); });
      return { pct: parseFloat(document.getElementById('prog').style.width)||0, idx:i+1 };
    })())`));
    ok(Math.abs(prog.pct - 100 * prog.idx / iso.sections) < 2,
      '进度条反映读到第几节（第 ' + prog.idx + ' 节 → ' + Math.round(prog.pct) + '%）');
    const totop = await djs(`document.getElementById('totop').classList.contains('show')`);
    ok(totop, '长节滚到底出现返回顶部按钮');

    // ---- 5) 检索质量 / 目录跟随 / 锚点分享 / 上下篇 / FAQ 折叠 / 速查页 ----
    console.log('文档可用性：');
    await djs(`window.scrollTo({top:0,behavior:'instant'}); true`);
    await sleep(300);
    const sh = JSON.parse(await djs(`(function(){
      var i=document.getElementById('q');
      i.value='TOFU'; i.dispatchEvent(new Event('input',{bubbles:true}));
      var hs=document.getElementById('hits');
      var rows=Array.prototype.slice.call(hs.querySelectorAll('.hit'));
      return JSON.stringify({ hitsShown: !hs.hidden && getComputedStyle(hs).display!=='none',
        treeHidden: getComputedStyle(document.getElementById('tree')).display==='none',
        n: rows.length,
        marks: rows.filter(function(r){ return !!r.querySelector('mark'); }).length,
        markTxt: (rows[0] && rows[0].querySelector('mark')) ? rows[0].querySelector('mark').textContent : '',
        snippet: rows[0] ? rows[0].querySelector('.hit-p').textContent : '',
        snips: rows.map(function(r){ return r.querySelector('.hit-p').textContent.length; }),
        overflow: rows.some(function(r){ return r.getBoundingClientRect().right > hs.getBoundingClientRect().right + 1; }),
        hasWhere: !!(rows[0] && rows[0].querySelector('.hit-where')) });
    })()`));
    ok(sh.hitsShown && sh.treeHidden, '搜索时左栏换成命中列表，目录收起');
    ok(sh.n >= 2 && sh.marks === sh.n, '每条命中都带原文片段并高亮关键词（' + sh.n + ' 条 / ' + sh.marks + ' 处高亮）');
    ok(/TOFU/i.test(sh.snippet) && sh.snippet.length >= 12, '片段来自正文而不是只有标题（' + (sh.snippet || '').slice(0, 24) + '…）');
    ok(!sh.overflow, '命中条不溢出左栏（正文区没被盖住）');
    ok(Math.min.apply(null, sh.snips) >= 16 && Math.max.apply(null, sh.snips) >= 30,
      '片段带上关键词前后的上下文，不是只剩一个词（' + sh.snips.join('/') + ' 字）');
    ok(!/use a private key|Trust on first use/i.test(sh.snippet), '中文模式的片段不混入英文原文');

    const hitJump = JSON.parse(await djs(`(function(){
      var r=document.querySelector('#hits .hit'); var go=r.getAttribute('data-go');
      r.click();
      return JSON.stringify({go:go, hash:location.hash,
        qv:document.getElementById('q').value,
        hitsHidden:document.getElementById('hits').hidden,
        treeHidden:document.getElementById('tree').hidden});
    })()`));
    await sleep(800);
    const hitTop = await djs(`(function(){var h=document.getElementById('${hitJump.go}');return h?h.getBoundingClientRect().top:9999;})()`);
    ok(hitJump.hash === '#' + hitJump.go && Math.abs(hitTop) < 90,
      '点命中条直接滚到锚点并改写 hash（' + hitJump.go + '，偏移 ' + Math.round(hitTop) + '）');
    ok(hitJump.qv === '' && hitJump.hitsHidden && !hitJump.treeHidden,
      '点命中条跳转后搜索框自动清空、命中列表收起、目录恢复（不必手动删字）');
    await sleep(100);

    // 搜索补洞：命令行也要能搜到；空格分词按 AND；+N 真能展开；↑↓/Enter 能选能跳。
    const sx = JSON.parse(await djs(`(function(){
      var i=document.getElementById('q'), hs=document.getElementById('hits');
      function search(w){
        i.value=w; i.dispatchEvent(new Event('input',{bubbles:true}));
        var rows=[].slice.call(hs.querySelectorAll('.hit'));
        return { all: rows.length,
                 visible: rows.filter(function(r){ return !r.hidden; }).length,
                 more: hs.querySelectorAll('.hit-more').length,
                 marks: [].slice.call(hs.querySelectorAll('mark')).map(function(m){ return m.textContent; }),
                 text: hs.textContent, none: !!hs.querySelector('.none') };
      }
      var code=search('web_build');
      var multi=search('端口 占用');
      var nomatch=search('端口 zzzzznotfound');
      var expand=search('配置');
      var moreBefore=expand.more, visibleBefore=expand.visible;
      var m=hs.querySelector('.hit-more');
      if(m) m.click();
      var visibleAfter=hs.querySelectorAll('.hit:not([hidden])').length;
      var moreAfter=hs.querySelectorAll('.hit-more:not([hidden])').length;
      var key=search('TOFU');
      document.getElementById('q').focus();
      document.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
      document.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true}));
      var list=[].slice.call(hs.querySelectorAll('.hit:not([hidden])'));
      var selIdx=list.findIndex(function(r){ return r.classList.contains('sel'); });
      var selGo=selIdx>=0 ? list[selIdx].getAttribute('data-go') : '';
      document.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}));
      var afterEnter={qv:document.getElementById('q').value,
        hitsHidden:document.getElementById('hits').hidden,
        treeHidden:document.getElementById('tree').hidden};
      return JSON.stringify({code:code,multi:multi,nomatch:nomatch,
        expand:{before:visibleBefore,after:visibleAfter,moreBefore:moreBefore,moreAfter:moreAfter},
        key:{selIdx:selIdx,selGo:selGo,hash:location.hash,after:afterEnter}});
    })()`));
    ok(sx.code.all > 0 && sx.code.text.indexOf('python tools/web_build.py') >= 0,
      '命令行参与搜索（web_build 命中源码构建命令）');
    ok(sx.multi.all > 0 && sx.multi.marks.indexOf('端口') >= 0 && sx.multi.marks.indexOf('占用') >= 0,
      '空格分词按 AND 命中并分别高亮（' + sx.multi.marks.join('/') + '）');
    ok(sx.nomatch.all === 0 && sx.nomatch.none, '两词有一个不匹配就不出命中');
    ok(sx.expand.moreBefore > 0 && sx.expand.after > sx.expand.before &&
       sx.expand.moreAfter === sx.expand.moreBefore - 1,
      '+N 是真按钮，点了展开全部命中（可见 ' + sx.expand.before + '→' + sx.expand.after + '）');
    ok(sx.key.selIdx === 1 && sx.key.hash === '#' + sx.key.selGo,
      '↓↓ 选中第二条命中，Enter 跳到对应锚点（' + sx.key.selGo + '）');
    ok(sx.key.after.qv === '' && sx.key.after.hitsHidden && !sx.key.after.treeHidden,
      'Enter 跳转后同样自动清空搜索框并恢复目录');
    // 键盘测试跳到了别的节，后面还要验链节标题的复制按钮，必须先回到链节。
    await djs(`(function(){var i=document.getElementById('q'); i.value=''; i.dispatchEvent(new Event('input',{bubbles:true})); document.querySelector('#tree button[data-sec="chain"]').click(); return true;})()`);
    await sleep(250);

    // 右栏「此页面」：滚到本节靠后的小节时，高亮要落在本节这条上
    await djs(`(function(){
      var hs=document.querySelectorAll('#body .sec h3');
      if(hs.length) window.scrollTo({top:hs[hs.length-1].getBoundingClientRect().top+window.scrollY-14,behavior:'instant'});
      return true;
    })()`);
    await sleep(500);
    const tocKeep = JSON.parse(await djs(`(function(){
      var a=document.querySelector('#toc a.on');
      if(!a) return JSON.stringify({on:false});
      var el=document.getElementById(a.dataset.anchor);
      return JSON.stringify({on:true, inSec: !!(el && el.closest('#body')),
        name:a.textContent, isH3:a.classList.contains('sub')});
    })()`));
    ok(tocKeep.on && tocKeep.inSec, '滚动时高亮落在本节的小节上（' + (tocKeep.name || '') + '）');

    const lk = JSON.parse(await djs(`(function(){
      var b=document.querySelector('#doc-chain .lk'); b.click();
      return JSON.stringify({hash:location.hash, title:b.getAttribute('title')});
    })()`));
    await sleep(300);
    const lkDone = await djs(`(function(){
      var b=document.querySelector('#doc-chain .lk');
      return JSON.stringify({done:b.classList.contains('done'), txt:b.textContent});
    })()`);
    const lkOk = JSON.parse(lkDone);
    ok(lkOk.done && lk.hash === '#doc-chain' && !!lk.title,
      '节标题锚点按钮复制链接并给出可见反馈（' + lkOk.txt + ' / ' + lk.hash + '）');

    // 只改 hash 不会重新加载页面，先用空白页逼一次真正的整页加载再验直开。
    await docs.send('Page.navigate', { url: 'about:blank' });
    await sleep(500);
    await docs.send('Page.navigate', { url: 'http://127.0.0.1:' + PORT + '/docs#doc-chain' });
    await sleep(3200);
    const deep2 = JSON.parse(await djs(`(function(){
      var h=document.getElementById('doc-chain'), a=document.querySelector('#toc a.on');
      var t=document.querySelector('#tree button.on');
      return JSON.stringify({top:h?h.getBoundingClientRect().top:9999, on:!!a, name:a?a.textContent:'',
        treeOn:t?t.dataset.sec:'', secs:document.querySelectorAll('#body .sec').length,
        scrollY:window.scrollY});
    })()`));
    // 分页后节标题本来就在封面下方，判据是「只渲染了这一节 + 停在页首」，不是偏移为 0。
    ok(deep2.on && deep2.secs === 1 && deep2.treeOn === 'chain' && deep2.scrollY === 0 &&
       deep2.top > 0 && deep2.top < 460,
      '带 #doc-chain 直接打开只渲染这一节、停在页首（标题距顶 ' + Math.round(deep2.top) + 'px）');

    // ---- 5) 分页路由：换页 / 后退 / 旧锚点 ----
    console.log('分页路由：');
    const pr = JSON.parse(await djs(`(function(){
      var bs=[].slice.call(document.querySelectorAll('#tree button[data-sec]'));
      bs[4].click(); var prev=bs[4].dataset.sec;
      bs[5].click();
      return JSON.stringify({id:bs[5].dataset.sec, prev:prev,
        cur:document.querySelector('#body .sec h2').id,
        hash:location.hash, path:location.pathname, secs:document.querySelectorAll('#body .sec').length,
        lead:getComputedStyle(document.querySelector('.lead')).display});
    })()`));
    ok(pr.secs === 1 && pr.cur === 'doc-' + pr.id && pr.hash === '#doc-' + pr.id,
      '点目录只换正文一节，地址栏跟着变（' + pr.id + '）');
    ok(pr.path === '/docs', '换页不离开 /docs（客户端路由）');
    ok(pr.lead === 'none', '非首页不显示引导卡');

    await djs(`history.back(); true`);
    await sleep(700);
    const back = JSON.parse(await djs(`JSON.stringify({
      cur:(document.querySelector('#body .sec h2')||{id:''}).id,
      hash:location.hash, path:location.pathname,
      secs:document.querySelectorAll('#body .sec').length})`));
    ok(back.cur === 'doc-' + pr.prev && back.hash === '#doc-' + pr.prev &&
       back.path === '/docs' && back.secs === 1,
      '浏览器后退回到上一节（' + pr.prev + '，实际 ' + (back.cur || '离开了文档页') + '）');

    await docs.send('Page.navigate', { url: 'about:blank' });
    await sleep(500);
    await docs.send('Page.navigate', { url: 'http://127.0.0.1:' + PORT + '/docs#doc-build' });
    await sleep(3200);
    const alias = JSON.parse(await djs(`JSON.stringify({cur:document.querySelector('#body .sec h2').id,
      on:(document.querySelector('#tree button.on')||{dataset:{}}).dataset.sec||''})`));
    ok(alias.cur === 'doc-install' && alias.on === 'install',
      '旧锚点 #doc-build 落到合并后的「安装与构建」（' + alias.cur + '）');

    const pn = JSON.parse(await djs(`JSON.stringify((function(){
      function info(){
        var nav=document.querySelector('#body .sec-nav');
        return { navs:document.querySelectorAll('#body .sec-nav').length,
          prev: !!nav.querySelector('a.prev'), next: !!nav.querySelector('a.next') };
      }
      var bs=[].slice.call(document.querySelectorAll('#tree button[data-sec]'));
      bs[0].click(); var first=info();
      bs[bs.length-1].click(); var last=info();
      return { navs:first.navs, firstNext:first.next, firstPrev:first.prev,
        lastPrev:last.prev, lastNext:last.next };
    })())`));
    ok(pn.navs === 1 && !pn.firstPrev && pn.firstNext && pn.lastPrev && !pn.lastNext,
      '每页只有一组上一篇 / 下一篇，首尾只给单边');

    // pn 停在最后一节（没有下一篇），回到第一节再点
    await djs(`document.querySelectorAll('#tree button[data-sec]')[0].click(); true`);
    await sleep(300);
    const nx = JSON.parse(await djs(`(function(){
      var a=document.querySelector('#body .sec-nav a.next'); var href=a.getAttribute('href');
      a.click(); return JSON.stringify({href:href});
    })()`));
    await sleep(700);
    const nxNow = JSON.parse(await djs(`JSON.stringify({hash:location.hash,
      cur:document.querySelector('#body .sec h2').id})`));
    ok(nxNow.hash === nx.href && nxNow.cur === nx.href.slice(1),
      '点「下一篇」换到下一节（' + nx.href + '）');

    // FAQ / 速查都是独立页，先切过去再验
    await djs(`document.querySelector('#tree button[data-sec="faq"]').click(); true`);
    await sleep(400);
    const faq = JSON.parse(await djs(`JSON.stringify((function(){
      var d=document.querySelectorAll('#body .faq details');
      var opens=Array.prototype.filter.call(d, function(x){ return x.open; }).length;
      return { n:d.length, opens:opens,
        q: d[1] ? d[1].querySelector('summary').textContent : '',
        a: d[0] ? (d[0].querySelector('.a') || {textContent:''}).textContent : '' };
    })())`));
    ok(faq.n >= 10 && faq.opens === 1, 'FAQ 收成折叠项，默认只展开第一条（' + faq.n + ' 条 / 展开 ' + faq.opens + '）');
    ok(faq.q.length > 4 && faq.q.indexOf('——') < 0 && faq.a.length > 10,
      '折叠项把「问题」和「答案」真正拆开了（' + faq.q.slice(0, 16) + '）');
    const faqOpen = await djs(`(function(){
      var d=document.querySelectorAll('#body .faq details');
      if(d.length < 2) return false;
      d[1].querySelector('summary').click(); return d[1].open;
    })()`);
    ok(!!faqOpen, '点标题能展开对应的问答');

    await djs(`document.querySelector('#tree button[data-sec="cheatsheet"]').click(); true`);
    await sleep(400);
    const cs = JSON.parse(await djs(`JSON.stringify((function(){
      var h=document.getElementById('doc-cheatsheet');
      var sec=h?h.closest('.sec'):null;
      var t=sec?sec.textContent:'';
      return { exists:!!h,
        ports:['18765','18766','18815'].filter(function(x){ return t.indexOf(x)>=0; }).length,
        deff:['20200','5002','backup_dir'].filter(function(x){ return t.indexOf(x)>=0; }).length,
        term: t.indexOf('TOFU')>=0, btn: !!document.getElementById('print-one'),
        secs: document.querySelectorAll('#body .sec').length };
    })())`));
    ok(cs.exists && cs.secs === 1 && cs.ports === 3, '速查页收全端口与顺延区间（' + cs.ports + '/3）');
    ok(cs.deff === 3 && cs.term, '速查页有默认值字段名与术语解释（TOFU）');
    ok(cs.btn, '速查页带「只打印本节」按钮');

    await docs.send('Emulation.setEmulatedMedia', { media: 'print' });
    await sleep(200);
    const prn = JSON.parse(await djs(`(function(){
      document.body.classList.add('print-one');
      var shell=['.hero','.side','.toc','.quick','.sec-nav'].every(function(sel){
        var el=document.querySelector(sel); return !el || getComputedStyle(el).display==='none'; });
      var one=document.querySelector('.sec-one');
      var shown=one && getComputedStyle(one).display!=='none';
      document.body.classList.remove('print-one');
      return JSON.stringify({shell:shell, one:shown, secs:document.querySelectorAll('#body .sec').length});
    })()`));
    await docs.send('Emulation.setEmulatedMedia', { media: '' });
    await sleep(150);
    ok(prn.shell && prn.one && prn.secs === 1, '打印模式隐藏页面外壳，正文只留当前节');

    // ---- 6) 双语：与主控制台共享 ek-lang ----
    console.log('页头双语与语言按钮：');
    await djs(`localStorage.setItem('ek-lang','zh'); true`);
    await sleep(200);
    const zhHead = JSON.parse(await djs(`JSON.stringify({
      h1:document.getElementById('hero-t').textContent,
      sub:document.getElementById('hero-p').textContent,
      langBtn:document.getElementById('lang').textContent,
      langTitle:document.getElementById('lang').title,
      themeBtn:document.getElementById('theme').title,
      themeSvg:!!document.querySelector('#theme svg')
    })`));
    ok(/使用文档/.test(zhHead.h1) && /安装到排障/.test(zhHead.sub), '中文模式页头标题与副标题是中文');
    ok(zhHead.langBtn === 'EN' && /English/.test(zhHead.langTitle),
      '中文模式按钮显示「EN」且 title 指向英文（' + zhHead.langBtn + ' / ' + zhHead.langTitle + '）');
    ok(zhHead.themeSvg && /切换暗色/.test(zhHead.themeBtn), '中文模式主题按钮是 SVG，aria/title 写「切换暗色」');

    console.log('英文模式：');
    await djs(`document.getElementById('lang').click(); true`);
    await sleep(500);
    const en = JSON.parse(await djs(`JSON.stringify((function(){
      var btns=[].slice.call(document.querySelectorAll('#tree button[data-sec]'));
      var bad='';
      btns.forEach(function(b){
        b.click();
        // 安装.bat / 卸载.bat 是发布包里的真实中文文件名，英文版也按原名保留。
        var s=document.getElementById('body').textContent.replace(/安装\.bat|卸载\.bat/g,'');
        if(!bad && /[\\u4e00-\\u9fa5]/.test(s)) bad=(s.match(/[\\u4e00-\\u9fa5]+/g)||[]).slice(0,4).join('|');
      });
      var nav=document.getElementById('tree').textContent+document.getElementById('toc').textContent;
      return { cjk:!!bad, sample:bad, navCjk:/[\\u4e00-\\u9fa5]/.test(nav),
        h1:document.getElementById('hero-t').textContent,
        sub:document.getElementById('hero-p').textContent,
        langBtn:document.getElementById('lang').textContent,
        langTitle:document.getElementById('lang').title,
        themeBtn:document.getElementById('theme').title,
        themeSvg:!!document.querySelector('#theme svg'),
        stored: localStorage.getItem('ek-lang'), path:location.pathname };
    })())`));
    ok(!en.cjk, '切英文后每节正文都没有中文残留（' + en.sample + '）');
    ok(!en.navCjk, '英文模式左目录 / 右栏也没有中文');
    ok(/Docs/.test(en.h1) && /From install/.test(en.sub), '英文模式页头标题与副标题切成英文（' + en.h1 + '）');
    ok(en.langBtn === '中文' && /Chinese/.test(en.langTitle),
      '英文模式按钮显示「中文」且 title 指向中文（' + en.langBtn + ' / ' + en.langTitle + '）');
    ok(en.themeSvg && /Switch to dark/.test(en.themeBtn), '英文模式主题按钮是 SVG，aria/title 写「Switch to dark」');
    ok(en.stored === 'en', '语言选择写入 ek-lang，与主控制台共享');
    await djs(`document.getElementById('lang').click(); localStorage.setItem('ek-lang','zh'); true`);
    await sleep(300);

    console.log('明暗主题：');
    const th0 = JSON.parse(await djs(`JSON.stringify({
      attr:document.body.dataset.theme||'', scheme:document.querySelector('meta[name="color-scheme"]').content,
      bg:getComputedStyle(document.body).backgroundColor,
      svg:!!document.querySelector('#theme svg'), title:document.getElementById('theme').title,
      aria:document.getElementById('theme').getAttribute('aria-label')
    })`));
    ok(th0.attr === 'light' && th0.scheme === 'light' && th0.svg && /暗色/.test(th0.title) && th0.aria === th0.title,
      '默认亮色：非 dark、scheme=light，主题按钮是月亮 SVG 且 title/aria 都是「切换暗色」');
    await djs(`document.getElementById('theme').click(); true`);
    await sleep(200);
    const th1 = JSON.parse(await djs(`JSON.stringify({
      attr:document.body.dataset.theme||'', scheme:document.querySelector('meta[name="color-scheme"]').content,
      bg:getComputedStyle(document.body).backgroundColor, stored:localStorage.getItem('ek-theme'),
      svg:!!document.querySelector('#theme svg'), title:document.getElementById('theme').title,
      card:getComputedStyle(document.querySelector('#body .sec')).backgroundColor
    })`));
    ok(th1.attr === 'dark' && th1.scheme === 'dark' && th1.stored === 'dark' && th1.svg && /亮色/.test(th1.title),
      '点主题按钮切暗色：data-theme/scheme/存储/按钮四处同步');
    ok(th1.bg !== th0.bg && th1.card !== th0.bg, '暗色下页面与卡片底色都真的变了（' + th0.bg + ' → ' + th1.bg + '）');
    await djs(`document.getElementById('theme').click(); true`);
    await sleep(200);
    const th2 = JSON.parse(await djs(`JSON.stringify({attr:document.body.dataset.theme||'', stored:localStorage.getItem('ek-theme')})`));
    ok(th2.attr === 'light' && th2.stored === 'light', '再点一次回亮色并写入 ek-theme');

    console.log('手机端适配：');
    await docs.send('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
    await sleep(300);
    // 切到含表格的 chain 节——表格是最容易撑破窄屏的元素
    await djs(`(function(){var b=document.querySelector('#tree button[data-sec="chain"]');if(b)b.click();return true})()`);
    await sleep(300);
    const mob = JSON.parse(await djs(`JSON.stringify((function(){
      var vw=document.documentElement.clientWidth;
      var over=[];
      // .side/.search 现在在收起的抽屉里（transform 移到视口外），不算横向溢出。
      ['.hero','.brand','.hero-side','#theme','#lang','#cta','#toc-btn','.quick','.layout','.sec','.sec>h2','.tbl','.sec-nav','.foot'].forEach(function(sel){
        var e=document.querySelector(sel); if(!e) return;
        var b=e.getBoundingClientRect();
        if(b.right>vw+1||b.left<-1) over.push(sel+':'+Math.round(b.right));
      });
      var tbl=document.querySelector('.tbl');
      var side=document.querySelector('.side'), tocBtn=document.getElementById('toc-btn');
      var cs=getComputedStyle(document.querySelector('.layout'));
      return { vw:vw, scrollW:document.documentElement.scrollWidth, over:over,
        cols:cs.display, toc:getComputedStyle(document.getElementById('toc')).display,
        btnShow:getComputedStyle(tocBtn).display,
        sideLeft:Math.round(side.getBoundingClientRect().left),
        sideOpen:side.classList.contains('open'),
        treeDisplay:getComputedStyle(document.getElementById('tree')).display,
        tblScroll:tbl?tbl.scrollWidth:0, tblClient:tbl?tbl.clientWidth:0,
        themeSvg:!!document.querySelector('#theme svg'),
        themeW:Math.round(document.getElementById('theme').getBoundingClientRect().width) };
    })())`));
    ok(mob.vw === 390 && mob.scrollW <= mob.vw && mob.over.length === 0,
      '390px 下整页不横向溢出（scrollW=' + mob.scrollW + '，越界=' + mob.over.join(',') + '）');
    ok(mob.cols === 'block' && mob.toc === 'none', '手机端三栏收成单列、右目录隐藏');
    ok(mob.btnShow !== 'none' && !mob.sideOpen && mob.sideLeft < -50,
      '手机端目录默认收在视口外，页头有「目录」把手（side.left=' + mob.sideLeft + '）');
    ok(mob.treeDisplay === 'block', '抽屉里目录恢复竖向分组，不再是横向滚动 chips');
    ok(mob.tblScroll > mob.tblClient && mob.tblClient < mob.vw,
      '宽表格自身横向滚动而不撑破页面（' + mob.tblClient + '/' + mob.tblScroll + '）');
    ok(mob.themeSvg && mob.themeW >= 28 && mob.themeW <= 40, '手机端主题按钮是 32px 级 SVG 图标');

    // 目录抽屉：按钮唤出→遮罩出现→动画落定后真的滑进视口→点节跳转自动收起
    const drawer = JSON.parse(await djs(`JSON.stringify((function(){
      var btn=document.getElementById('toc-btn'), side=document.querySelector('.side'), mask=document.getElementById('toc-mask');
      btn.click();
      return {opened:side.classList.contains('open') && mask.hidden===false &&
                     btn.getAttribute('aria-expanded')==='true'};
    })())`));
    ok(drawer.opened, '点「目录」把手抽屉展开、遮罩出现、aria-expanded 置真');
    await sleep(400); // 等 .24s 过渡落定，再量坐标（点击瞬间还在滑行途中）
    ok(await djs(`(function(){var s=document.querySelector('.side');return Math.round(s.getBoundingClientRect().left)>=0;})()`),
      '抽屉动画落定后真的滑进视口（left>=0）');
    const drawerPick = JSON.parse(await djs(`JSON.stringify((function(){
      var target=document.querySelector('#tree button[data-sec="runtime"]');
      var go=target?target.getAttribute('data-sec'):'';
      if(target) target.click();
      var side=document.querySelector('.side'), mask=document.getElementById('toc-mask');
      return {closed:!side.classList.contains('open') && mask.hidden===true,
              go:go, hash:location.hash};
    })())`));
    ok(drawerPick.closed && drawerPick.hash === '#doc-' + drawerPick.go,
      '抽屉里点一节跳到对应页并自动收起（' + drawerPick.hash + '）');
    await djs(`(function(){var b=document.getElementById('toc-btn');b.click();document.getElementById('toc-mask').click();return true})()`);
    ok(await djs(`!document.querySelector('.side').classList.contains('open')`), '点遮罩也能收起抽屉');
    await docs.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
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
