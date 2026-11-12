// CDP 实测：v3.8.1 四个离线组件是否真的接进主界面
//
// ## 这个用例防什么
//  1. 五个 vendor 产物真的从同域加载成功、全局导出名对得上 ——
//     只改 tpl 不 go build，或 embed/路由漏一个，这里会直接红。
//  2. AutoAnimate 只挂在真实增删的容器上，并且新增子节点时确实产生 WAAPI 动画
//     （不是只留个 data 标记的空壳）。
//  3. Ctrl+K 用**真实键盘**打开；索引里的每条都能用选择器找回真实 DOM 元素；
//     回车真的切了页面、Esc 真的关了面板。
//  4. 配置差异来自两份真实 JSON：键顺序不同不能报差异，含 <script> 的值
//     不能造出活的 script，预览阶段绝不 POST /api/config/import，
//     按 Esc 后 confirm Promise 必须 resolve（否则 await 卡死）。
//
// 用法：node tools/cdp_vendor_ux_test.js [--mut=no-diff|no-hotkeys]
// 前提：先 go build -o EnvKit.exe .
// 隔离：独立端口 18801 + 独立调试端口 + ENVKIT_INSTANCE_TAG + 独立 profile + exe 副本。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18801;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9363;
const DIR = path.join(REPO, 'tools', '_vendorux');
const EXE = path.join(DIR, 'EnvKit_vux.exe');
const PROF_PARENT = path.join(REPO, 'tools', '_cdptmp_vendorux');
const PROF = path.join(PROF_PARENT, 'run-' + Date.now());
const MUT = (process.argv.find(a => a.startsWith('--mut=')) || '').slice(6);

const sleep = ms => new Promise(r => setTimeout(r, ms));
function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function getRoute(p) {
  return new Promise(res => {
    http.get(URL_.replace(/\/$/, '') + p, r => {
      let n = 0; r.on('data', c => n += c.length); r.on('end', () => res({ status: r.statusCode, len: n }));
    }).on('error', () => res({ status: 0, len: 0 }));
  });
}

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    app_name: 'EnvKit', app_title: '统一开发环境助手',
    install_dir: 'C:\\EnvKit\\tools',
    projects: { frontend_dir: '', backend_dir: '', mysql_host: '127.0.0.1', mysql_port: 3306,
      mysql_user: 'root', db_name: 'farm', sql_file: '', backup_dir: '', scan_ports: [8080, 8888], web_scripts: [] },
    chain: { ssh_host: '', ssh_port: 22, ssh_user: 'root', chain_host: '', chain_port: 20200,
      chain_dir: '', group_id: 1, chain_guard: false, chain_autorecover: false, chain_notify: true },
    dev: { server_port: PORT },
    ai: { enabled: false, provider: 'deepseek', base_url: 'https://replay.invalid/v1',
      api_key: 'not-a-real-key', model: 'demo', memory_enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    lan: { enabled: false },
    update: { enabled: false, interval_hours: 72 },
    alerts: { webhook: '', cooldown_secs: 300 },
    notify: { muted: false },
  }, null, 2), 'utf8');
}

