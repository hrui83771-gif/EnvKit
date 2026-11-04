/* ---------- 行动（Mission）：Agent 闭环 Phase 1 ----------
   数据源 GET /api/missions；实时 /api/missions/events SSE（EventSource 自动重连，
   「刷新」兜底）。状态徽章**只显示服务端字段**——前端不重新判定成败（红线 1），
   与 tool_result/verify_result 徽章同一哲学。 */
const AI_MS_STATE = {
  pending:'待执行', awaiting_confirm:'待确认', running:'执行中', paused:'待你继续',
  done:'完成', failed:'失败', canceled:'已取消', expired:'已过期'
};
const AI_MS_CLS = {
  pending:'', awaiting_confirm:'warn', running:'run', paused:'warn',
  done:'ok', failed:'bad', canceled:'dim', expired:'dim'
};
const AI_MS_TRIGGER = {manual:'手动', patrol:'巡检', schedule:'晨报', webhook:'入站', guard:'守护'};
// 终态集合与渲染时的 open 判断同源，抽出来避免两处各写一遍（漏一个就会
// 让"已取消"的行动也冒出「存为预案」）。
const AI_MS_FINAL = ['done','failed','canceled','expired'];

function aiMissionsShell(){
  const box = $ai('ai-pane-missions'); if(!box) return null;
  if(!box.dataset.shell){
    box.dataset.shell = '1';
    box.innerHTML =
      '<div class="pbar">' +
        '<input id="ai-ms-goal" placeholder="' + esc(t('描述要让助手做什么…')) + '">' +
        '<button id="ai-ms-new">' + esc(t('新建行动')) + '</button>' +
        '<button id="ai-ms-re">' + esc(t('刷新')) + '</button>' +
      '</div>' +
      '<div id="ai-ms-list"></div>' +
      '<div class="ai-ms-sec"><span>' + esc(t('预案库')) + '</span>' +
        '<button id="ai-pbk-re">' + esc(t('刷新')) + '</button></div>' +
      '<div id="ai-pbk"></div>' +
      '<div class="ai-ms-sec"><span>' + esc(t('成长')) + '</span>' +
        '<button id="ai-gr-re">' + esc(t('刷新')) + '</button></div>' +
      '<div id="ai-growth"></div>';
    $ai('ai-ms-new').onclick = aiMissionsCreate;
    $ai('ai-ms-re').onclick = ()=>aiMissionsLoad(true);
    $ai('ai-pbk-re').onclick = ()=>aiPbkLoad(true);
    $ai('ai-gr-re').onclick = ()=>aiGrowthLoad(true);
    $ai('ai-ms-goal').addEventListener('keydown', e=>{ if(e.key === 'Enter') aiMissionsCreate(); });
  }
  return box;
}

function aiMissionsLoad(force){
  const box = aiMissionsShell(); if(!box) return;
  aiMissionsSubscribe();
  aiPbkLoad(force);
  aiGrowthLoad(force);
  const list = $ai('ai-ms-list');
  if(list && aiMissionRows == null) list.textContent = t('读取行动…');
  if(aiMissionRows && !force){ aiMissionsRender(); return; }
  fetch('/api/missions').then(r=>r.json()).then(d=>{
    aiMissionRows = (d && d.missions) || [];
    aiMissionsRender();
  }).catch(()=>{ if(list) list.textContent = t('读取失败'); });
}

function aiMissionsSubscribe(){
  if(aiMissionES) return;
  try{
    aiMissionES = new EventSource('/api/missions/events?t=' + encodeURIComponent(window.__EK_TOKEN__));
    aiMissionES.onmessage = e=>{
      try{
        const j = JSON.parse(e.data);
        if(j && j.type === 'missions'){
          aiMissionRows = j.missions || [];
          if(aiSideTab === 'missions') aiMissionsRender();
        }
      }catch(_){}
    };
    // onerror 不处理：EventSource 自动重连；连不上时靠「刷新」按钮兜底
  }catch(_){ aiMissionES = null; }
}

function aiMissionsRender(){
  const list = $ai('ai-ms-list'); if(!list) return;
  list.textContent = '';
  const rows = aiMissionRows || [];
  if(!rows.length){
    const p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('暂无行动。可在下方新建，或由巡检/入站触发自动产出。');
    list.appendChild(p);
    return;
  }
  rows.forEach(m=>{
    const row = document.createElement('div');
    row.className = 'ai-ms-row' + (AI_MS_CLS[m.state] ? ' ' + AI_MS_CLS[m.state] : '');
    const main = document.createElement('div'); main.className = 'ai-ms-main';

    const head = document.createElement('div'); head.className = 'ai-ms-head';
    const badge = document.createElement('span');
    badge.className = 'ai-ms-badge';
    badge.textContent = t(AI_MS_STATE[m.state] || m.state);
    head.appendChild(badge);
    const kind = document.createElement('span'); kind.className = 'ai-ms-kind muted';
    kind.textContent = t((AI_MS_TRIGGER[(m.trigger||{}).kind]) || (m.trigger||{}).kind || '');
    head.appendChild(kind);
    const at = document.createElement('span'); at.className = 'ai-ms-at muted';
    const ts = Date.parse(String(m.updated_at || '').replace(' ', 'T'));
    at.textContent = isFinite(ts) ? new Date(ts).toLocaleTimeString() : '';
    head.appendChild(at);
    main.appendChild(head);

    const goal = document.createElement('div'); goal.className = 'ai-ms-goal';
    goal.textContent = m.goal || '';
    main.appendChild(goal);

    // 预案（Phase 2）：命中的预案标题 + 起草步骤。全部来自服务端字段，
    // 前端不推断"这步会不会改状态"——写标记取自预案视图的 step_views。
    if(m.playbook){
      const pbv = (aiPbkRows || []).find(x=>x.id === m.playbook);
      const pb = document.createElement('div'); pb.className = 'ai-ms-pb';
      pb.textContent = t('预案：{0}', aiPbkTitle(m.playbook));
      if(pbv && pbv.state !== 'active') pb.classList.add('warn');
      main.appendChild(pb);
    }
    if((m.draft || []).length){
      const pbv = (aiPbkRows || []).find(x=>x.id === m.playbook);
      const dl = document.createElement('div'); dl.className = 'ai-ms-draft';
      const cap = document.createElement('div'); cap.className = 'muted';
      cap.textContent = t('预案步骤（未执行，需你批准）');
      dl.appendChild(cap);
      (m.draft || []).forEach((st,i)=>{
        const w = pbv && pbv.step_views && pbv.step_views[i] && pbv.step_views[i].write;
        const li = document.createElement('div');
        li.textContent = (i+1) + '. ' + st.tool + (st.target ? '（' + st.target + '）' : '') +
          (st.why ? ' · ' + st.why : '') + (w ? ' ' + t('【写操作】') : '');
        dl.appendChild(li);
      });
      main.appendChild(dl);
    }

    // 说明行：暂停原因 / 终态结论 / 断点——全部是服务端原文，esc 由 textContent 保证
    let note = '';
    if(m.state === 'paused' || m.state === 'awaiting_confirm') note = m.pause_why || '';
    else if(m.result && m.result.msg) note = (m.result.verified ? '✓ ' : '✗ ') + m.result.msg;
    if(note){
      const n = document.createElement('div'); n.className = 'ai-ms-note muted';
      n.textContent = note;
      main.appendChild(n);
    }
    row.appendChild(main);

    const acts = document.createElement('div'); acts.className = 'ai-ms-acts';
    const canRun = ['pending','paused','awaiting_confirm'].indexOf(m.state) >= 0;
    const isRun = m.state === 'running';
    const open = AI_MS_FINAL.indexOf(m.state) < 0;
    // 报告（Phase 2 晨报）：只看/下载，不影响行动状态
    if(m.report){
      const b = document.createElement('button');
      b.textContent = t('查看报告');
      b.onclick = ()=>aiMsReport(m);
      acts.appendChild(b);
    }
    if(canRun){
      const b = document.createElement('button');
      b.textContent = t('继续'); b.title = t('继续执行该行动');
      b.onclick = ()=>aiMissionsResume(m);
      acts.appendChild(b);
    }
    if(isRun){
      const b = document.createElement('button');
      b.textContent = t('暂停');
      b.onclick = ()=>aiMissionsOp('pause', m.id);
      acts.appendChild(b);
    }
    if(open){
      const c = document.createElement('button');
      c.textContent = t('取消'); c.className = 'danger';
      c.onclick = ()=>aiMissionsOp('cancel', m.id);
      acts.appendChild(c);
    }
    // 沉淀（Phase 2）：只有真跑过（有已完成步骤）的终态行动才值得存成预案。
    // 未收口的也可存 —— 但那样的"步骤"可能是错的，不如等它收口。
    if(AI_MS_FINAL.indexOf(m.state) >= 0 && (m.plan || []).length){
      const b = document.createElement('button');
      b.textContent = t('存为预案');
      b.title = t('把这次的步骤序列存成可复用预案');
      b.onclick = ()=>aiMsSavePbk(m);
      acts.appendChild(b);
    }
    row.appendChild(acts);
    list.appendChild(row);
  });
}

