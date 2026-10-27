// CDP 回归（Agent 闭环 Phase 2）：行动 tab 的预案库段落 + 行动卡片上的
// 预案徽标 / 起草步骤 / 查看报告。
//
// 用法：
//   1) go build -trimpath -ldflags "-s -w -X main.buildDate=<日期>" -o tools\_p2\EnvKit_p2.exe .
//   2) node tools/cdp_p2_mission_test.js
//
// 三条设计约束（都是踩过的坑）：
//  1. **不碰用户的真实数据**：配置/行动/预案/报告全部写到临时目录（EXE 旁边），
//     独立端口 18799。用户自己那份 missions.json 一个字都不动。
//  2. **不要求用户先关掉 EnvKit**：单实例互斥体是全局的，第二个进程会直接退出。
//     脚本给子进程设 ENVKIT_INSTANCE_TAG 换一个互斥体名，两边并存。
//  3. **先确认测的是 Phase 2 那份构建**：/api/playbooks 若 404 直接判错退出——
//     "评测装置在测旧构建" 是本项目真实踩过的坑，让它一开口就说清楚。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18799;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9341;
const EXE = (function () {
  if (process.env.ENVKIT_EXE) return process.env.ENVKIT_EXE;
  return path.join(REPO, 'tools', '_p2', 'EnvKit_p2.exe');
})();
// **数据目录 = exe 所在目录**（exeDir()），不是 cwd。
// 种子文件必须写这里，否则 EnvKit 读的是 exe 旁边的老数据——这个坑很隐蔽：
// 页面渲染得出来、断言看起来在跑，其实跑的是上一次的种子。
const DIR = path.dirname(EXE);

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

// ---------- 种子数据 ----------
const PLAYBOOKS = [
  {
    id: 'p-1', title: '后端崩溃：查状态 → 重启 → 复验',
    matcher: { services: ['backend'], err_kinds: ['verify_fail'], keywords: ['崩溃', '端口'] },
    steps: [
      { tool: 'get_system_state', why: '先看进程/端口现状，别盲目重启' },
      { tool: 'restart_service', target: 'backend', why: '重启后端' },
    ],
    stats: { used: 3, succ: 2, fail: 1, streak: 0, last_used_at: '2026-10-26 21:10:00' },
    state: 'active', created_at: '2026-10-20T09:00:00+08:00', updated_at: '2026-10-26T21:10:00+08:00',
  },
  {
    id: 'p-2', title: '前端白屏：清缓存 → 重启',
    matcher: { services: ['web'] },
    steps: [{ tool: 'get_system_state', why: '确认前端进程还在不在' }],
    stats: { used: 0, succ: 0, fail: 0, streak: 3 },
    state: 'review', created_at: '2026-10-21T09:00:00+08:00', updated_at: '2026-10-25T09:00:00+08:00',
  },
  {
    id: 'p-3', title: '旧预案：链端重连',
    matcher: { services: ['chain'] },
    steps: [{ tool: 'get_system_state', why: '看链端进程' }],
    stats: { used: 7, succ: 1, fail: 6, streak: 0 },
    state: 'retired', created_at: '2026-10-01T09:00:00+08:00', updated_at: '2026-10-10T09:00:00+08:00',
  },
];
const REPORT = 'cdp-report.md';
const MISSIONS = [
  {
    id: 'm-cdp-done', goal: '今日晨报', state: 'done',
    trigger: { kind: 'schedule', detail: 'briefing_at 08:30' },
    plan: [
      { seq: 1, tool: 'get_system_state', ok: true, evidence: 'web 正常 / backend 正常', at: '2026-10-27 08:30:01' },
      { seq: 2, tool: 'check_port', ok: true, evidence: '17682 监听中', at: '2026-10-27 08:30:02' },
    ],
    report: REPORT,
    result: { action: 'mission', target: 'm-cdp-done', ok: true, verified: true, msg: '晨报已生成：2 项需关注（只读，未改动任何状态）' },
    created_at: '2026-10-27T08:30:00+08:00', updated_at: '2026-10-27T08:30:03+08:00', expires_at: '2026-11-26T08:30:00+08:00',
  },
  {
    id: 'm-cdp-pending', goal: '后端恢复待确认', state: 'pending',
    trigger: { kind: 'guard', detail: 'backend 重启后复验未通过' },
    playbook: 'p-1',
    draft: [
      { tool: 'get_system_state', why: '先看进程/端口现状，别盲目重启' },
      { tool: 'restart_service', target: 'backend', why: '重启后端' },
    ],
    created_at: '2026-10-27T09:00:00+08:00', updated_at: '2026-10-27T09:00:00+08:00', expires_at: '2026-11-26T09:00:00+08:00',
  },
  {
    id: 'm-cdp-review', goal: '前端恢复待确认', state: 'pending',
    trigger: { kind: 'webhook', detail: '外部监控投递' },
    playbook: 'p-2',
    draft: [{ tool: 'get_system_state', why: '确认前端进程还在不在' }],
    created_at: '2026-10-27T09:05:00+08:00', updated_at: '2026-10-27T09:05:00+08:00', expires_at: '2026-11-26T09:05:00+08:00',
  },
];

