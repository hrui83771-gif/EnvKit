// CDP 回归（v3.6.2）：桌宠天气与投喂面板的三处用户可见缺陷
//
// 背景（2026-10-30 用户反馈）：
//   1) 「天气必须点应用才生效」—— 勾开关不发请求，只有旁边的「应用」按钮才写盘；
//   2) 「点应用生效的话好像还只存在一个进程」—— 页面自己的内存变量没有跨窗口同步，
//      服务端生效了，另一个窗口的菜单还是旧的；
//   3) 「天气细节都看不到」—— 气泡只有一句"合肥今天 19°，Smoky haze"，
//      体感/湿度/风/能见度全被后端丢掉，霾天还直接显示英文；
//   4) 「喂食会遮挡她说话」—— 食物栏横排悬在头顶，正好盖住"咔嚓咔嚓谢谢投喂"。
//
// 断言全部是几何量或服务端返回值，不看措辞：
//   · 食物栏与桌宠**横向不重叠**，且与气泡矩形不相交（遮挡是几何问题）
//   · 勾上开关后 GET /api/pet/config 必须已经是 true（不需要点应用）
//   · 天气气泡三行齐全、实况行不含 ASCII 字母（英文漏翻会露馅）
//   · 气泡不越出视口
//
// 跑法：node tools/cdp_pet_weather_food_test.js
// 前置：把 exe 与 config.json 复制到 _wxtest 目录（本脚本自己准备），
//       用 ENVKIT_INSTANCE_TAG 避免撞用户正在跑的实例。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const SRC_EXE = 'D:\\BCGD\\FarmTrace\\envkit\\EnvKit.exe';
const SRC_CFG = 'D:\\BCGD\\FarmTrace\\envkit\\config.json';
const WORK = 'D:\\BCGD\\_wxtest';
const DBG = 9337;

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