/* ---------- 报告（晨报）查看与下载 ----------
   报告正文是 Markdown（表格、标题、列表），在这里按 Markdown 渲染：
   纯文本展示会把表格压成一堆竖线，读者得自己在脑子里排版。
   渲染走 aiMd 的同一条白名单净化链路——报告里可能带主机名、日志片段等
   不可信文本，不能直接 innerHTML。下载下来的仍是原始 .md。 */
async function aiMsReport(m){
  let txt = '';
  try{
    const r = await fetch('/api/missions/report?id=' + encodeURIComponent(m.id));
    if(!r.ok){
      const j = await r.json().catch(()=>({}));
      toast((j && j.error) || t('读取失败'), 'err');
      return;
    }
    txt = await r.text();
  }catch(_){ toast(t('读取失败'), 'err'); return; }
  const want = await askConfirm(t('报告：{0}', m.goal || ''), aiMd(txt),
    t('下载 .md'), false, t('关闭'), {wide:true, html:true});
  if(want) aiMsDownload(m);
}

async function aiMsDownload(m){
  try{
    const r = await fetch('/api/missions/report?id=' + encodeURIComponent(m.id) + '&download=1');
    if(!r.ok){ toast(t('下载失败'), 'err'); return; }
    const blob = await r.blob();
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = 'envkit-' + (m.report || 'report.md');
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(()=>{ try{ URL.revokeObjectURL(a.href); }catch(_){} }, 4000);
  }catch(_){ toast(t('下载失败'), 'err'); }
}

async function aiMsSavePbk(m){
  try{
    const r = await fetch('/api/playbooks', {method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({op:'save_mission', mission_id: m.id})});
    const d = await r.json();
    if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
    toast(t('已存为预案：{0}', (d.playbook && d.playbook.title) || ''));
    aiPbkLoad(true);
  }catch(_){ toast(t('操作失败'), 'err'); }
}

/* ---------- 预案库（Phase 2） ----------
   入口只有一个：行动 tab 下方。不单开 tab —— 预案是行动的副产品，
   离开"这次干了什么"单独去看预案，用户很难判断它靠不靠得住。
   （缓存 aiPbkRows 的声明在文件顶部，见那里的 TDZ 说明。） */

function aiPbkLoad(force){
  if(aiPbkRows && !force){ aiPbkRender(); return; }
  fetch('/api/playbooks').then(r=>r.json()).then(d=>{
    aiPbkRows = (d && d.playbooks) || [];
    aiPbkRender();
    // 预案标题要显示在行动卡片上：拿到数据后补一次渲染
    if(aiSideTab === 'missions') aiMissionsRender();
  }).catch(()=>{ aiPbkRows = []; aiPbkRender(); });
}

function aiPbkTitle(id){
  const p = (aiPbkRows || []).find(x=>x.id === id);
  return p ? p.title : id;
}

const AI_PBK_STATE = {active:'可用', review:'待复审', retired:'已停用'};

function aiPbkRender(){
  const box = $ai('ai-pbk'); if(!box) return;
  box.textContent = '';
  const rows = aiPbkRows || [];
  if(!rows.length){
    const p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('还没有预案。行动收口后在卡片上点「存为预案」即可沉淀。');
    box.appendChild(p);
    return;
  }
  rows.forEach(p=>{
    const row = document.createElement('div');
    row.className = 'ai-pbk-row' + (p.state === 'review' ? ' warn' : (p.state === 'retired' ? ' dim' : ''));
    const main = document.createElement('div'); main.className = 'ai-pbk-main';
    const ttl = document.createElement('div'); ttl.className = 'ai-pbk-title';
    ttl.textContent = p.title || p.id;
    main.appendChild(ttl);
    // 统计只说事实：用了几次、几次复验通过。没跑过就明说"未使用"，
    // 不写成 0% —— 0% 看起来像"它总失败"。
    const meta = document.createElement('div'); meta.className = 'muted ai-pbk-meta';
    const used = (p.stats && p.stats.used) || 0;
    meta.textContent = t(AI_PBK_STATE[p.state] || p.state) +
      ' · ' + t('步骤 {0}', (p.steps || []).length) +
      ' · ' + (used ? t('用过 {0} 次，复验通过 {1} 次（{2}%）', used, p.stats.succ || 0, p.rate)
                   : t('未使用'));
    main.appendChild(meta);
    row.appendChild(main);

    const acts = document.createElement('div'); acts.className = 'ai-ms-acts';
    if(p.state === 'retired'){
      const b = document.createElement('button');
      b.textContent = t('启用'); b.onclick = ()=>aiPbkOp(p.id, 'set_state', 'active');
      acts.appendChild(b);
    } else {
      const b = document.createElement('button');
      b.textContent = t('停用'); b.onclick = ()=>aiPbkOp(p.id, 'set_state', 'retired');
      acts.appendChild(b);
    }
    const d = document.createElement('button');
    d.textContent = t('删除'); d.className = 'danger';
    d.onclick = ()=>aiPbkDel(p);
    acts.appendChild(d);
    row.appendChild(acts);
    box.appendChild(row);
  });
}

async function aiPbkOp(id, op, state){
  try{
    const r = await fetch('/api/playbooks', {method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({op: op, id: id, state: state})});
    const d = await r.json();
    if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
    aiPbkLoad(true);
  }catch(_){ toast(t('操作失败'), 'err'); }
}

async function aiPbkDel(p){
  // 确认框本身出异常时不能静默：原来这里没有 try 包住 askConfirm，
  // 一旦弹窗层坏掉（例如被 AI 侧栏压住点不到，见 app.css .modal 的注释），
  // 表现就是"点了删除什么都没发生"，连日志都没有。宁可弹原生 confirm 兜底。
  let yes = false;
  try{
    yes = await askConfirm(t('删除预案'), t('确定删除「{0}」？历史统计会一起丢掉。', p.title), t('删除'), true);
  }catch(_){
    yes = window.confirm(t('确定删除「{0}」？历史统计会一起丢掉。', p.title));
  }
  if(!yes) return;
  try{
    const r = await fetch('/api/playbooks', {method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({op:'delete', id: p.id})});
    const d = await r.json();
    if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
    toast(t('已删除预案「{0}」', p.title));
    aiPbkLoad(true);
  }catch(_){ toast(t('操作失败'), 'err'); }
}

/* ---------- 成长指标（Phase 3） ----------
   这是**报表**，不是实时数字：只在打开行动 tab 或点「刷新」时重算，
   并把「数据截至」写在区块底部。

   刻意不跟着 SSE 自动跳动：一场处置刚落一条，所有比率都会跳一格，
   而这种跳动会引诱人盯着 1/1 = 100% 下结论。报表的诚实来自
   「说得清分子分母」，不来自「变得快」。 */

function aiGrowthLoad(force){
  if(aiGrowthData && !force){ aiGrowthRender(); return; }
  const box = $ai('ai-growth');
  if(box && !aiGrowthData) box.textContent = t('读取中…');
  fetch('/api/growth').then(r=>r.json()).then(d=>{
    aiGrowthData = d || null;
    aiGrowthRender();
  }).catch(()=>{
    aiGrowthData = null;
    if(!box) return;
    box.textContent = '';
    const p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('读取失败');
    box.appendChild(p);
  });
}

// 服务端用 -1 表示"分母为 0"（rateNA）。这里必须显示成「—」而不是 0%：
// 0% 会被读成"一次都没成"，那是另一回事。
function aiRateText(v, suffix){
  if(v == null || v < 0) return '—';
  return v + (suffix == null ? '%' : suffix);
}

