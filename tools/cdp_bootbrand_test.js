// CDP 回归：每次打开 exe 都放 1.5s 启动片头（#ob-brand），与引导是否首次无关。
//
// 守的事实：
//   1) 非首次（ek-guide-v1=1）+ 真实 UA：刷新后片头出现且自动结束，不进 8 屏导览；
//   2) 片头时长配置 1500ms（源码 BRAND_MS），且结束时引导层恢复隐藏、不锁滚动；
//   3) 自动化环境（HeadlessChrome/webdriver）：不播启动片头，避免挡住真鼠标回归。
//
// 变异：--mut=duration 把 BRAND_MS 改回 1400 → 时长断言红；
//       --mut=firstonly 让启动片头只在首次播 → 非首次片头断言红。
// 用法：node tools/cdp_bootbrand_test.js [--mut=duration|--mut=firstonly]
// 隔离：独立端口 18845 + DBG 9385 + ENVKIT_INSTANCE_TAG + 独立 exe 副本。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18845;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9385;
const EXE = path.join(REPO, 'tools', '_bootbrand', 'EnvKit_bootbrand.exe');
const DIR = path.dirname(EXE);
const SRC_JS = path.join(REPO, 'web', 'src', 'js', '90-onboard.js');
const MUT = process.argv.find(a => a.startsWith('--mut=')) || '--mut=none';

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 变异在复制 exe 之前改源码并重建调用方负责；这里只保证每轮强制覆盖 exe。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
}
async function attach(target) {
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let mid = 0; const pending = new Map();
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
  // 源码级事实：片头时长常量。变异 duration 在这里就能精确红。
  const jsSrc = fs.readFileSync(SRC_JS, 'utf8');
  const brandMs = (jsSrc.match(/var\s+BRAND_MS\s*=\s*(\d+)/) || [])[1];
  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  FAIL ' + msg); } };

  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'bootbrand' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  if (!ready) { envkit.kill(); console.log('envkit not ready'); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_bootbrand'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets || !Array.isArray(targets)) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    const c = await attach(page);
    const { send, evalJS } = c;
    await send('Runtime.enable', {});
    await send('Page.enable', {});

    // 片头探针：记录 #ob-brand 出现/结束，以及当时是否在导览（.ob 有 slide）。
    const REAL_UA = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36';
    await send('Page.addScriptToEvaluateOnNewDocument', { source: `
      window.__bbSeen=false; window.__bbEnded=false; window.__bbAtEndNav=false;
      var __iv=setInterval(function(){
        var b=document.getElementById('ob-brand'); if(!b) return;
        if(b.classList.contains('play')) window.__bbSeen=true;
        else if(window.__bbSeen && !window.__bbEnded){
          window.__bbEnded=true;
          var ob=document.getElementById('onboard');
          window.__bbAtEndNav=!!(ob && ob.classList.contains('show') && document.querySelectorAll('#ob-swiper .swiper-slide').length>0 && !document.body.classList.contains('boot-brand'));
        }
      },30);
      setTimeout(function(){clearInterval(__iv);},5000);
    ` });

    // ---- 非首次 + 真实 UA：刷新后片头应出现并结束，且不进导览 ----
    await send('Emulation.setUserAgentOverride', { userAgent: REAL_UA, acceptLanguage: 'zh-CN', platform: 'Win32' });
    await send('Page.navigate', { url: URL_ });
    await sleep(600);
    await evalJS(`(function(){try{localStorage.setItem('ek-guide-v1','1');}catch(e){}return 'ok';})()`);
    await send('Page.reload', {});
    await sleep(2600); // 1.5s 片头 + 余量
    const boot = JSON.parse(await evalJS(`JSON.stringify({
      seen:window.__bbSeen, ended:window.__bbEnded, nav:window.__bbAtEndNav,
      playing:document.getElementById('ob-brand').classList.contains('play'),
      obShow:document.getElementById('onboard').classList.contains('show'),
      bootCls:document.body.classList.contains('boot-brand'),
      locked:document.body.classList.contains('ob-active'),
      diag:{ wd:navigator.webdriver, ua:navigator.userAgent.slice(0,60),
        reduced:!!(window.matchMedia&&window.matchMedia('(prefers-reduced-motion: reduce)').matches),
        seenKey:(function(){try{return localStorage.getItem('ek-guide-v1');}catch(e){return 'ERR';}})(),
        hasFn:typeof window.EKOnboard }
    })`));
    console.log('  DIAG ' + JSON.stringify(boot.diag));
    ok(boot.seen === true && boot.ended === true && boot.playing === false,
      '非首次打开 exe 也播片头并自动结束（seen=' + boot.seen + ' ended=' + boot.ended + '）');
    ok(boot.nav === false && boot.obShow === false && boot.bootCls === false && boot.locked === false,
      '独立片头不进入 8 屏导览、结束恢复隐藏且不锁滚动');
    ok(brandMs === '1500', '片头时长常量为 1500ms（实测 ' + brandMs + '）');

    // ---- 自动化 UA：重载后不播片头（不挡 CDP 真鼠标） ----
    await send('Emulation.setUserAgentOverride', { userAgent: '', acceptLanguage: 'zh-CN', platform: '' });
    await evalJS(`(function(){window.__bbSeen=false;window.__bbEnded=false;return 'ok';})()`);
    await send('Page.reload', {});
    await sleep(2200);
    const auto = JSON.parse(await evalJS(`JSON.stringify({seen:window.__bbSeen, playing:document.getElementById('ob-brand').classList.contains('play')})`));
    ok(auto.seen === false && auto.playing === false, '自动化环境不播启动片头');

    c.ws.close();
  } catch (e) {
    console.log('TESTERR:', e.stack || e.message);
    fails.push('EXCEPTION');
  } finally {
    try { chrome.kill(); } catch (_) {}
    try { envkit.kill(); } catch (_) {}
    console.log('RESULT: ' + (fails.length === 0 ? 'PASS' : 'FAIL') + ' (' + fails.length + ' failed)');
    process.exit(fails.length ? 1 : 0);
  }
})();
