// CDP 回归：「+号 选文件」与文档卡片的**格式样式**
//
// ## 这个用例防什么
//
//  1. +号 选中的 docx/pdf 以前只往消息里加一行路径引用，模型 read_file 时
//     头 512 字节一判二进制就返回「已跳过」—— 文件明明在列表里，模型说看不到。
//     现在这几类走后端抽取，产出与拖拽一模一样的卡片。
//  2. 所有文档卡片以前一个颜色（统一 var(--brand)），六种格式长得全一样。
//     现在按 kind 上色：PDF 红 / Word 蓝 / Excel 绿 / PPT 橙。
//
// ## 为什么给 fetch 打桩
//
// 这里验的是**前端分流与 CSS**：真后端解析由 ai_extract_test.go 覆盖，
// path 模式的沙箱边界由 ai_extract_path_test.go 覆盖。桩里故意回一句
// "机密正文"，用来做反向断言 —— 正文一旦被写进输入框或 DOM，这里就红。
//
// 用法：node tools/cdp_ai_pick_test.js
// 隔离：独立端口 18798 + ENVKIT_INSTANCE_TAG + 独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9355;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_aior', 'EnvKit_aior.exe');
const DIR = path.dirname(EXE);
const PROF = path.join(REPO, 'tools', '_cdptmp_aipick');
const SBOX = path.join(PROF, 'sandbox');