function aiGrowthRender(){
  const box = $ai('ai-growth'); if(!box) return;
  box.textContent = '';
  const d = aiGrowthData;
  if(!d){
    const p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('暂无数据');
    box.appendChild(p);
    return;
  }
  const rates = d.rates || {};
  const cards = [
    [t('首次解决率'), aiRateText(rates.first_try_rate)],
    [t('预案命中率'), aiRateText(rates.pbk_hit_rate)],
    [t('预案有效率'), aiRateText(rates.pbk_ok_rate)],
    [t('用户修正率'), aiRateText(rates.user_fix_rate)],
    [t('平均步数'), aiRateText(rates.avg_steps, '')]
  ];
  const grid = document.createElement('div'); grid.className = 'ai-gr-cards';
  cards.forEach(c=>{
    const cell = document.createElement('div'); cell.className = 'ai-gr-card';
    const k = document.createElement('div'); k.className = 'ai-gr-k muted';
    k.textContent = c[0];
    const v = document.createElement('div'); v.className = 'ai-gr-v';
    v.textContent = c[1];
    cell.appendChild(k); cell.appendChild(v);
    grid.appendChild(cell);
  });
  box.appendChild(grid);

  const tot = d.total || {};
  // 各桶必须能加回总数——加不回去就是口径出了问题，界面上直接露出来
  const sum = document.createElement('div'); sum.className = 'muted ai-gr-sum';
  sum.textContent = t('近 {0} 天：{1} 条收口行动 · 完成 {2} · 失败 {3} · 取消 {4} · 过期 {5}',
    d.days, tot.missions || 0, tot.done || 0, tot.failed || 0, tot.canceled || 0, tot.expired || 0);
  box.appendChild(sum);

  const weeks = d.weeks || [];
  if(weeks.length){
    const tb = document.createElement('div'); tb.className = 'ai-gr-table';
    const head = document.createElement('div'); head.className = 'ai-gr-tr ai-gr-th';
    [t('周'), t('行动'), t('完成'), t('首次'), t('步数')].forEach(x=>{
      const c = document.createElement('div'); c.textContent = x; head.appendChild(c);
    });
    tb.appendChild(head);
    weeks.forEach(w=>{
      const tr = document.createElement('div'); tr.className = 'ai-gr-tr';
      [w.key, w.missions, w.done, w.first_try, w.steps].forEach(x=>{
        const c = document.createElement('div'); c.textContent = String(x); tr.appendChild(c);
      });
      tb.appendChild(tr);
    });
    box.appendChild(tb);
  }

  if(d.note){
    const n = document.createElement('div'); n.className = 'muted ai-gr-note';
    n.textContent = d.note;
    box.appendChild(n);
  }
  if(d.to){
    const at = document.createElement('div'); at.className = 'muted ai-gr-note';
    at.textContent = t('数据截至 {0}', d.to);
    box.appendChild(at);
  }
}

function aiMissionsOp(op, id){
  // 正在跑的行动被暂停/取消：先掐断流，让服务端 settle 按中断收口（落 paused）
  if(aiCurMission === id && aiCtrl){ try{ aiCtrl.abort(); }catch(_){} }
  fetch('/api/missions', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({op: op, id: id})})
    .then(r=>r.json()).then(d=>{
      if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
      // 本地立刻更新（SSE 会再推一次全量，两相对齐）
      if(aiMissionRows){
        const i = aiMissionRows.findIndex(x=>x.id === id);
        if(i >= 0 && d.mission) aiMissionRows[i] = d.mission;
        aiMissionsRender();
      }
      if(op === 'cancel' && aiCurMission === id) aiCurMission = null;
    }).catch(()=>toast(t('读取失败'), 'err'));
}

function aiMissionsResume(m){
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  aiSide('trace');
  // 绑定交给 aiSendText 第二参；消息内容只是"继续"，
  // 真正的上下文（目标/已完成步骤/断点）由服务端注入 system（定案 3）
  aiSendText(t('继续执行该行动'), m.id);
}

function aiMissionsCreate(){
  const input = $ai('ai-ms-goal');
  const goal = String((input && input.value) || '').trim();
  if(!goal){ if(input) input.focus(); return; }
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  fetch('/api/missions', {method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({op:'create', goal: goal})})
    .then(r=>r.json()).then(d=>{
      if(!d || !d.ok){ toast((d && d.error) || t('操作失败'), 'err'); return; }
      if(input) input.value = '';
      // 手动新建 = 立即走 AI 对话通道执行（设计 2.3）
      aiSide('trace');
      aiSendText(goal, d.mission && d.mission.id);
    }).catch(()=>toast(t('读取失败'), 'err'));
}

/* ---------- 文件引用：只产出路径，不读内容 ---------- */
function aiChipsRender(){
  const box = $ai('ai-chips'); if(!box) return;
  box.textContent = '';
  box.hidden = !aiFiles.length;
  aiFiles.forEach((f, i)=>{
    const c = document.createElement('span'); c.className = 'ai-chip';
    const b = document.createElement('b'); b.textContent = f.name; b.title = f.path;
    const x = document.createElement('i'); x.textContent = '×'; x.title = t('移除');
    x.onclick = ()=>{ aiFiles.splice(i, 1); aiChipsRender(); };
    c.appendChild(b); c.appendChild(x);
    box.appendChild(c);
  });
}
function aiFileRef(){
  if(!aiFiles.length) return '';
  const lines = aiFiles.map(f=>'- ' + f.path).join('\n');
  aiFiles = []; aiChipsRender();
  return t('【附加文件引用】') + '\n' + lines + '\n' + t('（需要内容时用 read_file 读取；沙箱外路径会被拒绝）') + '\n\n';
}
function aiJoin(dir, name){
  if(!dir) return name;
  return (dir.slice(-1) === '\\' || dir.slice(-1) === '/') ? dir + name : dir + '\\' + name;
}
function aiParent(p){
  if(!p) return '';
  let s = p.replace(/[\\\/]+$/, '');
  const i = Math.max(s.lastIndexOf('\\'), s.lastIndexOf('/'));
  if(i < 0) return '';
  const up = s.slice(0, i);
  return /^[A-Za-z]:$/.test(up) ? up + '\\' : up;
}
function aiPickShow(on){
  const pop = $ai('ai-pick'); if(!pop) return;
  pop.classList.toggle('show', !!on);
  const b = $ai('ai-plus'); if(b) b.classList.toggle('on', !!on);
}
function aiPickClose(){ aiPickShow(false); }
function aiPickOpen(){
  aiPickShow(true);
  let p = '';
  try{ if(cfg && cfg.projects) p = cfg.projects.frontend_dir || cfg.projects.backend_dir || ''; }catch(_){}
  aiPickLs(p);
}
async function aiPickLs(p){
  const list = $ai('ai-pick-list'); if(!list) return;
  aiPickPath = p || '';
  const pv = $ai('ai-pick-path'); if(pv) pv.value = aiPickPath || t('驱动器');
  list.textContent = t('读取中…');
  let j = null;
  try{ j = await (await fetch('/api/ls?mode=file&path=' + encodeURIComponent(aiPickPath))).json(); }
  catch(e){ list.textContent = t('读取失败'); return; }
  list.textContent = '';
  const add = (ic, name, fn, title, cls)=>{
    const d = document.createElement('div'); d.className = 'ai-pick-item';
    if(cls) d.classList.add(cls);
    const i = document.createElement('i'); i.className = 'ic'; i.textContent = ic;
    const s = document.createElement('span'); s.textContent = name; if(title) s.title = title;
    d.appendChild(i); d.appendChild(s); d.onclick = fn;
    list.appendChild(d);
  };
  if(j.error){ const e = document.createElement('div'); e.className = 'muted'; e.style.padding = '6px'; e.textContent = j.error; list.appendChild(e); }
  if(!aiPickPath){
    (j.drives || []).forEach(dr=>add('▣', dr, ()=>aiPickLs(dr)));
    return;
  }
  if(j.parent) add('↑', t('上级目录'), ()=>aiPickLs(j.parent));
  (j.dirs || []).forEach(n=>add('▸', n, ()=>aiPickLs(aiJoin(aiPickPath, n))));
  // 文件图标先告诉用户"这个能不能读"：文档类给实心块并按格式上色，
  // 文本类给一个小点。选中后的差别也在这儿 —— 文档走后端抽取，文本走路径引用。
  (j.files || []).forEach(n=>{
    const k = aiDocExt(n);
    add(k ? '▤' : '·', n, ()=>aiPickAdd(aiJoin(aiPickPath, n), n), null, k ? ('f-' + k) : 'f-txt');
  });
  if(!(j.dirs || []).length && !(j.files || []).length){
    const e = document.createElement('div'); e.className = 'muted'; e.style.padding = '6px'; e.textContent = t('这个目录是空的'); list.appendChild(e);
  }
}
function aiPickAdd(p, name){
  const nm = name || p.split(/[\\\/]/).pop();
  if(aiFiles.some(f=>f.path === p) || aiDocs.some(f=>f.path === p)){ toast(t('已经加过了')); return; }
  aiPickClose();
  // 文档类走后端抽取，产出的卡片跟拖进来的一模一样。
  // 为什么必须分流：read_file 是按文本读的，头 512 字节判出二进制就直接
  // 「该文件疑似二进制，已跳过」—— 对着 docx 点了一路，模型什么都看不到。
  // 其余文本文件仍走"路径引用"：让 AI 自己按需分页读，
  // 一份 10MB 的日志不会被一次性截断到只有开头。
  if(aiDocExt(nm)){
    aiDocRun({name: nm, path: p}, ()=>aiExtractPost({path: p, name: nm}));
    return;
  }
  aiFiles.push({path: p, name: nm});
  aiChipsRender();
  toast(t('已附加引用：{0}', nm));
}
/* ==================== 图片附件：拖入 / 粘贴 → 载入 ====================
   DeepSeek 官方 deepseek-flash 已原生支持视觉，这里把那条件打通：
   拖到对话区、拖到桌宠、或在输入框里直接 Ctrl+V 粘贴截图，都能载入。

   **图片不内联进消息**，而是先落盘（POST /api/ai/attach）拿一个 id，
   消息里只留 {id,name,mime}。三条硬理由见 ai_attach.go 文件头：
   请求体 16MB 上限、历史每轮整体重发、localStorage 只有 5MB。

   为什么要在前端先压缩：一张 4032×3024 的手机照片 5MB，base64 之后 6.7MB，
   而 DeepSeek 会把图片缩到等效约 1300×1300 再算 token —— 上传原图是纯浪费带宽。
   长边 1600 是"缩完仍比上游的目标尺寸大一点"的取值，文字截图不会糊。 */