(async () => {
  // 隔离运行目录：测试会改 pet 配置，绝不能写用户真实 config.json
  fs.rmSync(WORK, { recursive: true, force: true });
  fs.mkdirSync(WORK, { recursive: true });
  fs.copyFileSync(SRC_EXE, path.join(WORK, 'EnvKit.exe'));
  const cfg = JSON.parse(fs.readFileSync(SRC_CFG, 'utf8'));
  cfg.pet = { weather_enabled: false, weather_city: '合肥' };   // 从"关闭"开始，才能验证勾上就生效
  fs.writeFileSync(path.join(WORK, 'config.json'), JSON.stringify(cfg, null, 2), 'utf8');

  const envkit = spawn(path.join(WORK, 'EnvKit.exe'), [], {
    cwd: WORK, stdio: 'ignore', env: { ...process.env, ENVKIT_INSTANCE_TAG: 'wxtest' },
  });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON('http://127.0.0.1:18765/'); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(WORK, '_cdp'),
    '--window-size=1280,900', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let fail = 0;
  const check = (name, ok, extra) => {
    if (!ok) fail++;
    console.log((ok ? 'PASS ' : 'FAIL ') + name + (extra === undefined ? '' : '  ' + JSON.stringify(extra)));
  };

  try {
    let targets = null;
    for (let i = 0; i < 15; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    const ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    const errs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.method === 'Runtime.exceptionThrown') errs.push(m.params.exceptionDetails.text);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return { __err: r.result.exceptionDetails.text };
      return r.result && r.result.result ? r.result.result.value : undefined;
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: 'http://127.0.0.1:18765/' });
    await sleep(4500);

    /* ---------- 1) 勾上开关就生效（不需要点「应用」） ---------- */
    // 读配置要在**页面里**读：/api/* 需要 window.__EK_TOKEN__，
    // Node 侧裸 fetch 只会拿到 403「未授权的访问」。
    const before = await evalJS(`fetch('/api/pet/config').then(r=>r.json())`);
    console.log('BEFORE:', JSON.stringify(before));

    await evalJS(`(function(){
      const el = document.getElementById('s_weather_on');
      el.checked = true;
      el.dispatchEvent(new Event('change', { bubbles: true }));
      return 'dispatched';
    })()`);
    await sleep(1200);
    const after = await evalJS(`fetch('/api/pet/config').then(r=>r.json())`);
    console.log('AFTER:', JSON.stringify(after));
    check('勾选开关后服务端立即生效（不需要点应用）', after.weather_enabled === true, { before: before.weather_enabled, after: after.weather_enabled });

    const hint = await evalJS(`(document.getElementById('h-weather')||{}).textContent || ''`);
    check('卡片里显示生效状态', /已开启/.test(hint || ''), hint);

    const disk = JSON.parse(fs.readFileSync(path.join(WORK, 'config.json'), 'utf8'));
    check('配置真的落盘了（不是只活在内存）', disk.pet && disk.pet.weather_enabled === true, disk.pet);

    const busMark = await evalJS(`localStorage.getItem('ek-pet-weather')`);
    check('跨窗口同步标记已写出', busMark === '1', busMark);

    /* ---------- 2) 右键「查看天气」→ 三行气泡 ---------- */
    await evalJS(`(function(){
      const pet = document.getElementById('pet');
      const r = pet.getBoundingClientRect();
      pet.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true,
        clientX: r.left + 40, clientY: r.top + 40 }));
      const b = document.getElementById('pet-act-weather');
      b.click();
      return b.hidden ? 'menu-hidden' : 'clicked';
    })()`);
    // 首次查询要打 wttr.in，等它回来（最多 20 秒）
    let wx = null;
    for (let i = 0; i < 40; i++) {
      wx = await evalJS(`(function(){
        const b = document.getElementById('pet-bubble');
        if(!b || !b.classList.contains('rich')) return null;
        const q = s => { const e = b.querySelector(s); return e ? e.textContent : ''; };
        const r = b.getBoundingClientRect();
        return { head: q('.wx1'), detail: q('.wx2'), advice: q('.wx3'),
                 rect: {top: r.top, left: r.left, right: r.right, bottom: r.bottom},
                 vw: innerWidth, vh: innerHeight };
      })()`);
      if (wx) break;
      await sleep(500);
    }
    console.log('BUBBLE:', JSON.stringify(wx));
    if(!wx){
      // 没出来必须能看出卡在哪：是查询失败、还是气泡没被标成 rich
      const diag = await evalJS(`(async function(){
        let j = null;
        try{ j = await fetch('/api/pet/weather').then(r=>r.json()); }catch(e){ j = {fetch_error: String(e)}; }
        return { api: j, bubble: (document.getElementById('pet-bubble')||{}).innerHTML || '' };
      })()`);
      console.log('DIAG:', JSON.stringify(diag).slice(0, 600));
    }
    check('天气气泡出现', !!wx);
    if (wx) {
      check('实况行不含英文（漏翻会露馅）', wx.head && !/[A-Za-z]/.test(wx.head), wx.head);
      check('细节行有内容（体感/湿度/风/能见度）', /体感|湿度|km\/h|能见度/.test(wx.detail || ''), wx.detail);
      check('建议行有内容（复用问候那套说法）', !!wx.advice, wx.advice);
      // 等 scale 过渡走完再量：过渡中途量到的矩形是缩小版，越界会被漏掉
      await sleep(450);
      const geom = await evalJS(`(function(){
        const b = document.getElementById('pet-bubble');
        const r = b.getBoundingClientRect();
        return { top: r.top, left: r.left, right: r.right, bottom: r.bottom,
                 vw: innerWidth, vh: innerHeight };
      })()`);
      console.log('GEOM:', JSON.stringify(geom));
      check('气泡不越出视口', geom.top >= 0 && geom.left >= 0 &&
        geom.right <= geom.vw && geom.bottom <= geom.vh, geom);
    }

    /* ---------- 3) 食物栏贴侧面，不挡气泡 ---------- */
    const food = await evalJS(`(function(){
      const pet = document.getElementById('pet');
      const b = document.getElementById('pet-bubble');
      // 故意在**三行天气气泡正在显示时**双击：这是最挤的情况
      // （气泡 250px 宽、贴右边缘，正是原来会被食物栏压住的那个场景）
      pet.classList.add('show-bubble');
      pet.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
      const f = document.getElementById('pet-food');
      const pr = pet.getBoundingClientRect(), fr = f.getBoundingClientRect(), br = b.getBoundingClientRect();
      const shown = f.classList.contains('show');
      const overlapX = Math.min(pr.right, fr.right) - Math.max(pr.left, fr.left);
      const overlapBubble = Math.min(fr.right, br.right) > Math.max(fr.left, br.left) &&
                            Math.min(fr.bottom, br.bottom) > Math.max(fr.top, br.top);
      return { shown, vertical: fr.height > fr.width, overlapX, overlapBubble,
               pet: {l: pr.left, r: pr.right, t: pr.top, b: pr.bottom},
               food: {l: fr.left, r: fr.right, t: fr.top, b: fr.bottom},
               bubble: {l: br.left, r: br.right, t: br.top, b: br.bottom} };
    })()`);
    console.log('FOOD:', JSON.stringify(food));
    check('食物栏出现了', !!(food && food.shown));
    if (food) {
      check('食物栏竖排', food.vertical);
      check('食物栏在桌宠侧面（横向不重叠）', food.overlapX <= 0, food.overlapX);
      check('食物栏不遮气泡', food.overlapBubble === false);
      check('食物栏在视口内', food.food.l >= 0 && food.food.t >= 0);
    }

    check('无 JS 运行时异常', errs.length === 0, errs.slice(0, 3));
  } catch (e) {
    fail++;
    console.log('FAIL 异常：' + (e && e.message));
  } finally {
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
    await sleep(600);
    try { fs.rmSync(WORK, { recursive: true, force: true }); } catch (e) {}
  }
  console.log(fail === 0 ? 'ALL PASS' : ('FAILED ' + fail));
  process.exit(fail === 0 ? 0 : 1);
})();
