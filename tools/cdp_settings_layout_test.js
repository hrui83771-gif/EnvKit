// CDP 回归：设置页「基础」区的布局与新增配置项（v3.7.1）
//
// ## 这条测试防的是什么
//
// 用户报："收紧一下基础设置页……目前页面很多空白缺失"。
// 原布局是 auto 两列 grid：每行按行内最高的卡对齐，矮卡右边直接留一个洞。
// 实测「安装与分发」100px 挨着「手机访问」340px → 右侧空 240px；
// 末行「界面」独占左列、右列整块空白；整页 1661px。
//
// **DOM 结构断言测不出这类问题**：卡片数量、顺序、id 全都对，CSS 一挤就难看。
// 所以这里的核心是**量盒子**：
//   - 同一列里相邻两张卡之间的空隙不许 > 9px（有洞就是从前的 240px 回来了）
//   - 两列总高差 ≤ 120px（否则页面底部一条大空白）
//   - 整页高度 ≤ 1560（收紧的量化目标；改前 1661）
//   - 旧的 .set-grid 不许复活（它才是留洞的原因）
//
// 另外补几条**行为**断言，因为这次同时新接了三个真实配置项：
//   - 备份目录：点了保存要落盘到 config.json 的 projects.backup_dir
//   - 端口线索：乱写的 "9000，8888, abc, 70000, 9000" 必须变成 [9000,8888]
//     （全角逗号要认、非法项要丢、越界要丢、重复要去重）
//   - projectsBody() 必须回传 backup_dir：老「程序配置」页整体替换 Projects，
//     不回传就会把设置页刚存的备份目录抹成空（与 scan_ports 同一类坑）
//   - 链端主机为空时只读行要显示「—」，不许露出孤零零的 ":20200"
//
// 用法：
//   1) go build -o tools\_setl\EnvKit_setl.exe .
//   2) node tools/cdp_settings_layout_test.js
//
// 隔离：独立端口 18797 + ENVKIT_INSTANCE_TAG + exe 旁独立数据目录。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18797;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9352;
const EXE = process.env.ENVKIT_EXE || path.join(REPO, 'tools', '_setl', 'EnvKit_setl.exe');
const DIR = path.dirname(EXE);
const CFG = path.join(DIR, 'config.json');

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 隔离目录跑完就删（见文件末尾的收尾）：exe 不预置，缺了就从仓库根那份复制一次，
  // 这样"删干净"之后下一次跑不必再手动 go build。
  if (!fs.existsSync(EXE)) { try { fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE); } catch (e) {} }
  fs.writeFileSync(CFG, JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
    ai: { memory_enabled: false },
    lan: { enabled: false },
    projects: {
      frontend_dir: 'D:\\BCGD\\FarmTrace\\web',
      backend_dir: 'D:\\BCGD\\FarmTrace\\server',
      backup_dir: '', scan_ports: [8080, 8888],
    },
    chain: { chain_host: '111.228.57.198', chain_port: 20200, ssh_host: '', ssh_user: '', chain_guard: false },
  }, null, 2), 'utf8');
}