const AI_IMG_IN = /^image\/(png|jpeg|jpg|gif|webp)$/i;
const AI_IMG_MAX = 6;      // 单条消息最多几张（一张 ≈ 1024 token，6 张已经很奢侈）
const AI_IMG_EDGE = 1600;  // 长边上限
const AI_IMG_RAW_KEEP = 2 * 1024 * 1024; // 已够小且不超这个体积就原样上传（无损）

function aiAttachToggle(){
  const box = $ai('ai-attach');
  // 图片与文档共用一个待发送区：只有两类都空时才收起
  if(box) box.hidden = (aiImgs.length === 0 && aiDocs.length === 0);
}

/* 文档卡片的外壳（待发送区与消息气泡共用一套）。
   kind 只当角标显示（PDF/DOCX/…），不做任何解析 —— 真正的类型判定在后端按魔数做，
   这里显示的只是"后端认为这是什么"。 */
function aiDocChip(it, onRemove){
  const d = document.createElement('div');
  const kind = String(it.kind || '').toLowerCase();
  // 格式色挂在 data-k 上：CSS 给 pdf/docx/xlsx/pptx 各一种品牌色，纯文本给灰。
  // 只是视觉区分 —— 类型判定仍然只在后端按魔数做，这里显示的是"后端认为这是什么"。
  d.dataset.k = kind || 'doc';
  d.className = 'ai-doc' + (it.busy ? ' busy' : '') + (it.err ? ' err' : '');
  const badge = document.createElement('span');
  badge.className = 'badge';
  badge.textContent = it.err ? '!' : (it.busy ? '…' : (kind ? kind.toUpperCase() : 'DOC'));
  const nm = document.createElement('span'); nm.className = 'nm';
  nm.textContent = it.name || t('文件');
  const mt = document.createElement('span'); mt.className = 'mt';
  mt.textContent = it.busy ? t('读取中') : (it.chars ? t('{0} 字', it.chars) : '');
  d.appendChild(badge); d.appendChild(nm); d.appendChild(mt);
  // 悬浮给"人话"：名字可能被省略号截断，字数是"读进来多少"的唯一凭据
  d.title = it.err ? (it.name + ' — ' + it.err)
    : [it.name, it.busy ? t('正在读取…') : t('{0} 字', it.chars || 0), it.note || ''].filter(Boolean).join(' · ');
  if(onRemove){
    const rm = document.createElement('button');
    rm.className = 'rm'; rm.type = 'button'; rm.textContent = '×'; rm.title = t('移除');
    rm.onclick = onRemove;
    d.appendChild(rm);
  }
  return d;
}

function aiAttachRender(){
  const box = $ai('ai-attach');
  if(!box) return;
  box.textContent = '';
  aiImgs.forEach((it, i)=>{
    const d = document.createElement('div');
    d.className = 'thumb' + (it.busy ? ' busy' : '') + (it.err ? ' err' : '');
    d.title = it.err ? (it.err + ' — ' + t('点击 × 移除')) : (it.name || '');
    if(it.url){
      const im = document.createElement('img');
      im.src = it.url; im.alt = it.name || t('图片');
      d.appendChild(im);
    }
    const rm = document.createElement('button');
    rm.className = 'rm'; rm.type = 'button'; rm.textContent = '×'; rm.title = t('移除');
    rm.onclick = ()=>{ aiImgs.splice(i, 1); aiAttachRender(); };
    d.appendChild(rm);
    box.appendChild(d);
  });
  // 文档排在图片后面：两类混拖时，先图后文的顺序与消息体里"文字在前、内容在后"一致
  aiDocs.forEach((it, i)=>{
    box.appendChild(aiDocChip(it, ()=>{ aiDocs.splice(i, 1); aiAttachRender(); }));
  });
  aiAttachToggle();
}

/* 压缩：长边 ≤ 1600，质量与插值都按"能不能读懂截图里的字"来定。
   **GIF 一律原样上传**：canvas 会把动图压成第一帧，那是静默改内容，比文件大糟糕得多。

   三个参数都是实测出来的（判据是"模型能不能读出截图里的 13px 正文小字"，见
   tools/cdp_ai_image_test.js 的 PROBE 用例），不是凭感觉调的：

   - imageSmoothingQuality:'high' —— Chrome 默认是 'low'，把 1920 宽的界面截图缩到
     1600 时走的是廉价双线性，13px 中文正文的笔画被抹平成灰块，模型只能认出标题。
     换成 high 走带预滤波的高质量重采样，**不增加任何体积**。
   - PNG 源给 q0.92、其它给 q0.86 —— PNG 源绝大多数是界面截图（大片纯色 + 细笔画），
     JPEG 的 8×8 块效应专啃这种高频细节；照片本身有噪声，0.86 与 0.92 肉眼无差，省三成体积。
   - 尺寸不够大就别重编码（≤2MB 原样上传）—— PNG 是无损的，重编码成 JPEG 只会更糊。

   **加载源必须是 data URL，不能用 URL.createObjectURL。**
   CSP 的 img-src 是 `'self' data:`，不含 blob: —— 用 blob: 会被拦，img.onerror 触发，
   这个函数就 resolve(file) 原样返回：**看起来在压缩，实际一个字节都没缩**。
   这个缺陷曾静默存在了很久（5MB 手机照片照原样上传，只是没人量过）。
   data URL 顺带让预览和上传共用同一份数据，少一次 FileReader。
   返回 data: URL。 */
function aiImgShrink(file){
  return aiImgDataURL(file).then(dataUrl=>new Promise(resolve=>{
    if(/^image\/gif$/i.test(file.type)){ resolve(dataUrl); return; }
    const img = new Image();
    img.onload = ()=>{
      const w = img.naturalWidth, h = img.naturalHeight;
      const scale = Math.min(1, AI_IMG_EDGE / Math.max(w, h));
      // 已经够小就别重新编码：JPEG 二次压缩会掉画质，PNG 截图重编码反而变大
      if(scale >= 1 && file.size <= AI_IMG_RAW_KEEP){ resolve(dataUrl); return; }
      const cv = document.createElement('canvas');
      cv.width = Math.max(1, Math.round(w * scale));
      cv.height = Math.max(1, Math.round(h * scale));
      const cx = cv.getContext('2d');
      cx.imageSmoothingEnabled = true;
      cx.imageSmoothingQuality = 'high';   // 缩图质量：默认 low 会把小字糊掉
      cx.fillStyle = '#fff';                       // PNG 透明区转 JPEG 会变黑，先铺白底
      cx.fillRect(0, 0, cv.width, cv.height);
      cx.drawImage(img, 0, 0, cv.width, cv.height);
      const shot = /^image\/png$/i.test(file.type); // PNG 源按"截图"对待，多给点质量
      cv.toBlob(b=>{
        if(!b){ resolve(dataUrl); return; }        // 编码失败就退回原图，别把图弄丢
        aiImgDataURL(b).then(resolve, ()=>resolve(dataUrl));
      }, 'image/jpeg', shot ? 0.92 : 0.86);
    };
    img.onerror = ()=>resolve(dataUrl);
    img.src = dataUrl;
  })).catch(()=>null);
}

/* 从 data URL 里取实际 MIME（服务端按内容判定，这里只用来回填展示） */
function aiImgMimeOf(dataUrl){
  const m = /^data:([^;,]+)/.exec(String(dataUrl || ''));
  return m ? m[1].toLowerCase() : '';
}

function aiImgDataURL(blob){
  return new Promise((res, rej)=>{
    const rd = new FileReader();
    rd.onload = ()=>res(rd.result);
    rd.onerror = ()=>rej(rd.error || 'read error');
    rd.readAsDataURL(blob);
  });
}

/* 载入一批 File（来自 drop / paste / 选择器）。返回真正挂上去的张数。
   每张各自异步上传，互不阻塞：一张 8MB 的图卡住不该让后面几张排队干等。 */
