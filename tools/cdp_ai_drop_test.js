// CDP 回归（v3.7.5）：把文件拖进 AI 对话区，出来的是**一张文件卡片**，不是一段正文
//
// ## 这个用例防什么
//
// 两道历史坑合并在这里守：
//
//  1. （v3.7.4）以前 aiDropFiles() 对非图片一律 `f.text()`：按 UTF-8 硬解，于是
//     拖 docx 进去 = zip 压缩流被当文本、拖 GBK 日志进去 = 中文全乱码、
//     拖 exe 进去 = 二进制垃圾。现在改成 POST /api/ai/extract，后端按**魔数**判类型。
//  2. （v3.7.5）抽取回来的正文以前被写进 textarea，于是输入框被几千字撑满、
//     气泡里回看一屏全是文件正文、还随消息进了 localStorage。
//     现在后端落盘成 aidoc/<id>，前端只拿一个引用，显示成卡片。
//
// 所以这里的断言是**反向的**：正文必须**不出现**在 DOM 里，
// 且 localStorage 里不许有正文（存了就写爆 5MB 配额，表现是"历史莫名丢了"）。
// 后端解析（docx/pdf/编码）由 ai_extract_test.go 覆盖，落盘/注入由 ai_doc_test.go 覆盖。
//
// 为什么在页面里现造 File 而不是仓库里放样本文件：docx/pdf 是二进制，提交进仓库既不可读
// 也容易被误改；这里要验的是"字节进去 → 卡片出来"的链路，GBK 文本 + OLE2 魔数就够触发全部分支。
//
// 用法：node tools/cdp_ai_drop_test.js
// 隔离：独立端口 18799 + ENVKIT_INSTANCE_TAG + 独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9354;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_aior', 'EnvKit_aior.exe');
const DIR = path.dirname(EXE);
const PROF = path.join(REPO, 'tools', '_cdptmp_aiod');

// GBK 编码的「第一行中文日志：连接超时\n异常：端口被占用\n」
// 由 tools/_gbkgen.py 算出，别手改：这些字节就是"非 UTF-8 的中文"这件事本身。
const GBK_BYTES = [181, 218, 210, 187, 208, 208, 214, 208, 206, 196, 200, 213, 214, 190, 163, 186,
  193, 172, 189, 211, 179, 172, 202, 177, 10, 210, 236, 179, 163, 163, 186, 182, 203, 191, 218,
  177, 187, 213, 188, 211, 195, 10];
// 按 GB18030 解出来应该是 22 个字。**解码坏掉时不会是 22**：
// 坏字节被换成 U+FFFD 后字数会明显变多（一个坏字节一个替换符），这条断言才有区分力。
const GBK_CHARS = 22;

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
    // 只为了让抽屉可用；本用例**不发真实 AI 请求**（发送那一步把 aiConsume 换成桩，
    // 验的是"消息怎么组装"，而不是"上游怎么回"）
    ai: { enabled: true, provider: 'deepseek', base_url: 'https://replay.invalid/v1',
      api_key: 'not-a-real-key', model: 'demo', memory_enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    lan: { enabled: false },
    update: { enabled: false, interval_hours: 72 },
    alerts: { webhook: '', cooldown_secs: 300 },
    notify: { muted: false },
  }, null, 2), 'utf8');
}

