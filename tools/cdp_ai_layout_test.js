// CDP 回归（v3.7.3）：聊天区「过程在上、结论在下」+ 确认卡不许被撑爆
//
// ## 这两条各防什么（都是用户看着截图报的）
//
// ① 顺序：一轮里只要"先说了句话再动手"，屏幕上就是**结论在上、过程在下**。
//    根因是气泡在**第一个 delta** 时就已经落到容器末尾，之后到达的过程卡
//    （执行计划 / 工具结果 / 复验徽章）只能追加到它后面，而后续 delta 又写回
//    **那条**气泡。用户原话："结果可不可以在过程下面"。
//    → 这个用例用 ENVKIT_AI_REPLAY 跑一轮真实的"先说话 → 调 list_project → 再给结论"，
//      断言 .ai-tool 在 .ai-wrap 之前，且 .ai-wrap 是容器最后一个元素。
//      **必须用回放走真代码**：这个顺序是 aiConsume 里的时序产物，注入 DOM 测不出来。
//
// ② 确认卡体积：全局 input,select{width:100%} 命中了批量授权的勾选框，
//    flex:none 只挡"伸缩"挡不住"本身 100% 宽" → 勾选框吃满一行、说明被挤成
//    一行一个字、整张卡三四百像素高。用户原话："确认卡体积膨胀了"。
//    → 直接调**真实的** aiConfirmCard() 造卡（纯构造、无状态、不出网），量盒子。
//      结构断言在这里永远是绿的（元素都在），只有宽度/高度能说明问题。
//
// 用法：
//   1) 构造带版本号的 exe（或直接 go build）
//   2) node tools/cdp_ai_layout_test.js
//
// 隔离：独立端口 18798 + ENVKIT_INSTANCE_TAG + exe 旁独立数据目录 + 回放夹具。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18798;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9353;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_aior', 'EnvKit_aior.exe');
const DIR = path.dirname(EXE);
const FIXTURE = path.join(REPO, 'testdata', 'ai-rec', 'text-then-tool-then-answer.jsonl');
const PROF = path.join(REPO, 'tools', '_cdptmp_aior');

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
    // 这三个字段只为了让 aiChatReady 放行；**请求不会真的出网**（ENVKIT_AI_REPLAY 拦在上游之前）
    ai: { enabled: true, provider: 'deepseek', base_url: 'https://replay.invalid/v1',
      api_key: 'replay-not-a-real-key', model: 'replay-demo', memory_enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    lan: { enabled: false },
    update: { enabled: false, interval_hours: 72 },
    alerts: { webhook: '', cooldown_secs: 300 },
    notify: { muted: false },
  }, null, 2), 'utf8');
}

