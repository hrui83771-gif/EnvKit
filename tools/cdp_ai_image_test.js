// CDP 回归：AI 图片输入（拖入 / 粘贴 / 拖到桌宠）+ 真实模型端到端。
//
// 用法：
//   1) go build -trimpath -X main.buildDate=<日期> -o tools\_img\EnvKit_img.exe .
//   2) node tools/cdp_ai_image_test.js
//
// 三条设计约束（沿用 cdp_p2_mission_test.js 那套）：
//  1. **不碰用户数据**：数据目录是 tools\_img\（exe 旁边），端口 18902 独立，
//     单实例互斥体用 ENVKIT_INSTANCE_TAG 另起，用户正开着的那份不受影响。
//  2. **测的是真模型**：脚本把 config.json 复制进测试目录（含 AI 配置），
//     最后一步真的把截图发给模型，断言它答得出画面里是什么。
//     "请求发出去了"不算证明——只有回复内容能证明图片真的到了模型。
//  3. **先确认这份构建含新路由**：/api/ai/attach 不通就判错退出，
//     而不是让后面所有断言在旧包上"看起来在跑"。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const DIR = path.join(REPO, 'tools', '_img');
const EXE = path.join(DIR, 'EnvKit_img.exe');
const PORT = 18902;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9347;

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function getText(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => res(d)); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  const src = path.join(REPO, 'config.json');
  if (!fs.existsSync(src)) {
    console.log('FAIL 仓库根没有 config.json —— 这个脚本要用真实 AI 配置才能验到模型层');
    process.exit(1);
  }
  const cfg = JSON.parse(fs.readFileSync(src, 'utf8'));
  cfg.dev = cfg.dev || {};
  cfg.dev.server_port = PORT;      // 独立端口，不和用户的 8888 抢
  cfg.dev.port_scan_limit = PORT + 20;
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify(cfg, null, 2), 'utf8');
}