(async () => {
  if (!fs.existsSync(EXE)) { console.log('先构建：go build -o tools\\_aior\\EnvKit_aior.exe .'); process.exit(1); }
  seed();
  // 端口被占 = 上一次的实例还在跑，浏览器会连到**旧二进制**（改了代码行为却没变）
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) {
    console.log('端口 ' + PORT + ' 已被占用：先 taskkill /IM EnvKit_aior.exe');
    process.exit(2);
  }
  try { fs.rmSync(PROF, { recursive: true, force: true }); } catch (e) {}

  const envkit = spawn(EXE, [], { cwd: DIR, env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'aiod' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + PROF,
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
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
    const page = targets.find(t => t.type === 'page');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map(); const errs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
      if (m.method === 'Runtime.exceptionThrown') {
        const d = m.params && m.params.exceptionDetails;
        errs.push((d && d.text) || 'exception');
      }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) return '__EXC__' + JSON.stringify(r.result.exceptionDetails.text);
      return r.result && r.result.result ? r.result.result.value : undefined;
    };
    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    await evalJS(`(function(){ var b=document.getElementById('ai-clear'); if(b) b.click(); return 'ok'; })()`);
    await sleep(500);
    await evalJS(`aiOpen(); 'ok'`);
    await sleep(500);

    // 每次用例前清空输入框、toast、待发卡片，互不污染。
    // 卡片用**点 ×**清（用户真会这么做，顺带验移除路径），不直接改内部状态。
    const reset = async () => {
      await evalJS(`(function(){
        for(var i=0;i<20;i++){ var b=document.querySelector('#ai-attach .ai-doc .rm'); if(!b) break; b.click(); }
        var t=document.getElementById('ai-in'); if(t) t.value='';
        var box=document.getElementById('toasts'); if(box) box.innerHTML='';
        return 'ok';
      })()`);
    };
    const dropped = async (bytesJS, name, mime) => {
      return await evalJS(`(async function(){
        var f = new File([new Uint8Array(${bytesJS})], ${JSON.stringify(name)}, {type: ${JSON.stringify(mime)}});
        await aiDropFiles([f]);
        return 'done';
      })()`);
    };
    const taVal = () => evalJS(`document.getElementById('ai-in').value`);
    const toastText = () => evalJS(`(document.getElementById('toasts')||{}).innerText || ''`);
    // 待发区的全部可见文字（卡片名 + 字数 + × 的 title 都不含正文，正文进来了这里就会露馅）
    const attachText = () => evalJS(`(document.getElementById('ai-attach')||{}).innerText || ''`);
    const pendingDocs = async () => {
      const s = await evalJS(`JSON.stringify(window.__ekDocProbe.pending())`);
      try { return JSON.parse(s) || []; } catch (e) { return []; }
    };
    const cardCount = () => evalJS(`document.querySelectorAll('#ai-attach .ai-doc').length`);

    // ===== 1) UTF-8 纯文本：出来一张卡片，字数对，正文不进输入框 =====
    await reset();
    const UTF8_TEXT = '第一行 UTF-8 日志\n第二行';   // 16 字
    const utf8Bytes = JSON.stringify(Array.from(Buffer.from(UTF8_TEXT, 'utf8')));
    await dropped(utf8Bytes, 'app.log', 'text/plain');
    await sleep(900);
    let v = await taVal();
    let docs = await pendingDocs();
    check('txt-input-empty', (v || '') === '', JSON.stringify((v || '').slice(0, 80)));
    check('txt-one-card', (await cardCount()) === 1, 'cards=' + (await cardCount()));
    check('txt-card-named', (await attachText()).includes('app.log'), await attachText());
    // 字数 = "读进来多少"的唯一凭据（正文本身已经看不到，这是用户/我们唯一的对照点）
    check('txt-chars', docs.length === 1 && docs[0].chars === UTF8_TEXT.length && docs[0].kind === 'text',
      JSON.stringify(docs));
    check('txt-toast', /已附加.*app\.log/.test(await toastText()), await toastText());

    // ===== 2) 正文一个字都不许出现在 DOM 里（本次改造的核心）=====
    check('txt-not-in-input', !(v || '').includes('第一行'), 'textarea 里不该有正文');
    check('txt-not-in-attach', !(await attachText()).includes('第一行'), '待发区里不该有正文');

    // ===== 3) GBK 文件：必须按 GB18030 解出 22 个字（解错会变成一堆 U+FFFD）=====
    await reset();
    await dropped(JSON.stringify(GBK_BYTES), 'gbk.log', 'text/plain');
    await sleep(900);
    docs = await pendingDocs();
    check('gbk-chars', docs.length === 1 && docs[0].chars === GBK_CHARS, JSON.stringify(docs));
    check('gbk-no-mojibake', !(await attachText()).includes('\uFFFD'), '卡片里不该有替换字符');

    // ===== 4) 旧版 Office（OLE2 魔数）：出失败卡片 + 说清怎么办 =====
    await reset();
    const ole = JSON.stringify([0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1].concat(new Array(64).fill(0)));
    await dropped(ole, 'old.doc', 'application/msword');
    await sleep(900);
    docs = await pendingDocs();
    check('ole2-card-err', docs.length === 1 && !docs[0].id && !!docs[0].err, JSON.stringify(docs));
    check('ole2-not-in-input', (await taVal() || '') === '', '失败时什么都不该进输入框');
    const tt = await toastText();
    // 错误要带"怎么解决"：只说"读取失败"，用户只能去猜
    check('ole2-hint', /另存为/.test(tt), tt);

    // ===== 5) 文件名里的引号/尖括号：卡片按文本渲染，不许当 HTML =====
    await reset();
    await dropped(utf8Bytes, 'a"><b>.txt', 'text/plain');
    await sleep(900);
    docs = await pendingDocs();
    check('filename-kept-verbatim', docs.length === 1 && docs[0].name === 'a"><b>.txt', JSON.stringify(docs));
    check('filename-not-html', await evalJS(`!document.querySelector('#ai-attach .ai-doc b')`), '文件名不许被解析成标签');
    check('filename-no-dump', (await taVal() || '') === '', '输入框必须是空的');

    // ===== 6) 超大文件：不读、给明确上限 =====
    await reset();
    const big = await evalJS(`(async function(){
      // 17MB 全零：超过 16MB 上限（后端硬闸，前端也拦一道免得白传）
      var f = new File([new Uint8Array(17*1024*1024)], 'huge.bin', {type:'application/octet-stream'});
      await aiDropFiles([f]);
      return 'done';
    })()`);
    await sleep(600);
    check('too-big-no-card', big === 'done' && (await cardCount()) === 0, 'cards=' + (await cardCount()));
    check('too-big-msg', /MB/.test(await toastText()), await toastText());

    // ===== 7) 发送：气泡里是卡片，历史里只有引用，谁都没见过正文 =====
    await reset();
    await dropped(utf8Bytes, 'app.log', 'text/plain');
    await sleep(900);
    // 桩掉流式回合：这一步验的是"消息怎么组装"，不该依赖上游是否可达
    await evalJS(`(function(){
      window.__ekOrigConsume = aiConsume;
      window.__ekConsumeCalls = [];
      aiConsume = async function(u, body){ window.__ekConsumeCalls.push({u:u, body:body}); return 'stubbed'; };
      return 'ok';
    })()`);
    await evalJS(`(function(){ document.getElementById('ai-in').value = '这份日志说明了什么？'; return 'ok'; })()`);
    await evalJS(`(function(){ document.getElementById('ai-send').click(); return 'ok'; })()`);
    await sleep(800);
    const sent = await evalJS(`JSON.stringify(window.__ekDocProbe.sent())`);
    let sentDocs = []; try { sentDocs = JSON.parse(sent) || []; } catch (e) {}
    check('send-msg-docs', sentDocs.length === 1 && /^[0-9a-f]{32}$/.test(sentDocs[0].id || ''), sent);
    check('send-bubble-card', await evalJS(`document.querySelectorAll('#ai-msgs .ai-msg.user .ai-msg-docs .ai-doc').length`) === 1,
      'bubble cards');
    // 发出去的请求体里 docs 只带引用（正文由服务端读盘补），这里断言"引用而非正文"
    const bodyStr = await evalJS(`JSON.stringify((window.__ekConsumeCalls[0]||{}).body || {})`);
    check('send-payload-refs-only', /"docs":\[\{/.test(bodyStr) && !bodyStr.includes('第一行'), bodyStr.slice(0, 300));
    check('send-input-cleared', (await taVal() || '') === '', '发送后输入框必须清空');
    check('send-attach-cleared', (await cardCount()) === 0, 'cards=' + (await cardCount()));
    // 历史落盘：正文进 localStorage 会写爆 5MB 配额（QuotaExceededError 被 try/catch 吞掉，
    // 表现是"历史莫名丢了"），所以这里直接查存储里有没有正文
    await evalJS(`(function(){ aiHistSave(); return 'ok'; })()`);
    const hist = await evalJS(`localStorage.getItem('ek-ai-hist') || ''`);
    check('hist-refs-only', hist.includes('"docs"') && !hist.includes('第一行'), hist.slice(0, 300));
    await evalJS(`(function(){ aiConsume = window.__ekOrigConsume; return 'ok'; })()`);

    // ===== 8) 页面没有未捕获异常 =====
    check('no-js-exception', errs.length === 0, errs.slice(0, 2).join(' | '));

    console.log(results.join('\n'));
    console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }
  try { ws.close(); } catch (e) {}
  try { chrome.kill(); } catch (e) {}
  try { envkit.kill(); } catch (e) {}
  await sleep(400);
  process.exit(exitCode);
})();
