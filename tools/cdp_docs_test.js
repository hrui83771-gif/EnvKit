// CDP 回归（本轮）：顶栏搜索入口 + 三栏使用文档页。
//
// 守的是几个能被用户撞见的事实：
//   1) 顶栏搜索框在个人中心左边，点它打开的是已有 Ctrl+K 面板（不是第二套搜索）；
//   2) 侧栏「关于 / 免责声明」变成跳文档，关于弹窗只能从文档页进入；
//   3) 文档页左目录 / 中正文 / 右目录同源生成，过滤与复制真能用；
//   4) 切英文后文档正文不含中文（dz 双语 + ek:lang 重渲染）。
//
// 变异：--mut=no-search 拿掉搜索按钮；--mut=about-modal 把侧栏按钮恢复成
// 打开关于弹窗。两组各红自己的断言，不靠提前 fatal。
// 用法：node tools/cdp_docs_test.js [--mut=no-search|--mut=about-modal]
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
    await send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);
    // CDP profile 复用，上一轮可能停在英文；这里显式回到中文再断言文案。
    await evalJS(`ekSetLang('zh', true); true`);
    await sleep(300);

    if (MUT === '--mut=no-search') {
      await evalJS(`(function(){ var b=document.getElementById('btn-search'); if(b) b.remove(); return true; })()`);
    }
    if (MUT === '--mut=about-modal') {
      await evalJS(`(function(){
        var b=document.getElementById('btn-about'); if(!b) return true;
        var nb=b.cloneNode(true); nb.removeAttribute('data-goto');
        nb.onclick=function(){ document.getElementById('about').classList.add('show'); };
        b.parentNode.replaceChild(nb,b); return true;
      })()`);
    }

    // ---- 1) 顶栏搜索把手 ----
    console.log('顶栏搜索：');
    const hdr = JSON.parse(await evalJS(`JSON.stringify((function(){
      var b=document.getElementById('btn-search'), p=document.getElementById('btn-profile');
      return { exists:!!b,
        before: !!(b && p && (b.compareDocumentPosition(p) & 4)),
        kbd: !!(b && b.querySelector('kbd')), icon: !!(b && b.querySelector('.ek svg')),
        kbdText: b && b.querySelector('kbd') ? b.querySelector('kbd').textContent : '' };
    })())`));
    ok(hdr.exists, '个人中心旁存在搜索入口');
    ok(hdr.before, '搜索入口在个人中心左侧（DOM 顺序）');
    ok(hdr.icon && hdr.kbd && /Ctrl/.test(hdr.kbdText), '放大镜图标 + Ctrl+K 键帽都在');

    const clicked = await evalJS(`(function(){
      var b=document.getElementById('btn-search'); if(!b) return false; b.click(); return true;
    })()`);
    await sleep(200);
    const cp = JSON.parse(await evalJS(`JSON.stringify((function(){
      var box=document.getElementById('ek-cmd');
      return { open: !!(box && box.classList.contains('show')),
        focus: document.activeElement ? document.activeElement.id : '',
        total: window.EKCmd ? window.EKCmd.state().total : 0 };
    })())`));
    ok(clicked && cp.open, '点搜索框打开 Ctrl+K 命令面板');
    ok(cp.focus === 'ekcp-q', '打开后输入焦点落在面板搜索框');
    ok(cp.total > 0, '面板索引来自真实控件（' + cp.total + ' 项）');
    const hit = JSON.parse(await evalJS(`(function(){
      var i=document.getElementById('ekcp-q');
      i.value='文档'; i.dispatchEvent(new Event('input',{bubbles:true}));
      return JSON.stringify(Array.from(document.querySelectorAll('#ekcp-list .ekcp-row b')).map(function(x){return x.textContent;}));
    })()`));
    ok(hit.indexOf('使用文档') >= 0, '搜「文档」能找到文档页（' + hit.join('/') + '）');
    await evalJS(`window.EKCmd.close(); true`);

    // ---- 2) 侧栏按钮跳文档，不再弹关于 ----
    console.log('侧栏跳转：');
    const side = JSON.parse(await evalJS(`JSON.stringify((function(){
      var b=document.getElementById('btn-about');
      return { goto: b ? b.getAttribute('data-goto') : null, text: b ? b.textContent.trim() : '' };
    })())`));
    ok(side.goto === 'docs', '原关于按钮改为 data-goto=docs');
    await evalJS(`(function(){ var b=document.getElementById('btn-about'); if(b) b.click(); return true; })()`);
    await sleep(250);
    const jump = JSON.parse(await evalJS(`JSON.stringify({
      docs: document.getElementById('panel-docs').classList.contains('show'),
      about: document.getElementById('about').classList.contains('show')
    })`));
    ok(jump.docs, '点侧栏按钮进入文档页');
    ok(!jump.about, '点侧栏按钮不再弹出长篇关于窗口');

    // ---- 3) 三栏结构 / 过滤 / 目录 / 复制 ----
    // 显式进一次文档页：变异 about-modal 会在上一步停住，结构组不应被连带判红。
    await evalJS(`goto('docs'); true`);
    await sleep(150);
    console.log('文档页结构：');
    const st = JSON.parse(await evalJS(`JSON.stringify({
      tree: document.querySelectorAll('#docs-tree .docs-item').length,
      groups: document.querySelectorAll('#docs-tree .docs-grpbox').length,
      secs: document.querySelectorAll('#docs-body .docs-sec').length,
      toc: document.querySelectorAll('#docs-toc [data-anchor]').length,
      code: document.querySelectorAll('#docs-body .docs-copy').length,
      cols: getComputedStyle(document.getElementById('docs-shell')).gridTemplateColumns.split(' ').length
    })`));
    ok(st.cols === 3, '宽屏是三栏布局（实际 ' + st.cols + '）');
    ok(st.tree >= 10 && st.secs === st.tree, '目录项与正文小节一一对应（' + st.tree + '/' + st.secs + '）');
    ok(st.groups >= 4, '目录按组分类（' + st.groups + ' 组）');
    ok(st.toc >= st.secs, '右侧目录来自正文真实锚点（' + st.toc + '）');
    ok(st.code >= 2, '正文有可复制代码块（' + st.code + '）');

    // 留一张实际渲染的预览图（gitignore 的 .gotmp，不进提交）。
    try{
      fs.mkdirSync(path.join(REPO, '.gotmp', 'ux-preview'), {recursive:true});
      const shot = await send('Page.captureScreenshot', {format:'png', captureBeyondViewport:false});
      fs.writeFileSync(path.join(REPO, '.gotmp', 'ux-preview', '06-docs.png'),
        Buffer.from(shot.result.data, 'base64'));
    }catch(e){}

    const filt = JSON.parse(await evalJS(`(function(){
      var i=document.getElementById('docs-q');
      i.value='TOFU'; i.dispatchEvent(new Event('input',{bubbles:true}));
      var bs=Array.from(document.querySelectorAll('#docs-tree .docs-item')).filter(function(b){return !b.hidden;});
      return JSON.stringify({n:bs.length, txt:bs.map(function(b){return b.textContent;})});
    })()`));
    ok(filt.n > 0 && filt.n < st.tree, '过滤真能收窄目录（' + filt.n + '/' + st.tree + '）');
    ok(filt.txt.some(x => x.indexOf('链端') >= 0), '过滤按正文命中而不是只按标题（TOFU 只在正文里）');
    await evalJS(`(function(){var i=document.getElementById('docs-q'); i.value=''; i.dispatchEvent(new Event('input',{bubbles:true})); return true;})()`);

    const sc = JSON.parse(await evalJS(`(function(){
      var b=document.querySelectorAll('#docs-tree .docs-item')[4], id=b.dataset.sec;
      b.click();
      var body=document.getElementById('docs-body'), h=document.getElementById('doc-'+id);
      return JSON.stringify({id:id, top: h ? h.getBoundingClientRect().top - body.getBoundingClientRect().top : 9999,
        on:b.classList.contains('on')});
    })()`));
    ok(sc.on && Math.abs(sc.top) < 24, '点左目录滚到对应正文（' + sc.id + '，偏移 ' + Math.round(sc.top) + '）');

    const tocClick = JSON.parse(await evalJS(`(function(){
      var a=document.querySelectorAll('#docs-toc [data-anchor]')[0], h=document.getElementById(a.dataset.anchor);
      a.click();
      var body=document.getElementById('docs-body');
      return JSON.stringify({on:a.classList.contains('on'),
        top:h.getBoundingClientRect().top - body.getBoundingClientRect().top});
    })()`));
    ok(tocClick.on && Math.abs(tocClick.top) < 90, '点右目录跳到对应章节');

    await evalJS(`document.querySelector('#docs-body .docs-copy').click(); true`);
    await sleep(250);
    const copied = await evalJS(`document.querySelector('#docs-body .docs-copy').textContent`);
    ok(copied !== '复制' && copied.length > 0, '代码块复制按钮给出可见反馈（' + copied + '）');

    // ---- 4) 关于入口仍保留，且重看引导没丢 ----
    console.log('关于入口：');
    await evalJS(`document.getElementById('docs-about').click(); true`);
    await sleep(150);
    const ab = JSON.parse(await evalJS(`JSON.stringify({
      shown: document.getElementById('about').classList.contains('show'),
      replay: !!document.getElementById('about-replay'),
      back: !!document.getElementById('about-docs')
    })`));
    ok(ab.shown && ab.replay && ab.back, '文档内「关于 EnvKit」可打开弹窗，重看引导按钮仍在');
    await evalJS(`document.getElementById('about-close').click(); true`);

    // ---- 5) 英文模式文档无中文残留 ----
    console.log('英文模式：');
    await evalJS(`ekSetLang('en', true); true`);
    await sleep(400);
    const en = JSON.parse(await evalJS(`JSON.stringify((function(){
      var s = document.getElementById('docs-body').textContent + document.getElementById('docs-tree').textContent +
              document.getElementById('docs-toc').textContent;
      return { cjk: /[\\u4e00-\\u9fa5]/.test(s), sample: s.slice(0, 60) };
    })())`));
    ok(!en.cjk, '切英文后文档正文 / 目录无中文（' + en.sample + '）');
    const enHdr = await evalJS(`document.getElementById('btn-search').textContent + '|' +
      document.getElementById('btn-about').textContent`);
    ok(!/[\u4e00-\u9fa5]/.test(enHdr), '英文模式搜索框 / 侧栏按钮无中文（' + enHdr + '）');
    await evalJS(`ekSetLang('zh', true); true`);
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