// 选择器只列目录项、不读内容，所以这里的 docx 不需要是真文档
function fixtures() {
  fs.mkdirSync(path.join(SBOX, 'sub'), { recursive: true });
  fs.writeFileSync(path.join(SBOX, '报告.docx'), Buffer.from([0x50, 0x6b, 0x03, 0x04, 0, 0, 0, 0]));
  fs.writeFileSync(path.join(SBOX, '报表.xlsx'), Buffer.from([0x50, 0x6b, 0x03, 0x04, 0, 0, 0, 0]));
  fs.writeFileSync(path.join(SBOX, 'note.txt'), 'x');
}

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
    projects: { frontend_dir: '', backend_dir: SBOX, mysql_host: '127.0.0.1', mysql_port: 3306,
      mysql_user: 'root', db_name: 'farm', sql_file: '', backup_dir: '', scan_ports: [8080, 8888], web_scripts: [] },
    chain: { ssh_host: '', ssh_port: 22, ssh_user: 'root', chain_host: '', chain_port: 20200,
      chain_dir: '', group_id: 1, chain_guard: false, chain_autorecover: false, chain_notify: true },
    dev: { server_port: PORT },
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
  try { fs.rmSync(SBOX, { recursive: true, force: true }); } catch (e) {}
  fixtures();
  seed();
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) {
    console.log('端口 ' + PORT + ' 已被占用：先 taskkill /IM EnvKit_aior.exe');
    process.exit(2);
  }
  try { fs.rmSync(PROF + '\\chrome', { recursive: true, force: true }); } catch (e) {}

  const envkit = spawn(EXE, [], { cwd: DIR, env: Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'aipick' }), stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(PROF, 'chrome'),
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
    let mid = 0; const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
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
    await sleep(4500);

    await evalJS(`(function(){ var b=document.getElementById('ai-clear'); if(b) b.click(); return 'ok'; })()`);
    await sleep(400);
    await evalJS(`aiOpen(); 'ok'`);
    await sleep(400);

    // ===== 1) 分流判据：只有解开全靠后端的几类走抽取 =====
    check('ext-docx', (await evalJS(`aiDocExt('报告.docx')`)) === 'docx');
    check('ext-pdf-upper', (await evalJS(`aiDocExt('A.PDF')`)) === 'pdf');
    // 文本类必须留在"路径引用"这条路上：大日志让 AI 自己分页读，别一次截断
    check('ext-log-empty', (await evalJS(`aiDocExt('app.log')`)) === '');
    check('ext-md-empty', (await evalJS(`aiDocExt('README.md')`)) === '');
    check('ext-noext-empty', (await evalJS(`aiDocExt('Makefile')`)) === '');

    // ===== 2) 装桩：/api/ai/extract 一律回一个"带正文"的假响应 =====
    // 桩里回 text 是有意的：前端若把正文写回输入框，下面几条会立刻红。
    await evalJS(`(function(){
      window.__origFetch = window.fetch;
      window.fetch = function(u, o){
        if(String(u).indexOf('/api/ai/extract') >= 0){
          var b = {};
          try{ b = JSON.parse((o && o.body) || '{}'); }catch(_){}
          if(!b.path) return window.__origFetch.apply(this, arguments);
          return Promise.resolve(new Response(JSON.stringify({
            ok:true, id:'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', kind:'docx', enc:'utf-8',
            chars:42, pages:0, truncated:false, note:'', text:'机密正文不该出现在界面里'
          }), {status:200, headers:{'Content-Type':'application/json'}}));
        }
        return window.__origFetch.apply(this, arguments);
      };
      return 'ok';
    })()`);

    // ===== 3) 打开选择器：文档项与文本项在图标上就要能分开 =====
    await evalJS(`aiPickOpen(); 'ok'`);
    await sleep(900);
    const pickItems = await evalJS(`JSON.stringify(Array.from(document.querySelectorAll('#ai-pick-list .ai-pick-item')).map(d=>({t:d.innerText.trim(), cls:d.className})))`);
    let pi = [];
    try { pi = JSON.parse(pickItems) || []; } catch (e) {}
    const docItem = pi.find(x => x.t.indexOf('报告.docx') >= 0);
    const txtItem = pi.find(x => x.t.indexOf('note.txt') >= 0);
    check('pick-listed', !!docItem && !!txtItem, pickItems);
    check('pick-doc-icon', !!docItem && /f-doc/.test(docItem.cls), docItem && docItem.cls);
    check('pick-txt-icon', !!txtItem && /f-txt/.test(txtItem.cls), txtItem && txtItem.cls);

    // ===== 4) 点 docx：出卡片（以前这里只加一条路径引用，模型读不到）=====
    await evalJS(`(function(){
      var d = Array.from(document.querySelectorAll('#ai-pick-list .ai-pick-item')).find(x=>x.innerText.indexOf('报告.docx')>=0);
      if(d) d.click();
      return d ? 'ok' : 'notfound';
    })()`);
    await sleep(900);
    const cardCount = () => evalJS(`document.querySelectorAll('#ai-attach .ai-doc').length`);
    const chipCount = () => evalJS(`document.querySelectorAll('#ai-chips .ai-chip').length`);
    check('doc-one-card', (await cardCount()) === 1, 'cards=' + (await cardCount()));
    check('doc-kind-attr', (await evalJS(`(document.querySelector('#ai-attach .ai-doc')||{dataset:{}}).dataset.k`)) === 'docx');
    check('doc-badge-text', (await evalJS(`(document.querySelector('#ai-attach .ai-doc .badge')||{}).innerText`)) === 'DOCX');
    // 字数来自响应，是"读进来多少"的唯一凭据（正文已看不到）
    check('doc-chars', /42 字/.test(await evalJS(`(document.getElementById('ai-attach')||{}).innerText || ''`)),
      await evalJS(`(document.getElementById('ai-attach')||{}).innerText || ''`));

    // ===== 5) 格式色真的生效了（CSS 变量优先级踩过坑，必须量计算值）=====
    const bg = await evalJS(`(function(){
      var b = document.querySelector('#ai-attach .ai-doc .badge');
      return b ? getComputedStyle(b).backgroundColor : '';
    })()`);
    check('doc-color-word', bg === 'rgb(43, 87, 154)', 'badge bg=' + bg);
    const bar = await evalJS(`(function(){
      var d = document.querySelector('#ai-attach .ai-doc');
      return d ? getComputedStyle(d).borderLeftColor : '';
    })()`);
    check('doc-color-bar', bar === 'rgb(43, 87, 154)', 'left border=' + bar);

    // ===== 6) 反向断言：桩里那句"机密正文"一个字都不许落到界面 =====
    const ta = await evalJS(`document.getElementById('ai-in').value`);
    const att = await evalJS(`(document.getElementById('ai-attach')||{}).innerText || ''`);
    check('no-body-in-input', (ta || '') === '', JSON.stringify((ta || '').slice(0, 80)));
    check('no-body-in-card', !(att || '').includes('机密正文'), att);

    // ===== 7) 文本文件仍走"路径引用"：进 chip，不进附件卡 =====
    await evalJS(`aiPickOpen(); 'ok'`);
    await sleep(800);
    await evalJS(`(function(){
      var d = Array.from(document.querySelectorAll('#ai-pick-list .ai-pick-item')).find(x=>x.innerText.indexOf('note.txt')>=0);
      if(d) d.click();
      return d ? 'ok' : 'notfound';
    })()`);
    await sleep(700);
    check('txt-goes-to-chip', (await chipCount()) === 1, 'chips=' + (await chipCount()));
    check('txt-no-second-card', (await cardCount()) === 1, 'cards=' + (await cardCount()));
    // chip 里只有路径，没有内容 —— 内容由 read_file 在沙箱内按需读
    const chipText = await evalJS(`(document.getElementById('ai-chips')||{}).innerText || ''`);
    check('chip-is-path', /note\.txt/.test(chipText), chipText);

    console.log('\n' + results.join('\n'));
    console.log('\nOVERALL: ' + (exitCode === 0 ? 'ALL PASS' : 'FAILED'));
  } catch (e) {
    console.log('EXCEPTION: ' + (e && e.message || e));
    exitCode = 1;
  } finally {
    try { if (ws) ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
    await sleep(600);
    process.exit(exitCode);
  }
})();