function aiAttachAdd(files){
  const list = Array.prototype.slice.call(files || []).filter(f=>f && AI_IMG_IN.test(f.type || ''));
  if(!list.length) return Promise.resolve(0);
  const jobs = [];
  for(const f of list){
    if(aiImgs.length >= AI_IMG_MAX){
      toast(t('一条消息最多 {0} 张图片', AI_IMG_MAX), 'err');
      break;
    }
    const item = {id:'', name:f.name || t('粘贴的图片'), mime:f.type || '', url:'', busy:true, err:'', p:null};
    aiImgs.push(item);
    const task = (async ()=>{
      try{
        const dataUrl = await aiImgShrink(f);
        if(!dataUrl) throw new Error(t('读取图片失败'));
        item.url = dataUrl;                 // 压缩后的图既做预览又做上传内容（data: 地址，
        aiAttachRender();                   // CSP 的 img-src 是 'self' data:，blob: 会被拦）
        const mime = aiImgMimeOf(dataUrl) || item.mime;
        const r = await fetch('/api/ai/attach', {method:'POST', headers:{'Content-Type':'application/json'},
          body: JSON.stringify({name: item.name, mime: mime, content: dataUrl})});
        if(!r.ok){ throw new Error((await r.text()).slice(0, 200) || ('HTTP ' + r.status)); }
        const j = await r.json();
        item.id = j.id; item.mime = j.mime || mime; item.bytes = j.bytes || 0;
      }catch(e){
        item.err = String((e && e.message) || e);
        toast(t('图片载入失败：{0}', item.name), 'err');
      }finally{
        item.busy = false;
        aiAttachRender();
      }
    })();
    item.p = task;
    jobs.push(task);
  }
  aiAttachRender();
  return Promise.all(jobs).then(()=>aiImgs.filter(x=>x.id && !x.err).length);
}

/* 取出待发送图片并清空输入区。等所有上传落地再返回 —— 否则会出现
   "消息发出去了，图片还在传"，服务端拿到的是一条纯文字消息（模型答"图呢？"）。 */
async function aiAttachTake(){
  const running = aiImgs.filter(x=>x.busy || x.p).map(x=>x.p).filter(Boolean);
  if(running.length) await Promise.all(running);
  const ok = aiImgs.filter(x=>x.id && !x.err).map(x=>({id:x.id, name:x.name, mime:x.mime}));
  const bad = aiImgs.filter(x=>x.err).length;
  aiImgs = [];
  aiAttachRender();
  if(bad && !ok.length) return null;   // 全失败：让调用方决定还发不发
  return ok;
}

/* 已发出的图片在消息气泡里回显。
   用服务端地址而不是本地 data URL：刷新后历史恢复时本地预览早没了，
   而服务端那份还在（附件保留 24 小时，之后按 TTL 自动清理）。img 不能带自定义头，令牌走 ?t=。 */
function aiMsgImgsRender(host, imgs){
  if(!host || !imgs || !imgs.length) return;
  const box = document.createElement('div');
  box.className = 'ai-msg-imgs';
  imgs.forEach(im=>{
    if(!im || !im.id) return;
    const href = '/api/ai/attach?id=' + encodeURIComponent(im.id) + '&t=' + encodeURIComponent(window.__EK_TOKEN__ || '');
    const a = document.createElement('a');
    a.href = href; a.target = '_blank'; a.rel = 'noopener';
    const el = document.createElement('img');
    el.src = href; el.alt = im.name || t('图片'); el.title = im.name || '';
    // 过了 24 小时保留期这里必然 404（也可以被浏览器缓存撑过一阵）。
    // 裂图看着像坏了、还占着 72px 空框；换成一句"已过期"，用户才知道该重新拖一次。
    el.onerror = ()=>{
      const ph = document.createElement('span');
      ph.className = 'ai-msg-img-gone';
      ph.textContent = t('图片已过期');
      ph.title = t('附件只保留 24 小时，需要的话请重新拖入');
      a.replaceWith(ph);
    };
    a.appendChild(el);
    box.appendChild(a);
  });
  if(box.childNodes.length) host.appendChild(box);
}

/* 已发出的文档在消息气泡里回显 —— 只回显**文件名卡片**，正文一个字节都不进 DOM。
   这就是"文件附件"与"粘贴一大段"的区别所在：用户能确认发了哪个文件，
   但对话框仍然是对话框。 */
function aiMsgDocsRender(host, docs){
  if(!host || !docs || !docs.length) return;
  const box = document.createElement('div');
  box.className = 'ai-msg-docs';
  docs.forEach(d=>{ if(d && d.id) box.appendChild(aiDocChip(d, null)); });
  if(box.childNodes.length) host.appendChild(box);
}

/* 调试探针（tools/cdp_ai_drop_test.js 用）：只暴露元数据，不暴露正文 ——
   正文本来就不在浏览器里，这里也拿不到，这正是本次改造的目的。 */
window.__ekDocProbe = {
  pending: ()=>aiDocs.map(d=>({name:d.name, kind:d.kind||'', chars:d.chars||0, id:d.id||'', err:d.err||'', busy:!!d.busy})),
  sent: ()=>{
    const m = aiMessages.filter(x=>x && Array.isArray(x.docs)).pop();
    return m ? m.docs.map(d=>({name:d.name, kind:d.kind||'', chars:d.chars||0, id:d.id||''})) : [];
  },
};

/* 粘贴处理。抽成独立函数是为了能被测试直接调用：
   ClipboardEvent 的 clipboardData 在浏览器里是只读的，
   用 `new ClipboardEvent('paste', {clipboardData})` 造不出来 —— 所以
   "从 clipboardData 里挑图片"这件事只能靠传一个形状相同的对象来验。
   **纯文本粘贴必须原样放过**（不 preventDefault），否则用户粘一段日志进输入框会失效。 */
function aiPasteHandle(e){
  const items = (e && e.clipboardData && e.clipboardData.items) || [];
  const files = [];
  for(const it of items){
    if(it.kind !== 'file' || !AI_IMG_IN.test(it.type || '')) continue;
    const f = it.getAsFile();
    if(f) files.push(f);
  }
  if(!files.length) return false;   // 纯文本：交回浏览器默认行为
  e.preventDefault();
  aiAttachAdd(files).then(n=>{ if(n) toast(t('已载入 {0} 张图片，回车发送', n)); });
  return true;
}

/* 把一批文件按类型分流：图片走附件通道，其它交给后端抽文本。
   原来这里一律 f.text()，拖进来一张截图会读成乱码塞进输入框；
   拖进来一个 docx 更是把 zip 的压缩流当 UTF-8 解，得到一屏问号。

   为什么文档必须走后端（/api/ai/extract 而不是前端读）：
   File.text() 只会按 UTF-8 硬解，改不了编码（GBK 的老日志全是乱码），
   也判断不了二进制；类型判定、解码、docx/xlsx/pptx/pdf 抽文本
   全放在后端一处做。前端只负责搬运 —— 和图片走 /api/ai/attach 同一个分工。

   **抽出来的正文不进输入框**（v3.7.5）：后端落盘成 aidoc/<id>，这里只留一个引用，
   显示成一张文件卡片。理由见 ai_doc.go 文件头：把几千字塞进 textarea 等于
   把"附件"变成"粘贴"，输入框被撑满、气泡变日志、localStorage 还会被写爆。 */
const AI_DOC_MAX = 16 * 1024 * 1024;   // 与后端 aiExtractMaxBytes 同一口径

/* 把 File 读成 data:URL（后端统一按 base64 解，省一套 multipart 解析） */
function aiFileDataURL(f){
  return new Promise((res, rej)=>{
    const rd = new FileReader();
    rd.onload = ()=>res(String(rd.result || ''));
    rd.onerror = ()=>rej(rd.error || new Error('read error'));
    rd.readAsDataURL(f);
  });
}

/* 请求后端抽文本。失败时抛出的 message 已是一句人话（"旧版 Office 请另存为…"
   这类），直接显示即可，不要再包一层"操作失败"把原因盖掉。
   成功返回 {id, kind, chars, pages, truncated, note} —— **不含正文**。 */
async function aiExtractPost(payload){
  const r = await fetch('/api/ai/extract', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify(payload),
  });
  if(!r.ok) throw new Error((await r.text()).slice(0, 300).trim() || ('HTTP ' + r.status));
  return r.json();
}
/* 拖拽来的 File：字节在浏览器手里，读成 data URL 送上去 */
async function aiExtractFile(f){
  return aiExtractPost({name: f.name || '', mime: f.type || '', content: await aiFileDataURL(f)});
}

/* 二进制文档的扩展名 —— 只用来**分流**，不是类型判定。
   判据很实在：这几类 read_file 读不了（它按文本读，头 512 字节一判二进制就退出），
   所以必须交后端解；其余文本文件让 AI 自己按需读更好。
   猜错也不致命：改名成 .txt 的 docx 会走引用那条路，read_file 会明确回
   "该文件疑似二进制"，用户照着做就行。 */