function seed() {
  const bir = path.join(DIR, 'briefings');
  fs.mkdirSync(bir, { recursive: true });
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
  fs.writeFileSync(path.join(DIR, 'playbooks.json'), JSON.stringify(PLAYBOOKS, null, 1), 'utf8');
  fs.writeFileSync(path.join(DIR, 'missions.json'), JSON.stringify(MISSIONS, null, 1), 'utf8');
  fs.writeFileSync(path.join(bir, REPORT), [
    '# EnvKit 晨报 · 2026-10-27 08:30',
    '',
    '> 本报告由系统按事实聚合生成，未经模型处理。',
    '',
    '## 服务状态',
    '',
    '- web：**未启动**',
    '- backend：**未启动**',
    '',
    '## 近 24 小时',
    '',
    '- 崩溃：0 条',
    '- 熔断：0 次',
    '',
    '（本文件仅用于前端渲染回归，非真实环境结论。）',
  ].join('\n'), 'utf8');
}

(async () => {
  // 安全闸：只在"专属测试构建"旁边写种子。若指向真实 EnvKit.exe，
  // 种子会覆盖用户自己的 config.json / missions.json —— 宁可不跑。
  if (!fs.existsSync(EXE)) {
    console.log('FAIL 找不到测试构建：' + EXE);
    console.log('     go build -trimpath -ldflags "-s -w" -o tools\\_p2\\EnvKit_p2.exe .');
    process.exit(1);
  }
  if (path.basename(EXE).toLowerCase() !== 'envkit_p2.exe') {
    console.log('拒绝在真实数据目录跑（会把种子写进用户配置）：' + EXE);
    console.log('     请构建到 tools\\_p2\\EnvKit_p2.exe，或用 ENVKIT_EXE 指向同名的测试构建');
    process.exit(1);
  }
  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'cdp2' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });

  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_); ready = true; break; } catch (e) { await sleep(500); }
  }
  console.log('exe:', EXE);
  console.log('envkit ready:', ready);
  if (!ready) { try { envkit.kill(); } catch (e) {} process.exit(1); }

  // 先确认这份构建真的含 Phase 2（否则后面所有断言都在测旧包）。
  // 注意：所有 /api/* 都要带本机令牌（防 CSRF），令牌写在首页 HTML 里。
  const home = await getText(URL_);
  const tm = /window\.__EK_TOKEN__="([0-9a-fA-F]+)"/.exec(home);
  let pbkApi = null;
  if (!tm) {
    console.log('FAIL 首页拿不到 API 令牌（EnvKit 没起在对的端口？）');
    try { envkit.kill(); } catch (e) {}
    process.exit(1);
  }
  try { pbkApi = await getJSON(URL_ + 'api/playbooks?t=' + tm[1]); } catch (e) { pbkApi = String(e); }
  if (!pbkApi || pbkApi.ok !== true) {
    console.log('FAIL 这份构建不含 Phase 2（/api/playbooks 不通）：' + JSON.stringify(pbkApi));
    console.log('     先 go build -trimpath -ldflags "-s -w" -o tools\\_p2\\EnvKit_p2.exe . 再跑本脚本');
    try { envkit.kill(); } catch (e) {}
    process.exit(1);
  }
  // Phase 3：成长指标。单独确认端点存在，理由同上——测旧构建的坑踩过。
  let grApi = null;
  try { grApi = await getJSON(URL_ + 'api/growth?t=' + tm[1]); } catch (e) { grApi = String(e); }
  if (!grApi || typeof grApi !== 'object' || !grApi.total) {
    console.log('FAIL 这份构建不含 Phase 3（/api/growth 不通）：' + JSON.stringify(grApi));
    try { envkit.kill(); } catch (e) {}
    process.exit(1);
  }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(DIR, '_chrome'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  let fails = 0;
  const ok = (name, cond, extra) => {
    console.log((cond ? 'PASS ' : 'FAIL ') + name + (extra === undefined ? '' : ' -> ' + JSON.stringify(extra)));
    if (!cond) fails++;
  };

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
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
      if (r.result && r.result.exceptionDetails) return 'EXC:' + r.result.exceptionDetails.text;
      return r.result && r.result.result ? r.result.result.value : JSON.stringify(r.result);
    };

    await send('Runtime.enable', {});
    await send('Page.enable', {});
    // 中文断言依赖 UI 文案，强制中文（浏览器默认语言可能是英文 → t() 全走英文）
    await send('Emulation.setLocaleOverride', { locale: 'zh-CN' });
    await send('Page.navigate', { url: URL_ });
    await sleep(4000);

    const opened = await evalJS(`(function(){ try{ aiSide('missions'); return 'ok'; }catch(e){ return 'ERR:'+e; } })()`);
    ok('打开行动 tab', opened === 'ok', opened);
    await sleep(1500);

    const snap = await evalJS(`(function(){
      const rows = [].slice.call(document.querySelectorAll('#ai-pbk .ai-pbk-row'));
      const ms = [].slice.call(document.querySelectorAll('#ai-ms-list .ai-ms-row'));
      const txt = el => (el ? el.textContent : '');
      return JSON.stringify({
        pbkCount: rows.length,
        pbkTitles: rows.map(r => txt(r.querySelector('.ai-pbk-title'))),
        pbkMeta: rows.map(r => txt(r.querySelector('.ai-pbk-meta'))),
        pbkCls: rows.map(r => r.className),
        pbkBtns: rows.map(r => [].slice.call(r.querySelectorAll('button')).map(b => b.textContent)),
        pbkSec: !!document.getElementById('ai-pbk'),
        secHeader: txt(document.querySelector('.ai-ms-sec')),
        msCount: ms.length,
        msTexts: ms.map(x => txt(x)),
        msBtns: ms.map(x => [].slice.call(x.querySelectorAll('button')).map(b => b.textContent)),
        msPbCls: ms.map(x => { const p = x.querySelector('.ai-ms-pb'); return p ? p.className : ''; }),
        grCards: [].slice.call(document.querySelectorAll('#ai-growth .ai-gr-card')).map(c =>
          txt(c.querySelector('.ai-gr-k')) + '=' + txt(c.querySelector('.ai-gr-v'))),
        grTr: [].slice.call(document.querySelectorAll('#ai-growth .ai-gr-tr')).length,
        grSum: txt(document.querySelector('#ai-growth .ai-gr-sum')),
        grNote: txt(document.querySelector('#ai-growth .ai-gr-note')),
        grSec: txt([].slice.call(document.querySelectorAll('.ai-ms-sec')).find(x => /成长/.test(x.textContent)))
      });
    })()`);
    const s = JSON.parse(snap);
    console.log('SNAP ' + snap);

    ok('预案库段落存在', s.pbkSec === true);
    ok('段落标题是「预案库」', /预案库/.test(s.secHeader), s.secHeader);
    ok('预案 3 条', s.pbkCount === 3, s.pbkCount);
    ok('预案标题渲染', s.pbkTitles[0] === '后端崩溃：查状态 → 重启 → 复验', s.pbkTitles);
    ok('统计按事实渲染（用过 3 次 / 未使用）',
      /用过 3 次/.test(s.pbkMeta[0]) && /未使用/.test(s.pbkMeta[1]), s.pbkMeta);
    ok('待复审标黄', /\bwarn\b/.test(s.pbkCls[1]), s.pbkCls[1]);
    ok('已停用置灰且给「启用」', /\bdim\b/.test(s.pbkCls[2]) && (s.pbkBtns[2] || []).indexOf('启用') >= 0, s.pbkBtns[2]);

    ok('行动 3 条', s.msCount === 3, s.msCount);
    // 列表按 updated_at 倒序，不按种子顺序 —— 断言一律按内容定位。
    // 用下标写死会在「排序规则一改」时给出假失败，而假失败比没有断言更费时间。
    const rowIdxByName = name => s.msTexts.findIndex(x => x.indexOf(name) >= 0);
    const iBrief = rowIdxByName('今日晨报');
    const iPending = rowIdxByName('后端恢复待确认');
    const iReview = rowIdxByName('前端恢复待确认');
    ok('三条行动都渲染出来', iBrief >= 0 && iPending >= 0 && iReview >= 0, [iBrief, iPending, iReview]);

    ok('行动卡片显示命中预案', /预案：后端崩溃/.test(s.msTexts[iPending] || ''), s.msTexts[iPending]);
    ok('起草步骤列出（含【写操作】标记）',
      /预案步骤（未执行，需你批准）/.test(s.msTexts[iPending] || '') && /【写操作】/.test(s.msTexts[iPending] || ''),
      s.msTexts[iPending]);
    ok('待复审预案的行动卡片标黄', /\bwarn\b/.test(s.msPbCls[iReview] || ''), s.msPbCls[iReview]);
    ok('晨报行动有「查看报告」', (s.msBtns[iBrief] || []).indexOf('查看报告') >= 0, s.msBtns[iBrief]);
    ok('已收口且有步骤的行动给「存为预案」', (s.msBtns[iBrief] || []).indexOf('存为预案') >= 0, s.msBtns[iBrief]);

    const clicked = await evalJS(`(function(){
      const rows = [].slice.call(document.querySelectorAll('#ai-ms-list .ai-ms-row'));
      const row = rows.find(r => r.textContent.indexOf('今日晨报') >= 0);
      if (!row) return 'no-row';
      const b = [].slice.call(row.querySelectorAll('button')).find(x => x.textContent === '查看报告');
      if (!b) return 'no-btn';
      b.click();
      return 'clicked';
    })()`);
    ok('点击查看报告', clicked === 'clicked', clicked);
    await sleep(900);

    const modal = await evalJS(`(function(){
      const c = document.getElementById('confirm');
      const msg = document.getElementById('cf-msg');
      return JSON.stringify({
        shown: c ? c.classList.contains('show') : false,
        mono: c ? c.querySelector('.mbox').classList.contains('mono') : false,
        title: (document.getElementById('cf-title') || {}).textContent || '',
        okText: (document.getElementById('cf-ok') || {}).textContent || '',
        len: msg ? msg.textContent.length : 0,
        head: msg ? msg.textContent.slice(0, 40) : '',
      });
    })()`);
    console.log('MODAL ' + modal);
    const md = JSON.parse(modal);
    ok('报告弹窗打开', md.shown === true);
    ok('报告正文是等宽全文', md.mono === true && md.len > 100, md.len);
    ok('弹窗标题带行动目标', /今日晨报/.test(md.title), md.title);
    ok('下载按钮存在', /下载/.test(md.okText), md.okText);

    await evalJS(`(function(){ closeConfirm(false); return 1; })()`);
    await sleep(400);

    // ---- Phase 3：成长指标 ----
    // 种子只有 1 条终态行动（今日晨报 done，plan 全成功），所以：
    //   首次解决率 = 100%，预案命中率/有效率分母为 0 → 必须显示「—」。
    // 这两条正好覆盖 rateNA 的两个分支。**"分母为 0 显示 — 而不是 0%"
    // 是这一块最容易做错的地方**：写成 0% 会让界面看起来像"它总失败"。
    ok('成长段落存在', /成长/.test(s.grSec || ''), s.grSec);
    ok('五张指标卡', (s.grCards || []).length === 5, s.grCards);
    ok('首次解决率 100%（1 条 done 且无失败步骤）',
      (s.grCards || []).some(x => /首次解决率=100%/.test(x)), s.grCards);
    ok('分母为 0 的比率显示「—」而不是 0%',
      (s.grCards || []).some(x => /预案有效率=—/.test(x)) &&
      !(s.grCards || []).some(x => /预案有效率=0/.test(x)), s.grCards);
    ok('汇总行写清各桶', /1 条收口行动/.test(s.grSum || ''), s.grSum);
    ok('周表渲染（表头 + 1 数据行）', s.grTr === 2, s.grTr);
    ok('小样本如实提示', /样本|波动/.test(s.grNote || ''), s.grNote);
    ok('API 层用 -1 表示分母为 0', !!(grApi.rates) && grApi.rates.pbk_ok_rate === -1,
      grApi.rates && grApi.rates.pbk_ok_rate);
    ok('API 层各桶之和等于总数',
      grApi.total.missions === (grApi.total.done + grApi.total.failed +
        grApi.total.canceled + grApi.total.expired), grApi.total);

    ok('无未捕获 JS 异常', errs.length === 0, errs.slice(0, 3));
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
