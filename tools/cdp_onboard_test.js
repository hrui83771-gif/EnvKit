// CDP 实测：首次引导 / 产品导览（v3）
//
// ## 这个用例防什么
//  1. 三个 vendor 库（Swiper 12 / Anime.js v4 / driver.js）真的从同域加载成功，
//     且全局导出名对得上 —— 名字猜错会「页面能开但动画/翻页全空转」。
//  2. 首次自动播 5 屏 + 1.2s 片头出现后结束；「关于 > 重新观看介绍」重放 8 屏。
//  3. 8 屏交互演示真的动了（不是静态摆设），且**全程不触发任何写操作 /api/**。
//  4. 翻页、跳过、演示按钮都用真实鼠标（Input.dispatchMouseEvent）+ 命中测试，
//     不用 element.click() —— DOM click 会绕过命中测试，overlay 层级错了也全绿。
//  5. 自动化环境（navigator.webdriver=true）不自动播，否则 33 个既有 CDP
//     回归脚本会被全屏 overlay 挡住点击；但手动 open('first') 仍必须可用。
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
      barW: (document.getElementById('ob-progress-bar')||{}).style.width||''
    })`);
    let F = {}; try { F = JSON.parse(first || '{}'); } catch (e) {}
    check('first-autoshow', F.show === true, first);
    check('layout-crumb', /开场/.test(F.crumb || ''), 'crumb=' + F.crumb);
    check('layout-progress', /%$/.test(F.barW || '') && parseFloat(F.barW) > 0, 'bar=' + F.barW);
    check('first-5-slides', JSON.stringify(F.slides) === JSON.stringify(['0','1','2','5','7']), 'slides=' + JSON.stringify(F.slides));
    check('first-5-dots', F.dots === 5, 'dots=' + F.dots);
    check('brand-appeared', F.brandSeen === true, first);
    check('brand-ended', F.brandEnded === true && F.brandPlaying === false, first);
    check('live-page-1', /第 1 屏/.test(F.live || '') && /共 5 屏/.test(F.live || ''), 'live=' + F.live);

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

    // 屏 3（四步走）：点步骤看说明卡切换
    await arrowRight(); await arrowRight();
    const stepBefore = await evalJS(`(document.getElementById('ob-step-desc')||{}).innerText||''`);
    await clickSel('#ob-track .ob-step[data-step="2"]', 'step2');
    await sleep(250);
    const stepAfter = await evalJS(`(document.getElementById('ob-step-desc')||{}).innerText||''`);
    check('demo-step-desc', /环境安装/.test(stepBefore || '') && /链端校验/.test(stepAfter || '') && /WeBASE|FISCO/.test(stepAfter || ''),
      'before=' + (stepBefore || '').slice(0, 30) + ' after=' + (stepAfter || '').slice(0, 40));

    // 屏 4（检测报告）：逐行扫入 + 总评
    await arrowRight();
    await clickSel('#ob-detect-run', 'detect');
    await sleep(1700);
    const det = await evalJS(`JSON.stringify({
      lit: document.querySelectorAll('#ob-detect-rows .ob-row.lit').length,
      warn: document.querySelectorAll('#ob-detect-rows .ob-row.warn').length,
      badge: (document.getElementById('ob-detect-badge')||{}).textContent||''
    })`);
    let D = {}; try { D = JSON.parse(det || '{}'); } catch (e) {}
    check('demo-detect-scan', D.lit === 6 && D.warn === 1 && /5 项正常/.test(D.badge || ''), det);
    const termLines = await evalJS(`document.querySelectorAll('#ob-detect-cmd div').length`);
    check('demo-detect-console', termLines === 4, 'console lines=' + termLines);

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
    await sleep(1200);
    const typed = await evalJS(`(document.getElementById('ob-bubble')||{}).textContent||''`);
    check('demo-typing-complete', /确认卡在这里/.test(typed || '') && /权限闸门/.test(typed || ''),
      'bubble=' + (typed || '').slice(0, 50));
    await clickSel('#ob-demo-ok', 'demo-ok');
    await sleep(250);
    const q2 = await evalJS(`JSON.stringify({
      confirm: document.getElementById('ob-demo-confirm').hidden,
      bubble: (document.getElementById('ob-bubble')||{}).textContent||''
    })`);
    check('demo-confirm-closes', /"confirm":true/.test(q2 || '') && /权限闸门/.test(q2 || ''), q2);

    // 屏 7（安全边界）：点最高档看 428 说明
    await arrowRight();
    await clickSel('#ob-perms button[data-perm="3"]', 'perm3');
    await sleep(250);
    const perm = await evalJS(`(document.getElementById('ob-perm-desc')||{}).innerText||''`);
    check('demo-perm-428', /428/.test(perm || '') && /不会执行任意命令/.test(perm || ''), 'desc=' + (perm || '').slice(0, 60));

    // 屏 8（收尾）：8 个章节点
    await arrowRight();
    const chapters = await evalJS(`document.querySelectorAll('#ob-chapter-grid button').length`);
    check('demo-8-chapters', chapters === 8, 'chapters=' + chapters);

    const fetches = await evalJS(`JSON.stringify(window.__obFetch||[])`);
    let FL = []; try { FL = JSON.parse(fetches || '[]'); } catch (e) {}
    const writes = FL.filter(x => /\|(POST|PUT|DELETE|PATCH)/i.test(x));
    const aiCalls = FL.filter(x => /\/api\/ai\//.test(x));
    check('demo-zero-write-api', writes.length === 0, 'writes=' + JSON.stringify(writes) + ' all=' + FL.length);
    check('demo-zero-ai-api', aiCalls.length === 0, 'ai=' + JSON.stringify(aiCalls));

    // ===== F. 源码级红线：演示代码不得碰接口 / 任意执行 =====
    const src = fs.readFileSync(path.join(REPO, 'web', 'src', 'js', '90-onboard.js'), 'utf8');
    check('src-no-fetch', !/fetch\s*\(|XMLHttpRequest|new WebSocket|sendBeacon/.test(src));
    check('src-no-api-path', !/['"`]\/api\//.test(src));
    check('src-no-eval', !/\beval\s*\(|new Function|document\.write/.test(src));
    check('src-only-seen-write', (src.match(/localStorage\.setItem/g) || []).length === 1);

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