function aiDocExt(n){
  // 内联正而不是提到模块顶部：aiPickLs（在文件更靠前）也调它，
  // 顶层 const 在 IIFE 里跨调用点会有 TDZ 风险，这种坑踩过一次就够了。
  const m = /\.(pdf|docx?|xlsx?|pptx?)$/i.exec(String(n || ''));
  return m ? m[1].toLowerCase() : '';
}
/* 文档卡片的公共流程：先挂一张"读取中"的卡片，读完补字数，失败标红并说明原因。
   为什么先挂卡片：读一份 300 页 PDF 要几秒，什么都不显示的话用户会以为拖丢了。

   `run` 是个返回 Promise<json> 的函数 —— 拖拽与"+号选文件"的差别只在
   **字节从哪来**（浏览器读 vs 服务端按路径读），卡片和后续处理一模一样。 */
function aiDocRun(item, run){
  item.busy = true;
  aiDocs.push(item);
  aiAttachRender();
  // 正在生成回答时不动状态行 —— 那行归主循环用，覆盖掉会显得"卡在读取"
  if(!aiBusy) aiHstat(t('正在读取：{0}', item.name));
  const task = (async ()=>{
    try{
      const j = await run();
      if(!j || !j.id) throw new Error(t('后端没有返回文件引用'));
      item.id = j.id;
      item.kind = j.kind || '';
      item.chars = Number(j.chars) || 0;
      item.note = j.note || '';
      // 提示跟在卡片外（toast）：它是给用户看的，不进上下文
      const head = t('已附加：{0}（{1} 字）', item.name, item.chars);
      if(j.note) toast(head + ' · ' + j.note); else toast(head);
    }catch(e){
      item.err = String((e && e.message) || e);
      toast(t('读取失败：{0}', item.name) + ' —— ' + item.err, 'err');
    }finally{
      item.busy = false;
      aiAttachRender();
      if(!aiBusy && !aiDocs.some(x=>x.busy)) aiHstat('');
    }
  })();
  item.p = task;
  return task;
}
function aiDocAdd(f){
  return aiDocRun({name: f.name || t('文件')}, ()=>aiExtractFile(f));
}

async function aiDropFiles(files){
  const arr = Array.prototype.slice.call(files || []);
  const imgs = arr.filter(f=>f && AI_IMG_IN.test(f.type || ''));
  const docs = arr.filter(f=>f && !AI_IMG_IN.test(f.type || ''));
  // 图片走附件通道（模型真的能看到像素）
  if(imgs.length) await aiAttachAdd(imgs);
  const jobs = [];
  for(const f of docs){
    if(f.size > AI_DOC_MAX){
      toast(t('文件超过 {0} MB，已跳过：{1}', AI_DOC_MAX >> 20, f.name), 'err');
      continue;
    }
    jobs.push(aiDocAdd(f));   // 各自异步读，互不阻塞：一份大 PDF 不该让后面的文件排队
  }
  if(jobs.length){
    await Promise.all(jobs);
    try{ $ai('ai-in').focus(); }catch(_){}
  }
}

/* 取出待发送的文档并清空待发区。返回 [{id,name,kind,chars}]。
   失败的项直接丢弃（每条失败都单独 toast 过，留着也发不出去）。
   与 aiAttachTake 不同，这里**不返回 null**：文档读失败不该连累用户已经写好的那句话。 */
async function aiDocTake(){
  const running = aiDocs.filter(x=>x.p).map(x=>x.p);
  if(running.length) await Promise.all(running);
  const ok = aiDocs.filter(x=>x.id && !x.err)
    .map(x=>({id:x.id, name:x.name || '', kind:x.kind || '', chars:x.chars || 0}));
  aiDocs = [];
  aiAttachRender();
  return ok;
}
/* 导出本轮：给答辩/工单留一份可读证据，轨迹一起带走 */
function aiExportTurn(d){
  const ts = d.dataset.ts || aiNow();
  const body = d.dataset.raw || d.innerText || '';
  const nodes = Array.isArray(d.__trace) ? d.__trace : [];
  let md = '# EnvKit 对话导出 · ' + ts + '\n\n' + body + '\n\n';
  if(nodes.length){
    md += '## 轨迹\n\n';
    nodes.forEach(n=>{
      md += '- [' + aiClock(n.at) + '] ' + (AI_KIND[n.kind] || n.kind) + '：' + n.text + '\n';
      if(n.detail) md += '  ```\n  ' + n.detail.replace(/\n/g, '\n  ') + '\n  ```\n';
    });
  }
  const blob = new Blob([md], {type: 'text/markdown;charset=utf-8'});
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'envkit-chat-' + Date.now() + '.md';
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(()=>URL.revokeObjectURL(a.href), 3000);
  toast(t('已导出本轮'));
}

/* 请求体被中间层丢弃时的提示卡（v3.2）。
   为什么不只打一行灰字：这件事的后果是"助手忘事 + 缓存命中率封顶"，
   用户看不见原因、只会觉得模型变笨。所以给一张能关掉的卡，附上修法。
   同一会话最多提示 3 次——第 4 次开始只是噪音。 */
let aiBodyWarnN = 0;
function aiBodyWarn(n){
  if(++aiBodyWarnN > 3) return;
  const box = document.createElement('div'); box.className = 'ai-warn';
  const h = document.createElement('b'); h.textContent = t('⚠ 请求内容被中间层丢弃');
  box.appendChild(h);
  const p = document.createElement('div');
  p.textContent = t('本次只恢复了最近 {0} 条消息，历史可能不完整——这会让助手"忘事"，也会让缓存命中率封顶。', n);
  box.appendChild(p);
  const det = document.createElement('details');
  const sm = document.createElement('summary'); sm.textContent = t('最常见原因与修法');
  const fix = document.createElement('div'); fix.className = 'ai-warn-fix';
  fix.textContent = t('1) 本机代理（Clash / V2Ray 等，常见 127.0.0.1:7890）没有绕过 localhost：\n   在代理的「绕过 / 例外 / Bypass」里加入 127.0.0.1;localhost;::1\n2) 或换一个不走系统代理的浏览器打开本控制台\n\n验证：到「AI 助手」页点「检测请求传输」，显示"发送 N / 收到 N"即已修好。');
  det.appendChild(sm); det.appendChild(fix); box.appendChild(det);
  $ai('ai-msgs').appendChild(box);
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
}

/* 确认卡的决定：**先走原地送达**（v3.2），失败才退回旧路径。
   原地送达 = 服务端还在同一个 SSE 流里等着，决定一到就接着执行；
   旧路径 = 带 confirm 重新提交，等于把上下文整体重建一次（贵，且把任务切成两段）。 */
async function aiDecide(approved){
  const p = aiPending; if(!p) return;
  aiPending = null;
  try{ p.card.remove(); }catch(_){}
  petConfirmHide();
  aiAdd('sys', approved ? t('已确认，继续执行…') : t('已拒绝，让助手换个做法…'));
  aiTraceAdd('human', approved ? t('你已确认：{0}', p.tool) : t('你已拒绝：{0}', p.tool), {cls: approved ? 'ok' : 'warn'});
  aiHstat(approved ? t('执行中') : t('已拒绝'));
  // 批量授权只认"确认"这一次：拒绝时即使勾了也不开
  const wantBatch = !!(approved && p.batchOf && p.batchOf());
  try{
    const r = await fetch('/api/ai/confirm', {
      method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({tool_call_id: p.id, tool: p.tool, args: p.args || {},
        approved: !!approved, batch: wantBatch})
    });
    // **不能只看 r.ok**：旧版服务端没有这个路由时会落到 "/" 的页面处理器，
    // 同样回 200（一段 HTML）。只认 JSON 里的 ok:true，否则一律当"没送达"走旧路径。
    if(r.ok){
      const j = await r.json().catch(()=>null);
      if(j && j.ok) return;   // 同一个流会继续吐 delta / tool_result / usage
    }
  }catch(_){}
  // 兜底：404=没有等待者（旧版本、已超时、或卡来自旧路径）→ 走回原路。
  // 这里原来是"abort → 睡 150ms 等旧流的 finally 复位 aiBusy → 不 await 地开新流"。
  // 150ms 是竞态赌注：旧流 finally 晚于它执行就会把新流刚设的 aiBusy/按钮态冲掉。
  // v3.3 起 aiConsume 有代际计数（aiGen）：新流一开跑，旧流的 finally 就无权复位，
  // 睡眠可以整个删掉。
  if(aiCtrl){ try{ aiCtrl.abort(); }catch(_){} }
  await aiConsume('/api/ai/chat', {messages: aiMessages,
    confirm: {tool_call_id: p.id, tool: p.tool, args: p.args || {}, approved: !!approved, batch: wantBatch}});
}

