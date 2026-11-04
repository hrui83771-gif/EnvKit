// CDP 回归：行动列表的删除 / 清理 / 折叠
//
// ## 这个用例防什么
//
//  1. 行动列表以前只进不出：30 天保留期 + 200 条上限在管"自动增长"，
//     但用户没有任何手动收拾的手段。现在终态行有「删除」，pbar 有「清理」。
//  2. 删除的守门规则（后端也判，前端必须同向）：**未收口的行不许出现删除按钮**。
//     让人看见一个点下去必然失败的按钮，比不给它更糟。
//  3. delete / purge 的回包**不带 mission 键**——前端看见 d.mission 就去动列表，
//     零值对象会凭空插进一张 id 为空的卡。
//  4. 折叠：面板很窄，默认只铺最近 AI_MS_SHOW 条，"显示全部"点开才全铺。
//     折叠只是显示层的，不动 missions.json。
//
// ## 为什么直接注入 aiMissionRows
//
// 这里验的是**渲染与交互逻辑**：删除/清理的守门与落盘由 mission_test.go、
// mission_api_test.go 覆盖（变异已验证）。fetch 打桩回 ok，用来走完
// "点删除 → 确认 → 行消失" 的整条链路。
//
// 用法：node tools/cdp_ai_mission_test.js
// 隔离：独立端口 18799 + ENVKIT_INSTANCE_TAG + 独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9356;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_aior', 'EnvKit_aior.exe');
const DIR = path.dirname(EXE);
const PROF = path.join(REPO, 'tools', '_cdptmp_mission');

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
  try { fs.rmSync(PROF, { recursive: true, force: true }); } catch (e) {}
  seed();
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) {
    console.log('端口 ' + PORT + ' 已被占用：先 taskkill /IM EnvKit_aior.exe');
    process.exit(2);
  }

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

    await evalJS(`aiOpen(); 'ok'`);
    await sleep(400);

    // ===== 装桩：/api/missions 的 POST 按 op 回 ok；GET 不碰（列表全走注入）=====
    const REQ = [];   // 记录发出去的 body，后面断言 op / id
    await evalJS(`(function(){
      window.__origFetch = window.fetch;
      window.__reqs = [];
      window.fetch = function(u, o){
        if(String(u).indexOf('/api/missions') >= 0 && o && o.method === 'POST'){
          window.__reqs.push((o && o.body) || '');
          var b = {}; try{ b = JSON.parse((o && o.body) || '{}'); }catch(_){}
          var resp = { ok: true };
          if(b.op === 'purge') resp.removed = 9;
          // 故意**不带** mission 键：后端契约测试钉死了 delete/purge 不回零值对象
          return Promise.resolve(new Response(JSON.stringify(resp),
            {status:200, headers:{'Content-Type':'application/json'}}));
        }
        return window.__origFetch.apply(this, arguments);
      };
      return 'ok';
    })()`);

    // ===== 注入 11 条：3 条未收口 + 8 条已收口（updated_at 倒序 = 服务端口径）=====
    await evalJS(`(function(){
      var rows = [];
      for(var i=0;i<3;i++) rows.push({id:'m-live-'+i, goal:'未收口 '+i, state: i===0?'running':(i===1?'pending':'paused'),
        trigger:{kind:'manual'}, updated_at:'2026-11-04T10:0'+i+':00'});
      for(var j=0;j<8;j++) rows.push({id:'m-dead-'+j, goal:'已收口 '+j, state: j===0?'done':'canceled',
        trigger:{kind:'patrol'}, updated_at:'2026-11-03T09:0'+j+':00',
        result:{verified:true, msg:'ok'}});
      aiMissionRows = rows;
      aiMissionsShell();
      aiMissionsRender();
      return 'ok';
    })()`);
    await sleep(300);

    // ===== 1) 折叠：11 条只铺 8 条 + "显示全部" =====
    const rowCount = () => evalJS(`document.querySelectorAll('#ai-ms-list .ai-ms-row').length`);
    check('fold-default-8', (await rowCount()) === 8, 'rows=' + (await rowCount()));
    const moreBtn = await evalJS(`(document.querySelector('#ai-ms-list .ai-ms-more')||{}).innerText || ''`);
    check('fold-more-text', /还有 3 条/.test(moreBtn), moreBtn);
    await evalJS(`(document.querySelector('#ai-ms-list .ai-ms-more')||{click(){}}).click(); 'ok'`);
    await sleep(200);
    check('fold-expand-11', (await rowCount()) === 11, 'rows=' + (await rowCount()));

    // ===== 2) 守门：未收口的行没有「删除」，已收口的行有且为 danger =====
    const btnInfo = await evalJS(`(function(){
      var out = {};
      Array.from(document.querySelectorAll('#ai-ms-list .ai-ms-row')).forEach(function(r){
        var goal = (r.querySelector('.ai-ms-goal')||{}).innerText || '';
        var del = Array.from(r.querySelectorAll('.ai-ms-acts button')).some(function(b){ return b.innerText === '删除'; });
        out[goal] = del;
      });
      return JSON.stringify(out);
    })()`);
    let del = {};
    try { del = JSON.parse(btnInfo) || {}; } catch (e) {}
    check('live-no-delete', del['未收口 0'] === false && del['未收口 1'] === false && del['未收口 2'] === false, btnInfo);
    check('dead-has-delete', del['已收口 0'] === true && del['已收口 7'] === true, btnInfo);

    // ===== 3) 点删除 → 确认 → 行消失；请求体 op=delete、id 对得上 =====
    await evalJS(`(function(){
      var rows = Array.from(document.querySelectorAll('#ai-ms-list .ai-ms-row'));
      var r = rows.find(function(x){ return (x.querySelector('.ai-ms-goal')||{}).innerText === '已收口 0'; });
      var b = r && Array.from(r.querySelectorAll('.ai-ms-acts button')).find(function(b){ return b.innerText === '删除'; });
      if(b) b.click();
      return b ? 'ok' : 'nobutton';
    })()`);
    await sleep(400);
    const cfTitle = await evalJS(`(document.getElementById('cf-title')||{}).innerText || ''`);
    check('confirm-shown', (await evalJS(`document.getElementById('confirm').classList.contains('show')`)) === true, cfTitle);
    await evalJS(`document.getElementById('cf-ok').click(); 'ok'`);
    await sleep(500);
    check('row-removed', (await rowCount()) === 10, 'rows=' + (await rowCount()));
    const gone = await evalJS(`(aiMissionRows||[]).some(function(x){ return x.id === 'm-dead-0'; })`);
    check('cache-updated', gone === false, String(gone));
    const reqs = await evalJS(`JSON.stringify(window.__reqs)`);
    // reqs 是 stringify 过的数组，内层引号带转义 —— 只匹配不带引号的子串
    check('req-delete-op', reqs.indexOf('m-dead-0') >= 0 && /delete/.test(reqs), reqs);

    // ===== 4) purge：未收口的保留，只清终态 =====
    await evalJS(`document.getElementById('ai-ms-purge').click(); 'ok'`);
    await sleep(400);
    await evalJS(`document.getElementById('cf-ok').click(); 'ok'`);
    await sleep(500);
    const afterPurge = await evalJS(`JSON.stringify((aiMissionRows||[]).map(function(x){ return x.state; }))`);
    check('purge-keeps-live', !/done|canceled/.test(afterPurge) && /running/.test(afterPurge), afterPurge);
    const reqs2 = await evalJS(`JSON.stringify(window.__reqs)`);
    check('req-purge-op', /purge/.test(reqs2), reqs2);

    // ===== 5) 回包不带 mission 也不产生幽灵卡：行数与缓存一致 =====
    check('no-phantom-row', (await rowCount()) === 3, 'rows=' + (await rowCount()));

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