(async () => {
  const MAIN_EXE = path.join(REPO, 'EnvKit.exe');
  if (!fs.existsSync(MAIN_EXE)) { console.log('先构建：go build -o EnvKit.exe .'); process.exit(1); }
  seed();
  fs.copyFileSync(MAIN_EXE, EXE);
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) { console.log('端口 ' + PORT + ' 已被占用，先关掉残留实例'); process.exit(2); }
  fs.mkdirSync(PROF_PARENT, { recursive: true });
  for (const d of fs.readdirSync(PROF_PARENT)) {
    if (d.startsWith('run-')) { try { fs.rmSync(path.join(PROF_PARENT, d), { recursive: true, force: true }); } catch (e) {} }
  }
  fs.mkdirSync(PROF, { recursive: true });

  const envkit = spawn(EXE, [], { cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'vendorux' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  if (!ready) { console.log('envkit 未就绪'); envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(PROF, 'chrome'),
    '--window-size=1440,960', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
  const results = [];
  const check = (name, ok, extra) => {
    results.push((ok ? 'PASS ' : 'FAIL ') + name + (extra ? ' :: ' + extra : ''));
    if (!ok) exitCode = 1;
  };

  let ws = null;
  try {
    let targets = null;
    for (let i = 0; i < 10; i++) { try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); } }
    const page = (targets || []).find(t => t.type === 'page');
    if (!page) throw new Error('no page target');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params: params || {} })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return '__EXC__' + JSON.stringify(r.result.exceptionDetails.text);
      return r.result && r.result.result ? r.result.result.value : undefined;
    };
    const key = async (type, k, code, vk, mods) => send('Input.dispatchKeyEvent', {
      type: type, key: k, code: code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers: mods || 0,
    });
    const ctrlK = async () => {
      await key('keyDown', 'Control', 'ControlLeft', 17, 2);
      await key('keyDown', 'k', 'KeyK', 75, 2);
      await key('keyUp', 'k', 'KeyK', 75, 2);
      await key('keyUp', 'Control', 'ControlLeft', 17, 0);
      await sleep(180);
    };
    const esc = async () => { await key('keyDown', 'Escape', 'Escape', 27, 0); await key('keyUp', 'Escape', 'Escape', 27, 0); await sleep(160); };
    const enter = async () => { await key('keyDown', 'Enter', 'Enter', 13, 0); await key('keyUp', 'Enter', 'Enter', 13, 0); await sleep(220); };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    if (MUT === 'no-hotkeys') {
      // 变异：让 hotkeys-js 的全局赋值被吞掉，验证门禁能抓到「快捷键库没接上」
      await send('Page.addScriptToEvaluateOnNewDocument', { source:
        'Object.defineProperty(window,"hotkeys",{get:function(){return undefined;},set:function(){},configurable:true});' });
    }
    await send('Page.navigate', { url: URL_ });
    await sleep(1600);

    if (MUT === 'no-diff') {
      await evalJS('window.Diff2Html.html=function(){return "<div class=mut-no-diff></div>";};');
    }

    // ---- 1. 五个 vendor 真的从同域加载并嵌进 exe ----
    for (const fn of ['auto-animate.iife.js', 'diff.min.js', 'diff2html.min.js', 'diff2html.min.css', 'hotkeys.min.js']) {
      const r = await getRoute('/vendor/' + fn);
      check('route-/vendor/' + fn, r.status === 200 && r.len > 1000, 'http=' + r.status + ' len=' + r.len);
    }
    const g = await evalJS(`({
      aa: typeof window.autoAnimate,
      diff: typeof (window.Diff && window.Diff.createTwoFilesPatch),
      d2h: typeof (window.Diff2Html && window.Diff2Html.html),
      hk: typeof window.hotkeys,
      ics: typeof window.ekIcon,
      nIcons: (typeof EK_ICONS !== 'undefined') ? Object.keys(EK_ICONS).length : 0
    })`);
    check('global-autoAnimate', g.aa === 'function', 'got=' + g.aa);
    check('global-Diff.createTwoFilesPatch', g.diff === 'function', 'got=' + g.diff);
    check('global-Diff2Html.html', g.d2h === 'function', 'got=' + g.d2h);
    check('global-hotkeys', g.hk === 'function', 'got=' + g.hk);
    check('global-ekIcon-and-icon-table', g.ics === 'function' && g.nIcons >= 20, 'icons=' + g.nIcons);
    const ext = await evalJS(`Array.from(document.querySelectorAll('script[src],link[href]'))
      .map(function(e){return e.getAttribute('src')||e.getAttribute('href');})
      .filter(function(u){return /^https?:/.test(u);}).length`);
    check('zero-cdn', ext === 0, 'external=' + ext);

    // ---- 2. Lucide：占位全部换成 svg，描边统一 2px ----
    const ic = await evalJS(`({
      left: document.querySelectorAll('[data-ek-icon]').length,
      total: document.querySelectorAll('.ek-ic').length,
      all2: Array.from(document.querySelectorAll('.ek-ic')).every(function(s){return s.getAttribute('stroke-width')==='2';}),
      close: !!document.querySelector('#ai-close svg.ek-ic'),
      caret: !!document.querySelector('.pcaret svg.ek-ic'),
      cmd: !!document.querySelector('#ek-cmd .ek-ic')
    })`);
    check('icons-fill-no-placeholder', ic.left === 0, 'left=' + ic.left);
    check('icons-present-and-uniform', ic.total >= 5 && ic.all2 && ic.close && ic.caret, JSON.stringify(ic));

    // ---- 3. AutoAnimate：四个真实容器已挂，新增子节点确有 WAAPI 动画 ----
    const mo = await evalJS(`({
      targets:(window.ekMotionTargets||[]).length,
      mounted:(window.ekMotionTargets||[]).filter(function(id){var e=document.getElementById(id);return e&&e.dataset.aa==='1';}).length,
      reinit:(typeof window.ekMotionInit==='function')?window.ekMotionInit():-1
    })`);
    check('motion-4-real-containers', mo.targets === 4 && mo.mounted === 4 && mo.reinit === 0, JSON.stringify(mo));
    // 上游 autoAnimate 的 add() 对视口外元素刻意跳过入场动画（isOffscreen）。
    // .cards 本身是 display:grid 且没有 position 规则，库接管后会把父元素改成
    // position:relative —— 这是「真的挂上了」的客观痕迹，不靠我们自己的 data 标记。
    check('motion-library-took-over',
      (await evalJS(`getComputedStyle(document.getElementById('detect-cards')).position`)) === 'relative');
    const anim = await evalJS(`(async function(){
      var box=document.getElementById('detect-cards'); if(!box) return {err:'no-box'};
      box.scrollIntoView({block:'center'});
      await new Promise(function(r){setTimeout(r,90);});
      var d=document.createElement('div'); d.id='ek-aa-probe'; d.textContent='probe';
      box.appendChild(d);
      return await new Promise(function(res){
        var samples=[], times=[40,120,240,400];
        times.forEach(function(ms,i){ setTimeout(function(){
          var mine=document.getAnimations().filter(function(a){return a.effect&&a.effect.target===d;}).length;
          samples.push({t:ms,n:(d.getAnimations?d.getAnimations().length:-1),mine:mine});
          if(i===times.length-1){ d.remove(); res({samples:samples}); }
        },ms); });
      });
    })()`);
    check('motion-real-flip-animation',
      anim && anim.samples && anim.samples.some(s => s.n > 0 || s.mine > 0), JSON.stringify(anim));

    // ---- 4. 命令面板：真实键盘 + 真实 DOM 索引 ----
    const st0 = await evalJS('window.EKCmd ? EKCmd.state() : null');
    check('cmd-keysource-hotkeys', st0 && st0.keySource === 'hotkeys', JSON.stringify(st0));
    const its = await evalJS(`(function(){
      var a=EKCmd.collect(); var withSel=a.filter(function(i){return i.sel;});
      return {total:a.length, withSel:withSel.length,
        real:withSel.filter(function(i){return !!document.querySelector(i.sel);}).length,
        groups:Array.from(new Set(a.map(function(i){return i.group;}))).join(',')};
    })()`);
    check('cmd-index-only-real-controls', its.total >= 30 && its.withSel >= 20 && its.real === its.withSel, JSON.stringify(its));
    check('cmd-index-groups', /page/.test(its.groups) && /act/.test(its.groups) && /tour/.test(its.groups), its.groups);

    await ctrlK();
    const st1 = await evalJS('EKCmd.state()');
    check('cmd-real-ctrl-k-opens', st1.open === true && st1.total >= 30, JSON.stringify(st1));
    // 面板 DOM 是打开时才建的，图标断言必须放在这之后
    check('icons-in-command-palette',
      (await evalJS(`!!document.querySelector('#ek-cmd .ek-ic')`)) === true);
    await send('Input.insertText', { text: '程序配置' });
    await sleep(150);
    const st2 = await evalJS('EKCmd.state()');
    const shown = await evalJS('EKCmd.shown()');
    check('cmd-filter-narrows', st2.shown > 0 && st2.shown < st2.total &&
      shown.every(x => (x.text + x.hint).indexOf('程序配置') >= 0), JSON.stringify(st2) + ' first=' + (shown[0] && shown[0].text));
    await enter();
    const after = await evalJS(`({open:EKCmd.state().open, cfg:!!document.querySelector('#panel-config.show')})`);
    check('cmd-enter-runs-real-action', after.open === false && after.cfg === true, JSON.stringify(after));
    await ctrlK();
    await esc();
    check('cmd-esc-closes', (await evalJS('EKCmd.state().open')) === false);

    // ---- 5. 配置差异：真实数据、键顺序不制造假差异、无活 script ----
    const d = await evalJS(`(function(){
      var a=JSON.stringify({z:1,a:'old'}), b=JSON.stringify({a:'new',z:2,c:true});
      var r=window.ekCfgDiffHTML(a,b);
      return {same:r.same, changed:r.changed, table:r.html.indexOf('d2h-diff-table')>=0,
        ins:(r.html.match(/d2h-ins/g)||[]).length, del:(r.html.match(/d2h-del/g)||[]).length};
    })()`);
    check('diff-render-real-changes', !d.same && d.changed >= 3 && d.table && d.ins > 0 && d.del > 0, JSON.stringify(d));
    const same = await evalJS(`(function(){
      var r=window.ekCfgDiffHTML('{"b":2,"a":1}','{"a":1,"b":2}');
      return {same:r.same,changed:r.changed};
    })()`);
    check('diff-key-order-noise-free', same.same === true && same.changed === 0, JSON.stringify(same));
    const xss = await evalJS(`(function(){
      var r=window.ekCfgDiffHTML(JSON.stringify({v:'clean'}),JSON.stringify({v:'<script>alert(1)</script>'}));
      var d=document.createElement('div'); d.innerHTML=r.html;
      return {scripts:d.querySelectorAll('script').length, plain:d.textContent.indexOf('<script>')>=0};
    })()`);
    check('diff-no-live-script', xss.scripts === 0 && xss.plain === true, JSON.stringify(xss));

    // ---- 6. 预览阶段不写、Esc 后 Promise 必须 resolve ----
    await evalJS(`(function(){
      window.__ekPosts=[];
      if(!window.__ekOrigFetch){ window.__ekOrigFetch=window.fetch;
        window.fetch=function(u,i){ var s=typeof u==='string'?u:(u&&u.url)||'';
          if(i&&String(i.method).toUpperCase()==='POST'&&s.indexOf('/api/config/import')>=0) window.__ekPosts.push(s);
          return window.__ekOrigFetch.apply(this,arguments); }; }
      return true;
    })()`);
    const mp = await evalJS(`(async function(){
      var old=await fetch('/api/config/export?t='+encodeURIComponent(window.__EK_TOKEN__)).then(function(r){return r.text();});
      window.__ekdfDiag=[]; var ot=window.toast;
      window.toast=function(m,c){window.__ekdfDiag.push(String(m));return ot.apply(this,arguments);};
      window.__ekdfP=window.ekCfgConfirmImport(old,'probe.json');
      // 轮询等真实弹窗出现（后端首次响应可能 >180ms），最多 2.5s
      for(var i=0;i<25;i++){
        if(document.querySelector('#ek-cfgdiff.show')) break;
        await new Promise(function(r){setTimeout(r,100);});
      }
      var box=document.querySelector('#ek-cfgdiff');
      return {shown:!!document.querySelector('#ek-cfgdiff.show'),
        same:!!document.querySelector('#ekdf-body .ekdf-same'),
        boxClass:box?box.className:'none',
        body:box?box.querySelector('#ekdf-body').innerHTML.slice(0,100):'',
        diag:window.__ekdfDiag,
        posts:window.__ekPosts.length};
    })()`);
    check('diff-modal-shows-identical', mp.shown === true && mp.same === true, JSON.stringify(mp));
    check('diff-preview-no-import-post', mp.posts === 0, 'posts=' + mp.posts);
    await esc();
    const resolved = await evalJS(`(async function(){
      return await Promise.race([window.__ekdfP,
        new Promise(function(r){setTimeout(function(){r('TIMEOUT');},900);})]);
    })()`);
    check('diff-esc-resolves-promise', resolved === false, 'got=' + resolved);
    check('diff-no-import-post-after', (await evalJS('window.__ekPosts.length')) === 0);
    check('no-js-errors', (await evalJS('window.__errs.length')) === 0,
      'errs=' + JSON.stringify(await evalJS('window.__errs.slice(0,3)')));

  } catch (e) {
    check('cdp-fatal', false, String(e && e.stack || e));
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
    await sleep(400);
    const pass = results.filter(r => r.startsWith('PASS')).length;
    console.log('\n--- CDP vendor/UX ' + (MUT ? 'MUT=' + MUT : 'baseline') + ' ---');
    results.filter(r => r.startsWith('FAIL')).forEach(r => console.log(r));
    console.log('RESULT: ' + pass + '/' + results.length + ' ' + (exitCode ? 'FAIL' : 'PASS'));
    process.exit(exitCode);
  }
})();