/* ---------- 绑定 ---------- */
(function aiBindUI(){
  const on = (id, ev, fn)=>{ const el = document.getElementById(id); if(el) el.addEventListener(ev, fn); };
  on('ai-expand', 'click', ()=>aiBig(!aiBigOn));
  on('ai-trace-open', 'click', ()=>aiSide(aiSideTab || 'trace'));
  document.querySelectorAll('#ai-tabs button').forEach(b=>b.addEventListener('click', ()=>aiSide(b.dataset.tab)));
  on('ai-plus', 'click', ()=>{ const pop = $ai('ai-pick'); if(pop && pop.classList.contains('show')) aiPickClose(); else aiPickOpen(); });
  on('ai-pick-close', 'click', aiPickClose);
  on('ai-pick-up', 'click', ()=>{ const up = aiParent(aiPickPath); aiPickLs(up || ''); });
  /* 拖拽：挂在整条抽屉上，而不是只挂对话区。
     用户说"拖到 AI 区域"就是**任何位置都应该接住**——把图拖到输入框上、
     拖到消息列表空白处、拖到侧栏上，期望都一样。只挂 #ai-msgs 时，
     拖到偏下的输入区会变成浏览器默认行为（直接打开那张图，把对话顶掉）。 */
  const drawer = $ai('ai-drawer');
  if(drawer){
    let depth = 0;   // dragenter/dragleave 会在子元素间反复触发，用计数防抖
    const hint = on=>{
      if(on){ drawer.dataset.dropHint = t('松手载入图片'); drawer.classList.add('ai-dropping'); }
      else { drawer.classList.remove('ai-dropping'); delete drawer.dataset.dropHint; }
    };
    drawer.addEventListener('dragenter', e=>{ e.preventDefault(); depth++; hint(true); });
    drawer.addEventListener('dragover', e=>{
      e.preventDefault();
      if(e.dataTransfer) e.dataTransfer.dropEffect = 'copy';
    });
    drawer.addEventListener('dragleave', ()=>{ depth = Math.max(0, depth - 1); if(!depth) hint(false); });
    drawer.addEventListener('drop', e=>{
      e.preventDefault(); depth = 0; hint(false);
      if(!e.dataTransfer || !e.dataTransfer.files || !e.dataTransfer.files.length) return;
      aiDropFiles(Array.prototype.slice.call(e.dataTransfer.files));
    });
  }
  /* 粘贴：截图工具（Win+Shift+S / 微信截图）出来的就是剪贴板里的位图，
     用户的下意识动作是 Ctrl+V，而不是先存成文件再拖进来。 */
  const taIn = $ai('ai-in');
  if(taIn){
    taIn.addEventListener('paste', e=>aiPasteHandle(e));
  }
  // Esc：先关文件选择器，再关抽屉（避免误关正在输入的对话）
  document.addEventListener('keydown', e=>{
    if(e.key !== 'Escape') return;
    const pop = $ai('ai-pick');
    if(pop && pop.classList.contains('show')){ aiPickClose(); e.stopPropagation(); return; }
    const dr = $ai('ai-drawer');
    if(dr && dr.classList.contains('open') && !aiBusy){ dr.classList.remove('open'); petShift(false); }
  });
})();

/* 服务商与模型 */
let aiPresets = [];
let aiPulledModels = [];   // 「拉取模型列表」得到的模型（**优先于静态预设**）
let aiPulledFrom = '';     // 上面那份列表来自哪个 Base URL（改动后即作废）
async function aiLoadProviders(){
  try{ aiPresets = (await (await fetch('/api/ai/providers')).json()).providers || []; }catch(e){ aiPresets = []; }
  const sel = $ai('ai_provider');
  // 「自定义」由后端预置表统一给出（带 icon），**前端不再追加**——
  // 2026-10-23 用户截图发现两个「自定义」：后端列表已含 custom 项，前端又硬拼了一个。
  sel.innerHTML = aiPresets.map(p=>`<option value="${p.id}">${p.name}</option>`).join('');
  // 自绘下拉菜单：原生 select 的 option 渲染不了图片，官方品牌图标走这里
  const menu = $ai('ai_provider_menu');
  menu.innerHTML = aiPresets.map(p=>{
    const ic = aiProvIconHTML(p.id);
    return `<div class="prov-item" data-id="${p.id}" role="option">${ic}<span>${p.name}</span></div>`;
  }).join('');
  menu.querySelectorAll('.prov-item').forEach(it=>{
    it.addEventListener('click', ()=>{
      menu.style.display = 'none';
      if(sel.value !== it.dataset.id){
        sel.value = it.dataset.id;
        sel.dispatchEvent(new Event('change'));   // 复用既有 change 链：清拉取缓存 + 应用预置 + 自动保存
      }
    });
  });
  sel.value = 'deepseek';
  aiProviderDDSync();
}
// 图标渲染：内联 SVG（tools/brand_icons_build.py 注入的 AI_PROV_ICONS）；
// GLM 官方矢量没拿到，用品牌色徽章兜底
function aiProvIconHTML(id){
  const ic = (typeof AI_PROV_ICONS !== 'undefined') && AI_PROV_ICONS[id];
  if(ic) return `<svg viewBox="${ic.vb}" fill="${ic.fill}" aria-hidden="true">${ic.svg}</svg>`;
  if(id === 'glm') return '<span class="picon-z">Z</span>';
  return '';
}
// 按钮态（当前选中项的图标+名字）与菜单高亮跟随 select.value——
// aiApplyProvider 会在每次值变化后被调，同步挂这里最省心
function aiProviderDDSync(){
  const sel = $ai('ai_provider');
  const p = aiPresets.find(x=>x.id === sel.value);
  $ai('ai_provider_cur_name').textContent = p ? p.name : (sel.value || '');
  $ai('ai_provider_cur_ic').innerHTML = aiProvIconHTML(sel.value);
  $ai('ai_provider_menu').querySelectorAll('.prov-item').forEach(it=>{
    it.classList.toggle('on', it.dataset.id === sel.value);
  });
}
(function(){
  const btn = $ai('ai_provider_btn'), menu = $ai('ai_provider_menu');
  btn.addEventListener('click', e=>{
    e.stopPropagation();
    menu.style.display = menu.style.display === 'none' ? '' : 'none';
  });
  menu.addEventListener('click', e=>e.stopPropagation());
  document.addEventListener('click', ()=>{ menu.style.display = 'none'; });
})();
function aiProviderPreset(){ return aiPresets.find(p=>p.id === $ai('ai_provider').value) || null; }
function aiApplyProvider(keepModel){
  const p = aiProviderPreset();
  const custom = !p || p.id === 'custom';
  if(!custom && p.base_url) $ai('ai_base').value = p.base_url;
  $ai('ai_base').readOnly = !custom;
  const sel = $ai('ai_model_sel');
  // **拉取到的模型优先于静态预设。**
  //
  // 原来写的是 `custom ? aiPulledModels : (p.models || [])`——
  // 非自定义服务商下**把拉取结果丢掉了**，于是下拉框显示的仍是预设。
  // 实测踩过：厂商停用deepseek-chat / deepseek-reasoner 之后，
  // 用户点了「拉取模型列表」，界面明确提示「已拉取 2 个模型，已在下拉框中回填」，
  // 而下拉框里**仍然是那两个失效名字**。
  //
  // **提示说回填了、实际没回填** —— 这比不回填更坏：
  // 用户会以为列表是厂商的真实在售模型，实际看到的是软件自己的过期快照。
  // 预设是硬编码的静态列表，厂商改名后它不会自己更新；
  // 拉取到的才是真值。**有真值就该用真值。**
  //
  // 预设仍作为**拉取失败时的兜底**（无网络、Key 错、服务商没实现 /models）。
  // 拉取结果只在「有列表 且 地址没变」时才算数。
  // 地址变了还沿用旧列表，会让用户挑一个这个地址上并不存在的模型。
  const sameAddr = aiPulledFrom === $ai('ai_base').value.trim();
  const pulled = (aiPulledModels && aiPulledModels.length && sameAddr) ? aiPulledModels : [];
  const models = pulled.length ? pulled : (p && p.models) || [];
  const useSelect = !custom || models.length > 0;
  sel.style.display = useSelect ? '' : 'none';
  $ai('ai_model_row').style.display = useSelect ? 'none' : '';
  const current = keepModel || '';
  sel.innerHTML = models.map(m=>`<option value="${m}">${m}</option>`).join('') + '<option value="__custom__">自定义模型名...</option>';
  if(current){
    if([...sel.options].some(o=>o.value===current)) sel.value = current;
    else { const o = document.createElement('option'); o.value = current; o.textContent = current; sel.prepend(o); sel.value = current; }
  }
  if(!useSelect) $ai('ai_model_row').style.display = '';
  // 服务商说明（预设的 Note）——2026-10-17 起 DeepSeek 不再带说明：
  // 用户反馈「这句话不需要看」。机制保留：哪个服务商确需说明就在后端加 Note，
  // 走 t() 翻译（服务端直出中文会让英文界面露中文）；没有 Note 时整行隐藏。
  const hint = $ai('ai_provider_note');
  if(hint){
    const raw = (p && !custom && p.note) ? p.note : '';
    const txt = raw ? t(raw) : '';
    hint.querySelector('span').textContent = txt;
    hint.style.display = txt ? '' : 'none';
  }
  aiProviderDDSync();   // 自绘下拉的按钮图标/名字/菜单高亮跟随当前值
}
// 换服务商等于换地址，上一个地址的模型列表必须一起作废
$ai('ai_provider').addEventListener('change', ()=>{ aiPulledModels = []; aiPulledFrom = ''; aiApplyProvider(''); aiScheduleAutoSave(); });
$ai('ai_model_sel').addEventListener('change', ()=>{
  const isCustom = $ai('ai_model_sel').value === '__custom__';
  $ai('ai_model_row').style.display = isCustom ? '' : 'none';
  if(isCustom){ $ai('ai_model_sel').style.display = 'none'; setTimeout(()=>$ai('ai_model').focus(), 30); }
});
function aiCurrentModel(){
  if($ai('ai_model_sel').style.display === 'none' || $ai('ai_model_sel').value === '__custom__') return $ai('ai_model').value.trim();
  return $ai('ai_model_sel').value;
}