(async () => {
  if (!fs.existsSync(EXE)) {
    // 隔离目录跑完就删（见文件末尾）：exe 不预置，缺了就从仓库根那份复制一次，
    // 这样「删干净」之后下一次跑不必再手动 go build。
    fs.mkdirSync(DIR, { recursive: true });
    fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  }
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'setl' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore' });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('envkit ready:', ready, '(' + URL_ + ')');
  if (!ready) { envkit.kill(); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_setl'),
    '--window-size=1400,1000', '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let ws = null;
  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  FAIL ' + msg); } };

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets || !Array.isArray(targets)) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    if (!page) throw new Error('no page target');

    ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
    let mid = 0;
    const pending = new Map();
    ws.onmessage = ev => {
      const m = JSON.parse(ev.data);
      if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
    };
    const send = (method, params) => new Promise(res => { const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params })); });
    const evalJS = async (expr) => {
      const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
      if (r.result && r.result.exceptionDetails) throw new Error('页面异常：' + JSON.stringify(r.result.exceptionDetails));
      return r.result && r.result.result ? r.result.result.value : undefined;
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(4500);

    // 用户实际点的两步：左侧「设置」→ 默认落在「基础」
    await evalJS(`goto('settings')`);
    await sleep(400);
    await evalJS(`(function(){var b=document.querySelector('[data-set-tab="base"]'); if(b) b.click();})()`);
    await sleep(900);

    // ---- A. 结构：旧布局不许复活 ----
    const st = JSON.parse(await evalJS(`(function(){
      var pane = document.querySelector('[data-set-pane="base"]');
      return JSON.stringify({
        cols: pane.querySelectorAll('.set-cols').length,
        colN: pane.querySelectorAll('.set-col').length,
        legacyGrid: pane.querySelectorAll('.set-grid').length,
        cards: pane.querySelectorAll('.set-card').length,
        perCol: [].slice.call(pane.querySelectorAll('.set-col')).map(function(c){ return c.children.length; })
      });
    })()`));
    ok(st.cols === 1, '基础区用 .set-cols 两列容器');
    ok(st.legacyGrid === 0, '旧的 .set-grid 没复活（它才是"矮卡右边留洞"的来源）→ ' + st.legacyGrid);
    ok(st.colN === 2, '恰好两列 → ' + st.colN);
    ok(st.perCol[0] >= 4 && st.perCol[1] >= 4, '两列各 ≥ 4 张卡 → ' + JSON.stringify(st.perCol));
    ok(st.cards === 13, '基础区共 13 张卡（6 + 6 + 能力开关）→ ' + st.cards);

    // ---- B. 盒子：没有洞、两列配平、整页收紧 ----
    const box = JSON.parse(await evalJS(`(function(){
      var pane = document.querySelector('[data-set-pane="base"]');
      var cols = [].slice.call(pane.querySelectorAll('.set-col')).map(function(col){
        var cards = [].slice.call(col.children).map(function(el){
          var b = el.getBoundingClientRect();
          return { t:(el.querySelector('.set-card-head .t')||{}).textContent||'?',
                   y:Math.round(b.top), h:Math.round(b.height) };
        });
        var holes = [];
        for(var i=1;i<cards.length;i++){
          var g = cards[i].y - (cards[i-1].y + cards[i-1].h);
          if(g > 9) holes.push(cards[i-1].t + ' > ' + cards[i].t + ' = ' + g + 'px');
        }
        return { h: Math.round(col.getBoundingClientRect().height), holes: holes, n: cards.length };
      });
      var pr = pane.getBoundingClientRect();
      return JSON.stringify({ cols: cols, paneH: Math.round(pr.height), vw: innerWidth });
    })()`));

    ok(box.cols[0].holes.length === 0, '左列同列内没有洞 → ' + JSON.stringify(box.cols[0].holes));
    ok(box.cols[1].holes.length === 0, '右列同列内没有洞 → ' + JSON.stringify(box.cols[1].holes));
    const dh = Math.abs(box.cols[0].h - box.cols[1].h);
    ok(dh <= 120, '两列高度配平：' + box.cols[0].h + ' vs ' + box.cols[1].h + '（差 ' + dh + 'px）');
    // 改前整页 1661：这里给 1560 的上限，收紧 100px 以上才算数
    ok(box.paneH <= 1560, '基础区整页收紧到 ' + box.paneH + 'px（改前 1661）');

    // 每张卡都不许宽过列：长路径/长 URL 把列撑破时，卡会横向溢出
    const over = JSON.parse(await evalJS(`(function(){
      var out = [];
      [].slice.call(document.querySelectorAll('[data-set-pane="base"] .set-col')).forEach(function(col){
        var cw = col.getBoundingClientRect().width;
        [].slice.call(col.children).forEach(function(el){
          if(el.scrollWidth > Math.ceil(cw) + 1) out.push((el.querySelector('.set-card-head .t')||{}).textContent + ':' + el.scrollWidth + '>' + Math.round(cw));
        });
      });
      return JSON.stringify(out);
    })()`));
    ok(over.length === 0, '没有卡片被内容撑破列宽 → ' + JSON.stringify(over));

    // ---- C. 纯函数：端口解析 ----
    const pp = JSON.parse(await evalJS(`JSON.stringify({
      mixed: ekParsePorts('9000，8888, abc, 70000, 9000'),
      empty: ekParsePorts(''),
      zero:  ekParsePorts('0'),
      many:  ekParsePorts('1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20').length
    })`));
    ok(JSON.stringify(pp.mixed) === '[9000,8888]', '全角逗号/非法项/越界/重复 → [9000,8888] → ' + JSON.stringify(pp.mixed));
    ok(JSON.stringify(pp.empty) === '[]', '留空 = []（退回自动推断，不是 [0]）→ ' + JSON.stringify(pp.empty));
    ok(JSON.stringify(pp.zero) === '[]', '端口 0 被丢掉 → ' + JSON.stringify(pp.zero));
    ok(pp.many === 16, '超长清单截到 16 个 → ' + pp.many);

    // ---- D. 保存：两个新字段真的落盘 ----
    const BACKUP = 'D:\\BCGD\\FarmTrace\\backups-test';
    await evalJS(`(function(){
      document.getElementById('s_backup_dir').value = ${JSON.stringify(BACKUP)};
      document.getElementById('s_scan_ports').value = '9000，8888, abc, 70000, 9000';
      document.getElementById('btn-set-save').click();
    })()`);
    await sleep(2200);
    let saved = null;
    try { saved = JSON.parse(fs.readFileSync(CFG, 'utf8')); } catch (e) {}
    if (!saved) throw new Error('保存后读不到 config.json');
    ok((saved.projects || {}).backup_dir === BACKUP,
      '备份目录落盘 → ' + JSON.stringify((saved.projects || {}).backup_dir));
    ok(JSON.stringify((saved.projects || {}).scan_ports) === '[9000,8888]',
      '端口清单落盘为 [9000,8888] → ' + JSON.stringify((saved.projects || {}).scan_ports));

    const hint = await evalJS(`(document.getElementById('s_backup_now')||{}).textContent`);
    ok(!!hint && hint.indexOf('backups-test') >= 0, '备份提示跟着变成实际路径 → ' + hint);

    // ---- E. 口径：程序配置页不许把备份目录抹掉 ----
    const pb = JSON.parse(await evalJS(`(function(){
      var b = projectsBody();
      return JSON.stringify({ has: Object.prototype.hasOwnProperty.call(b, 'backup_dir'), v: b.backup_dir });
    })()`));
    ok(pb.has, 'projectsBody() 回传了 backup_dir（不回传 = 老页面一保存就抹掉）');
    ok(pb.v === BACKUP, 'projectsBody() 里的值就是刚存的那份 → ' + JSON.stringify(pb.v));

    // ---- F. 本机体检：5 行、非管理员不涂警告色 ----
    const hl = JSON.parse(await evalJS(`(function(){
      ekSetHealth({admin:false, env:{has_results:false}, services:{}, issues:[]});
      var rows = [].slice.call(document.querySelectorAll('#s_health_view .ro-row'));
      return JSON.stringify(rows.map(function(r){
        return { k:r.querySelector('.k').textContent, v:r.querySelector('.v').textContent,
                 cls:r.querySelector('.v').className };
      }));
    })()`));
    ok(hl.length === 5, '体检 5 行 → ' + hl.length);
    ok(hl.length > 0 && hl[0].k === '权限', '第一行是权限 → ' + (hl[0] || {}).k);
    ok(hl.length > 0 && !/warn|bad/.test(hl[0].cls || ''),
      '非管理员不着警告色（上一轮刚把"未以管理员运行"从 WARN 降级）→ ' + JSON.stringify(hl[0] && hl[0].cls));
    ok(hl.length > 1 && hl[1].v === '未检测', '没检测过写「未检测」而不是「未就绪」→ ' + JSON.stringify(hl[1] && hl[1].v));
    ok(hl.length > 4 && hl[4].v === '无' , '无问题时待处理写「无」→ ' + JSON.stringify(hl[4] && hl[4].v));

    // ---- G. 链端空主机：不许露出孤零零的 ":20200" ----
    // 用**构造的值**测显示规则，不走"清空配置再保存"：
    // 后端 mergeFormConfig 在链端字段全空时会故意保留旧值（防止部分提交抹掉
    // 远端配置），所以 POST 根本清不空 chain_host —— 那样测出来的红是假的。
    const ep = JSON.parse(await evalJS(`(function(){
      // ekRo 把空串画成「—」：这里走真实渲染路径，不是自己拼字符串
      var d = document.createElement('div'); d.className = 'ro-list';
      document.body.appendChild(d);
      ekRo(d, [{k:'链节点', v: ekEpText('', 20200)}, {k:'登录', v: ekLoginText('root', '', 22)}]);
      var vs = [].slice.call(d.querySelectorAll('.v')).map(function(x){ return x.textContent; });
      d.remove();
      return JSON.stringify({
        blank: vs[0], loginBlank: vs[1],
        withHost: ekEpText('1.2.3.4', 20200),
        noPort: ekEpText('1.2.3.4', 0),
        login: ekLoginText('root', '1.2.3.4', 22),
        loginNoUser: ekLoginText('', '1.2.3.4', 22)
      });
    })()`));
    ok(ep.blank === '—', '主机为空 → 渲染成「—」而不是 ":20200" → ' + JSON.stringify(ep.blank));
    ok(ep.withHost === '1.2.3.4:20200', '主机+端口 → 1.2.3.4:20200 → ' + JSON.stringify(ep.withHost));
    ok(ep.noPort === '1.2.3.4', '端口为 0 时不留尾冒号 → ' + JSON.stringify(ep.noPort));
    ok(ep.login === 'root@1.2.3.4:22', '登录行 → root@1.2.3.4:22 → ' + JSON.stringify(ep.login));
    ok(ep.loginNoUser === '1.2.3.4:22', '没填用户时不许出现 "@host" → ' + JSON.stringify(ep.loginNoUser));
    ok(ep.loginBlank === '—', '没填主机时登录行也是「—」→ ' + JSON.stringify(ep.loginBlank));

  } catch (e) {
    fails.push('异常：' + e.message);
    console.log('  FAIL 异常：' + e.message);
  } finally {
    try { ws && ws.close(); } catch (e) {}
    try { chrome.kill(); } catch (e) {}
    try { envkit.kill(); } catch (e) {}
  }

  console.log(fails.length ? ('\n失败 ' + fails.length + ' 条：\n - ' + fails.join('\n - ')) : '\n全部通过');
  // 成功即删隔离目录：每个测试都在 DIR 里复制一份 exe 加一个 Chrome profile，
  // 不删就会累积（2026-11-01 实测 tools/ 下 17 个目录共 255MB）。
  // 失败时保留现场 —— 那时 exe / config / 日志本身就是证据。CDP_KEEP=1 强制保留。
  if (fails.length === 0 && !process.env.CDP_KEEP) {
    try { fs.rmSync(DIR, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch (e) {}
  }
  /* cdpCleanupMarker */
  process.exit(fails.length ? 1 : 0);
})();
