// CDP 实测：首次引导 / 产品导览（v3）
//
// ## 这个用例防什么
//  1. 三个 vendor 库（Swiper 12 / Anime.js v4 / driver.js）真的从同域加载成功，
//     且全局导出名对得上 —— 名字猜错会「页面能开但动画/翻页全空转」。
//  2. 首次自动播 8 屏 + 1.2s 片头出现后结束；「关于 > 重新观看介绍」重放 8 屏。
//  3. 8 屏交互演示真的动了（不是静态摆设），且**全程不触发任何写操作 /api/**。
//  4. 翻页、跳过、演示按钮都用真实鼠标（Input.dispatchMouseEvent）+ 命中测试，
//     不用 element.click() —— DOM click 会绕过命中测试，overlay 层级错了也全绿。
//  5. 自动化环境（navigator.webdriver=true）不自动播，否则 33 个既有 CDP
//     回归脚本会被全屏 overlay 挡住点击；但手动 open('first') 仍必须可用。
//  6. v5 细节层（HUD / 收拢计数 / 检测结论行 / 分区计数 / 多轮对话 / 权限三栏 / 章节摘要）
//     真的有内容，不是留了个空壳 class。
//
// 用法：node tools/cdp_onboard_test.js（需先 go build -o EnvKit.exe .）
// 隔离：独立端口 18799 + 独立调试端口 + ENVKIT_INSTANCE_TAG + 独立 profile + exe 副本。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9361;
const DIR = path.join(REPO, 'tools', '_onboard');
const EXE = path.join(DIR, 'EnvKit_onb.exe');
// 每次运行用独立 profile：Chrome 的 localStorage 会跨运行持久化，
// 复用同一个 user-data-dir 会让「首次自动播」从第二次起永远不再触发（假绿）。
const PROF_PARENT = path.join(REPO, 'tools', '_cdptmp_onboard');
const PROF = path.join(PROF_PARENT, 'run-' + Date.now());

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

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
  // 清掉之前运行留下的 profile（chrome.kill 是异步的，删不掉就忽略，新目录本来就是干净的）
  fs.mkdirSync(PROF_PARENT, { recursive: true });
  for (const d of fs.readdirSync(PROF_PARENT)) {
    if (d.startsWith('run-')) { try { fs.rmSync(path.join(PROF_PARENT, d), { recursive: true, force: true }); } catch (e) {} }
  }
  fs.mkdirSync(PROF, { recursive: true });

  const envkit = spawn(EXE, [], { cwd: DIR,
    env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'onboard' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(PROF, 'chrome'),
    '--window-size=1440,960', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
  const results = [];
  const check = (name, ok, extra) => {
    results.push((ok ? 'PASS' : 'FAIL') + ' ' + name + (extra ? ' :: ' + extra : ''));
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

    // 真实鼠标：先量盒子 + elementFromPoint 命中测试，再发 mousePressed/mouseReleased
    const clickSel = async (sel, label) => {
      const box = await evalJS(`(function(){
        var e=document.querySelector(${JSON.stringify(sel)});
        if(!e) return null;
        var r=e.getBoundingClientRect();
        if(r.width<1||r.height<1) return {err:'zero-box'};
        var x=r.left+r.width/2, y=r.top+r.height/2;
        var h=document.elementFromPoint(x,y);
        var hit=!!(h && (h===e || e.contains(h) || (h.closest && h.closest(${JSON.stringify(sel)})===e)));
        return {x:x,y:y,hit:hit,hitTag:h?h.tagName+'.'+h.className:''};
      })()`);
      if (!box || box.err || !box.x) { check('mouse-' + label, false, JSON.stringify(box)); return false; }
      check('mouse-hit-' + label, !!box.hit, 'hit=' + box.hit + ' top=' + box.hitTag);
      await send('Input.dispatchMouseEvent', { type: 'mousePressed', x: box.x, y: box.y, button: 'left', clickCount: 1 });
      await send('Input.dispatchMouseEvent', { type: 'mouseReleased', x: box.x, y: box.y, button: 'left', clickCount: 1 });
      return true;
    };
    // 纯移动鼠标（不点击）：用来验证 hover 联动真的接上了
    const hoverSel = async (sel, label) => {
      const box = await evalJS(`(function(){
        var e=document.querySelector(${JSON.stringify(sel)});
        if(!e) return null;
        var r=e.getBoundingClientRect();
        if(r.width<1||r.height<1) return {err:'zero-box'};
        return {x:r.left+r.width/2, y:r.top+r.height/2};
      })()`);
      if (!box || box.err || !box.x) { check('hover-' + label, false, JSON.stringify(box)); return false; }
      await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: box.x, y: box.y });
      return true;
    };
    const arrowRight = async () => {
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'ArrowRight', code: 'ArrowRight', windowsVirtualKeyCode: 39, nativeVirtualKeyCode: 39 });
      await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'ArrowRight', code: 'ArrowRight', windowsVirtualKeyCode: 39, nativeVirtualKeyCode: 39 });
      await sleep(680);
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});

    // 轮 A：用 CDP 协议层覆写 UA 成真实 Chrome（JS 里 defineProperty 盖不住判据），
    // 并注入探针客观记录片头出现/结束，不受 1.2s 时序影响。
    const REAL_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';
    const injected = await send('Page.addScriptToEvaluateOnNewDocument', { source: `
      window.__obBrandSeen=false; window.__obBrandEnded=false;
      var __iv=setInterval(function(){
        var b=document.getElementById('ob-brand'); if(!b) return;
        if(b.classList.contains('play')) window.__obBrandSeen=true;
        else if(window.__obBrandSeen) window.__obBrandEnded=true;
      },40);
      setTimeout(function(){clearInterval(__iv);},6000);
    ` });
    const ident = injected && injected.result && injected.result.identifier;
    await send('Emulation.setUserAgentOverride', { userAgent: REAL_UA, acceptLanguage: 'zh-CN', platform: 'Win32' });

    await send('Page.navigate', { url: URL_ });
    await sleep(1200);
    // 双保险：显式清空该 origin 的 localStorage 再 reload，确保轮 A 是「真首次」。
    // 否则一旦 profile 里有残留的 ek-guide-v1，后面所有首次播放断言都是空转。
    await evalJS(`(function(){ try{ localStorage.clear(); }catch(e){} return 'ok'; })()`);
    try { await send('Storage.clearDataForOrigin', { origin: 'http://127.0.0.1:' + PORT, storageTypes: 'local_storage' }); } catch (e) {}
    await send('Page.reload', {});
    await sleep(3000);
    await evalJS(`(function(){ try{ ekSetLang('zh', true); }catch(e){} return 'ok'; })()`);

    const envA = await evalJS(`JSON.stringify({
      wd: navigator.webdriver, headless: /HeadlessChrome/i.test(navigator.userAgent),
      ua: navigator.userAgent.slice(0,90)
    })`);
    let EA = {}; try { EA = JSON.parse(envA || '{}'); } catch (e) {}
    check('envA-real-user-ua', EA.headless === false && EA.wd !== true, envA);

    // ===== A. 三库加载 =====
    const libs = await evalJS(`JSON.stringify({
      swiper: typeof window.Swiper,
      anime: typeof (window.anime&&window.anime.animate),
      stagger: typeof (window.anime&&window.anime.stagger),
      driver: typeof (window.driver&&window.driver.js&&window.driver.js.driver)
    })`);
    let L = {}; try { L = JSON.parse(libs || '{}'); } catch (e) {}
    check('lib-swiper', L.swiper === 'function', libs);
    check('lib-anime-animate', L.anime === 'function', libs);
    check('lib-anime-stagger', L.stagger === 'function', libs);
    check('lib-driver', L.driver === 'function', libs);

    // ===== B. 首次自动播 + 片头 =====
    const first = await evalJS(`JSON.stringify({
      show: document.getElementById('onboard').classList.contains('show'),
      brandSeen: window.__obBrandSeen, brandEnded: window.__obBrandEnded,
      brandPlaying: document.getElementById('ob-brand').classList.contains('play'),
      slides: Array.from(document.querySelectorAll('#ob-swiper .swiper-slide')).map(function(s){return s.getAttribute('data-idx');}),
      dots: document.querySelectorAll('#ob-dots > *').length,
      live: (document.getElementById('ob-live')||{}).textContent||'',
      crumb: (document.getElementById('ob-sec-name')||{}).textContent||'',
      secNo: (document.getElementById('ob-sec-no')||{}).textContent||'',
      hud: document.querySelectorAll('.ob-hud span').length,
      fam: document.querySelectorAll('.ob-slab.focus').length,
      note: (document.getElementById('ob-art-note')||{}).textContent||'',
      barW: (document.getElementById('ob-progress-bar')||{}).style.width||''
    })`);
    let F = {}; try { F = JSON.parse(first || '{}'); } catch (e) {}
    check('first-autoshow', F.show === true, first);
    check('layout-crumb', /开场/.test(F.crumb || ''), 'crumb=' + F.crumb);
    check('layout-crumb-total', /^01 \/ 08$/.test((F.secNo || '').trim()), 'secNo=' + F.secNo);
    check('layout-progress', /%$/.test(F.barW || '') && parseFloat(F.barW) > 0, 'bar=' + F.barW);
    check('first-8-slides', JSON.stringify(F.slides) === JSON.stringify(['0','1','2','3','4','5','6','7']), 'slides=' + JSON.stringify(F.slides));
    check('first-8-dots', F.dots === 8, 'dots=' + F.dots);
    check('brand-appeared', F.brandSeen === true, first);
    check('brand-ended', F.brandEnded === true && F.brandPlaying === false, first);
    check('live-page-1', /第 1 屏/.test(F.live || '') && /共 8 屏/.test(F.live || ''), 'live=' + F.live);

    // 细节层：开场右下四个硬数字 + 剖面轮转高亮（等一轮 2.6s 才轮到下一块）
    check('detail-hud', F.hud === 4, 'hud spans=' + F.hud);
    await sleep(2900);
    const slab = await evalJS(`JSON.stringify({
      focus: document.querySelectorAll('.ob-slab.focus').length,
      note: (document.getElementById('ob-art-note')||{}).textContent||'',
      first: (document.querySelector('.ob-slab.s1')||{}).className||''
    })`);
    let SL = {}; try { SL = JSON.parse(slab || '{}'); } catch (e) {}
    check('detail-slab-rotate', SL.focus === 1 && /环境安装|程序配置|链端校验|同一个入口/.test(SL.note || ''), slab);

    // ===== C. 真实鼠标翻页 / 跳过 =====
    await clickSel('#ob-next', 'next');
    await sleep(700);
    const live2 = await evalJS(`(document.getElementById('ob-live')||{}).textContent||''`);
    check('mouse-next-page2', /第 2 屏/.test(live2 || ''), 'live=' + live2);

    await clickSel('#ob-skip', 'skip');
    await sleep(400);
    const afterSkip = await evalJS(`JSON.stringify({
      show: document.getElementById('onboard').classList.contains('show'),
      key: localStorage.getItem('ek-guide-v1'),
      obActive: document.body.classList.contains('ob-active')
    })`);
    let S = {}; try { S = JSON.parse(afterSkip || '{}'); } catch (e) {}
    check('skip-closes', S.show === false && S.obActive === false, afterSkip);
    check('skip-marks-seen', S.key === '1', afterSkip);

    // reload：已看过 → 不再自动播
    await send('Page.reload', {});
    await sleep(2600);
    check('no-autoplay-after-seen', (await evalJS(`document.getElementById('onboard').classList.contains('show')`)) === false);

    // ===== C2. 产品头像状态体：接在真实等待上、CSS 真的有呼吸环、且必须能结束 =====
    const logo0 = await evalJS(`JSON.stringify((function(){
      var g=document.getElementById('app-logo');
      return {exists:!!g, src:g?g.src.slice(0,22):'', waiting:g?g.classList.contains('ek-wait'):null,
              busy:g?g.getAttribute('aria-busy'):null};
    })())`);
    let LG = {}; try { LG = JSON.parse(logo0 || '{}'); } catch (e) {}
    check('avatar-exists-and-loaded', LG.exists === true && /^data:image\/png;base64/.test(LG.src || ''), logo0);
    check('avatar-wait-ended-after-load', LG.waiting === false && LG.busy === null, logo0);
    const waitOn = await evalJS(`(function(){
      var g=document.getElementById('app-logo');
      window.__endWait = EKOnboard.wait(g, '演示等待');
      return JSON.stringify({cls:g.classList.contains('ek-wait'), busy:g.getAttribute('aria-busy'),
        title:g.getAttribute('title'), anim:getComputedStyle(g).animationName});
    })()`);
    let WO = {}; try { WO = JSON.parse(waitOn || '{}'); } catch (e) {}
    check('avatar-wait-has-real-style', WO.cls === true && WO.busy === 'true' &&
      WO.title === '演示等待' && WO.anim === 'ekWaitPulse', waitOn);
    const waitOff = await evalJS(`(function(){
      window.__endWait(); window.__endWait();
      var g=document.getElementById('app-logo');
      return JSON.stringify({cls:g.classList.contains('ek-wait'), busy:g.getAttribute('aria-busy'),
        title:g.getAttribute('title')});
    })()`);
    check('avatar-wait-clears-and-idempotent', /"cls":false/.test(waitOff || '') &&
      /"busy":null/.test(waitOff || '') && /"title":null/.test(waitOff || ''), waitOff);

    // ===== D. 「关于 > 重新观看介绍」重放 8 屏 =====
    await evalJS(`(function(){ document.getElementById('about').classList.add('show'); return 'ok'; })()`);
    await sleep(250);
    await clickSel('#about-replay', 'replay');
    await sleep(700);
    const rep = await evalJS(`JSON.stringify({
      show: document.getElementById('onboard').classList.contains('show'),
      slides: Array.from(document.querySelectorAll('#ob-swiper .swiper-slide')).map(function(s){return s.getAttribute('data-idx');}),
      dots: document.querySelectorAll('#ob-dots > *').length,
      tourHidden: document.getElementById('ob-cta-tour').hidden,
      live: (document.getElementById('ob-live')||{}).textContent||''
    })`);
    let R = {}; try { R = JSON.parse(rep || '{}'); } catch (e) {}
    check('replay-opens', R.show === true, rep);
    check('replay-8-slides', JSON.stringify(R.slides) === JSON.stringify(['0','1','2','3','4','5','6','7']), 'slides=' + JSON.stringify(R.slides));
    check('replay-8-dots', R.dots === 8, 'dots=' + R.dots);
    check('tour-btn-visible', R.tourHidden === false, 'hidden=' + R.tourHidden);
    check('replay-no-brand', (await evalJS(`document.getElementById('ob-brand').classList.contains('play')`)) === false);

    // ===== E. 演示期间零写操作（打桩记 fetch；init 的 POST /api/check 在打桩之前） =====
    await evalJS(`(function(){
      window.__obFetch=[]; var o=window.fetch;
      window.fetch=function(u,opt){ window.__obFetch.push(String(u)+'|'+String((opt&&opt.method)||'GET')); return o.apply(this,arguments); };
      return 'ok';
    })()`);

    // 屏 2（为什么存在）：v10 复线轨道。车头只在点击后移动，不是执行进度。
    await arrowRight();
    await sleep(500);
    // 存完整请求字节，不存数量：后台有周期轮询，窗口一长 count 必然涨，
    // 只比数量会把「全局轮询」误判成「02 屏发了请求」。
    const rcFetchBase = await evalJS(`JSON.stringify(window.__obFetch||[])`);
    const rcShape = await evalJS(`JSON.stringify({
      rows: document.querySelectorAll('#ob-recheck .ob-rc-row').length,
      crit: document.querySelectorAll('#ob-recheck .ob-rc-crit').length,
      code: document.querySelectorAll('#ob-recheck .ob-rc-obs code').length,
      audit: document.querySelectorAll('#ob-recheck .ob-rc-audit span').length,
      stamp: document.querySelectorAll('#ob-recheck .ob-rc-stamp').length,
      empty: document.querySelectorAll('#ob-rc-detail .ob-rc-detail-empty').length,
      axis: !!document.getElementById('ob-rc-rows')
    })`);
    let RCV_SHAPE = {}; try { RCV_SHAPE = JSON.parse(rcShape || '{}'); } catch (e) {}
    check('detail-recheck-shape', RCV_SHAPE.rows === 3 && RCV_SHAPE.crit === 3 && RCV_SHAPE.code === 6 &&
      RCV_SHAPE.audit === 5 && RCV_SHAPE.stamp === 1 && RCV_SHAPE.empty === 1 && RCV_SHAPE.axis === true, rcShape);

    /* 图纸描线动画两条判据：
       ① 进屏时必须真的在画（SVG 子元素上挂着 >20 条动画），否则就是"看起来有动画其实没跑"；
       ② 画完必须自己收干净——onfinish 不 cancel 会把 dasharray 留在元素上，虚线被抹成实线；
          卡在 backwards 填充则元素永久透明，比不画更糟。 */
    const rcInkLive = await evalJS(`(function(){
      var svg=document.querySelector('#ob-recheck .ob-rc-sheet'); if(!svg) return -1;
      var list=document.getAnimations?document.getAnimations():[], n=0;
      for(var i=0;i<list.length;i++){
        var t=list[i].effect&&list[i].effect.target;
        if(t&&t.ownerSVGElement===svg) n++;
      }
      return n;
    })()`);
    check('detail-bp-ink-running', rcInkLive > 20, 'anims=' + rcInkLive);

    await sleep(3200);
    const rcInkSettled = await evalJS(`(function(){
      var svg=document.querySelector('#ob-recheck .ob-rc-sheet'); if(!svg) return 'null';
      var list=document.getAnimations?document.getAnimations():[], left=0;
      for(var i=0;i<list.length;i++){
        var t=list[i].effect&&list[i].effect.target;
        if(t&&t.ownerSVGElement===svg) left++;
      }
      var kids=svg.children, hidden=0;
      for(var k=0;k<kids.length;k++){
        var el=kids[k]; if(el.tagName==='defs') continue;
        /* 只抓「卡在 0」：网格层本来就故意 .55/.9，用 <0.99 判会把设计值误判成缺陷 */
        if(parseFloat(getComputedStyle(el).opacity)<0.05) hidden++;
      }
      return JSON.stringify({left:left,hidden:hidden});
    })()`);
    let RC_INK = {}; try { RC_INK = JSON.parse(rcInkSettled || '{}'); } catch (e) {}
    check('detail-bp-ink-settled', RC_INK.left === 0 && RC_INK.hidden === 0, rcInkSettled);

    // 旧造型必须真删干净；进屏也不允许自动展开/自动播（v3.8 删假进度那条红线）
    const rcOldShape = await evalJS(`JSON.stringify({
      win: document.querySelectorAll('.ob-win-card').length,
      wire: document.querySelectorAll('.ob-wires path').length,
      foot: document.querySelectorAll('.ob-merge-foot').length,
      flow: document.querySelectorAll('.ob-merge.flow').length,
      replay: document.querySelectorAll('#ob-merge-replay').length,
      autoOn: document.querySelectorAll('#ob-recheck .ob-rc-row.on').length
    })`);
    let RCV_OLD = {}; try { RCV_OLD = JSON.parse(rcOldShape || '{}'); } catch (e) {}
    check('detail-recheck-no-fake', RCV_OLD.win === 0 && RCV_OLD.wire === 0 && RCV_OLD.foot === 0 &&
      RCV_OLD.flow === 0 && RCV_OLD.replay === 0 && RCV_OLD.autoOn === 0, rcOldShape);

    // 进屏时车头必须停在车库，且轨道/车头不能截获指针（装饰层挡住按钮是老坑）
    const rcPark = await evalJS(`JSON.stringify({
      stop: document.getElementById('ob-rc-train').style.getPropertyValue('--stop'),
      moving: document.getElementById('ob-rc-rows').classList.contains('moving'),
      routed: document.getElementById('ob-rc-rows').classList.contains('routed'),
      railPE: getComputedStyle(document.querySelector('#ob-recheck .ob-rc-rail')).pointerEvents,
      trainPE: getComputedStyle(document.getElementById('ob-rc-train')).pointerEvents
    })`);
    let RCV_PARK = {}; try { RCV_PARK = JSON.parse(rcPark || '{}'); } catch (e) {}
    // 车库态允许两种写法：从未写过 --stop（CSS 默认 0px），或已被 rcDrive(null) 显式归零
    check('detail-recheck-train-park', RCV_PARK.moving === false && RCV_PARK.routed === false &&
      (RCV_PARK.stop === '' || RCV_PARK.stop === '0px') &&
      RCV_PARK.railPE === 'none' && RCV_PARK.trainPE === 'none', rcPark);

    // 真鼠标点第 2 个判据 → 展开它的探测方式/阈值/来源，且只有第 2 行点亮
    await clickSel('#ob-recheck .ob-rc-crit[data-crit="1"]', 'crit2');
    // 行进态是短时的（700ms 后自动摘掉），必须紧跟点击读，晚一步就恒假
    const rcMoving = await evalJS(`JSON.stringify({
      moving: document.getElementById('ob-rc-rows').classList.contains('moving'),
      routed: document.getElementById('ob-rc-rows').classList.contains('routed'),
      stop: document.getElementById('ob-rc-train').style.getPropertyValue('--stop')
    })`);
    let RCV_MV = {}; try { RCV_MV = JSON.parse(rcMoving || '{}'); } catch (e) {}
    check('detail-recheck-train-moving', RCV_MV.moving === true && RCV_MV.routed === true &&
      /^\d+px$/.test(RCV_MV.stop || '') && parseFloat(RCV_MV.stop) > 20, rcMoving);
    await sleep(180);
    const rcOpen = await evalJS(`JSON.stringify({
      items: document.querySelectorAll('#ob-rc-detail .ob-rc-ditem').length,
      on: document.querySelectorAll('#ob-recheck .ob-rc-row.on').length,
      onIdx: (document.querySelector('#ob-recheck .ob-rc-row.on')||{getAttribute:function(){return null}}).getAttribute('data-crit'),
      txt: (document.getElementById('ob-rc-detail')||{}).innerText||'',
      empty: document.querySelectorAll('#ob-rc-detail .ob-rc-detail-empty').length
    })`);
    let RCV_OPEN = {}; try { RCV_OPEN = JSON.parse(rcOpen || '{}'); } catch (e) {}
    check('detail-recheck-open', RCV_OPEN.items === 3 && RCV_OPEN.on === 1 && RCV_OPEN.onIdx === '1' &&
      RCV_OPEN.empty === 0 && /farm/.test(RCV_OPEN.txt||''), rcOpen);

    // 等过渡跑完，量真实盒子：车头中心必须落在被点那一站的中心（不读 --stop，
    // 那只是我们自己写的目标值；这里用 DOM 盒子做独立事实源）
    await sleep(620);
    const rcArrive = await evalJS(`(function(){
      var t=document.getElementById('ob-rc-train');
      var r=document.querySelector('#ob-recheck .ob-rc-row[data-crit="1"]');
      if(!t||!r) return 'null';
      var a=t.getBoundingClientRect(), b=r.getBoundingClientRect();
      return JSON.stringify({d:Math.abs((a.top+a.height/2)-(b.top+b.height/2)),
        moving:document.getElementById('ob-rc-rows').classList.contains('moving')});
    })()`);
    let RCV_AR = {}; try { RCV_AR = JSON.parse(rcArrive || '{}'); } catch (e) {}
    check('detail-recheck-train-arrive', typeof RCV_AR.d === 'number' && RCV_AR.d <= 3 &&
      RCV_AR.moving === false, rcArrive);

    // 换一个判据 → 展开内容必须跟着换。防「永远渲染第一项」这种最容易被放过的回归
    await clickSel('#ob-recheck .ob-rc-crit[data-crit="2"]', 'crit3');
    await sleep(180);
    const rcSwitch = await evalJS(`JSON.stringify({
      on: document.querySelectorAll('#ob-recheck .ob-rc-row.on').length,
      onIdx: (document.querySelector('#ob-recheck .ob-rc-row.on')||{getAttribute:function(){return null}}).getAttribute('data-crit'),
      txt: (document.getElementById('ob-rc-detail')||{}).innerText||'',
      stop: document.getElementById('ob-rc-train').style.getPropertyValue('--stop')
    })`);
    let RCV_SW = {}; try { RCV_SW = JSON.parse(rcSwitch || '{}'); } catch (e) {}
    // 链端判据＝共识视图推进，不是块高（verify.go:617 verifyChain）。
    // 必须含「共识」，且禁止出现被推翻的旧口径「区块高度」——谁把旧判据写回来这里就红。
    check('detail-recheck-switch', RCV_SW.on === 1 && RCV_SW.onIdx === '2' &&
      /共识/.test(RCV_SW.txt||'') && !/区块高度/.test(RCV_SW.txt||'') &&
      !/farm/.test(RCV_SW.txt||''), rcSwitch);
    // 两行观测值也必须是同一把尺子：首测视图停住、复验视图推进，且起点相同。
    // 抓「把块高读数写回演示」和「编一组对不上的数字」。
    const rcView = await evalJS(`(function(){
      var r=document.querySelector('#ob-recheck .ob-rc-row[data-crit="2"]'); if(!r) return '{}';
      var b=r.querySelector('.ob-rc-obs.before code'), a=r.querySelector('.ob-rc-obs.after code');
      return JSON.stringify({before:b?b.textContent:'', after:a?a.textContent:''});
    })()`);
    let RCV_VIEW = {}; try { RCV_VIEW = JSON.parse(rcView || '{}'); } catch (e) {}
    const rcNums = function(s){ return (String(s||'').match(/\d+/g)||[]).map(Number); };
    const vb = rcNums(RCV_VIEW.before), va = rcNums(RCV_VIEW.after);
    check('detail-recheck-chain-view', /^view /.test(RCV_VIEW.before||'') &&
      /^view /.test(RCV_VIEW.after||'') &&
      !/height|区块高度/.test((RCV_VIEW.before||'')+(RCV_VIEW.after||'')) &&
      vb.length === 2 && vb[0] === vb[1] && va.length === 2 && va[1] > va[0] && va[0] === vb[0], rcView);
    // 换站时车头必须换落点，不能停在上一站装作"已经到过"
    check('detail-recheck-train-retarget', parseFloat(RCV_SW.stop) - parseFloat(RCV_MV.stop) > 30,
      'crit1=' + RCV_MV.stop + ' crit2=' + RCV_SW.stop);

    // 移开鼠标，点住态不能被 hover 清掉
    await send('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 4, y: 4 });
    await sleep(150);
    const rcSticky = await evalJS(`document.querySelectorAll('#ob-recheck .ob-rc-row.on').length`);
    check('detail-recheck-sticky', rcSticky === 1, 'on after leaving=' + rcSticky);

    // 再点一次同一个判据 → 收起，回到空态
    await clickSel('#ob-recheck .ob-rc-crit[data-crit="2"]', 'crit3-off');
    await sleep(160);
    const rcOff = await evalJS(`JSON.stringify({
      on: document.querySelectorAll('#ob-recheck .ob-rc-row.on').length,
      empty: document.querySelectorAll('#ob-rc-detail .ob-rc-detail-empty').length,
      items: document.querySelectorAll('#ob-rc-detail .ob-rc-ditem').length
    })`);
    let RCV_OFF = {}; try { RCV_OFF = JSON.parse(rcOff || '{}'); } catch (e) {}
    check('detail-recheck-toggle-off', RCV_OFF.on === 0 && RCV_OFF.empty === 1 && RCV_OFF.items === 0, rcOff);
    // 收起 = 回车库：车头回到起点且不再高亮，不能留在最后一站冒充结果
    const rcGarage = await evalJS(`JSON.stringify({
      stop: document.getElementById('ob-rc-train').style.getPropertyValue('--stop'),
      routed: document.getElementById('ob-rc-rows').classList.contains('routed')
    })`);
    let RCV_GR = {}; try { RCV_GR = JSON.parse(rcGarage || '{}'); } catch (e) {}
    check('detail-recheck-train-garage', RCV_GR.stop === '0px' && RCV_GR.routed === false, rcGarage);

    // 两个"没意义"的按钮仍然不能回来：重播汇聚 / 依次打勾
    const deadBtn = await evalJS(`JSON.stringify({
      replay: !!document.getElementById('ob-merge-replay'),
      run: !!document.getElementById('ob-track-run')
    })`);
    let DB = {}; try { DB = JSON.parse(deadBtn || '{}'); } catch (e) {}
    check('no-fake-buttons', DB.replay === false && DB.run === false, deadBtn);

    // 02 屏不许引入新请求：轨道是静态结构示意，车头移动只是选中态而不是在跑检测。
    // 判据用请求字节本身（独立于实现）：新增项必须全是 GET，且 URL 在进屏前就
    // 出现过（已知全局轮询）。任何新端点、任何写方法都会红，不受停留时长影响。
    const rcFetchAfter = await evalJS(`JSON.stringify(window.__obFetch||[])`);
    let RC_BEFORE = [], RC_AFTER = [];
    try { RC_BEFORE = JSON.parse(rcFetchBase || '[]'); RC_AFTER = JSON.parse(rcFetchAfter || '[]'); } catch (e) {}
    const rcAdded = RC_AFTER.slice(RC_BEFORE.length);
    const rcKnown = new Set(RC_BEFORE.map(s => String(s).split('|')[0]));
    check('detail-recheck-zero-write',
      rcAdded.every(s => /\|GET$/.test(String(s)) && rcKnown.has(String(s).split('|')[0])),
      'before=' + RC_BEFORE.length + ' after=' + RC_AFTER.length +
      ' added=' + JSON.stringify(rcAdded));

    // 屏 3（四步走）：点步骤看说明卡切换（自动前进已在此前 2.3s 内走了一格）
    await arrowRight();
    const stepBefore = await evalJS(`(document.getElementById('ob-step-desc')||{}).innerText||''`);
    await clickSel('#ob-track .ob-step[data-step="2"]', 'step2');
    await sleep(250);
    const stepAfter = await evalJS(`(document.getElementById('ob-step-desc')||{}).innerText||''`);
    check('demo-step-desc', /环境安装/.test(stepBefore || '') && /链端校验/.test(stepAfter || '') && /WeBASE|FISCO/.test(stepAfter || ''),
      'before=' + (stepBefore || '').slice(0, 30) + ' after=' + (stepAfter || '').slice(0, 40));
    // 详情卡现在是 2×2：输入 / 产出 / 前置 / 预计耗时
    const ioCount = await evalJS(`JSON.stringify({
      n: document.querySelectorAll('#ob-step-desc .ob-io span').length,
      t: (document.getElementById('ob-step-desc')||{}).innerText||''
    })`);
    let IO = {}; try { IO = JSON.parse(ioCount || '{}'); } catch (e) {}
    check('detail-step-io4', IO.n === 4 && /前置/.test(IO.t || '') && /预计耗时/.test(IO.t || ''),
      'spans=' + IO.n + ' txt=' + (IO.t || '').replace(/\n/g, ' ').slice(0, 70));

    // 没有"完成"标记：演示一次也没跑过，勾号等于撒谎。切到纯只读的那一步，
    // 标签必须变成「只读」而不是「会写」。
    const stepMeta = await evalJS(`document.querySelectorAll('#ob-track .ob-step.done').length`);
    await clickSel('#ob-track .ob-step[data-step="2"]', 'step3-ro');
    await sleep(220);
    const roTxt = await evalJS(`JSON.stringify({
      chip: (document.querySelector('#ob-step-desc .ob-writes em')||{}).textContent||'',
      body: (document.querySelector('#ob-step-desc .ob-writes span')||{}).textContent||'',
      done: document.querySelectorAll('#ob-track .ob-step.done').length
    })`);
    let RO = {}; try { RO = JSON.parse(roTxt || '{}'); } catch (e) {}
    check('detail-step-nowrite-check', RO.done === 0 && stepMeta === 0, roTxt + ' / done=' + stepMeta);
    check('detail-step-writes', RO.chip === '只读' && /不写任何东西/.test(RO.body || ''), roTxt);
    await clickSel('#ob-track .ob-step[data-step="0"]', 'step1-back');
    await sleep(180);

    // 屏 4（检测报告）：逐行扫入 + 总评
    await arrowRight();
    await clickSel('#ob-detect-run', 'detect');
    await sleep(1700);
    const det = await evalJS(`JSON.stringify({
      lit: document.querySelectorAll('#ob-detect-rows .ob-row.lit').length,
      warn: document.querySelectorAll('#ob-detect-rows .ob-row.warn').length,
      grp: document.querySelectorAll('#ob-detect-rows .ob-grp').length,
      score: (document.getElementById('ob-vd-score')||{}).textContent||'',
      msg: (document.getElementById('ob-vd-msg')||{}).textContent||'',
      segOk: document.querySelectorAll('#ob-segs i.ok').length,
      segWarn: document.querySelectorAll('#ob-segs i.warn').length
    })`);
    let D = {}; try { D = JSON.parse(det || '{}'); } catch (e) {}
    check('demo-detect-scan', D.lit === 6 && D.warn === 1, det);
    // 结论先行的三段结构：分组卡 / 记分 5 / 六段 5 绿 1 黄 / 判断句
    check('detail-detect-groups', D.grp === 3, 'groups=' + D.grp);
    check('detail-detect-score', D.score === '5', 'score=' + D.score);
    check('detail-detect-segs', D.segOk === 5 && D.segWarn === 1, 'segs=' + D.segOk + 'ok/' + D.segWarn + 'warn');
    check('detail-detect-msg', /待校验/.test(D.msg || '') && /链端/.test(D.msg || ''), 'msg=' + D.msg);
    const termLines = await evalJS(`document.querySelectorAll('#ob-detect-cmd div').length`);
    check('demo-detect-console', termLines === 4, 'console lines=' + termLines);
    // 细节层：终端进度条跑满 + 报告结论行出现
    const dfoot = await evalJS(`JSON.stringify({
      on: document.getElementById('ob-report-foot').classList.contains('on'),
      txt: (document.getElementById('ob-report-foot')||{}).innerText||'',
      pct: (document.getElementById('ob-term-pct')||{}).textContent||'',
      progW: (document.getElementById('ob-term-prog').firstElementChild.getAttribute('style')||'')
    })`);
    let DF = {}; try { DF = JSON.parse(dfoot || '{}'); } catch (e) {}
    check('detail-detect-conclusion', DF.on === true && /配置/.test(DF.txt || '') && /链端/.test(DF.txt || '') &&
      /1\.8s/.test(DF.pct || '') && /100%/.test(DF.progW || ''), dfoot);

    // 屏 5（日志分区）：切 scope + 插一条错误看联动
    await arrowRight();
    await clickSel('#ob-scopes button[data-scope="chain"]', 'scope-chain');
    await sleep(220);
    const scopeTxt = await evalJS(`(document.getElementById('ob-log-lines')||{}).innerText||''`);
    await clickSel('#ob-log-insert', 'log-insert');
    await sleep(220);
    const errState = await evalJS(`JSON.stringify({
      hot: document.getElementById('ob-recent-err').classList.contains('hot'),
      recent: (document.getElementById('ob-recent-err')||{}).innerText||'',
      last: (document.getElementById('ob-log-lines')||{}).lastElementChild?(document.getElementById('ob-log-lines').lastElementChild.textContent||''):''
    })`);
    let E = {}; try { E = JSON.parse(errState || '{}'); } catch (e) {}
    check('demo-scope-switch', /WeBASE-Front/.test(scopeTxt || ''), 'log=' + (scopeTxt || '').slice(0, 50));
    check('demo-log-error-link', E.hot === true && /端口 8888/.test(E.recent || '') && /演示异常/.test(E.last || ''), errState);
    // 细节层：侧栏五个分区计数都在，且当前分区高亮到了「链端」
    const stats = await evalJS(`JSON.stringify({
      n: document.querySelectorAll('#ob-scope-stats i').length,
      on: (document.querySelector('#ob-scope-stats i.on em')||{}).textContent||'',
      rot: (document.querySelector('.ob-rotate span')||{}).textContent||''
    })`);
    let ST = {}; try { ST = JSON.parse(stats || '{}'); } catch (e) {}
    check('detail-scope-stats', ST.n === 5 && ST.on === '链端' && /30 天/.test(ST.rot || ''), stats);

    // 屏 6（AI 与桌宠）：真桌宠克隆 + 写类问句弹演示确认卡，确认后收起
    await arrowRight();
    const pet = await evalJS(`JSON.stringify({
      svg: !!document.querySelector('#ob-pet-slot svg'),
      imgs: document.querySelectorAll('#ob-pet-slot .pet-view').length,
      shown: (function(){ var v=document.querySelectorAll('#ob-pet-slot .pet-view');
        if(!v.length) return false;
        return getComputedStyle(v[v.length-1]).opacity === '1'; })()
    })`);
    let P = {}; try { P = JSON.parse(pet || '{}'); } catch (e) {}
    check('demo-pet-cloned', P.svg === true && P.imgs === 3 && P.shown === true, pet);

    await clickSel('.ob-questions button[data-q="write"]', 'q-write');
    await sleep(250);
    const q = await evalJS(`JSON.stringify({
      confirm: document.getElementById('ob-demo-confirm').hidden,
      bubble: (document.getElementById('ob-bubble')||{}).textContent||''
    })`);
    let Q = {}; try { Q = JSON.parse(q || '{}'); } catch (e) {}
    check('demo-confirm-card', Q.confirm === false && /确认卡/.test(Q.bubble || ''), q);

    // 确认卡是排进对话流里的，不是浮层：浮层会正好盖住刚打出来的那句回答
    const geo = await evalJS(`JSON.stringify((function(){
      var c=document.getElementById('ob-demo-confirm'), l=document.getElementById('ob-chat-log'),
          b=document.getElementById('ob-bubble'), qs=document.querySelector('.ob-questions');
      var cr=c.getBoundingClientRect(), lr=l.getBoundingClientRect(), br=b.getBoundingClientRect(),
          qr=qs.getBoundingClientRect();
      return {pos:getComputedStyle(c).position, cardTop:Math.round(cr.top),
        logBottom:Math.round(lr.bottom), bubbleBottom:Math.round(br.bottom), qTop:Math.round(qr.top)};
    })())`);
    let G = {}; try { G = JSON.parse(geo || '{}'); } catch (e) {}
    check('detail-confirm-inline', G.pos === 'relative' && G.cardTop >= G.logBottom - 1 &&
      G.cardTop >= G.bubbleBottom - 1 && G.cardTop < G.qTop, geo);

    // 桌宠气泡两个阶段都要断到：打字期间「思考中」，回答落地后「点我确认！」。
    // 顺序反了就是"先撒花再干活"。
    const say0 = await evalJS(`(document.getElementById('ob-pet-say')||{}).innerText||''`);
    check('detail-pet-bubble-think', /思考中/.test(say0 || ''), 'bubble=' + (say0 || '').slice(0, 20));
    await sleep(1200);
    const typed = await evalJS(`(document.getElementById('ob-bubble')||{}).textContent||''`);
    check('demo-typing-complete', /确认卡在这里/.test(typed || '') && /权限闸门/.test(typed || ''),
      'bubble=' + (typed || '').slice(0, 50));
    // 回答打完 → 气泡换成「点我确认！」并常驻（sticky，不自动收）
    const say1 = await evalJS(`JSON.stringify({
      shown: document.getElementById('ob-pet-say').classList.contains('show'),
      txt: (document.getElementById('ob-pet-say')||{}).innerText||''
    })`);
    let S1 = {}; try { S1 = JSON.parse(say1 || '{}'); } catch (e) {}
    check('detail-pet-bubble-wait', S1.shown === true && /点我确认/.test(S1.txt || ''), say1);
    await clickSel('#ob-demo-ok', 'demo-ok');
    await sleep(250);
    const q2 = await evalJS(`JSON.stringify({
      confirm: document.getElementById('ob-demo-confirm').hidden,
      bubble: (document.getElementById('ob-bubble')||{}).textContent||''
    })`);
    check('demo-confirm-closes', /"confirm":true/.test(q2 || '') && /权限闸门/.test(q2 || ''), q2);

    // 细节层：桌宠状态跟着对话变；再问一句，上一轮回答要冻结成历史气泡（多轮而不是覆盖）
    const petAfterOk = await evalJS(`(document.getElementById('ob-pet-msg')||{}).textContent||''`);
    await clickSel('.ob-questions button[data-q="read"]', 'q-read');
    await sleep(1500);
    const hist = await evalJS(`JSON.stringify({
      frozen: document.querySelectorAll('#ob-chat-log .ob-bubble.frozen').length,
      frozenTxt: (document.querySelector('#ob-chat-log .ob-bubble.frozen')||{}).textContent||'',
      traces: document.querySelectorAll('#ob-chat-log .ob-trace').length,
      ms: (document.querySelector('#ob-chat-log .ob-trace em')||{}).textContent||'',
      pet: (document.getElementById('ob-pet-msg')||{}).textContent||'',
      bubble: (document.getElementById('ob-bubble')||{}).textContent||'',
      confirm: document.getElementById('ob-demo-confirm').hidden
    })`);
    let H = {}; try { H = JSON.parse(hist || '{}'); } catch (e) {}
    check('detail-ai-history', H.frozen >= 1 && /权限闸门/.test(H.frozenTxt || '') && H.traces >= 2 &&
      /0\.31s|0\.42s/.test(H.ms || ''), hist);
    check('detail-ai-pet-state', /已就绪/.test(petAfterOk || '') || /已就绪/.test(H.pet || ''),
      'petAfterOk=' + petAfterOk + ' petNow=' + (H.pet || ''));
    check('detail-ai-second-answer', /后端在跑/.test(H.bubble || '') && H.confirm === true, hist);

    // 桌宠内置对话：四个菜单动作，说的必须是真的内置台词（不是为演示另编的）
    const menuN = await evalJS(`document.querySelectorAll('#ob-pet-menu button[data-pact]').length`);
    check('detail-pet-menu', menuN === 4, 'menu=' + menuN);
    await clickSel('#ob-pet-menu button[data-pact="say"]', 'pet-say');
    await sleep(320);
    const sayRes = await evalJS(`JSON.stringify({
      shown: document.getElementById('ob-pet-say').classList.contains('show'),
      txt: (document.getElementById('ob-pet-say')||{}).innerText||''
    })`);
    let SR = {}; try { SR = JSON.parse(sayRes || '{}'); } catch (e) {}
    check('detail-pet-say-line', SR.shown === true &&
      /我不思考了|我先去吃饭啦|大肥鱼坐的住/.test(SR.txt || ''), sayRes);
    // 这句只有台词、没有补充说明：d 缺失时不能拼出 undefined，也不能多出旁白
    check('detail-pet-say-only', !/undefined|抽一条|台词都是内置的/.test(SR.txt || ''), sayRes);
    check('detail-pet-no-tagline', (await evalJS(`!document.querySelector('.ob-pet-tag')`)) === true);
    // 查天气是两步：先「我瞅瞅天…」，再给实况。一步到位反而假。
    await clickSel('#ob-pet-menu button[data-pact="weather"]', 'pet-weather');
    await sleep(260);
    const wx1 = await evalJS(`(document.getElementById('ob-pet-say')||{}).innerText||''`);
    await sleep(880);
    const wx2 = await evalJS(`(document.getElementById('ob-pet-say')||{}).innerText||''`);
    check('detail-pet-weather', /瞅瞅天/.test(wx1 || '') && /合肥|湿度/.test(wx2 || ''),
      'step1=' + (wx1 || '').slice(0, 16) + ' step2=' + (wx2 || '').slice(0, 30));

    // 屏 7（安全边界）：点最高档看 428 说明
    await arrowRight();
    await clickSel('#ob-perms button[data-perm="3"]', 'perm3');
    await sleep(250);
    const perm = await evalJS(`(document.getElementById('ob-perm-desc')||{}).innerText||''`);
    check('demo-perm-428', /428/.test(perm || '') && /不会执行任意命令/.test(perm || ''), 'desc=' + (perm || '').slice(0, 60));
    // 细节层：三栏清单（直接做 / 要确认 / 不做）
    const acts = await evalJS(`JSON.stringify({
      n: document.querySelectorAll('#ob-perm-acts > div').length,
      ok: (document.querySelector('#ob-perm-acts .ok')||{}).innerText||'',
      no: (document.querySelector('#ob-perm-acts .no')||{}).innerText||''
    })`);
    let PA = {}; try { PA = JSON.parse(acts || '{}'); } catch (e) {}
    check('detail-perm-acts', PA.n === 3 && /直接做/.test(PA.ok || '') &&
      /任意 shell/.test(PA.no || ''), acts);

    // 07 屏联锁图：四路并联，点最高档后只有 P4 点亮；428 与危险色必须在图里。
    // perm 属性只在 showPerm() 里写 —— 抓的正是「改了逻辑忘了同步图」。
    await sleep(400);   /* .il-b 的 opacity 有 .28s 过渡，不等收敛会把过渡中间值误判成缺陷 */
    const ilock = await evalJS(`(function(){
      var w=document.querySelector('.ob-permwrap'), s=document.querySelector('.ob-ilock');
      if(!s) return '{}';
      var gs=[].slice.call(s.querySelectorAll('.il-b'));
      var op=gs.map(function(g){return parseFloat(getComputedStyle(g).opacity);});
      return JSON.stringify({
        perm:w?w.getAttribute('data-perm'):null,
        n:gs.length, hot:op.length===4?op[3]:-1, rest:op.slice(0,3),
        t428:/428/.test(s.textContent||''),
        hotStroke:(function(){var p=s.querySelector('.bp-n.il-hot');
          return p?getComputedStyle(p).stroke:'';})(),
        pe:getComputedStyle(s).pointerEvents
      });
    })()`);
    let IL = {}; try { IL = JSON.parse(ilock || '{}'); } catch (e) {}
    check('detail-ilock-gate', IL.perm === '3' && IL.n === 4 && IL.hot === 1 &&
      Array.isArray(IL.rest) && IL.rest.every(x => x > 0.2 && x < 0.6) &&
      IL.t428 === true && /rgb/.test(IL.hotStroke || '') && IL.pe === 'none', ilock);

    // 屏 8（收尾）：8 个章节点
    await arrowRight();
    const chapters = await evalJS(`document.querySelectorAll('#ob-chapter-grid button').length`);
    check('demo-8-chapters', chapters === 8, 'chapters=' + chapters);
    // 细节层：每章一句摘要 + 总览行
    const chSum = await evalJS(`JSON.stringify({
      sums: Array.from(document.querySelectorAll('.ob-ch-sum')).filter(function(e){return (e.textContent||'').trim().length>3;}).length,
      stats: (document.querySelector('.ob-end-stats')||{}).innerText||''
    })`);
    let CS = {}; try { CS = JSON.parse(chSum || '{}'); } catch (e) {}
    check('detail-chapter-summary', CS.sums === 8 && /8/.test(CS.stats || '') && /90/.test(CS.stats || ''), chSum);

    // 收尾「跟着做一遍」：从「关于」里重看再点它时，#about 还开着。
    // driver.js 会把浮层画在遮罩之上、却按底下控件的坐标定位 —— 于是导航指着一个
    // 被弹窗盖住的位置。断言弹窗被收掉、浮层真的出现、而且落在视口内。
    await clickSel('#ob-cta-tour', 'cta-tour');
    await sleep(400);
    const tour = await evalJS(`JSON.stringify((function(){
      var p=document.querySelector('.driver-popover');
      var r=p?p.getBoundingClientRect():null;
      return {about:document.getElementById('about').classList.contains('show'),
        ob:document.getElementById('onboard').classList.contains('show'),
        pop:!!p, left:r?Math.round(r.left):-1, top:r?Math.round(r.top):-1,
        right:r?Math.round(r.right):-1, vw:window.innerWidth, vh:window.innerHeight,
        overlay:!!document.querySelector('.driver-overlay')};
    })())`);
    let TR = {}; try { TR = JSON.parse(tour || '{}'); } catch (e) {}
    check('tour-closes-modal', TR.about === false && TR.ob === false, tour);
    check('tour-popover-shown', TR.pop === true && TR.overlay === true, tour);
    check('tour-popover-inside', TR.left >= 0 && TR.top >= 0 && TR.right <= TR.vw && TR.top < TR.vh, tour);
    await evalJS(`(function(){ var b=document.querySelector('.driver-popover-close-btn'); if(b) b.click(); return 1; })()`);
    await sleep(300);
    check('tour-closed', (await evalJS(`!!document.querySelector('.driver-popover')`)) === false);

    const fetches = await evalJS(`JSON.stringify(window.__obFetch||[])`);
    let FL = []; try { FL = JSON.parse(fetches || '[]'); } catch (e) {}
    const writes = FL.filter(x => /\|(POST|PUT|DELETE|PATCH)/i.test(x));
    const aiCalls = FL.filter(x => /\/api\/ai\//.test(x));
    check('demo-zero-write-api', writes.length === 0, 'writes=' + JSON.stringify(writes) + ' all=' + FL.length);
    check('demo-zero-ai-api', aiCalls.length === 0, 'ai=' + JSON.stringify(aiCalls));

    // ===== E2. EN 模式：引导 8 屏不得留中文（含 CODE/PRE 与 aria-label） =====
    // 判据是「可见文本节点里还有没有 CJK」，同时记 cnt 作为正对照：
    // 引导没渲染出来时残留也是 0 条，只看 0 会把「没开页面」误判成「全翻好了」。
    await evalJS(`(function(){
      window.__obScan=function(){
        var root=document.getElementById('onboard');
        if(!root) return {err:'no-onboard'};
        var CJK=/[\\u4e00-\\u9fa5]/;
        function vis(el){
          if(!el||el.nodeType!==1) return true;
          var s=getComputedStyle(el);
          if(s.display==='none'||s.visibility==='hidden'||s.opacity==='0') return false;
          return el.getClientRects().length>0;
        }
        /* 语言开关按钮上写的是"点了会变成什么"，EN 模式下就是「中文」——
           那是它该有的样子（主界面 #btn-lang 也是 data-no-i18n），不算残留。 */
        function skipped(el){
          for(var p=el;p&&p!==root;p=p.parentNode){
            if(p.nodeType===1&&p.hasAttribute&&p.hasAttribute('data-no-i18n')) return true;
          }
          return false;
        }
        var cnt=0,bad=[];
        var w=document.createTreeWalker(root,NodeFilter.SHOW_TEXT),n;
        while((n=w.nextNode())){
          var s=(n.data||'').trim();
          if(!s||!vis(n.parentNode)||skipped(n.parentNode)) continue;
          cnt++;
          if(CJK.test(s)) bad.push(s.slice(0,42));
        }
        root.querySelectorAll('[placeholder],[title],[aria-label]').forEach(function(el){
          if(skipped(el)) return;
          ['placeholder','title','aria-label'].forEach(function(a){
            var v=el.getAttribute(a);
            if(v&&CJK.test(v)) bad.push(a+'='+v.slice(0,42));
          });
        });
        return {cnt:cnt,bad:bad.slice(0,6),n:bad.length};
      };
      return 'ok';
    })()`);
    const scanOB = async () => { const s = await evalJS('JSON.stringify(window.__obScan())'); try { return JSON.parse(s); } catch (e) { return { err: String(s) }; } };
    await evalJS(`(function(){ try{ ekSetLang('en', true); }catch(e){} try{ EKOnboard.close(); }catch(e){} EKOnboard.open('replay'); return 'ok'; })()`);
    await sleep(900);
    for (let i = 0; i < 8; i++) {
      await evalJS(`(function(){ var el=document.getElementById('ob-swiper'); if(el&&el.swiper) el.swiper.slideTo(${i},0); return 'ok'; })()`);
      await sleep(1000);
      const sc = await scanOB();
      check('i18n-en-slide-' + (i + 1), !sc.err && sc.cnt > 60 && sc.n === 0, JSON.stringify(sc));
    }
    // 只有点开/找到才可见的细节层：四步详情、四档权限、六个分区、三轮问答、四种桌宠动作
    const i18nDeep = async (selList, label) => {
      for (const sel of selList) {
        // found 一起断言：选择器写错时点击不会发生，残留自然是 0 条 —— 那是空转不是通过
        const found = await evalJS(`(function(){ var b=document.querySelector(${JSON.stringify(sel)}); if(!b) return false; b.click(); return true; })()`);
        await sleep(900);
        const sc = await scanOB();
        check('i18n-en-' + label + '-' + sel.replace(/[^0-9a-z]/gi, ''),
          found === true && !sc.err && sc.n === 0, sel + ' found=' + found + ' :: ' + JSON.stringify(sc));
      }
    };
    await evalJS(`(function(){ var el=document.getElementById('ob-swiper'); if(el&&el.swiper) el.swiper.slideTo(2,0); return 'ok'; })()`);
    await i18nDeep([0, 1, 2, 3].map(k => '#ob-track .ob-step[data-step="' + k + '"]'), 'step');
    await evalJS(`(function(){ var el=document.getElementById('ob-swiper'); if(el&&el.swiper) el.swiper.slideTo(6,0); return 'ok'; })()`);
    await i18nDeep([0, 1, 2, 3].map(k => '#ob-perms [data-perm="' + k + '"]'), 'perm');
    await evalJS(`(function(){ var el=document.getElementById('ob-swiper'); if(el&&el.swiper) el.swiper.slideTo(4,0); return 'ok'; })()`);
    await i18nDeep(['install', 'config', 'chain', 'start', 'sys'].map(k => '#ob-scopes [data-scope="' + k + '"]'), 'scope');
    await evalJS(`(function(){ var el=document.getElementById('ob-swiper'); if(el&&el.swiper) el.swiper.slideTo(5,0); return 'ok'; })()`);
    await i18nDeep(['read', 'write', 'explain'].map(k => '.ob-questions [data-q="' + k + '"]'), 'ask');
    await i18nDeep(['say', 'feed', 'weather', 'err'].map(k => '#ob-pet-menu [data-pact="' + k + '"]'), 'pet');
    // 02 屏两次观测值在 <code> 里：全局翻译器跳过 CODE（真实日志要原文），必须由 trCode() 补
    const codeTxt = await evalJS(`JSON.stringify(Array.from(document.querySelectorAll('#ob-recheck .ob-rc-obs code')).map(function(c){return c.textContent.trim();}))`);
    let CT = []; try { CT = JSON.parse(codeTxt || '[]'); } catch (e) {}
    check('i18n-en-code-nodes', CT.length === 6 && CT.every(s => !/[\u4e00-\u9fa5]/.test(s)), codeTxt);
    const ariaTxt = await evalJS(`JSON.stringify({
      prev: document.getElementById('ob-prev').getAttribute('aria-label'),
      next: document.getElementById('ob-next').getAttribute('aria-label')
    })`);
    let AR = {}; try { AR = JSON.parse(ariaTxt || '{}'); } catch (e) {}
    check('i18n-en-aria-label', /[A-Za-z]/.test(AR.prev || '') && /[A-Za-z]/.test(AR.next || ''), ariaTxt);
    // 挨着切回来：CODE 里的英文要能原路反查到中文（否则切回中文会停在英文）
    await evalJS(`(function(){ try{ ekSetLang('zh', true); }catch(e){} return 'ok'; })()`);
    await sleep(500);
    const backZh = await evalJS(`JSON.stringify(Array.from(document.querySelectorAll('#ob-recheck .ob-rc-obs code')).map(function(c){return c.textContent.trim();}).filter(function(s){return /[\\u4e00-\\u9fa5]/.test(s);}))`);
    let BZ = []; try { BZ = JSON.parse(backZh || '[]'); } catch (e) {}
    // 6 个 code 里现在只有「握手成功 · 12ms」是中文原文（其余是日志/读数，
    // 链端观测值改成 view 号后本来就全是英文与数字）。切回中文时它必须能
    // 从英文原路反查回来——只数数量太松，连文本一起钉住。
    check('i18n-zh-restore-code', BZ.length === 1 && /握手成功/.test(BZ[0] || ''), backZh);
    await evalJS(`(function(){ try{ EKOnboard.close(); }catch(e){} return 'ok'; })()`);
    await sleep(300);

    // ===== E2. 顶栏两个开关：中英 / 明暗 =====
    // 断言的是"软件本身也变了"（body[data-theme] / html[lang] / 主界面按钮文案 / localStorage），
    // 不是"引导层里看起来变了"。两个按钮都用真实鼠标点，并且要求命中测试通过。
    await evalJS(`(function(){ try{ ekSetLang('zh', true); applyTheme('light'); }catch(e){} EKOnboard.open('replay'); return 'ok'; })()`);
    await sleep(900);
    check('tools-count', await evalJS(`document.querySelectorAll('.ob-tools .ob-tool').length`) === 2);

    await clickSel('#ob-theme', 'ob-theme');
    await sleep(600);
    const TH = await evalJS(`JSON.stringify({
      theme: document.body.dataset.theme || '',
      main: (document.getElementById('btn-theme') || {}).textContent,
      ls: (function(){ try{ return localStorage.getItem('ek-theme'); }catch(e){ return null; } })(),
      bg: getComputedStyle(document.getElementById('onboard')).backgroundColor
    })`);
    let th = {}; try { th = JSON.parse(TH || '{}'); } catch (e) {}
    check('theme-switch-app', th.theme === 'dark' && th.ls === 'dark', TH);
    check('theme-switch-main-ui', /亮色|Light/.test(th.main || ''), TH);
    check('theme-switch-page', /9, 13, 20|#090d14/i.test(th.bg || ''), TH);

    // 粒子层：画布有尺寸、真的画了东西、并且不吃点击
    const FX = await evalJS(`(function(){
      var c = document.getElementById('ob-fx');
      if(!c || !c.getContext) return JSON.stringify({ok:false});
      var ctx = c.getContext('2d'), r = c.getBoundingClientRect();
      var d = ctx.getImageData(0, 0, c.width, c.height).data, lit = 0;
      for (var i = 3; i < d.length; i += 4) { if (d[i] > 8) lit++; }
      return JSON.stringify({ok:true, w:Math.round(r.width), h:Math.round(r.height), lit:lit,
        pe:getComputedStyle(c).pointerEvents});
    })()`);
    let fx = {}; try { fx = JSON.parse(FX || '{}'); } catch (e) {}
    check('fx-canvas', fx.ok === true && fx.w > 1200 && fx.h > 700, FX);
    check('fx-drawing', fx.ok === true && fx.lit > 200, FX);        /* 只建 canvas 不画也算失败 */
    check('fx-click-through', fx.pe === 'none', FX);                 /* 铺满全屏的图层不许挡住按钮 */

    await clickSel('#ob-lang', 'ob-lang');
    await sleep(700);
    const LGT = await evalJS(`JSON.stringify({
      html: document.documentElement.getAttribute('lang'),
      main: (document.getElementById('btn-lang') || {}).textContent,
      ob: (document.getElementById('ob-lang') || {}).textContent,
      crumb: (document.getElementById('ob-sec-name') || {}).textContent
    })`);
    let lg = {}; try { lg = JSON.parse(LGT || '{}'); } catch (e) {}
    check('lang-switch-html', lg.html === 'en', LGT);
    check('lang-switch-main-ui', lg.main === '中文', LGT);
    /* 语言按钮上写的就是"点了会变成什么"，EN 模式下应是「中文」——它是 data-no-i18n 的，
       所以中文残留扫描要跳过它（__obScan 的 skipped()） */
    check('lang-switch-ob-btn', lg.ob === '中文', LGT);
    check('lang-switch-onboard-text', !!lg.crumb && !/[\u4e00-\u9fa5]/.test(lg.crumb), LGT);

    // 复原：暗色关掉、语言切回中文、引导关掉，后面的检查还按默认状态跑
    await clickSel('#ob-theme', 'ob-theme-back');
    await sleep(400);
    await evalJS(`(function(){ try{ ekSetLang('zh', true); }catch(e){} EKOnboard.close(); return 'ok'; })()`);
    await sleep(400);
    const RS = await evalJS(`JSON.stringify({theme:document.body.dataset.theme||'', lang:document.documentElement.getAttribute('lang'),
      show:document.getElementById('onboard').classList.contains('show')})`);
    let rs = {}; try { rs = JSON.parse(RS || '{}'); } catch (e) {}
    check('tools-restored', rs.theme === 'light' && rs.lang !== 'en' && rs.show === false, RS);

    // ===== E3. AI 抽屉的 sys 提示条跟着语言走 =====
    // 用户实测反馈：中文模式下「恢复最近对话」还写着 "Restored previous conversation (10 messages)"。
    // 根因不是漏翻，是 aiAdd 给所有消息打了 data-no-i18n（保护模型原文），而 sys 条是界面文案、
    // 且 {0} 已替换成数字，切语言时既进不了 Observer、也查不到字典 → 永远停在生成时那门语言。
    const SY = await evalJS(`(function(){
      try{
        var out = {};
        ekSetLang('zh', false);
        var a = aiSys('已恢复上次的对话（{0} 条）', 10);
        out.zh = a.textContent.trim();
        ekSetLang('en', false);
        out.en = a.textContent.trim();
        ekSetLang('zh', false);
        out.zh2 = a.textContent.trim();
        /* 反向：英文态生成（等于"上次是英文，这次切回中文"）也必须能翻回来 */
        ekSetLang('en', false);
        var b = aiSys('已恢复上次的对话（{0} 条）', 7);
        out.en2 = b.textContent.trim();
        ekSetLang('zh', false);
        out.zh3 = b.textContent.trim();
        a.remove(); b.remove();
        return JSON.stringify(out);
      }catch(e){ return 'EX:' + e.message; }
    })()`);
    let sy = {}; try { sy = JSON.parse(SY || '{}'); } catch (e) {}
    check('sys-i18n-zh', sy.zh === '已恢复上次的对话（10 条）', SY);
    check('sys-i18n-zh2en', sy.en === 'Restored previous conversation (10 messages)', SY);
    check('sys-i18n-en2zh', sy.zh2 === '已恢复上次的对话（10 条）', SY);
    check('sys-i18n-en-born-zh', sy.zh3 === '已恢复上次的对话（7 条）', SY);
    // 源码级：aiAdd('sys', …) 直接调用会绕过重译表，必须走 aiSys（aiSys 自身那一处除外）
    const aiSrc = fs.readFileSync(path.join(REPO, 'web', 'src', 'js', '60-ai.js'), 'utf8')
      + fs.readFileSync(path.join(REPO, 'web', 'src', 'js', '62-agent.js'), 'utf8')
      + fs.readFileSync(path.join(REPO, 'web', 'src', 'js', '64-ai-settings.js'), 'utf8');
    const rawSys = (aiSrc.match(/aiAdd\('sys'/g) || []).length;
    check('src-sys-via-aiSys', rawSys === 1, 'aiAdd(\'sys\') 出现 ' + rawSys + ' 次（应只有 aiSys 内部那一处）');

    // ===== F. 源码级红线：演示代码不得碰接口 / 任意执行 =====
    const src = fs.readFileSync(path.join(REPO, 'web', 'src', 'js', '90-onboard.js'), 'utf8');
    check('src-no-fetch', !/fetch\s*\(|XMLHttpRequest|new WebSocket|sendBeacon/.test(src));
    check('src-no-api-path', !/['"`]\/api\//.test(src));
    check('src-no-eval', !/\beval\s*\(|new Function|document\.write/.test(src));
    check('src-only-seen-write', (src.match(/localStorage\.setItem/g) || []).length === 1);
    // 06 屏那两句解释性旁白已按需求删掉，别再被顺手加回来
    const tpl = fs.readFileSync(path.join(REPO, 'web', 'src', 'index.tpl.html'), 'utf8');
    check('src-no-pet-tagline', !/台词都是内置的|抽一条/.test(src + tpl));

    // ===== G. 全站柔和配色落地（换色后无绿、docx 格式色仍在） =====
    const brandVal = await evalJS(`getComputedStyle(document.documentElement).getPropertyValue('--brand').trim()`);
    check('theme-brand-soft-blue', /#526E96/i.test(brandVal || ''), '--brand=' + brandVal);
    const css = fs.readFileSync(path.join(REPO, 'web', 'src', 'app.css'), 'utf8');
    check('theme-docx-format-color-kept', /#2b579a/i.test(css));

    // ===== H. 自动化兼容：恢复默认 headless UA + 清标记 → 不自动播，但手动可开 =====
    if (ident) await send('Page.removeScriptToEvaluateOnNewDocument', { identifier: ident });
    await evalJS(`(function(){ try{ localStorage.removeItem('ek-guide-v1'); }catch(e){}; try{ EKOnboard.close(); }catch(e){}; return 'ok'; })()`);
    await send('Emulation.setUserAgentOverride', { userAgent: '', acceptLanguage: '', platform: '' });
    await send('Page.reload', {});
    await sleep(2600);
    const auto = await evalJS(`JSON.stringify({
      wd: navigator.webdriver,
      headless: /HeadlessChrome/i.test(navigator.userAgent),
      show: document.getElementById('onboard').classList.contains('show')
    })`);
    let A = {}; try { A = JSON.parse(auto || '{}'); } catch (e) {}
    check('automation-no-autoshow', A.headless === true && A.show === false, auto);
    const manual = await evalJS(`(function(){ try{ EKOnboard.open('first'); }catch(e){ return 'EXC:'+e.message; } return document.getElementById('onboard').classList.contains('show'); })()`);
    check('automation-manual-open-works', manual === true, 'open=' + manual);
    await evalJS(`(function(){ try{ EKOnboard.close(); }catch(e){} return 'ok'; })()`);

    // 共用图纸语汇：四屏各有一层底框，且 .bp-* 笔触挂在共用类上（不是只认 02）。
    // CSSOM 那两项守的是 A1 的核心 —— 谁把 .ob-sheet 改回 .ob-rc-sheet，联锁图会先掉线宽。
    const sheet = await evalJS(`(function(){
      var ds=[].slice.call(document.querySelectorAll('.swiper-slide .ob-sheet-deco'));
      var ok=ds.length===4 && ds.every(function(d){
        var c=getComputedStyle(d);
        return d.getAttribute('aria-hidden')==='true' && /^DWG-0[3578]$/.test(d.getAttribute('data-dwg')||'')
          && c.pointerEvents==='none' && c.zIndex==='0';
      });
      var sh=0, o2=0;
      [].slice.call(document.styleSheets).forEach(function(ss){
        var rs; try{ rs=ss.cssRules; }catch(e){ return; }
        [].slice.call(rs).forEach(function(r){
          var t=r.cssText||'';
          if(t.indexOf('.ob-sheet .bp-n')>=0) sh++;
          if(t.indexOf('.ob-rc-sheet .bp-n')>=0) o2++;
        });
      });
      return JSON.stringify({deco:ds.length, ok:ok, shared:sh, only02:o2,
        svgShared:!!document.querySelector('svg.ob-rc-sheet.ob-sheet')});
    })()`);
    let SH = {}; try { SH = JSON.parse(sheet || '{}'); } catch (e) {}
    check('detail-sheet-deco-shared', SH.deco === 4 && SH.ok === true &&
      SH.shared >= 1 && SH.only02 === 0 && SH.svgShared === true, sheet);

    ws.close();
  } catch (e) {
    console.log('TESTERR:', e.stack || e.message);
    exitCode = 1;
  } finally {
    try { chrome.kill(); } catch (_) {}
    try { envkit.kill(); } catch (_) {}
    console.log(results.join('\n'));
    console.log('SUMMARY: ' + results.filter(x => x.startsWith('PASS')).length + '/' + results.length + ' PASS');
    process.exit(exitCode);
  }
})();