/* 自动保存/自动拉取链的状态——必须声明在 aiLoadConfig 之前：
   aiLoadConfig 在页面初始化（ekSetLang）就被调用，而 let 有暂时性死区，
   声明放在它后面会炸 "Cannot access 'aiSuppress' before initialization"，
   连锁炸断整个 AI 设置卡片初始化（下拉空白、启用开关失灵）。 */
let aiAutoTimer = null;      // 防抖句柄
let aiAutoBusy = false;      // 上一次自动链还没跑完时，新改动只重置计时器
let aiKeySet = false;        // 后端是否已存有 Key（决定自动拉取要不要发起）
let aiSuppress = false;      // 程序化回填（加载配置/切换服务商）期间不触发自动保存

/* 设置卡片 */
async function aiLoadConfig(){
  try{
    aiSuppress = true;   // 程序化回填不触发自动保存（change/input 事件都会被它拦下）
    const c = await (await fetch('/api/ai/config')).json();
    await aiLoadProviders();
    $ai('ai_provider').value = c.provider || 'deepseek';
    if([...$ai('ai_provider').options].every(o=>o.value!==c.provider)){ $ai('ai_provider').value = 'custom'; }
    aiApplyProvider(c.model || '');
    $ai('ai_base').value = c.base_url || $ai('ai_base').value;
    $ai('ai_enabled').checked = !!c.enabled;
    $ai('ai_memory_on').checked = !!c.memory_enabled;
    aiKeySet = !!c.key_set;   // 自动拉取要用：后端已存 Key 才值得发 /models 请求
    $ai('ai_key').placeholder = c.key_set ? t('已保存（{0}），留空保留', c.key_masked||'****') : 'sk-...';
    $ai('ai_temp').value = (c.temperature === undefined || c.temperature === null) ? '' : (Math.round(Number(c.temperature)*100)/100);
    // 内部调用模型：显示"配置值"本身（空串即占位符提示的"与主模型相同"），
    // 不在这里把它替换成主模型名——那会让用户分不清自己到底配没配。
    $ai('ai_utility_model').value = c.utility_model || '';
    const q = c.quirk || {};
    const notes = [];
    if(q.temp_mode === 'one') notes.push(t('temperature 固定为 1'));
    if(q.temp_mode === 'none') notes.push(t('不发送 temperature（上游不接受）'));
    if(q.max_tokens_field === 'max_completion_tokens') notes.push(t('使用 max_completion_tokens'));
    if(q.no_tools) notes.push(t('该模型不支持工具调用（已降级为纯问答）'));
    $ai('ai_quirk').textContent = notes.join('；');
    $ai('ai_quirk_row').style.display = notes.length ? '' : 'none';
  }catch(e){}
  finally{ aiSuppress = false; }   // 无论成功失败都要放行，否则自动保存永久失灵
}
aiLoadConfig();
$ai('btn-ai-models').onclick = async ()=>{
  $ai('h-ai').textContent = t('拉取模型列表中...');
  try{
    const c = await (await fetch('/api/ai/models', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({base_url:$ai('ai_base').value.trim()})})).json();
    if(!c.ok){ $ai('h-ai').textContent = aiPullErrHuman(c.error); return; }
    const cur = aiCurrentModel();
    aiPulledModels = c.models || [];
    // 记录这份列表来自哪个地址：**Base URL 改动后必须作废**，
    // 否则会出现「上一个服务商的模型名配着新地址显示」——
    // 用户挑了一个这个地址上并不存在的模型，保存后才发现。
    aiPulledFrom = $ai('ai_base').value.trim();
    aiApplyProvider(cur);   // 重新渲染：下拉框显示拉取到的真值并选中当前模型
    $ai('h-ai').textContent = t('已拉取 {0} 个模型，已在「模型」下拉框中回填', aiPulledModels.length);
  }catch(e){ $ai('h-ai').textContent = aiPullErrHuman(e); }
};
$ai('btn-ai-test').onclick = async ()=>{
  $ai('h-ai').textContent = t('测试中...');
  try{
    const c = await (await fetch('/api/ai/test', {method:'POST'})).json();
    $ai('h-ai').textContent = c.ok ? (t('连接正常 · ')+c.latency_ms+'ms') : (t('失败：')+(c.error||t('未知')));
  }catch(e){ $ai('h-ai').textContent = t('失败：')+e; }
};

/* 传输自检（v3.3）：**两种发法各发一次**，用来分辨"到底是哪一部分被中间层吃了"。
   背景：自检（纯 body）能完整到达，而真实的 /api/ai/chat 请求会「头到了、body 0 字节」。
   两者只差三样东西：大自定义头（对话兜底头）、AbortSignal、SSE 流式响应。
   所以这里分别发：
     A 普通：只有 body；
     B 带对话头：body 一样大，但附上和真实请求同尺寸的 X-EnvKit-Chat-Fallback。
   A 通过、B 失败 → 就是那个大头惹的祸（那它就不该继续出现在热路径上）；
   两个都通过 → 问题在信号/流式那一侧，继续往那边查。 */
const _btnTransport = $ai('btn-ai-transport');
if(_btnTransport) _btnTransport.onclick = async ()=>{
  const h = $ai('h-ai');
  h.textContent = t('正在检测…');
  const mk = ()=>{
    const raw = new Uint8Array(4096);
    crypto.getRandomValues(raw);
    let s = '';
    for(let i = 0; i < raw.length; i++) s += String.fromCharCode(raw[i] & 0x7f);
    return JSON.stringify({probe: btoa(s)});
  };
  const probe = async (withChatHeader)=>{
    const bodyStr = mk();
    const sent = new TextEncoder().encode(bodyStr).length;
    const headers = {'Content-Type': 'application/json', 'X-EnvKit-Body-Len': String(sent)};
    if(withChatHeader){
      // 造一个和真实对话同量级（约 6 条 × 200 字）的兜底头
      const msgs = [];
      for(let i = 0; i < 6; i++) msgs.push({r: i % 2 ? 'assistant' : 'user', c: '这是用于测量请求头体积的一段中文内容'.repeat(5)});
      headers['X-EnvKit-Chat-Fallback'] = encodeURIComponent(JSON.stringify({last: 'x', recent: msgs, lang: 'zh'}));
    }
    try{
      const r = await fetch('/api/ai/echo', {method:'POST', headers, body: bodyStr});
      const j = await r.json();
      return {sent, got: (j && j.len) || 0, ok: !!(j && j.ok && j.len === sent), headerLen: (headers['X-EnvKit-Chat-Fallback'] || '').length};
    }catch(e){ return {sent, got: -1, ok: false, err: String(e), headerLen: (headers['X-EnvKit-Chat-Fallback'] || '').length}; }
  };
  const a = await probe(false);
  const b = await probe(true);
  const fmt = (x)=> x.got < 0 ? t('请求失败') : t('发送 {0} / 收到 {1}', x.sent, x.got);
  if(a.ok && b.ok){
    h.textContent = t('传输正常（普通 {0}；带对话头 {1}，头长 {2} 字节）——问题不在体积或请求头上', fmt(a), fmt(b), b.headerLen);
  } else if(a.ok && !b.ok){
    h.textContent = t('普通请求正常（{0}），但带对话头的那次失败（{1}，头长 {2} 字节）—— 中间层在处理大请求头时丢掉了 body', fmt(a), fmt(b), b.headerLen);
  } else {
    h.textContent = t('连普通请求都不完整（{0}）—— 传输链路本身有问题', fmt(a));
  }
};