(async () => {
  // 安全闸：只在专属测试构建旁边跑。指向真实 EnvKit.exe 时会把配置写到用户目录。
  if (!fs.existsSync(EXE)) {
    console.log('FAIL 找不到测试构建：' + EXE);
    console.log('     go build -trimpath -X main.buildDate=2026-10-31 -o tools\\_img\\EnvKit_img.exe .');
    process.exit(1);
  }
  if (path.basename(EXE).toLowerCase() !== 'envkit_img.exe') {
    console.log('拒绝在真实数据目录跑：' + EXE);
    process.exit(1);
  }
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdpimg' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('exe:', EXE, 'ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(1); }

  const home = await getText(URL_);
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  if (!tm) {
    console.log('FAIL 首页拿不到 API 令牌');
    try { envkit.kill(); } catch (e) {}
    process.exit(1);
  }
  const TK = tm[1];

  // 先确认路由存在：空 POST 若返回 400（内容解码失败）说明路由在；
  // 404 说明这份构建不含图片功能 —— 后面所有断言都是在测旧包。
  const probe = await new Promise(resolve => {
    const req = http.request({ host: '127.0.0.1', port: PORT, path: '/api/ai/attach?t=' + TK, method: 'POST', headers: { 'Content-Type': 'application/json' } },
      r => { let d = ''; r.on('data', c => d += c); r.on('end', () => resolve({ code: r.statusCode, body: d.slice(0, 120) })); });
    req.on('error', e => resolve({ code: 0, body: String(e) }));
    req.end('{}');
  });
  if (probe.code === 404 || probe.code === 0) {
    console.log('FAIL 这份构建不含 /api/ai/attach：' + JSON.stringify(probe));
    try { envkit.kill(); } catch (e) {}
    process.exit(1);
  }
  console.log('attach probe:', probe.code, probe.body);

  // 落盘断言用"本次新增了哪些文件"，而不是"目录里有几个"：
  // 上一轮跑剩下的文件会让 1 变成 2，看起来像一次上传写了两份（查过，不是）。
  const attachDir = path.join(DIR, 'aiattach');
  const filesBefore = new Set(fs.existsSync(attachDir) ? fs.readdirSync(attachDir) : []);

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(DIR, '_chrome'),
    '--window-size=1920,1080', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let fails = 0;
  const ok = (name, cond, extra) => {
    console.log((cond ? 'PASS ' : 'FAIL ') + name + (extra === undefined ? '' : ' -> ' + JSON.stringify(extra).slice(0, 400)));
    if (!cond) fails++;
  };

  try {
    let targets = null;
    for (let i = 0; i < 12; i++) {
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
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return 'EXC:' + JSON.stringify(r.result.exceptionDetails).slice(0, 300);
      return r.result && r.result.result ? r.result.result.value : JSON.stringify(r.result);
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Emulation.setLocaleOverride', { locale: 'zh-CN' });
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    // ---- 1. 拖拽：把"当前页面截图"当成被拖入的文件 ----
    // 用真截图而不是造一张纯色图：后面要断言模型答得出画面内容，
    // 纯色图只能证明"字节到了"，证明不了"模型看见了"。
    //
    // 截图前先往页面上钉一个**只有这一屏才有的编号**（正文级 13px，与界面同色系）。
    // 这是"压缩后文字还能不能读"的硬判据：答得出编号 = 小字真的没被糊掉；
    // 只答得出"这是 EnvKit 界面"什么都证明不了 —— 那种句子靠猜也能编出来。
    // 视口故意开到 1920 宽：长边 1600 的压缩路径只有**需要缩放**时才会走到，
    // 1280 宽的窗口根本不缩，测不到真正会糊字的那一步。
    const PROBE = 'QN-4827-KT';
    await evalJS(`(function(){
      const d = document.createElement('div');
      d.id = '__ek_probe';
      d.style.cssText = 'position:fixed;left:60px;bottom:90px;z-index:99999;background:#fff;color:#1f2933;' +
        'font:13px/1.6 system-ui,"Microsoft YaHei",sans-serif;padding:8px 12px;border:1px solid #c9d3e0;border-radius:6px';
      d.textContent = '工单编号 ${PROBE} 校验码 7XF2';
      document.body.appendChild(d);
      return 'ok';
    })()`);
    await sleep(300);
    const shot = await send('Page.captureScreenshot', { format: 'png' });
    const b64 = shot.result && shot.result.data;
    ok('截到一张页面图', !!b64 && b64.length > 1000, b64 ? b64.length : 0);

    const dropRet = await evalJS(`(function(){
      aiOpen();
      const bin = atob(${JSON.stringify(b64 || '')});
      const u8 = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
      const f = new File([u8], 'screen.png', {type:'image/png'});
      const dt = new DataTransfer();
      dt.items.add(f);
      const dr = document.getElementById('ai-drawer');
      dr.dispatchEvent(new DragEvent('drop', {dataTransfer: dt, bubbles: true, cancelable: true}));
      return JSON.stringify({n: aiImgs.length, hintShown: dr.classList.contains('ai-dropping')});
    })()`);
    const dr = JSON.parse(dropRet);
    ok('拖入后进入待发送队列', dr.n === 1, dr);
    ok('drop 后撤掉拖拽高亮', dr.hintShown === false, dr);

    // 等上传完成（busy 去掉、拿到 id）
    let up = null;
    for (let i = 0; i < 40; i++) {
      up = JSON.parse(await evalJS(`JSON.stringify(aiImgs.map(x=>({busy:x.busy, id:!!x.id, err:x.err||'', url:(x.url||'').slice(0,22), len:(x.url||'').length})))`));
      if (up.length && up.every(x => !x.busy)) break;
      await sleep(300);
    }
    ok('图片上传完成且拿到服务端 id', Array.isArray(up) && up.length === 1 && up[0].id === true, up);
    ok('缩略图是压缩后的 data URL（不是 blob:，CSP 拦得住）', Array.isArray(up) && /^data:image\//.test(up[0].url || ''), up);
    // 1920×1080 的 PNG 截图必须被转成 JPEG 才叫"真的压缩过"。
    // 原样返回时它仍是 data:image/png——这条断言就是那个静默缺陷的探针。
    ok('截图被压成 JPEG（证明压缩真的执行了，不是原样返回）',
      Array.isArray(up) && /^data:image\/jpeg/.test(up[0].url || ''), up && up[0].url);
    ok('压缩后体积可控（dataURL < 2.5MB）', Array.isArray(up) && up[0].len > 1000 && up[0].len < 2500000, up && up[0].len);

    // 缩略图 DOM 真的出现了（这是"用户看得见"的判据）
    const thumbDom = await evalJS(`(function(){
      const box = document.getElementById('ai-attach');
      const im = box.querySelectorAll('img');
      return JSON.stringify({hidden: box.hidden, n: im.length,
        w: im.length ? im[0].getBoundingClientRect().width : 0,
        rm: box.querySelectorAll('.thumb .rm').length});
    })()`);
    const td = JSON.parse(thumbDom);
    ok('输入区上方出现缩略图条', td.hidden === false && td.n === 1, td);
    ok('缩略图真的占了位置（宽 > 20px）', td.w > 20, td);
    ok('缩略图带移除按钮', td.rm === 1, td);

    // 后端目录里也要真的多一个文件（落盘判据，不是"内存里有"）
    const filesNow = fs.existsSync(attachDir) ? fs.readdirSync(attachDir).filter(x => /^[0-9a-f]{32}$/.test(x)) : [];
    const added = filesNow.filter(x => !filesBefore.has(x));
    ok('附件已落盘到 aiattach/（本次新增 1 个）', added.length === 1, { added: added.length, total: filesNow.length });

    // ---- 1b. 压缩参数体检：同一张截图，量各档的体积 ----
    // 三个数字都是给"为什么这么设"留证据的，不是凑断言：
    //   q92 vs q86 → 截图类多花多少体积（值不值）
    //   high vs low 插值 → 画质提升是不是白赚（体积差不该超过 20%）
    const shrinkInfo = JSON.parse(await evalJS(`(async function(){
      try{
      // 用 data URL 加载，不用 URL.createObjectURL：CSP 的 img-src 是 'self' data:，
      // blob: 会被拦（这正是前端 aiImgShrink 曾经"看起来在压缩、实际原样返回"的原因）。
      const D = ${JSON.stringify(b64 || '')};
      const img = await new Promise((res, rej) => { const im = new Image(); im.onload = () => res(im); im.onerror = () => rej(new Error('图片加载失败（CSP？）')); im.src = 'data:image/png;base64,' + D; });
      const W = img.naturalWidth, H = img.naturalHeight;
      const EDGE = 1600;   // 与前端 AI_IMG_EDGE 同值；这里显式写死，脚本不依赖页面内的 const
      const scale = Math.min(1, EDGE / Math.max(W, H));
      const cw = Math.round(W * scale), ch = Math.round(H * scale);
      function encode(quality, sm) {
        const cv = document.createElement('canvas'); cv.width = cw; cv.height = ch;
        const cx = cv.getContext('2d');
        cx.imageSmoothingEnabled = true; cx.imageSmoothingQuality = sm;
        cx.fillStyle = '#fff'; cx.fillRect(0, 0, cw, ch);
        cx.drawImage(img, 0, 0, cw, ch);
        return new Promise(res => cv.toBlob(b => res(b ? b.size : -1), 'image/jpeg', quality));
      }
      const out = {W, H, cw, ch, srcBytes: D.length,
        q86: await encode(0.86, 'high'), q88: await encode(0.88, 'high'),
        q92: await encode(0.92, 'high'), low92: await encode(0.92, 'low')};
      return JSON.stringify(out);
      }catch(e){ return JSON.stringify({err: String((e && e.message) || e)}); }
    })()`));
    console.log('shrink:', JSON.stringify(shrinkInfo));
    ok('压缩体检脚本可执行', !shrinkInfo.err, shrinkInfo.err || 'ok');
    ok('1920 宽截图确实走了缩放（否则测不到糊字那一步）',
      shrinkInfo.cw < shrinkInfo.W && shrinkInfo.cw === 1600, shrinkInfo);
    ok('q0.92 的体积仍在 1.5MB 以内（截图类多给质量不至于失控）',
      shrinkInfo.q92 > 1000 && shrinkInfo.q92 < 1.5 * 1024 * 1024, shrinkInfo.q92);
    ok('高质量插值不额外花体积（与 low 相差 < 20%）',
      Math.abs(shrinkInfo.q92 - shrinkInfo.low92) / shrinkInfo.q92 < 0.2,
      { high: shrinkInfo.q92, low: shrinkInfo.low92 });

    // ---- 1c. 大图：4032×3024 手机照片尺寸，量耗时与出图体积 ----
    const bigInfo = JSON.parse(await evalJS(`(async function(){
      try{
      const cv = document.createElement('canvas'); cv.width = 4032; cv.height = 3024;
      const cx = cv.getContext('2d');
      const g = cx.createLinearGradient(0, 0, 4032, 3024);
      g.addColorStop(0, '#8ab4f8'); g.addColorStop(1, '#f6c26b');
      cx.fillStyle = g; cx.fillRect(0, 0, 4032, 3024);
      for (let i = 0; i < 3000; i++) {          // 加点噪点，别让它被 JPEG 压成一块平板
        cx.fillStyle = 'rgba(' + (i % 255) + ',' + ((i * 7) % 255) + ',' + ((i * 13) % 255) + ',0.3)';
        cx.fillRect(Math.random() * 4032, Math.random() * 3024, 40, 40);
      }
      cx.fillStyle = '#111'; cx.font = '48px sans-serif'; cx.fillText('手机照片模拟 4032x3024', 200, 300);
      const blob = await new Promise(r => cv.toBlob(r, 'image/jpeg', 0.9));
      const t0 = performance.now();
      const out2 = await aiImgShrink(blob);   // 返回 data: URL
      const ms = Math.round(performance.now() - t0);
      const im2 = await new Promise((res, rej) => { const i = new Image(); i.onload = () => res(i); i.onerror = rej; i.src = out2; });
      const bytes = Math.round((out2.length - out2.indexOf(',') - 1) * 3 / 4);
      return JSON.stringify({inBytes: blob.size, outBytes: bytes, ms,
        W: im2.naturalWidth, H: im2.naturalHeight});
      }catch(e){ return JSON.stringify({err: String((e && e.message) || e)}); }
    })()`));
    console.log('big:', JSON.stringify(bigInfo));
    ok('大图体检脚本可执行', !bigInfo.err, bigInfo.err || 'ok');
    // 这一条是"压缩真的发生了"的硬判据：之前 CSP 拦掉 blob: 时，
    // 函数会静默 resolve(原文件)，体积和尺寸都跟原图一模一样，只有量尺寸能发现。
    ok('12MP 大图真的被缩到长边 1600', bigInfo.W === 1600, bigInfo);
    ok('12MP 大图压缩到 1.5MB 以内', bigInfo.outBytes > 1000 && bigInfo.outBytes < 1.5 * 1024 * 1024, bigInfo);
    ok('12MP 大图压缩耗时 < 3s（不要让人以为卡死了）', bigInfo.ms < 3000, bigInfo.ms + 'ms');

    // ---- 2. 粘贴：ClipboardEvent 的 clipboardData 只读，造不出来，
    //         所以直接调用被 paste 监听器调用的那个函数。----
    const pasteRet = await evalJS(`(function(){
      aiImgs = []; aiAttachRender();
      const k = new Uint8Array([137,80,78,71,13,10,26,10,0,0,0,13,73,72,68,82]);
      const img = new File([k], 'pasted.png', {type:'image/png'});
      const imgItem = {kind:'file', type:'image/png', getAsFile: ()=>img};
      let prevented = 0;
      const took = aiPasteHandle({clipboardData:{items:[imgItem]}, preventDefault: ()=>{ prevented++; }});
      const textOnly = aiPasteHandle({clipboardData:{items:[{kind:'string', type:'text/plain', getAsFile: ()=>null}]}, preventDefault: ()=>{ prevented += 10; }});
      return JSON.stringify({took, textOnly, prevented, n: aiImgs.length});
    })()`);
    const pr = JSON.parse(pasteRet);
    ok('粘贴图片：被识别并进队列', pr.took === true && pr.n === 1 && pr.prevented === 1, pr);
    ok('粘贴纯文本：不拦截（否则粘日志进输入框会失效）', pr.textOnly === false && pr.prevented === 1, pr);

    // ---- 2b. 内容判据在服务端：声明成 image/png 的**文本**必须被拒，
    //          而且失败项不能留在队列里假装能发。----
    const fakeRet = await evalJS(`(async function(){
      const p = aiImgs.map(x=>x.p).filter(Boolean); if(p.length) await Promise.all(p);
      aiImgs = []; aiAttachRender();
      const f = new File(['<?php echo 1; ?>'], 'evil.png', {type:'image/png'});
      await aiAttachAdd([f]);
      const out = {n: aiImgs.length, err: (aiImgs[0]||{}).err || '', hasId: !!(aiImgs[0]||{}).id};
      aiImgs = aiImgs.filter(x=>!x.err); aiAttachRender();
      return JSON.stringify(out);
    })()`);
    const fr = JSON.parse(fakeRet);
    ok('内容不是图片的文件被服务端拒绝（不信前端声明的 MIME）', fr.n === 1 && fr.hasId === false && !!fr.err, fr);

    // ---- 3. 拖到桌宠：应当打开抽屉并载入 ----
    const dropPet = await evalJS(`(function(){
      aiImgs = []; aiAttachRender();   // 先清空，否则 n 会把上一张算进来
      const b = document.createElement('canvas'); b.width = 40; b.height = 40;
      const cx = b.getContext('2d'); cx.fillStyle = '#4a6fa5'; cx.fillRect(0,0,40,40);
      return new Promise(res=>{
        b.toBlob(blob=>{
          const f = new File([blob], 'pet.png', {type:'image/png'});
          const dt = new DataTransfer(); dt.items.add(f);
          document.getElementById('pet').dispatchEvent(new DragEvent('drop', {dataTransfer: dt, bubbles: true, cancelable: true}));
          setTimeout(()=>res(JSON.stringify({n: aiImgs.length, open: document.getElementById('ai-drawer').classList.contains('open')})), 1200);
        }, 'image/png');
      });
    })()`);
    const dp = dropPet && dropPet.startsWith('EXC:') ? {err: dropPet} : JSON.parse(dropPet);
    ok('拖到桌宠会载入图片', dp.n === 1, dp);
    ok('拖到桌宠会顺手打开对话抽屉', dp.open === true, dp);

    // 等这次上传完成，再发消息（发送按钮会等 busy）
    for (let i = 0; i < 30; i++) {
      const s = JSON.parse(await evalJS(`JSON.stringify(aiImgs.map(x=>!!x.busy))`));
      if (s.length && s.every(x => x === false)) break;
      await sleep(300);
    }

    // ---- 4. 真实模型端到端：把截图发出去，看它答不答得出画面 ----
    await evalJS(`(function(){
      aiMessages = []; document.getElementById('ai-msgs').innerHTML = '';
      aiImgs = [];
      // 记录真正发出去的请求体：这是"前端有没有把图带上"的唯一硬证据。
      // 只看 DOM 是不够的——气泡里回显的图可能只是本地预览。
      window.__ekSent = [];
      const of = window.fetch;
      window.fetch = function(u, init){
        try{
          if(String(u).indexOf('/api/ai/chat') >= 0 && init && init.body){
            const b = JSON.parse(init.body);
            const ums = (b.messages || []).filter(m => m.role === 'user');
            const last = ums.length ? ums[ums.length - 1] : {};
            window.__ekSent.push({userImgs: (last.images || []).length,
              anyImgs: (b.messages || []).reduce((n, m) => n + ((m.images || []).length), 0)});
          }
        }catch(e){}
        return of.apply(this, arguments);
      };
      return 'hooked';
    })()`);
    // 重新拖入那张页面截图（前面清空过）
    await evalJS(`(function(){
      const bin = atob(${JSON.stringify(b64 || '')});
      const u8 = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
      const dt = new DataTransfer();
      dt.items.add(new File([u8], 'screen.png', {type:'image/png'}));
      document.getElementById('ai-drawer').dispatchEvent(new DragEvent('drop', {dataTransfer: dt, bubbles: true, cancelable: true}));
    })()`);
    for (let i = 0; i < 40; i++) {
      const s = JSON.parse(await evalJS(`JSON.stringify(aiImgs.map(x=>({b:!!x.busy, id:!!x.id})))`));
      if (s.length === 1 && !s[0].b && s[0].id) break;
      await sleep(300);
    }
    await evalJS(`(function(){ document.getElementById('ai-in').value = '这张截图左下角有一个工单编号，把它原样抄给我，并说一句这是什么界面。不要列步骤。'; return 'ok'; })()`);
    await evalJS(`(function(){ document.getElementById('ai-send').click(); return 'ok'; })()`);
    // 等一轮流跑完：发送按钮从 disabled 恢复，且已有 assistant 消息
    let done = false, reply = '';
    for (let i = 0; i < 120; i++) {
      const s = await evalJS(`(function(){
        const as = [].slice.call(document.querySelectorAll('#ai-msgs .ai-msg.ai'));
        return JSON.stringify({busy: document.getElementById('ai-send').disabled, n: as.length,
          last: as.length ? as[as.length-1].innerText.slice(0, 400) : '',
          userImgs: document.querySelectorAll('#ai-msgs .ai-msg.user .ai-msg-imgs img').length});
      })()`);
      let o = null; try { o = JSON.parse(s); } catch (e) { continue; }
      if (!o.busy && o.n > 0 && o.last.trim().length > 4) { done = true; reply = o.last; }
      if (done) break;
      await sleep(1000);
    }
    ok('用户气泡里回显了已发送的图片', (await evalJS(`document.querySelectorAll('#ai-msgs .ai-msg.user .ai-msg-imgs img').length`)) === 1);
    const sent = JSON.parse(await evalJS(`JSON.stringify(window.__ekSent || [])`));
    ok('请求体确实带上了图片引用（不是只在本机预览）',
      Array.isArray(sent) && sent.length > 0 && sent[0].userImgs === 1, sent);
    ok('模型对截图给出了回复', done === true, reply.slice(0, 120));
    // 真凭据：回复里要提到画面里的东西。EnvKit 控制台的标题/导航里有这些词。
    ok('回复确实在看图（提到界面/控制台/EnvKit 等画面元素）',
      /EnvKit|环境|控制台|界面|助手|仪表|侧栏|菜单/i.test(reply), reply.slice(0, 200));
    // 硬判据：13px 正文级小字在缩放 + JPEG 之后还读得出来吗。
    // "这是什么界面"那种问题靠上下文就能蒙对，只有这串编号必须真的看见。
    const flat = reply.replace(/[^A-Za-z0-9]/g, '').toUpperCase();
    ok('截图里的 13px 小字压缩后仍可读（复述出编号 ' + PROBE + '）',
      flat.indexOf(PROBE.replace(/[^A-Za-z0-9]/g, '')) >= 0, reply.slice(0, 300));

    ok('无未捕获 JS 异常', errs.length === 0, errs.slice(0, 3));
    console.log('REPLY: ' + reply.slice(0, 300));
  } catch (e) {
    console.log('FAIL harness:', e && e.message);
    fails++;
  } finally {
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log(fails === 0 ? 'ALL PASS' : ('FAILED ' + fails));
  process.exit(fails === 0 ? 0 : 1);
})();
