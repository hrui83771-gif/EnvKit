// CDP 回归（v3.7.4）：把文件拖进 AI 对话区，必须走**后端抽取**而不是前端硬读
//
// ## 这个用例防什么
//
// 以前 aiDropFiles() 对非图片一律 `f.text()`：按 UTF-8 硬解，于是
//   - 拖 docx 进去 = zip 压缩流被当文本，一屏问号；
//   - 拖 GBK 日志进去 = 中文全变乱码；
//   - 拖 exe/zip 进去 = 二进制垃圾塞满输入框，还包着 <untrusted_data>。
// 现在改成 POST /api/ai/extract，由后端按**魔数**判类型、按内容探测编码。
//
// 这个脚本专门验**前端这一半**：请求真的发出去了、回来的文本被正确包进
// <untrusted_data>、失败原因原样显示给用户（而不是一句"操作失败"）。
// 后端解析本身由 ai_extract_test.go 的 10 条单测覆盖，两边不重复。
//
// 为什么用页面里现造的 File 而不是仓库里放样本文件：
// docx/pdf 是二进制，提交进仓库既不可读也容易被误改；而这里要验的是
// "字节进去 → 文本出来"的链路，用 GBK 文本 + OLE2 魔数就够触发全部分支。
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

// GBK 编码的「第一行中文日志：连接超时\n异常：端口被占用」
// 由 tools/_gbkgen.py 算出，别手改：这些字节就是"非 UTF-8 的中文"这件事本身。
const GBK_BYTES = [181, 218, 210, 187, 208, 208, 214, 208, 206, 196, 200, 213, 214, 190, 163, 186,
  193, 172, 189, 211, 179, 172, 202, 177, 10, 210, 236, 179, 163, 163, 186, 182, 203, 191, 218,
  177, 187, 213, 188, 211, 195, 10];
const GBK_EXPECT = '第一行中文日志';

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
    // 只为了让抽屉可用；本用例**不发 AI 请求**（拖文件走 /api/ai/extract，不碰上游）
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

    // 每次用例前清空输入框与 toast，互不污染
    const reset = async () => {
      await evalJS(`(function(){
        var t=document.getElementById('ai-in'); if(t) t.value='';
        var box=document.getElementById('toasts'); if(box) box.innerHTML='';
        return 'ok';
      })()`);
    };
    const dropped = async (bytesJS, name, mime) => {
      const r = await evalJS(`(async function(){
        var f = new File([new Uint8Array(${bytesJS})], ${JSON.stringify(name)}, {type: ${JSON.stringify(mime)}});
        await aiDropFiles([f]);
        return 'done';
      })()`);
      return r;
    };
    const taVal = () => evalJS(`document.getElementById('ai-in').value`);
    const toastText = () => evalJS(`(document.getElementById('toasts')||{}).innerText || ''`);

    // ===== 1) UTF-8 纯文本：进包裹、正文在 =====
    await reset();
    const utf8Bytes = JSON.stringify(Array.from(Buffer.from('第一行 UTF-8 日志\n第二行', 'utf8')));
    await dropped(utf8Bytes, 'app.log', 'text/plain');
    await sleep(800);
    let v = await taVal();
    check('txt-wrapped', /<untrusted_data source="file:app\.log">\n第一行 UTF-8 日志/.test(v || ''), JSON.stringify((v || '').slice(0, 80)));
    check('txt-closed', (v || '').includes('</untrusted_data>'), 'ok');
    check('txt-toast', /已读入.*app\.log/.test(await toastText()), await toastText());

    // ===== 2) GBK 文件：必须解出中文，而不是一串问号 =====
    await reset();
    await dropped(JSON.stringify(GBK_BYTES), 'gbk.log', 'text/plain');
    await sleep(900);
    v = await taVal();
    check('gbk-decoded', (v || '').includes(GBK_EXPECT), JSON.stringify((v || '').slice(0, 120)));
    check('gbk-no-mojibake', !(v || '').includes('\uFFFD'), '希望不含替换字符');

    // ===== 3) 旧版 Office（OLE2 魔数）：拒绝，并说清怎么办 =====
    await reset();
    const ole = JSON.stringify([0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1].concat(new Array(64).fill(0)));
    await dropped(ole, 'old.doc', 'application/msword');
    await sleep(900);
    v = await taVal();
    check('ole2-not-inserted', !(v || '').includes('untrusted_data'), JSON.stringify((v || '').slice(0, 60)));
    const tt = await toastText();
    // 错误要带"怎么解决"：只说"读取失败"，用户只能去猜
    check('ole2-hint', /另存为/.test(tt), tt);

    // ===== 4) 文件名里的引号/尖括号不能把包裹标签提前闭合 =====
    await reset();
    await dropped(utf8Bytes, 'a"><b>.txt', 'text/plain');
    await sleep(800);
    v = await taVal();
    const opens = (v || '').split('<untrusted_data').length - 1;
    const closes = (v || '').split('</untrusted_data>').length - 1;
    check('filename-sanitized', opens === 1 && closes === 1 && !(v || '').includes('"><b>'),
      'opens=' + opens + ' closes=' + closes);

    // ===== 5) 超大文件：不读、给明确上限 =====
    await reset();
    const big = await evalJS(`(async function(){
      // 17MB 全零：超过 16MB 上限（后端硬闸，前端也拦一道免得白传）
      var f = new File([new Uint8Array(17*1024*1024)], 'huge.bin', {type:'application/octet-stream'});
      await aiDropFiles([f]);
      return 'done';
    })()`);
    await sleep(600);
    v = await taVal();
    check('too-big-skipped', big === 'done' && !(v || '').includes('untrusted_data'), JSON.stringify((v || '').slice(0, 60)));
    check('too-big-msg', /MB/.test(await toastText()), await toastText());

    // ===== 6) 页面没有未捕获异常 =====
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