(async () => {
  if (!fs.existsSync(EXE)) { console.log('先构建：go build -o tools\\_aior\\EnvKit_aior.exe .'); process.exit(1); }
  if (!fs.existsSync(FIXTURE)) { console.log('缺回放夹具：' + FIXTURE); process.exit(1); }
  seed();
  // **端口已被占用就直接拒跑。** EnvKit 发现同端口的实例在跑会"已在运行"地退出，
  // 于是浏览器连上的是**上一次的旧二进制**：改了半天代码，量的是老行为，
  // 而且旧实例的回放游标/历史还在，请求次数都对不上 —— 这类假象排查成本极高。
  let occupied = false;
  try { await getJSON(URL_); occupied = true; } catch (e) {}
  if (occupied) {
    console.log('端口 ' + PORT + ' 已被占用：上一次的实例还在跑。先 taskkill /IM EnvKit_aior.exe，否则量的是旧二进制。');
    process.exit(2);
  }
  // **每次跑都清掉浏览器配置目录**：localStorage 里的对话历史会在下次启动时
  // 被 aiHistRestore() 复原，于是"容器里第几个元素"这件事每跑一次都不一样 ——
  // 顺序断言会时绿时红，而原因跟被测代码毫无关系。
  try { fs.rmSync(PROF, { recursive: true, force: true }); } catch (e) {}
  const env = Object.assign({}, process.env, {
    ENVKIT_INSTANCE_TAG: 'aior',
    ENVKIT_AI_REPLAY: FIXTURE,
  });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore' });
  let ready = false;
  for (let i = 0; i < 60; i++) { try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); } }
  console.log('envkit ready:', ready);
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions', '--hide-scrollbars',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + PROF,
    '--window-size=1400,1200', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let exitCode = 0;
  const results = [];
  const check = (name, ok, extra) => { results.push((ok ? 'PASS' : 'FAIL') + ' ' + name + (extra ? ' :: ' + extra : '')); if (!ok) exitCode = 1; };

  let ws = null;
  try {
    let targets = null;
    for (let i = 0; i < 10; i++) { try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); } }
    const page = targets.find(t => t.type === 'page');
    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0; const pending = new Map();
    ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); } };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async expr => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      return r.result && r.result.result ? r.result.result.value : undefined;
    };
    await send('Runtime.enable', {});
    await send('Page.enable', {});
    // 收集页面运行时异常：抽屉里任何 JS 抛错都会让下面某条"看起来只是没过"
    const errs = [];
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); return; }
      if (m.method === 'Runtime.exceptionThrown') {
        const d = m.params && m.params.exceptionDetails;
        errs.push((d && d.text) || 'exception');
      }
    };
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    // 用应用自己的「清空」把会话归零。
    // **不要靠"删配置目录"**：Windows 上目录常被句柄占住删不掉，aiHistRestore() 会把
    // 上次的对话复原回来 —— 于是这一轮 DOM 前面多出好几条旧消息，历史一长还会触发
    // 摘要（白白多消耗一条回放录像）。清空按钮走的是产品路径，且不依赖目录状态。
    await evalJS(`(function(){ var b=document.getElementById('ai-clear'); if(b) b.click(); return 'ok'; })()`);
    await sleep(600);

    // 抽屉打开 + 放大：用户报的两张截图都是放大态
    await evalJS(`aiOpen(); aiBig(true); 'ok'`);
    await sleep(600);

    // ===== A) 确认卡：量盒子 =====
    const card = JSON.parse(await evalJS(`(function(){
      var cc = aiConfirmCard({tool:'stop_service', text:'停止后端服务'});
      var host = document.getElementById('ai-msgs');
      host.appendChild(cc.card);
      var r = cc.card.getBoundingClientRect();
      var inp = cc.card.querySelector('.ai-batch input');
      var sp  = cc.card.querySelector('.ai-batch span');
      var lab = cc.card.querySelector('.ai-batch');
      var bi = inp ? inp.getBoundingClientRect() : null;
      var bs = sp ? sp.getBoundingClientRect() : null;
      var bl = lab ? lab.getBoundingClientRect() : null;
      var out = {h:Math.round(r.height), w:Math.round(r.width),
        inpW: bi ? Math.round(bi.width) : -1,
        spW:  bs ? Math.round(bs.width) : -1,
        spH:  bs ? Math.round(bs.height) : -1,
        labH: bl ? Math.round(bl.height) : -1};
      host.removeChild(cc.card);
      return JSON.stringify(out);
    })()`));
    // 勾选框必须只是那个小方块：100% 宽 = 全局 input 规则又压下来了
    check('confirm-checkbox-not-full-width', card.inpW > 0 && card.inpW <= 30, 'checkbox ' + card.inpW + 'px');
    // 说明文字要能横向铺开（被挤成一行一个字时这里只剩十几像素）
    check('confirm-label-text-wide', card.spW >= 150, 'span ' + card.spW + 'px');
    // 两行以内：挤成一列时这里会是几百像素
    check('confirm-label-two-lines', card.spH > 0 && card.spH <= 60, 'span h=' + card.spH + 'px');
    // 整张卡不该有"一屏那么高"
    check('confirm-card-not-inflated', card.h > 0 && card.h <= 220, 'card ' + card.w + '×' + card.h);

    // ===== B) 顺序：回放一轮「先说话 → 调工具 → 给结论」 =====
    await evalJS(`aiSendText('看看项目结构'); 'sent'`);
    let busy = true;
    for (let i = 0; i < 80; i++) {
      await sleep(500);
      busy = await evalJS(`aiBusy === true`);
      if (busy !== true) break;
    }
    check('replay-turn-finished', busy !== true, 'aiBusy=' + busy);

    const order = JSON.parse(await evalJS(`(function(){
      var host = document.getElementById('ai-msgs');
      var kids = [].slice.call(host.children).map(function(e){ return e.className || ''; });
      function idx(re){ for(var i=0;i<kids.length;i++) if(re.test(kids[i])) return i; return -1; }
      var lastWrap = -1;
      for(var i=0;i<kids.length;i++) if(/ai-wrap/.test(kids[i])) lastWrap = i;
      // 用**最后一个**工具卡：一轮可能有多次工具调用，只要有一张落在结论下面就是错的
      var lastTool = -1;
      for(var j=0;j<kids.length;j++) if(/ai-tool/.test(kids[j])) lastTool = j;
      return JSON.stringify({
        kids: kids, plan: idx(/ai-plan/), tool: idx(/ai-tool/), lastTool: lastTool,
        lastWrap: lastWrap, last: kids.length - 1,
        // 故障时要能一眼看出是"回放用完了"还是真报错，否则只能猜
        errText: (function(){ var e = host.querySelector('.ai-msg.err'); return e ? (e.innerText||'').slice(0,120) : ''; })(),
        lastCls: kids[kids.length-1] || '',
        answer: (function(){
          var ws = host.querySelectorAll('.ai-wrap');
          if(!ws.length) return '';
          return (ws[ws.length-1].innerText || '').slice(0, 60);
        })()
      });
    })()`));
    check('turn-has-tool-card', order.tool >= 0, 'kids=' + JSON.stringify(order.kids));
    check('tool-card-above-answer', order.lastTool >= 0 && order.lastWrap > order.lastTool,
      'lastTool@' + order.lastTool + ' wrap@' + order.lastWrap + ' kids=' + JSON.stringify(order.kids));
    check('answer-is-last-node', order.lastWrap === order.last, 'last=' + order.last + '(' + order.lastCls + ')');
    check('answer-text-rendered', /环境就绪/.test(order.answer), JSON.stringify(order.answer) + (order.errText ? ' ERR=' + order.errText : ''));
    check('no-runtime-errors', errs.length === 0, errs.slice(0, 3).join(' | '));

    try { ws.close(); } catch (e) {}
  } catch (e) {
    console.log('ERROR', e.message);
    exitCode = 1;
  }

  console.log(results.join('\n'));
  console.log('OVERALL ' + (exitCode === 0 ? 'PASS' : 'FAIL'));

  try { chrome.kill(); } catch (e) {}
  try { envkit.kill(); } catch (e) {}
  await sleep(500);
  process.exit(exitCode);
})();
