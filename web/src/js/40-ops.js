/* ---------- 环境安装：演练 + 版本管理 ---------- */
$('#btn-dryrun').onclick = ()=>post('/api/install-dryrun');
$('#btn-ver-check').onclick = loadVersions;
function vtag(txt, cls){ return `<span class="vtag ${cls}">${esc(txt)}</span>`; }
async function loadVersions(){
  const h = document.getElementById('h-ver');
  h.textContent = '查询中（探测官方最新版本，约几秒）...';
  const box = document.getElementById('ver-table');
  box.innerHTML = '';
  try{
    const list = await (await fetch('/api/component-versions')).json();
    const table = document.createElement('table'); table.className = 'vtab';
    // 表头是静态文案可以 innerHTML；数据行一律 DOM + textContent 构建，杜绝注入
    table.innerHTML = '<colgroup><col style="width:14%"><col style="width:30%"><col style="width:18%"><col style="width:18%"><col style="width:20%"></colgroup><tr><th>组件</th><th>已安装</th><th>配置目标</th><th>官方最新</th><th></th></tr>';
    for(const v of list){
      const updatable = v.latest && v.configVer && verCmpJs(v.latest, v.configVer) > 0;
      const tr = document.createElement('tr');
      const tdName = document.createElement('td'); const nb = document.createElement('b'); nb.textContent = v.name; tdName.appendChild(nb);
      const tdInst = document.createElement('td');
      if(v.installedKnown){
        tdInst.innerHTML = vtag(v.installedVer + ' · ' + (v.toolDir ? 'tools' : '系统'), v.toolDir ? '' : 'muted');
      } else {
        const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('未检测（先点「重新检测」）'); tdInst.appendChild(sp);
      }
      const tdCfg = document.createElement('td'); tdCfg.textContent = v.configVer || '—';
      const tdLat = document.createElement('td');
      if(v.latest){ tdLat.innerHTML = vtag(v.latest, updatable ? '' : 'muted'); }
      else { const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('未知'); tdLat.appendChild(sp); }
      const tdOp = document.createElement('td');
      if(updatable){
        // 不再用内联 onclick 字符串拼接（v.name/v.latest 来自外部版本探测，可被注入），改闭包传参
        const btn = document.createElement('button'); btn.className = 'mini on'; btn.textContent = t('升级');
        btn.onclick = ()=>upgradeComp(v.name, v.configVer, v.latest);
        tdOp.appendChild(btn);
      } else if(v.latest){
        const sp = document.createElement('span'); sp.className = 'muted'; sp.textContent = t('已是最新'); tdOp.appendChild(sp);
      }
      tr.appendChild(tdName); tr.appendChild(tdInst); tr.appendChild(tdCfg); tr.appendChild(tdLat); tr.appendChild(tdOp);
      table.appendChild(tr);
    }
    box.appendChild(table);
    h.textContent = '查询完成。升级完成后点「检查更新」刷新状态。';
  }catch(e){ h.textContent = t('查询失败：{0}', e); }
}
function verCmpJs(a, b){
  const pa = String(a).split('.'), pb = String(b).split('.');
  for(let i = 0; i < Math.max(pa.length, pb.length); i++){
    const x = parseInt(pa[i])||0, y = parseInt(pb[i])||0;
    if(x !== y) return x < y ? -1 : 1;
  }
  return 0;
}
async function upgradeComp(name, from, to){
  if(!(await askConfirm('升级组件', t(`将把 {0} 从 {1} 升级到 {2}：\n下载新版（数百 MB）→ 解压新目录 → 自动更新 PATH 与配置。\n旧版本目录会保留，可随时回滚。\n\n继续？`, name, from, to),'升级'))) return;
  post('/api/component-upgrade', {name});
  document.getElementById('h-ver').textContent = t('{0} 升级中，进度见下方安装日志，完成后点「检查更新」刷新。', name);
}

/* ---------- 配置导入 / 导出 ---------- */
$('#btn-cfg-export').onclick = ()=>{ location.href='/api/config/export?t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#btn-cfg-sanitize').onclick = ()=>{ location.href='/api/config/export?sanitize=1&t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#btn-cfg-import').onclick = ()=>{ $('#cfg-file').click(); };
/* 导出分发包：把当前 exe 打成 zip（exe+安装/卸载+说明）直接下载；泄漏扫描失败时后端返回 409 文本 */
$('#btn-dist-export').onclick = async ()=>{
  try{
    const resp = await fetch('/api/dist/export?t=' + encodeURIComponent(window.__EK_TOKEN__));
    if(!resp.ok){
      const tx = await resp.text();
      alert(t('导出失败：') + tx);
      return;
    }
    const blob = await resp.blob();
    const m = (resp.headers.get('Content-Disposition')||'').match(/filename=([^;]+)/);
    const a=document.createElement('a');
    a.href=URL.createObjectURL(blob);
    a.download = m ? m[1] : 'EnvKit-dist.zip';
    a.click();
    setTimeout(()=>URL.revokeObjectURL(a.href), 30000);
  }catch(e){ alert(t('导出失败：') + e); }
};

/* ---------- 数据库备份 / 还原 ---------- */
$('#btn-db-backup').onclick = ()=>post('/api/program/db-backup');
// 恢复演练：备份 → 恢复到 __drill_ 临时库 → 比对表数 → 删临时库，全程不碰业务库
$('#btn-db-drill').onclick = ()=>post('/api/program/db-drill');
$('#btn-db-restore').onclick = async ()=>{
  const f=$('#restore_path').value.trim();
  if(!f){ alert('请先选择备份 .sql 文件'); return; }
  const db=$('#db_name').value.trim();
  await postConfirm('/api/program/db-restore', {file:f}, t('还原数据库'),
    t('将用备份文件覆盖数据库 {0}：\n{1}\n\n当前库中的同表数据将被替换，且无法撤销！', db, f));
};
function pickRestore(){ openBrowser('file', (cfg.projects&&cfg.projects.backend_dir)||'', '.sql', 'file', p=>{ $('#restore_path').value=p; }); }
$('#btn-pick-restore').onclick = pickRestore;

/* ---------- 环境诊断报告 ---------- */
$('#btn-report').onclick = ()=>{ location.href = '/api/report?t=' + encodeURIComponent(window.__EK_TOKEN__); };
$('#cfg-file').onchange = async e=>{
  const f=e.target.files[0]; if(!f) return;
  try{
    const text=await f.text();
    JSON.parse(text); // 先本地校验，避免把坏文件发给后端
    const r=await fetch('/api/config/import',{method:'POST',body:text});
    if(!r.ok){
      // 400 时后端返回 {ok:false, problems:[...]}，把问题逐条亮出来
      try{ const j=await r.json(); if(j.problems&&j.problems.length) throw new Error(j.problems.map(p=>p.field+' → '+p.msg).join('\n')); }catch(e2){ if(e2.message&&e2.message.includes('→')) throw e2; }
      throw new Error(await r.text());
    }
    $('#h-cfg-io').textContent='导入成功，已重新加载';
    await loadConfig();
    refreshGuards();
  }catch(err){
    $('#h-cfg-io').textContent=t('导入失败：{0}', err.message||err);
  }
  e.target.value='';
};

function pickDir(inputId, projField){
  openBrowser('dir', (cfg.projects&&cfg.projects[projField])||'', '', 'dir', p=>{
    if(inputId) $('#'+inputId).value = p;
    if(projField==='frontend_dir'){ $('#fe_dir').value = p; loadLaunchPlan(); }
    if(projField==='backend_dir'){ $('#be_dir').value = p; detectSQL(); loadLaunchPlan(); }
    syncStartDirs(); markDirty(); refreshGuards();
  });
}
function pickSQL(){ openBrowser('file', (cfg.projects&&cfg.projects.backend_dir)||'', '.sql', 'file', p=>{ $('#sql_path').value=p; markDirty(); refreshGuards(); }); }
document.querySelectorAll('[data-pick]').forEach(b=>b.addEventListener('click',()=>pickDir(b.dataset.pick,b.dataset.proj)));
document.querySelectorAll('[data-remote]').forEach(b=>b.addEventListener('click',()=>pickRemoteDir(b.dataset.remote)));
$('#btn-pick-sql').onclick = pickSQL;

/* 配置页 tab：只切显示，不动任何字段——隐藏页里的控件照样能被脚本读写，
   自动保存与前置校验因此完全不需要感知 tab 的存在。 */
(function(){
  const tabs = document.getElementById('cfg-tabs');
  if(!tabs) return;
  tabs.addEventListener('click', ev=>{
    const b = ev.target.closest('.cfg-tab'); if(!b) return;
    tabs.querySelectorAll('.cfg-tab').forEach(x=>x.classList.toggle('on', x === b));
    document.querySelectorAll('#panel-config .cfg-pane').forEach(p=>
      p.classList.toggle('show', p.dataset.cfgPane === b.dataset.cfgTab));
  });
})();

/* 自动探测后端目录下的 dbs\*.sql */
// v2.3 N4：拉取链端守护的运行状态。
// 开关只说明"你要求了它"，这里说明"它实际在做什么"——两者可能不一致
// （比如刚勾上还没到下一轮，或配置没保存）。
async function loadGuardStatus(){
  const box = $('#guard-box');
  if(!box) return;
  const badge = $('#guard-badge'), note = $('#guard-note'), recent = $('#guard-recent');
  let d = null;
  try{ d = await (await fetch('/api/guard/status')).json(); }catch(e){}
  const s = (d && d.status) || null;
  if(!s){ badge.textContent = t('读取失败'); note.textContent = ''; recent.innerHTML=''; return; }

  // 徽标分三色：进行中=绿、未开启=灰、有问题=橙。
  // **连续不可达单独一档**：它需要用户去处理（虚拟机没开/IP 变了），
  // 不是"守护坏了"，也不能算正常。
  let cls = 'var(--muted)', label = t('未开启');
  if(!s.host){ cls = 'var(--muted)'; label = t('未配置'); }
  else if(s.last_unreachable > 0){ cls = 'var(--warn)'; label = t('连不上'); }
  else if(s.enabled && s.last_check){ cls = 'var(--ok)'; label = t('运行中'); }
  else if(s.enabled){ cls = 'var(--muted)'; label = t('已开启'); }
  badge.textContent = label;
  badge.style.color = cls;
  badge.style.background = 'var(--card)';

  note.textContent = s.note || '';
  // 最近动作：这才是"它到底做了什么"的答案
  const acts = (d && d.recent) || [];
  if(acts.length){
    let h = '<div class="muted" style="font-size:11px;margin-bottom:4px">' + esc(t('最近动作')) + '</div>';
    for(const a of acts){
      const okk = a.result === 'ok' || a.result === 'started';
      const mark = a.result === 'fail' ? '<span style="color:var(--err)">✕</span>' : (okk ? '<span style="color:var(--ok)">✓</span>' : '·');
      const when = String(a.ts||'').slice(5,16);
      h += '<div class="ai-plan-row" style="font-size:12px;padding:1px 0">' +
           '<span class="ai-plan-n">' + esc(when) + '</span>' +
           '<span>' + mark + ' ' + esc(a.action||'') +
           (a.detail ? ' <span class="muted">' + esc(String(a.detail).slice(0,80)) + '</span>' : '') +
           (a.verify ? ' <span class="muted">' + esc(String(a.verify).slice(0,80)) + '</span>' : '') +
           '</span></div>';
    }
    recent.innerHTML = h;
  } else {
    recent.innerHTML = '<div class="muted" style="font-size:11px">' + esc(t('暂无守护动作记录')) + '</div>';
  }
}

async function detectSQL(){
  if(!loaded) return;
  const bd = $('#be_dir').value.trim();
  if(!bd || $('#sql_path').value.trim()) return;
  try{
    const d1 = await (await fetch('/api/ls?path='+encodeURIComponent(bd))).json();
    if((d1.dirs||[]).some(x=>x.toLowerCase()==='dbs')){
      const dbs = winJoin(bd,'dbs');
      const d2 = await (await fetch('/api/ls?path='+encodeURIComponent(dbs)+'&mode=file&ext=.sql')).json();
      if((d2.files||[]).length){ $('#sql_path').value = winJoin(dbs, d2.files[0]); markDirty(); refreshGuards(); }
    }
  }catch(e){}
}

/* ---------- 前置校验 ---------- */
function refreshGuards(){
  const fe=$('#fe_dir').value.trim(), be=$('#be_dir').value.trim(), db=$('#db_name').value.trim(), sql=$('#sql_path').value.trim();
  const set=(btnId,hintId,ok,hint)=>{ const b=$('#'+btnId); if(b) b.disabled=(!ok)||!!busyNow; const h=$('#'+hintId); if(h) h.textContent=ok?'':hint; };
  set('btn-init-db','h-init-db', !!db, '缺少：数据库名');
  if(sql==='') $('#h-init-db').textContent = db? '未选 SQL：将只建库' : '缺少：数据库名';
  set('btn-apply-sql-only','h-init-db', !!sql, '');
  set('btn-web-install','h-deps', !!fe, '缺少：前端目录');
  set('btn-backend-tidy','h-deps', !!be, '缺少：后端目录');
  if(fe || be) $('#h-deps').textContent = '';
  set('btn-web-start','h-web-start', !!fe, '缺少：前端目录');
  set('btn-backend-start','h-backend-start', !!be, '缺少：后端目录');
  $('#dep-state').textContent = t('前端 {0} · 后端 {1}', fe?t('已配置'):'—', be?t('已配置'):'—');
  const cd=$('#ch_dir').value.trim(), cst=$('#ch_start').value.trim(), wd=$('#ch_wdir').value.trim(), wst=$('#ch_wstart').value.trim();
  set('btn-start-chain','h-chain', !!(cd&&cst), '需先填链端目录与启动脚本');
  set('btn-start-webase','h-webase', !!(wd&&wst), '需先填 WeBASE 目录与启动脚本');
  ['btn-check','btn-reinstall','btn-db-test','btn-ssh-test','btn-ssh-exec','btn-chain-check','btn-ssh-term'].forEach(id=>{ const b=document.getElementById(id); if(b) b.disabled=!!busyNow; });
}

/* ---------- 检测 / 安装 ---------- */
function cardHTML(r){
  const yes=r.installed;
  const ver=r.version?`<div class="kv">${t('版本：')}${esc(r.version)}</div>`:'';
  const loc=r.location?`<div class="kv">${t('位置：')}${esc(r.location)}</div>`:(yes?'':'<div class="kv">' + t('未检测到') + '</div>');
  /* 失败项直接给"一键安装"：安装流程幂等，已装的会跳过，只补缺的 */
  const fix = yes ? '' : `<div style="margin-top:6px"><button class="mini" data-fix="1">${t('一键安装')}</button></div>`;
  return `<div class="comp"><div class="name">${esc(r.name)}<span class="badge ${yes?'yes':'no'}">${yes?t('已安装'):t('缺失')}</span></div>${ver}${loc}${fix}</div>`;
}
/* 检测卡片"一键安装"：事件委托，两个卡片容器都生效 */
document.addEventListener('click', async ev=>{
  const b = ev.target.closest('[data-fix]'); if(!b) return;
  try{
    const r = await fetch('/api/install',{method:'POST'});
    if(r.ok) toast(t('开始安装缺失组件，进度看下方安装日志'));
    else toast(t('有任务正在执行，请稍候'), 'err');
  }catch(e){ toast(t('请求失败：') + e, 'err'); }
});
function renderResults(rs){
  const html=(rs||[]).map(cardHTML).join('');
  $('#detect-cards').innerHTML = html || '<div class="muted">尚未检测</div>';
  $('#install-cards').innerHTML = html || '<div class="muted">尚未检测</div>';
}
let prevPhase = null;   // 检测 phase 迁移判定（桌宠差分表情用）
async function pollState(){
  try{
    const s = await (await fetch('/api/state')).json();
    renderResults(s.results);
    const ph=s.phase;
    /* checking→checked：检测完成，桌宠按结果做差分表情；→checking：检测开始，进忙碌态 */
    if(prevPhase==='checking' && ph==='checked'){
      const nf=(s.results||[]).filter(x=>!x.installed);
      try{ petCheckDone(nf.length, nf.map(x=>x.name).join('、')); }catch(e){}
    } else if(prevPhase!=='checking' && ph==='checking'){
      try{ petCheckStart(); }catch(e){}
    }
    prevPhase = ph;
    const d=$('#pg-detect'), c=$('#pg-config'), i=$('#pg-install');
    d.className='pseg'+(ph!=='idle'?' done':''); c.className='pseg'+(ph!=='idle'?' done':''); i.className='pseg'+(ph==='installing'?' run':(ph==='done'?' done':''));
    const ib=$('#install-banner');
    if(ph==='done'){
      const fails=(s.results||[]).filter(x=>!x.installed).length;
      if(fails>0){ ib.className='banner err'; ib.textContent=t('仍有 {0} 个组件未就绪', fails); }
      else { ib.className='banner ok'; ib.textContent='全部就绪，请重开终端使环境变量生效'; }
    }
    $('#btn-install').disabled = !!busyNow || (ph==='checking'||ph==='installing');
  }catch(e){}
}
$('#btn-check').onclick = ()=>fetch('/api/check',{method:'POST'});
$('#btn-install').onclick = ()=>fetch('/api/install',{method:'POST'});
$('#btn-reinstall').onclick = async ()=>{ if(await askConfirm('重新安装','将重新走一遍安装流程：重新下载并解压所有组件（已装的会跳过）。\n可能耗时较久。','开始重装')) fetch('/api/install',{method:'POST'}); };

/* ---------- 健康状态 ---------- */
function chip(id, cls, text){ const el=$('#'+id); el.className='chip '+cls; el.textContent=text; }
/* ---------- 操作审计：把审计流从"只有文件"变成页面上能看 ---------- */
const AUDIT_ACTOR = {user:'用户', ai:'AI', guard:'守护', system:'系统', shadow:'影子'};
let auditActor = '';
let auditDays = [];

function auditFillDays(days){
  if(!days || !days.length) return;
  const same = days.length === auditDays.length && days.every((d,i)=>d===auditDays[i]);
  auditDays = days;
  const sel = $('#audit-day'); if(!sel) return;
  if(same && sel.options.length) return;               // 没变化就别重建，否则会丢当前选择
  const cur = sel.value;
  sel.innerHTML = '';
  const o0 = document.createElement('option'); o0.value='all'; o0.textContent=t('全部日期'); sel.appendChild(o0);
  for(const d of days){
    const o = document.createElement('option'); o.value=d;
    o.textContent = d.slice(0,4)+'-'+d.slice(4,6)+'-'+d.slice(6,8);
    sel.appendChild(o);
  }
  sel.value = (cur && (cur==='all' || days.indexOf(cur)>=0)) ? cur : 'all';
}

async function loadAudit(){
  const tb = $('#audit-body'); if(!tb) return;
  const day = ($('#audit-day') && $('#audit-day').value) || 'all';
  let q = '/api/audit?n=500&day=' + encodeURIComponent(day) + (auditActor ? ('&actor='+auditActor) : '');
  let d = null;
  try{ d = await (await fetch(q)).json(); }
  catch(e){ tb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('读取失败'))+'</td></tr>'; return; }
  auditFillDays(d && d.days);
  const rows = (d && d.entries) || [];
  tb.innerHTML = '';
  const h = $('#h-audit'); if(h) h.textContent = t('{0} 条', rows.length);
  if(!rows.length){
    tb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('暂无审计记录'))+'</td></tr>';
    return;
  }
  for(const e of rows){
    const tr = document.createElement('tr');
    const rk = (e.result==='ok') ? 'ok' : ((e.result==='fail'||e.result==='denied') ? 'err' : 'warn');
    const col = rk==='ok' ? 'var(--ok)' : (rk==='err' ? 'var(--err)' : 'var(--warn)');
    let extra = '';
    if(e.dur_ms) extra += t('耗时 {0}ms', e.dur_ms);
    if(e.verify) extra += (extra ? '　' : '') + t('复验：{0}', e.verify);
    tr.innerHTML =
      '<td class="mini">'+esc(e.ts||'')+'</td>'+
      '<td>'+esc(AUDIT_ACTOR[e.actor] || e.actor || '')+'</td>'+
      '<td>'+esc(e.action||'')+'</td>'+
      '<td>'+esc(e.target||'')+'</td>'+
      '<td style="color:'+col+';font-weight:600">'+esc(e.result||'')+'</td>'+
      '<td>'+esc(((e.detail||'') + (extra ? ('　'+extra) : '')).trim())+'</td>';
    tb.appendChild(tr);
  }
}

function auditBind(){
  const map = {'audit-f-all':'', 'audit-f-user':'user', 'audit-f-ai':'ai', 'audit-f-guard':'guard', 'audit-f-system':'system', 'audit-f-shadow':'shadow'};
  for(const id in map){
    const b = $('#'+id); if(!b) continue;
    b.onclick = ()=>{
      auditActor = map[id];
      document.querySelectorAll('#panel-audit .mini').forEach(x=>x.classList.remove('on'));
      b.classList.add('on');
      loadAudit();
    };
  }
  const sel = $('#audit-day'); if(sel) sel.onchange = loadAudit;
  const rf = $('#audit-refresh'); if(rf) rf.onclick = ()=>{ loadAudit(); loadStats(); };
  const sr = $('#st-refresh'); if(sr) sr.onclick = loadStats;
  const sd = $('#st-days'); if(sd) sd.onchange = loadStats;
}

/* ---------- v2.7 统计看板：纯 SVG，不引图表库 ----------
 *
 * 「单文件零依赖」是本工具的硬约束，所以不用 Chart.js 之类。
 * 数据全部来自 /api/stats（由 stats.go 从审计与轨迹实时算）。
 *
 * 三条口径纪律在渲染侧同样要守：
 *  1. **无样本显式说明**，不画成 0
 *  2. **分母不同的指标不共用一根轴**（任务结局只覆盖 14 天，token 同理）
 *  3. **denied 不算失败**，图例单独一档
 */

/* stFmt 千分位；大数缩写（12.3k）——轴标签用，tooltip 用完整值 */
function stFmt(n){
  n = Number(n)||0;
  if(Math.abs(n) >= 1000000) return (n/1000000).toFixed(1).replace(/\.0$/,'')+'M';
  if(Math.abs(n) >= 1000)    return (n/1000).toFixed(1).replace(/\.0$/,'')+'k';
  return String(n);
}
function stFull(n){ return (Number(n)||0).toLocaleString('en-US'); }

/* stBars 画一组竖条。
 * data: [{label, values:{k:v}, tip}]，keys: [{k,color,name}]
 * 用 SVG 而不是 canvas：可缩放、能加<title> 悬浮提示、样式跟随 CSS 变量。 */
function stBars(box, days, keys, opt){
  opt = opt || {};
  const el = typeof box === 'string' ? $(box) : box;
  if(!el) return;
  if(!days || !days.length){ el.innerHTML = '<div class="hint">'+esc(t('暂无数据'))+'</div>'; return; }

  const W = 100, H = 100;                 // viewBox 单位
  // padL 要够宽容纳 Y 轴语义：这里不画数值刻度（数值在 tooltip 与 KPI 里），
  // 但**必须留出左边距**，否则第一根柱子会贴边甚至被 viewBox 裁掉。
  // 用 preserveAspectRatio=none 横向拉伸时，viewBox 里的百分比是相对宽度算的，
  // padL=2 相当于实际 2% 宽——太窄。
  const padL = opt.padL==null ? 4 : opt.padL, padR = 2, padB = 5, padT = 3;
  const iw = W - padL - padR, ih = H - padT - padB;

  // 顶部留白按最大值留 12%，避免柱子顶到边
  let max = 0;
  days.forEach(d => keys.forEach(k => { const v = Number(d[k.k])||0; if(v>max) max=v; }));
  if(max <= 0){
    el.innerHTML = '<div class="hint">'+esc(t('区间内没有可统计的数据'))+'</div>';
    return;
  }
  const top = max * 1.12;
  const n = days.length;
  const slot = iw / n;
  const bw = Math.max(slot * 0.62, 0.35);

  let bars = '', grid = '';
  // 横向参考线：0 / 50% / 100%
  [0, 0.5, 1].forEach(f => {
    const y = padT + ih * (1 - f);
    grid += '<line x1="'+padL+'" y1="'+y.toFixed(2)+'" x2="'+(W-padR)+'" y2="'+y.toFixed(2)+
            '" stroke="var(--line)" stroke-width="0.3" vector-effect="non-scaling-stroke"/>';
  });

  days.forEach((d, i) => {
    const x = padL + slot * i + (slot - bw) / 2;
    let stackY = padT + ih;
    keys.forEach(k => {
      const v = Number(d[k.k])||0;
      if(v <= 0) return;
      const h = (v / top) * ih;
      stackY -= h;
      bars += '<rect x="'+x.toFixed(2)+'" y="'+stackY.toFixed(2)+'" width="'+bw.toFixed(2)+
              '" height="'+h.toFixed(2)+'" fill="'+k.color+'" rx="0.3">'+
              '<title>'+esc(d.date)+' · '+esc(t(k.name))+': '+stFull(v)+'</title></rect>';
    });
  });

  // X 轴标签：**画在 HTML 层，不放进 SVG。**
  //
  // ## 为什么不能放在 SVG 里（v2.8 修）
  //
  // 原来是 `<text>` + `preserveAspectRatio="none"` + `viewBox="0 0 100 100"`。
  // 那个组合有两个坏处：
  //
  //   1. **`preserveAspectRatio=none` 会把整个坐标系横向拉伸** ——
  //      **文字也跟着变形**（横向拉长/压扁）。
  //      用户看到的「不清楚」主要就是这个。
  //   2. `font-size:2.4` 是 viewBox 单位。宽度 400px 时实际约 `2.4 * 4 = 9.6px`，
  //      太小；而高度被压到 120px，字号在不同宽度下**还会变**。
  //
  // 标签挪到 HTML 后：字号是 CSS 像素（不随图宽变），
  // 而且**能换行/用 CSS 类**，深浅色自动跟随。
  const LAB = 13;                       // 最多几个标签
  const every = Math.max(1, Math.ceil(n / LAB));
  const showAt = new Set();
  // 从 0 开始按 every 取，最后一天必标（n<=3 时 every=1，自然全标）
  for (let i = 0; i < n; i += every) showAt.add(i);
  showAt.add(n - 1);

  // 日期显示。**这里踩过一个一直存在的真 bug**：
  //
  // 后端 `stats.go:157` 用的是 `Format("20060102")` —— **没有分隔符**，
  // 所以 `d.date` 是 `'20260908'`。
  // 而旧代码写的是 `d.date.slice(5)`（那是按 `'2006-01-02'` 算的），
  // 于是从 `'20260908'` 上slice(5) 得到 `'0908'` ——
  // **跨月时更糟**：`'20261001'.slice(5)` 得到 `'0001'`，
  // 整个 10 月看起来像「1 月」。
  //
  // 用户反馈的「横坐标不清楚」底下藏着这个：
  // 不是字体问题，**是日期本身显示错了**。
  //
  // 口径：按真实格式 `'YYYYMMDD'` 解析，跨年时补年份。
  const thisYear = days[n - 1].date.slice(0, 4);
  const fmtDay = ds => {
    if (ds.length < 8) return ds;         // 不是预期格式就原样显示，不瞎猜
    const y = ds.slice(0, 4), m = ds.slice(4, 6), d = ds.slice(6, 8);
    return y === thisYear ? m + '-' + d : y.slice(2) + '/' + m + '-' + d;
  };

  let xlab = '<div class="stXlab">';
  days.forEach((d, i) => {
    if (!showAt.has(i)) return;
    const x = padL + slot * i + slot / 2;
    // 位置用百分比，与 SVG 里算出来的 x 一致 —— SVG 是拉伸的，
    // 但 padding 与 slot 都是 viewBox 单位的**比例**，换算成 % 正好对齐。
    let pct = x / W * 100;
    // 首末标签 translateX(-50%) 会溢出容器 —— 真实修正放在插入之后
    // （用实测像素宽度，只改 transform 不重排，理由见下方注释）。
    xlab += '<span style="left:'+pct.toFixed(3)+'%">'
          + esc(fmtDay(d.date)) + '</span>';
  });
  xlab += '</div>';

  el.innerHTML = '<svg viewBox="0 0 '+W+' '+H+'" preserveAspectRatio="none" '+
    'style="width:100%;height:'+(opt.height||120)+'px;display:block" role="img">'+
    grid + bars + '</svg>' + xlab;

  // ## 贴边修正：用实测像素宽度，**不重排**
  //
  // ## 为什么不能在渲染前算
  //
  // 标签宽是**像素**（约 17.8px），而位置是**百分比**。
  // 换算要知道容器宽度 —— 此刻DOM 还没插入，拿不到。
  //
  // 第一版拍了个「夹 3%」，CDP 实测 **overflowL = 17.3px**：
  // 3% 约 18px，一半 9px < 需要的 8.9px… 边缘上，
  // 而且**容器宽度会随窗口变**，拍死的百分比必然在某个宽度下失效。
  //
  // 所以插入后量一次真实边界，只改 `transform` ——
  // **不重写 `left`，因为那会触发二次布局、也可能与下次渲染不一致**。
  const labEl = el.querySelector('.stXlab');
  if (labEl && labEl.children.length) {
    const cb = labEl.getBoundingClientRect();
    const spans = labEl.querySelectorAll('span');
    const first = spans[0], last = spans[spans.length - 1];
    if (cb.width > 0 && first && last) {
      // **必须用 calc 保住 -50%**。
      //
      // 直接写 `transform = translateX(Npx)` 会**整个覆盖掉 CSS 里的
      // `translateX(-50%)`**，标签于是跳回未居中的位置 ——
      // 实测 overflowL 从 17.3 变成 22.5，越修越糟。
      const f = first.getBoundingClientRect();
      if (f.left < cb.left - 0.5) {
        first.style.transform =
          'translateX(calc(-50% + ' + (cb.left - f.left).toFixed(1) + 'px))';
      }
      const l = last.getBoundingClientRect();
      if (l.right > cb.right + 0.5) {
        last.style.transform =
          'translateX(calc(-50% + ' + (cb.right - l.right).toFixed(1) + 'px))';
      }
    }
  }
}

/* stHBar 横向条形图（用于主体分布、高频动作——标签长，横向更好读）*/
function stHBar(box, items, color){
  const el = typeof box === 'string' ? $(box) : box;
  if(!el) return;
  if(!items || !items.length){ el.innerHTML = '<div class="hint">'+esc(t('暂无数据'))+'</div>'; return; }
  const max = Math.max.apply(null, items.map(i=>i.v)) || 1;
  el.innerHTML = items.map(i => {
    const pct = (i.v / max * 100).toFixed(1);
    return '<div style="display:flex;align-items:center;gap:8px;margin:4px 0;font-size:12px">'+
      '<span style="width:104px;flex:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" '+
      'title="'+esc(i.k)+'">'+esc(i.k)+'</span>'+
      '<span style="flex:1;background:var(--off);border-radius:4px;height:9px;overflow:hidden">'+
      '<i style="display:block;height:100%;width:'+pct+'%;background:'+(i.color||color)+';border-radius:4px"></i></span>'+
      '<b style="width:52px;text-align:right;flex:none;font-variant-numeric:tabular-nums">'+stFull(i.v)+'</b>'+
      '</div>';
  }).join('');
}

async function loadStats(){
  const box = $('#st-kpis');
  if(!box) return;
  const days = ($('#st-days') && $('#st-days').value) || '30';
  // 区间标题跟着下拉走——不更新就会出现「标题写 30 天、图画的是 7 天」
  const rng = $('#st-range'); if(rng) rng.textContent = t('近 {0} 天', days);
  let d = null;
  try{ d = await (await fetch('/api/stats?days='+encodeURIComponent(days))).json(); }
  catch(e){
    box.innerHTML = '<div class="hint">'+esc(t('读取统计失败：{0}', e))+'</div>';
    return;
  }
  const t2 = d.totals || {}, ds = d.days || [];

  /* ---- KPI 卡（v2.7.1：4 张，正好一行）----
   *
   * 去掉「失败」「被拦下」「任务数」三张：
   * 它们的数字**已经在下面的图表里**（结果分布堆叠条 + 图例数字），
   * 再放一张卡是重复；而「Token 消耗」是 v2.7 才有数据的新指标，
   * 样本还只有十几个，摆在最显眼的位置反而像结论。
   *
   * 留这 4 张的理由：它们是**图表画不出来的**——
   * 操作量给分母、复验率给质量、延迟给性能、Token 给成本。
   */
  const vr = t2.verify_rate_has_sample
    ? {v: (t2.verify_rate*100).toFixed(1)+'%', cls:'', s: t('通过 {0} / {1} 次复验', stFull(t2.verify_ok), stFull(t2.verify_count))}
    : {v: t('无样本'), cls:'none', s: t('区间内没有带复验结论的记录')};
  const kpis = [
    {k:t('操作总数'), v: stFull(t2.ops), cls:'', s: t('{0} 天有记录', t2.audit_days_available)},
    {k:t('复验通过率'), v: vr.v, cls: vr.cls, s: vr.s},
    {k:t('延迟 p50'), v: t2.latency_p50? stFull(t2.latency_p50)+' ms': t('无数据'),
     cls: t2.latency_p50?'':'none', s: t('p90 {0} ms', stFull(t2.latency_p90))},
    {k:t('Token 消耗'), v: t2.token_samples? stFmt(t2.token_total): t('无数据'),
     cls: t2.token_samples?'':'none',
     s: t2.token_samples
        ? (t2.token_has_cache && t2.token_prompt
            // 有缓存细分时把命中率写进去 —— 它是判断成本的关键，
            // 而原来那张 token 图被移除后，这个信息不该一起丢
            ? t('缓存命中 {0}%　输入 {1} · 输出 {2}',
                ((t2.token_cache_hit/t2.token_prompt)*100).toFixed(0),
                stFmt(t2.token_prompt), stFmt(t2.token_completion))
            : t('输入 {0} · 输出 {1}', stFmt(t2.token_prompt), stFmt(t2.token_completion)))
        : t('v2.7 起才记录')},
  ];
  box.innerHTML = kpis.map(x =>
    '<div class="kpi '+x.cls+'"><div class="k">'+esc(x.k)+'</div>'+
    '<div class="v">'+esc(x.v)+'</div><div class="s">'+esc(x.s)+'</div></div>').join('');

  /* ---- 口径提醒（无样本必须说出来，不能画成 0）---- */
  const notes = $('#st-notes');
  const ns = d.notices || [];
  if(notes){
    if(ns.length){
      notes.style.display='';
      notes.innerHTML = ns.map(x=>'<li>'+esc(x)+'</li>').join('');
    } else notes.style.display='none';
  }

  /* ---- 操作量与结果 ---- */
  const RKEY = [
    {k:'ok',       color:'var(--ok)',   name:'成功'},
    {k:'fail',     color:'var(--err)',  name:'失败'},
    {k:'denied',   color:'var(--warn)', name:'被拦下'},
    {k:'started',  color:'var(--info)', name:'已启动'},
    // v3.0 T1.6：未证明恢复单列。它进分母（复验率的分母是"复验过次数"），
    // 但不进"失败"——把"还没验到"画成失败，看板就在教人误读。
    {k:'unverified', color:'var(--muted)', name:'未证明恢复'},
  ];
  stBars('#st-ops-chart', ds, RKEY, {height:130});
  const stack = $('#st-ops-stack');
  if(stack){
    const tot = t2.ops || 1;
    stack.innerHTML = RKEY.map(k =>
      '<i style="width:'+((t2[k.k]||0)/tot*100).toFixed(2)+'%;background:'+k.color+'" '+
      'title="'+esc(t(k.name))+': '+stFull(t2[k.k]||0)+'"></i>').join('');
    // 图例后面的数字也更新
    document.querySelectorAll('#panel-audit .lgnv').forEach((el,i)=>{
      el.textContent = stFull(t2[RKEY[i].k]||0);
    });
  }
  const tip = $('#st-ops-tip');
  if(tip) tip.textContent = t('日均 {0} 次', (t2.ops/Math.max(1,t2.audit_days_available)).toFixed(0));

  /* ---- v2.7.1：任务结局与 Token 两张图已从界面移除（DOM 与渲染代码一并删掉）----
   *
   * 删而不只是隐藏：留着渲染代码查空元素是死代码，
   * 而空元素上`getElementById` 返回 null、`.innerHTML=` 会抛——
   * 第一版若只删 DOM 不删代码，页面会报 TypeError。
   *
   * **数据仍从 /api/stats 返回**（stats.go 的统计与测试全部保留），
   * 将来样本够了（轨迹 14 天覆盖、token 样本上百）再加回来即可。
   *
   * token 的缓存命中率挪进了 KPI 副标题 —— 它是成本判断的关键，
   * 值得留下，但不值得单独占一张图。
   */

  /* ---- 主体分布 ---- */
  const ACTOR_NAME = {user:'用户', ai:'AI', guard:'守护', system:'系统', shadow:'影子'};
  const ac = t2.by_actor || {};
  stHBar('#st-actor-chart',
    Object.keys(ac).sort((a,b)=>ac[b]-ac[a])
      .map(k=>({k: t(ACTOR_NAME[k]||k), v: ac[k], color: k==='ai'?'var(--brand)':'var(--info)'})));

  /* ---- 高频动作 Top 8 ---- */
  const acts = t2.actions || {};
  stHBar('#st-action-chart',
    Object.keys(acts).sort((a,b)=>acts[b]-acts[a]).slice(0,8)
      .map(k=>({k, v: acts[k], color:'var(--muted)'})));
}

/* ---------- 启动方式推断展示（v2.1） ---------- */
// 把服务端推断结果与合法候选显示出来，并提供"一键采用"。
// 用户不填也能启动（走推断），填了就永久覆盖——这比让 AI 去猜可靠得多。
async function loadLaunchPlan(){
  const brief = $('#launch-brief'), cands = $('#launch-cands');
  if(!brief || !cands) return;
  let d = null;
  try{ d = await (await fetch('/api/launch')).json(); }catch(e){ return; }
  if(!d || d.ok === false) return;
  const p = d.plan || {};
  const L = [];
  L.push(t('前端：{0}', p.web_script ? ('npm run ' + p.web_script + t('（{0}）', launchSrc(p.web_source)))
                                     : t('未识别，不可自动启动')));
  L.push(t('后端：{0}', p.backend_file ? ('go run ' + p.backend_file) : t('未识别，不可自动启动')));
  if(p.note) L.push(p.note);
  brief.textContent = L.join('\n');
  brief.style.whiteSpace = 'pre-line';

  // 合法候选：点一下就填进输入框，省得手打
  cands.innerHTML = '';
  const all = p.web_all || [];
  if(all.length){
    const lab = document.createElement('span');
    lab.className = 'hint'; lab.textContent = t('合法候选：');
    cands.appendChild(lab);
    for(const s of all){
      const b = document.createElement('button');
      b.className = 'mini'; b.textContent = s;
      b.onclick = ()=>{ const inp = $('#cfg_web_script'); if(inp){ inp.value = s; markDirty(); toast(t('已填入 {0}，记得保存配置', s), 'ok'); } };
      cands.appendChild(b);
    }
  }
  if((p.blocked||[]).length){
    const w = document.createElement('span');
    w.className = 'hint';
    w.style.color = 'var(--warn)';
    w.textContent = t('已按安全规则拒绝：{0}', p.blocked.join('、'));
    cands.appendChild(w);
  }
}

function launchSrc(s){
  return s === 'config' ? t('你指定的') : (s === 'package.json' ? t('package.json 推断') : t('未识别'));
}

/* 侧栏导航不再用状态点：状态看顶部胶囊、步骤条和右侧运行状态卡。 */

// ---------- 记忆与经验（M1 自动经验 / M3 用户记忆） ----------

const LESSON_KIND = {
  repeat_fail:'反复失败', user_fix:'你手动修好的', denied:'已被安全策略拒绝',
  slow:'慢路径', verify_fail:'复验反复失败'
};

// memSyncGate 记忆层总开关关闭时给出显式提示——
// 否则用户会困惑"我明明写了记忆，助手怎么还是不知道"。
function memSyncGate(){
  const box = $('#mem-gate-off');
  if(!box) return;
  const cb = document.getElementById('ai_memory_on');
  box.style.display = (cb && !cb.checked) ? '' : 'none';
}

/* loadMemory 取「有什么记忆 / 有什么经验」并渲染两张表。
   v3.7.1 起不再支持传入假设任务：界面上那行「试算」已删除（它逼用户先编一句
   假任务才能看到注入情况，实测没人用）。于是注入口径退化为不带任务的那种：
   Always 的记忆每轮都注入，其余按需——统计条据此改成永远成立的事实。 */
async function loadMemory(){
  const mb = $('#mem-body'), lb = $('#les-body');
  if(!mb || !lb) return;
  let d = null;
  try{ d = await (await fetch('/api/lessons')).json(); }
  catch(e){
    mb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('读取失败'))+'</td></tr>';
    return;
  }
  // 我的记忆
  const ms = (d && d.memories) || [];
  const lss = (d && d.lessons) || [];
  const nAlways = ms.filter(m=>m.injected).length;
  const sm = $('#mem-sum'), sl = $('#les-sum');
  if(sm) sm.textContent = ms.length ? t('共 {0} 条 · {1} 条每轮注入', ms.length, nAlways) : '';
  if(sl) sl.textContent = lss.length ? t('共 {0} 条', lss.length) : '';
  mb.innerHTML = '';
  if(!ms.length){
    mb.innerHTML = '<tr><td colspan="6" class="muted" style="padding:14px;text-align:center">'+esc(t('还没有记忆条目'))+'</td></tr>';
  } else {
    for(const m of ms){
      const tr = document.createElement('tr');
      // 失效的记忆不删除、只标出来——用户需要知道它为什么不再生效
      const dimStale = m.stale ? ' style="opacity:.5"' : '';
      tr.title = m.stale ? (m.stale_why || '') : (m.reason || '');
      tr.innerHTML =
        '<td style="text-align:center;color:var(--warn);font-weight:700">'+(m.always?'★':'')+'</td>'+
        '<td'+dimStale+'>'+esc(m.text||'')+(m.stale?' <span class="mini" style="color:var(--warn)">['+esc(t('已失效'))+']</span>':'')+'</td>'+
        '<td class="mini muted">'+esc(m.tags||'')+'</td>'+
        '<td class="mini muted">'+esc((m.created||'').slice(5,16))+'</td>'+
        '<td class="mini"></td>'+
        '<td></td>';
      // 范围列：单按钮切换（本项目 / 全局）
      const scopeTd = tr.children[4];
      const sb = document.createElement('button');
      sb.className = 'mini' + (m.global ? ' on' : '');
      sb.textContent = m.global ? t('全局') : t('本项目');
      sb.title = m.global ? t('任何项目都生效') : t('换个项目后自动失效');
      sb.onclick = async ()=>{ await memPost({op:'global', id:m.id, global:!m.global}); loadMemory(); };
      scopeTd.appendChild(sb);
      const td = tr.lastElementChild;
      td.style.whiteSpace = 'nowrap';
      const b1 = document.createElement('button');
      // 按钮要写**动作**（点了会发生什么），不能只写状态词：原来 always=false 时显示
      // 「每轮注入」，读起来像"当前已经在每轮注入"，于是用户以为自己写的规矩一直在生效。
      b1.className = 'mini'; b1.textContent = m.always ? t('改为按需注入') : t('设为每轮注入');
      b1.style.marginRight = '4px';
      b1.onclick = async ()=>{ await memPost({op:'toggle', id:m.id}); loadMemory(); };
      const b2 = document.createElement('button');
      b2.className = 'mini'; b2.textContent = t('删除');
      b2.onclick = async ()=>{ if(!confirm(t('确定删除这条记忆？'))) return; await memPost({op:'delete', id:m.id}); loadMemory(); };
      td.appendChild(b1); td.appendChild(b2);
      mb.appendChild(tr);
    }
  }
  // 自动经验
  const ls = (d && d.lessons) || [];
  lb.innerHTML = '';
  if(!ls.length){
    lb.innerHTML = '<tr><td colspan="7" class="muted" style="padding:14px;text-align:center">'+esc(t('暂无经验，失败与被拒的记录累积到一定量后会自动出现'))+'</td></tr>';
    return;
  }
  for(const l of ls){
    const tr = document.createElement('tr');
    const dim = l.enabled ? '' : ' style="opacity:.45"';
    const ev = (l.evidence && l.evidence.length) ? '<div class="mini muted" style="margin-top:3px">'+esc(t('依据：{0}', l.evidence.join('　·　')))+'</div>' : '';
    // 成败：0/0 时显示占位而不是 0/0 —— 没人提取到新经验时会误导成"零次"
    const of = (l.succ||0) + '/' + (l.fail||0);
    const ofText = (l.succ||l.fail) ? of : '—';
    // 置信度分档着色，让"仅供参考"在视觉上就弱一档
    const cf = l.conf_label || '';
    const cfColor = cf==='高' ? 'var(--ok)' : (cf==='中' ? 'var(--warn)' : 'var(--muted)');
    // 适用范围 + 失效理由
    let scopeCell = esc(scopeLabelOf(l));
    if(l.stale){
      scopeCell = '<span style="color:var(--warn)">'+esc(t('已失效'))+'</span>';
      if(l.stale_why) scopeCell += '<div class="mini muted" style="margin-top:2px">'+esc(l.stale_why)+'</div>';
    }
    tr.innerHTML =
      '<td class="mini">'+esc(LESSON_KIND[l.kind] || l.kind || '')+'</td>'+
      '<td'+dim+'>'+esc(l.title||'')+ev+'</td>'+
      '<td class="mini" style="text-align:center;white-space:nowrap">'+esc(ofText)+'</td>'+
      '<td class="mini" style="text-align:center;white-space:nowrap;color:'+cfColor+'">'+esc(cf||'—')+'</td>'+
      '<td class="mini">'+scopeCell+'</td>'+
      '<td class="mini" style="text-align:center">'+esc(String(l.hits||0))+'</td>'+
      '<td></td>';
    const td = tr.lastElementChild;
    const b = document.createElement('button');
    b.className = 'mini' + (l.enabled ? ' on' : '');
    b.textContent = l.enabled ? t('已采用') : t('已否决');
    b.onclick = async ()=>{
      const r = await memPost({op:'__lesson', id:l.id}, '/api/lesson/toggle');
      if(r && r.ok) loadMemory();
    };
    td.appendChild(b);
    lb.appendChild(tr);
  }
}

// scopeLabelOf 把经验的作用域压成一行短文本。
function scopeLabelOf(l){
  const sc = l.scope || {};
  const parts = [];
  const who = [sc.frontend, sc.backend].filter(Boolean).join('/');
  parts.push(who || t('本项目'));
  if(sc.action) parts.push(sc.action);
  return parts.join(' · ');
}

async function memPost(body, url){
  url = url || '/api/memory';
  try{
    const r = await (await fetch(url, {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)})).json();
    if(r && r.ok === false && r.error) toast(r.error, 'err');
    return r;
  }catch(e){ toast(t('请求失败'), 'err'); return null; }
}

function memBind(){
  const add = $('#mem-add');
  const submit = async ()=>{
    const inp = $('#mem-text'), tg = $('#mem-tags'), al = $('#mem-always'), gl = $('#mem-global');
    // 输入框是多行 textarea（粘贴一段说明很容易带换行）。记忆会被拼成 "- 内容"
    // 的单行条目注入，所以换行必须压成空格，否则一条记忆会被渲染成一堆碎行。
    const text = (inp.value||'').replace(/\s*[\r\n]+\s*/g, ' ').trim();
    if(!text){ toast(t('请先填写记忆内容'), 'err'); return; }
    const r = await memPost({op:'add', text: text, tags:(tg.value||'').trim(), always: !!(al && al.checked)});
    if(r && r.ok){
      inp.value=''; tg.value='';
      if(al) al.checked=false;
      inp.focus();   // 连续写第二条时不用再点一次输入框
      // 全局勾选保持原状：用户可能正在批量写通用规矩
      loadMemory();
    }
    if(gl && gl.checked) toast(t('已记为全局适用（换项目也生效）'), 'ok');
  };
  if(add) add.onclick = submit;
  // 多行框里 Enter 是换行，Ctrl/⌘+Enter 才是提交（与对话输入框一致的操作习惯）
  const mi = $('#mem-text');
  if(mi) mi.addEventListener('keydown', e=>{
    if(e.key === 'Enter' && (e.ctrlKey || e.metaKey)){ e.preventDefault(); submit(); }
  });
  const ib = $('#mem-import-btn');
  if(ib) ib.onclick = async ()=>{
    const text = prompt(t('粘贴要导入的记忆，每行一条：'));
    if(!text) return;
    const r = await memPost({text: text}, '/api/memory/import');
    if(r && r.ok){ toast(r.msg, 'ok'); loadMemory(); }
  };
  const rf = $('#mem-refresh');
  if(rf) rf.onclick = loadMemory;
  const toggle = $('#btn-ai-memory');
  if(toggle) toggle.onclick = ()=>{
    const box = $('#ai-memory-box');
    if(!box) return;
    const show = box.style.display === 'none';
    box.style.display = show ? '' : 'none';
    if(show){ memSyncGate(); loadMemory(); }
  };
  // 记忆层总开关：勾选状态变化即时反映到面板提示（改动会自动保存，无需手动点保存）
  const gate = document.getElementById('ai_memory_on');
  if(gate) gate.addEventListener('change', memSyncGate);
  memSyncGate();
}
async function pollHealth(){
  let h; try{ h = await (await fetch('/api/health')).json(); }catch(e){ return; }
  /* 忙碌互斥 */
  busyNow = h.busy || '';
  const bb = $('#busy-banner');
  if(busyNow){ bb.style.display=''; bb.textContent=t('正在执行：{0} …（完成前其它操作已禁用）', busyNow); }
  else { bb.style.display='none'; }
  /* 顶部状态条 */
  if(!h.env.hasResults) chip('chip-env','off','环境 未检测');
  else chip('chip-env', h.env.ready?'ok':'err', h.env.ready?'环境 就绪':'环境 缺失');
  if(h.db.connected) chip('chip-db', h.db.dbExists?'ok':'warn', h.db.dbExists?'数据库 就绪':'数据库 未建库');
  else if(h.db.msg==='未检测') chip('chip-db','off','数据库 未检测');
  else chip('chip-db','err','数据库 连接失败');
  const depsOk = h.deps.frontend && h.deps.backend;
  chip('chip-deps', depsOk?'ok':'warn', depsOk?'依赖 就绪':'依赖 待办');
  /* v2.7 状态灯以 verified 为条件，不再只看 running。
   *
   * **改之前**：svcOk = web.running || backend.running
   * 那样端口被别人的服务占着、进程在但没通过复验，一样显示绿——
   * **等于把「执行不等于成功」在视觉上抹掉**，
   * 前面所有验证层的努力在界面上等于零。
   *
   * 现在：running 但未 verified → 黄（降级），明确区分「没起来」与「起来了但没验过」。
   */
  const svcState = aggregateSvcState(h.services);
  chip('chip-svc', svcState.cls, svcState.label);
  /* 权限 */
  $('#chip-admin').style.display = h.admin?'':'none';
  $('#btn-elevate').style.display = h.admin?'none':'';
  /* 侧栏导航已不再用状态点：状态看顶部胶囊、步骤条和右侧运行状态卡 */
  /* 服务卡：同样以 verified 为条件 */
  const w=h.services.web, b=h.services.backend;
  const sw=$('#su-web'), sb=$('#su-backend');
  sw.textContent = svcLabel(w); sw.className = 'svcstate' + svcDotCls(w);
  sb.textContent = svcLabel(b); sb.className = 'svcstate' + svcDotCls(b);
  const lw=$('#sl-web');
  if(w.running && w.url){ lw.style.display=''; lw.href=w.url; lw.textContent=t('打开 ↗'); lw.title=w.url; }
  else { lw.style.display='none'; }
  const lb=$('#sl-backend');
  if(b.running && b.url){ lb.style.display=''; lb.href=b.url; lb.textContent=t('打开 ↗'); lb.title=b.url; }
  else { lb.style.display='none'; }
  applyBreaker('web', w);
  applyBreaker('backend', b);
  $('#btn-stop-all').disabled = !svcState.running;
  $('#db-test-msg').textContent = h.db.connected ? (h.db.Version||'') + (h.db.dbExists?' · 库已存在':' · 库不存在') : (h.db.msg||'');
  updateChain(h.chain);
  /* 问题清单（状态源算好的，含「该怎么办」） */
  renderIssues(h.issues);
  /* 四步进度指引：done=该步就绪；第一个未就绪的步高亮为"当前步" */
  const chainDone = !!(h.chain && h.chain.checked);
  const gSteps = [
    ['gs-1', h.env.hasResults && h.env.ready],
    ['gs-2', h.db.connected && h.db.dbExists && depsOk],
    ['gs-3', chainDone],
    ['gs-4', svcState.running]
  ];
  let gCur = false;
  gSteps.forEach(([id, done])=>{
    const el = document.getElementById(id); if(!el) return;
    el.classList.toggle('done', !!done);
    const cur = !done && !gCur; gCur = gCur || cur;
    el.classList.toggle('cur', cur);
  });
  refreshGuards();
}

/* ---------- 服务状态的呈现：running 与 verified 分开 ---------- */

/* applyBreaker 熔断可见性 + 复位入口。
 *
 * 为什么必须单独显示：熔断时服务确实是"没起来"，但原因是**产品拦住了崩溃循环**，
 * 不是启动失败。两者混成同一种红，用户只会反复点"启动"，而每一次都被断路器拒绝
 * ——界面不把拒绝说出来，就等于让用户跟产品对着撞。
 *
 * 复位按钮必须有人工出口：冷静期只是节流，不是监狱。 */
function applyBreaker(key, s){
  const row = document.getElementById('brk-' + key);
  if(!row) return;
  if(!s || !s.breaker_tripped){ row.style.display='none'; return; }
  row.style.display='';
  let txt = document.querySelector('#brk-' + key + ' .brk-txt');
  if(!txt){ txt = document.createElement('span'); txt.className='brk-txt'; row.insertBefore(txt, row.firstChild); }
  const secs = s.breaker_remain_secs || 0;
  txt.textContent = secs > 0
    ? t('已停止自动重启（连续失败达上限）· {0} 秒后放行一次探针', secs)
    : t('已停止自动重启（连续失败达上限）');
}

/* 复位断路器：人工确认崩溃原因已处理后解除熔断。
 * 必须 POST 到后端而不是本地改变量——否则界面说"已复位"而断路器还在拦，
 * 那是界面在替产品撒谎。走统一的 post()，失败由它弹 toast。 */
document.querySelectorAll('[data-brk]').forEach(btn=>{
  btn.addEventListener('click', async ()=>{
    const key = btn.getAttribute('data-brk');
    btn.disabled = true;
    const r = await post('/api/program/breaker-reset', {service:key});
    if(r && r.ok){
      let j = {}; try{ j = await r.json(); }catch(e){}
      toast(j.reset ? t('断路器已复位，恢复接受自动重启') : t('当前不在熔断态，无需复位'), j.reset ? 'ok' : '');
    }
    btn.disabled = false;
    pollHealth();
  });
});

/* svcDotCls 单个服务的状态点颜色。
 * 绿 = 跑着**且验过**；黄 = 跑着但没验过（或在启动中）；红 = 失败；灰 = 停止。
 * 后端 ServiceState.phase 已给出五态，这里只做映射，不再自己推断。 */
function svcDotCls(s){
  if(!s) return '';
  if(s.phase==='failed') return ' err';
  if(s.phase==='degraded') return ' warn';   // 进程在但未通过复验
  if(s.phase==='starting') return ' warn';
  if(s.phase==='running') return s.verified ? ' ok' : ' warn';
  return '';
}

/* svcLabel 服务状态文案。**degraded 必须说清是「没验过」**，
 * 显示「运行中」等于把未验证说成已验证。 */
function svcLabel(s){
  if(!s) return t('未启动');
  switch(s.phase){
    case 'running': return s.verified ? t('运行中 · 已复验') : t('运行中 · 未复验');
    case 'starting': return t('启动中');
    case 'degraded': return t('运行中 · 未通过复验');
    case 'failed': return t('失败');
    default: return t('未启动');
  }
}

/* aggregateSvcState 汇总两个服务成一个顶部灯。
 * 口径：**全部已复验才绿**；有跑着的但没验过 → 黄；全停 → 灰。 */
function aggregateSvcState(services){
  const list = [services && services.web, services && services.backend].filter(Boolean);
  if(!list.length) return {cls:'off', label:t('服务 未启动'), running:false};
  const running = list.some(s => s.running);
  const failed  = list.some(s => s.phase === 'failed');
  const allVerified = list.every(s => s.verified);
  if(failed) return {cls:'err', label:t('服务 异常'), running:running};
  if(running && allVerified) return {cls:'ok', label:t('服务 已就绪'), running:true};
  if(running) return {cls:'warn', label:t('服务 未复验'), running:true};
  return {cls:'off', label:t('服务 未启动'), running:false};
}

/* renderIssues 渲染状态源算好的问题清单。
 * 每条带「该怎么办」——只报问题不给下一步，等于把负担推回给用户。 */
function renderIssues(issues){
  const box = $('#issues-box'), list = $('#issues-list');
  if(!box || !list) return;
  const arr = Array.isArray(issues) ? issues : [];
  if(!arr.length){ box.style.display='none'; return; }
  box.style.display='';
  list.innerHTML = arr.map(i=>{
    const cls = i.level==='error' ? 'err' : (i.level==='warn' ? 'warn' : 'info');
    return '<li class="'+cls+'"><b>'+esc(i.what)+'</b>'+
           (i.action ? '<span class="act">'+esc(t('建议：{0}', i.action))+'</span>' : '')+'</li>';
  }).join('');
}

/* ---------- 链端校验 ---------- */
function updateChain(ch){
  ch = ch || {};
  const dot=(id,on)=>{ const d=document.getElementById(id); if(d) d.className='dot'+(on?' ok':''); };
  dot('cd-20200', ch.checked && ch.port20200);
  dot('cd-5002', ch.checked && ch.port5002);
  const cs20200 = document.getElementById('cs-20200');
  if(!ch.checked){ cs20200.textContent='未检测'; }
  else if(ch.port20200 && ch.chainExtReach){ cs20200.textContent=t('可达 · {0}ms', ch.lat20200||0); }
  else if(ch.port20200 && ch.chainBindLocal){ cs20200.textContent='本地监听（公网不可达·已加固）'; }
  else { cs20200.textContent='不通'; }
  document.getElementById('cs-5002').textContent = !ch.checked ? '未检测' : (ch.port5002? t('可达 · {0}ms', ch.lat5002||0) : '不通');
  document.getElementById('m-block').textContent = ch.blockNumber || '—';
  document.getElementById('m-tx').textContent = ch.txCount || '—';
  document.getElementById('m-ver').textContent = ch.clientVer || '—';
  const mp=document.getElementById('m-procs');
  if(mp) mp.textContent = !ch.checked ? '—' : (ch.nodeProcs===undefined || ch.nodeProcs<0 ? '未知' : t('{0} 个存活', ch.nodeProcs));
  const mr=document.getElementById('m-recover');
  if(mr) mr.textContent = (ch.recCount>0) ? t('共 {0} 次 · 最近 {1}', ch.recCount, ch.lastRecover) : (ch.checked ? '未发生' : '—');
  const wl=document.getElementById('m-webase');
  if(ch.webaseUrl){ wl.href=ch.webaseUrl; wl.textContent=ch.webaseUrl; } else { wl.removeAttribute('href'); wl.textContent='—'; }
  document.getElementById('chain-at').textContent = ch.at ? t('检测于 {0}', ch.at) : '';
  const st=document.getElementById('st-chain');
  if(!ch.checked){ st.className='status idle'; st.textContent='未检测'; }
  else if(ch.port20200 && ch.port5002){ st.className='status ok'; st.textContent='正常'; }
  else if(ch.port20200 && !ch.port5002){ st.className='status warn'; st.textContent='节点正常·WeBASE未运行'; }
  else { st.className='status fail'; st.textContent='节点未运行'; }
  const ss=document.getElementById('st-ssh');
  if(ch.sshMsg){ ss.className='status '+(ch.sshOk?'ok':'fail'); ss.textContent = ch.sshOk ? '连接成功' : '失败'; }
  document.getElementById('ssh-node').textContent = ch.nodeInfo || '';
  dot('sd-webase', ch.checked && ch.port5002);
  const su=document.getElementById('su-webase');
  if(su){ su.textContent = !ch.checked ? '未检测' : (ch.port5002 ? (ch.webaseOk?'正常':'可达') : '不通');
    su.className = 'svcstate' + (!ch.checked ? '' : (ch.port5002 ? ' ok' : ' err')); }
  const sl=document.getElementById('sl-webase');
  if(sl){ if(ch.webaseUrl && ch.checked && ch.port5002){ sl.style.display=''; sl.href=ch.webaseUrl; sl.textContent=t('打开 ↗'); sl.title=ch.webaseUrl; } else { sl.style.display='none'; } }
  const nd=document.getElementById('nd-chain');
  if(nd) nd.className='dot'+(ch.checked ? (ch.port20200 ? (ch.port5002?' ok':' warn') : ' fail') : '');
}
$('#btn-ssh-test').onclick = ()=>post('/api/chain/ssh-test');
$('#btn-ssh-term').onclick = ()=>post('/api/chain/terminal');
$('#btn-ssh-exec').onclick = ()=>{ const c=$('#ssh_cmd').value.trim(); if(!c){ alert('请输入要执行的命令'); return; } post('/api/chain/exec',{cmd:c}); };
$('#btn-chain-check').onclick = ()=>post('/api/chain/check');
$('#btn-start-chain').onclick = async ()=>{ if(await askConfirm('启动区块链节点',t('将通过 SSH 在链端目录执行启动脚本：\n{0} && {1}\n\n可能耗时较久。', $('#ch_dir').value.trim(), $('#ch_start').value.trim()),'启动')) post('/api/chain/start',{target:'chain'}); };
$('#btn-start-webase').onclick = async ()=>{ if(await askConfirm('启动 WeBASE-Front',t('将通过 SSH 在 WeBASE 目录执行启动脚本：\n{0} && {1}', $('#ch_wdir').value.trim(), $('#ch_wstart').value.trim()),'启动')) post('/api/chain/start',{target:'webase'}); };

/* 启动菜单：选完就收；点外面也收——否则下拉会一直挂在操作面上 */
(function(){
  const m = document.getElementById('start-menu');
  if(!m) return;
  const close = ()=>{ m.open = false; };
  m.querySelectorAll('button').forEach(b=>b.addEventListener('click', close));
  document.addEventListener('click', e=>{ if(m.open && !m.contains(e.target)) close(); });
})();

/* ---------- 程序配置 / 启动 ---------- */
const post=async (url,body)=>{
  try{
    const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body||{})});
    if(!r.ok){
      // 以前是 fire-and-forget：后端 400/500 用户毫无感知。现在统一弹 toast（428 确认流由 postConfirm 自行处理）
      let msg = 'HTTP ' + r.status;
      try{ const j = await r.json(); if(j.reason) msg = j.reason; else if(j.error) msg = j.error; else if(j.problems && j.problems.length) msg = j.problems.map(p=>p.field+' → '+p.msg).join('；'); }catch(e){}
      toast(t('操作失败：') + msg, 'err');
    }
    return r;
  }catch(e){ toast(t('网络错误：') + e, 'err'); return null; }
};
// postConfirm：先探一次，服务端若判定为破坏性操作会返回 428 + 风险说明（含 SQL 高危语句扫描结果），
// 弹窗用的是服务端给的真实风险，比前端写死的提示更准确；确认后才带 confirm:true 重发。
async function postConfirm(url, body, title, fallback){
  const withLang = {...(body||{}), lang: (typeof EK_LANG!=='undefined' ? EK_LANG : 'zh')};
  const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(withLang)});
  if(r.status !== 428) return r;
  let j = {}; try{ j = await r.json(); }catch(e){}
  /* 风险分级：服务端标记 highrisk（drop/truncate/覆盖库等）或文案含高危字样 → 红色警示样式；
     常规确认保持精简，避免确认疲劳 */
  const danger = !!j.highrisk || /高危|不可撤销|覆盖库/.test(j.reason || '');
  if(await askConfirm(title, j.reason || fallback, t('确认执行'), danger)){
    return fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...(body||{}), confirm:true})});
  }
  return null;
}
$('#btn-db-test').onclick = ()=>post('/api/program/db-test');
$('#btn-init-db').onclick = async ()=>{
  const db=$('#db_name').value.trim(), sql=$('#sql_path').value.trim();
  const msg = sql ? t('将创建数据库 {0}，并执行 SQL：\n{1}\n\n注意：若库中已有同名表，可能被覆盖/追加。', db, sql) : t('将创建数据库 {0}（未选 SQL，仅建库）。', db);
  if(await askConfirm(t('初始化数据库'), msg, t('开始执行'))) post('/api/program/init-db',{db, sql});
};
$('#btn-apply-sql-only').onclick = async ()=>{
  const p=$('#sql_path').value.trim(); if(!p){ alert(t('请先选择 SQL 文件')); return; }
  await postConfirm('/api/program/apply-sql', {path:p, db:$('#db_name').value.trim()},
    t('执行 SQL'), t('将对数据库 {0} 执行：\n{1}\n\n注意：可能覆盖/追加已有数据。', $('#db_name').value.trim(), p));
};
$('#btn-web-install').onclick = ()=>post('/api/program/web-install',{dir:$('#fe_dir').value.trim()});
$('#btn-backend-tidy').onclick = ()=>post('/api/program/backend-tidy',{dir:$('#be_dir').value.trim()});
/* 停止→启动宽限：端口释放需要时间，刚停止就点启动会失败；3 秒内自动延迟 */
const lastStopTs = {};
function stopPost(key){ lastStopTs[key||'all'] = Date.now(); return post('/api/program/stop', key?{key}:{}); }
function guardedStart(key, fn){
  const last = lastStopTs[key] || lastStopTs.all || 0;
  const gap = Date.now() - last;
  if(gap < 3000){
    const wait = Math.ceil((3000-gap)/1000);
    toast(t('刚停止，等 {0} 秒让端口释放后自动启动…', wait));
    setTimeout(fn, 3000-gap);
  } else fn();
}
$('#btn-web-start').onclick = ()=>guardedStart('web', ()=>post('/api/program/web-start',{dir:$('#fe_dir').value.trim(), script:$('#web_script').value}));
$('#btn-backend-start').onclick = ()=>guardedStart('backend', ()=>post('/api/program/backend-start',{dir:$('#be_dir').value.trim()}));
$('#btn-web-stop').onclick = ()=>stopPost('web');
$('#btn-backend-stop').onclick = ()=>stopPost('backend');
$('#btn-stop-all').onclick = ()=>stopPost('');
$('#btn-quick-web').onclick = ()=>$('#btn-web-start').click();
$('#btn-quick-backend').onclick = ()=>$('#btn-backend-start').click();
$('#btn-quick-both').onclick = ()=>{ $('#btn-quick-web').click(); setTimeout(()=>$('#btn-quick-backend').click(), 800); };

/* ---------- 端口占用诊断 + 自定义启动脚本 ---------- */
function psPorts(){
  if(!cfg.projects) cfg.projects={};
  if(!Array.isArray(cfg.projects.scan_ports) || !cfg.projects.scan_ports.length) cfg.projects.scan_ports=[8080,8888];
  var spj=cfg.projects.scan_ports.slice().sort(function(a,b){return a-b;}).join();
  if(spj==='20200,3306,8080'||spj==='20200,8080') cfg.projects.scan_ports=[8080,8888]; // 旧默认迁移（3306/20200 → 8888）
  return cfg.projects.scan_ports;
}
function psRenderChips(){
  const box=$('#ps-chips'); box.textContent='';
  psPorts().forEach(p=>{
    const c=document.createElement('span'); c.className='chip'; c.style.cursor='default'; c.textContent=p;
    const x=document.createElement('span'); x.textContent=' ×'; x.style.cursor='pointer'; x.style.opacity='.6';
    x.onclick=()=>{ cfg.projects.scan_ports=psPorts().filter(v=>v!==p); markDirty(); psRenderChips(); };
    c.appendChild(x); box.appendChild(c);
  });
}
function psAddPort(){
  const v=parseInt($('#ps-new').value,10);
  if(!v || v<1 || v>65535){ toast(t('无效端口号'),'err'); return; }
  if(psPorts().includes(v)){ toast(t('该端口已在列表中'),'err'); return; }
  cfg.projects.scan_ports.push(v); $('#ps-new').value=''; markDirty(); psRenderChips();
}
function psSetSt(s,cls){ const el=$('#st-portscan'); el.textContent=s; el.className='status '+(cls||'idle'); }
async function psScan(){
  psSetSt(t('扫描中…'),'run');
  try{
    const j=await (await fetch('/api/ports/scan?ports='+psPorts().join(','))).json();
    const rows=j.ports||[];
    psRenderResult(rows);
    const occ=rows.filter(p=>p.state==='LISTENING').length;
    psSetSt(occ>0 ? t('{0} 个被占用', occ) : t('全部空闲'), occ>0 ? 'warn' : 'ok');
  }catch(e){ psSetSt(t('扫描失败'),'fail'); toast(t('扫描失败：{0}', e.message),'err'); }
}
function psRenderResult(rows){
  const box=$('#ps-result'); box.textContent='';
  if(!rows.length) return;
  const tbl=document.createElement('table'); tbl.style.cssText='width:100%;font-size:12.5px;border-collapse:collapse';
  const mk=(txt,head,color)=>{ const c=document.createElement(head?'th':'td'); c.textContent=txt;
    c.style.cssText=(head?'text-align:left;':'')+'padding:4px 8px;'; if(color) c.style.color=color; else if(head) c.style.color='var(--muted)'; return c; };
  const hr=document.createElement('tr');
  [t('端口'),t('状态'),'PID',t('进程名'),''].forEach(x=>hr.appendChild(mk(x,true)));
  tbl.appendChild(hr);
  rows.forEach(r=>{
    const tr=document.createElement('tr');
    tr.appendChild(mk(r.port));
    if(r.state==='LISTENING'){
      tr.appendChild(mk(t('占用'), false, 'var(--err)'));
      tr.appendChild(mk(r.pid)); tr.appendChild(mk(r.name||'—'));
      const td=document.createElement('td'); td.style.padding='4px 8px';
      const b=document.createElement('button'); b.className='warn'; b.style.padding='2px 10px'; b.textContent=t('结束');
      b.onclick=()=>psKill(r); td.appendChild(b); tr.appendChild(td);
    } else {
      tr.appendChild(mk(t('空闲'), false, 'var(--ok)'));
      tr.appendChild(mk('—')); tr.appendChild(mk('—')); tr.appendChild(mk(''));
    }
    tbl.appendChild(tr);
  });
  box.appendChild(tbl);
}
async function psKill(r){
  const okgo = await askConfirm(t('结束进程'),
    t('将强制结束进程 {0}（PID {1}，占用端口 {2}），未保存的数据可能丢失。请确认它不是系统关键进程。', r.name||t('未知进程'), r.pid, r.port), t('结束'));
  if(!okgo) return;
  try{
    const res=await fetch('/api/ports/kill',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({pid:r.pid, port:r.port, confirm:true, lang:(EK_LANG==='en'?'en':'zh')})});
    if(!res.ok){ const tx=await res.text(); throw new Error(tx||('HTTP '+res.status)); }
    toast(t('已结束 PID {0}，可重新扫描确认', r.pid));
    setTimeout(psScan, 800);
  }catch(e){ toast(t('结束失败：{0}', e.message),'err'); }
}
$('#btn-ps-add').onclick = psAddPort;
$('#ps-new').addEventListener('keydown', e=>{ if(e.key==='Enter'){ e.preventDefault(); psAddPort(); } });
$('#btn-ps-scan').onclick = psScan;

function wlList(){
  if(!cfg.projects) cfg.projects={};
  if(!Array.isArray(cfg.projects.web_scripts)) cfg.projects.web_scripts=[];
  return cfg.projects.web_scripts;
}
function validScript(s){ return /^[\w:.\/-]{1,64}$/.test(s); }
function wlRender(){
  const dl=$('#wl-scripts'); dl.textContent='';
  ['serve','dev'].concat(wlList()).forEach(s=>{ const o=document.createElement('option'); o.value=s; o.textContent='npm run '+s; dl.appendChild(o); });
}
function wlRemember(silent){
  const v=$('#web_script').value.trim();
  if(!v) return v;
  if(!validScript(v)){ toast(t('脚本名含非法字符'),'err'); return v; }
  if(!['serve','dev'].includes(v) && !wlList().includes(v)){
    wlList().push(v); markDirty();
    if(!silent) toast(t('已收藏脚本 {0}', v));
  }
  try{ localStorage.setItem('ek-web-script', v); }catch(_){}
  return v;
}
$('#btn-script-save').onclick = ()=>wlRemember(false);

/* ---------- 数据浏览（只读） ---------- */
const dbv = { open:false, tab:'rows', db:null, table:null, page:1, size:50,
  dbs:[], tables:[], counts:{}, cols:[], rows:[], mode:'table' };

async function dbvFetch(url, opts){
  const r = await fetch(url, opts);
  const t = await r.text();
  let j = null; try{ j = JSON.parse(t); }catch(_){}
  if(!r.ok || (j && j.error)) throw new Error((j && j.error) || t || ('HTTP ' + r.status));
  return j;
}

function dbvSetSt(s, cls){ const el=$('#st-dbview'); el.textContent=s; el.className='status '+(cls||'idle'); }

function dbvRenderSide(){
  const side = $('#dbv-side'); side.textContent = '';
  const mk = (label, cls, cnt, onclick) => {
    const d = document.createElement('div');
    d.className = 'dbv-item ' + cls;
    const span = document.createElement('span'); span.textContent = label;
    d.appendChild(span);
    if(cnt !== null && cnt !== undefined){ const c=document.createElement('span'); c.className='cnt'; c.textContent=cnt; d.appendChild(c); }
    if(onclick) d.onclick = onclick;
    side.appendChild(d);
    return d;
  };
  if(!dbv.dbs.length){ mk(t('未检测到数据库（点「刷新」重试）'), 'db', null, null); return; }
  for(const d of dbv.dbs){
    const cur = d === dbv.db;
    mk(d, 'db' + (cur ? ' cur' : ''), null, ()=>{ dbv.db = cur ? null : d; dbv.table = null; dbv.tables = []; dbv.counts = {}; dbv.render(); if(dbv.db) dbvLoadTables(); });
    if(cur){
      if(dbv.tables.length === 0 && dbv.tablesLoaded){ mk(t('（无表）'), 'tb', null, null); }
      for(const tb of dbv.tables){
        const c = dbv.counts[tb];
        mk(tb, 'tb' + (tb === dbv.table ? ' cur' : ''), c !== undefined ? c : '…', ()=>{ dbv.table = tb; dbv.page = 1; dbv.mode = 'table'; dbvTabSwitch('rows'); dbv.render(); });
      }
    }
  }
}

dbv.render = function(){ dbvRenderSide(); };

async function dbvLoadDbs(){
  dbvSetSt(t('读取中…'), 'run');
  try{
    const j = await dbvFetch('/api/db/list');
    dbv.dbs = j.databases || [];
    dbvSetSt(t('已打开'), 'ok');
  }catch(e){ dbv.dbs = []; dbvSetSt(t('连接失败'), 'fail'); toast(t('数据库列表读取失败：{0}', e.message), 'err'); }
  dbvRenderSide();
}

async function dbvLoadTables(){
  if(!dbv.db) return;
  try{
    const j = await dbvFetch('/api/db/tables?db=' + encodeURIComponent(dbv.db));
    dbv.tables = j.tables || []; dbv.counts = j.counts || {}; dbv.tablesLoaded = true;
  }catch(e){ dbv.tables = []; dbv.counts = {}; toast(t('表列表读取失败：{0}', e.message), 'err'); }
  dbvRenderSide();
}

async function dbvLoadRows(){
  if(!dbv.db || !dbv.table) return;
  const grid = $('#dbv-grid'); grid.textContent = '';
  const tip = document.createElement('tr'); const td = document.createElement('td');
  td.textContent = t('加载中…'); td.colSpan = 1; tip.appendChild(td); grid.appendChild(tip);
  try{
    const j = await dbvFetch(`/api/db/rows?db=${encodeURIComponent(dbv.db)}&table=${encodeURIComponent(dbv.table)}&page=${dbv.page}&size=${dbv.size}`);
    dbv.cols = j.cols || []; dbv.rows = j.rows || [];
    dbvRenderGrid();
    const total = parseInt(j.total, 10) || 0;
    const pages = Math.max(1, Math.ceil(total / dbv.size));
    $('#dbv-pageinfo').textContent = t('共 {0} 行 · 第 {1}/{2} 页', total.toLocaleString(), dbv.page, pages);
  }catch(e){ grid.textContent = ''; const tr=document.createElement('tr'), c=document.createElement('td');
    c.textContent = t('查询失败：{0}', e.message); c.style.color='var(--err)'; c.style.padding='10px'; tr.appendChild(c); grid.appendChild(tr); }
}

function dbvRenderGrid(){
  const grid = $('#dbv-grid'); grid.textContent = '';
  if(!dbv.cols.length){
    const tr=document.createElement('tr'), c=document.createElement('td');
    c.textContent = dbv.tab === 'rows' ? t('请先在左侧选择一张表') : t('（空结果）');
    c.style.cssText='padding:14px;color:var(--muted)'; tr.appendChild(c); grid.appendChild(tr); return;
  }
  const thead = document.createElement('thead'); const hr = document.createElement('tr');
  for(const c of dbv.cols){ const th=document.createElement('th'); th.textContent=c; hr.appendChild(th); }
  thead.appendChild(hr); grid.appendChild(thead);
  const tbody = document.createElement('tbody');
  for(const rw of dbv.rows){
    const tr = document.createElement('tr');
    for(let i=0;i<dbv.cols.length;i++){
      const td = document.createElement('td');
      const v = rw[i] === undefined ? '' : rw[i];
      if(v === 'NULL'){ td.textContent = 'NULL'; td.classList.add('null'); }
      else { td.textContent = v; if(v === '') td.innerHTML = '&nbsp;'; }
      td.onclick = ()=>{ const box=$('#dbv-cell'); box.style.display=''; box.textContent = (dbv.cols[i] + ' = ') + (v === 'NULL' ? 'NULL' : v); };
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
  grid.appendChild(tbody);
}

function dbvRunSQL(){
  const sql = $('#dbv-sql').value.trim(); if(!sql) return;
  $('#h-dbv-sql').textContent = t('运行中…');
  dbvFetch('/api/db/query', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({db: dbv.db || '', sql})})
    .then(j=>{ $('#h-dbv-sql').textContent = t('{0} 行', (j.rows||[]).length); dbv.cols = j.cols||[]; dbv.rows = j.rows||[]; dbvRenderGrid(); $('#dbv-pageinfo').textContent = t('自定义查询 · {0} 行', (j.rows||[]).length); })
    .catch(e=>{ $('#h-dbv-sql').textContent = ''; toast(t('查询失败：{0}', e.message), 'err'); });
}

function dbvExportCSV(){
  if(!dbv.cols.length){ toast(t('没有可导出的数据'), 'err'); return; }
  const q = v => '"' + String(v === 'NULL' ? '' : v).replace(/"/g, '""') + '"';
  const csv = '\uFEFF' + dbv.cols.map(q).join(',') + '\n' + dbv.rows.map(r=>r.map(v=>q(v === undefined ? '' : v)).join(',')).join('\n');
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob([csv], {type:'text/csv;charset=utf-8'}));
  a.download = (dbv.mode === 'table' && dbv.table ? dbv.table : 'query-result') + '.csv';
  a.click(); URL.revokeObjectURL(a.href);
  toast(t('已导出 {0}', a.download));
}

function dbvTabSwitch(tab){
  dbv.tab = tab;
  $('#dbv-tab-rows').classList.toggle('on', tab === 'rows');
  $('#dbv-tab-sql').classList.toggle('on', tab === 'sql');
  $('#dbv-sqlbox').style.display = tab === 'sql' ? '' : 'none';
  if(tab === 'sql'){ dbv.cols = []; dbv.rows = []; dbvRenderGrid(); $('#dbv-pageinfo').textContent=''; }
  else if(dbv.db && dbv.table){ dbvLoadRows(); }
  else { dbv.cols = []; dbv.rows = []; dbvRenderGrid(); }
}

$('#btn-dbview').onclick = ()=>{
  dbv.open = !dbv.open;
  $('#dbview').style.display = dbv.open ? '' : 'none';
  $('#btn-dbview').textContent = dbv.open ? t('收起数据浏览') : t('打开数据浏览');
  if(dbv.open){ dbvLoadDbs(); } else { dbvSetSt(t('未打开'), 'idle'); }
};
$('#dbv-tab-rows').onclick = ()=>dbvTabSwitch('rows');
$('#dbv-tab-sql').onclick = ()=>dbvTabSwitch('sql');
$('#btn-dbv-run').onclick = dbvRunSQL;
$('#dbv-sql').addEventListener('keydown', e=>{ if((e.ctrlKey||e.metaKey) && e.key === 'Enter'){ e.preventDefault(); dbvRunSQL(); } });
$('#dbv-refresh').onclick = ()=>{ dbv.tablesLoaded = false; dbv.table = null; dbvLoadDbs().then(()=>{ if(dbv.db) dbvLoadTables(); }); };
$('#dbv-csv').onclick = dbvExportCSV;
$('#dbv-prev').onclick = ()=>{ if(dbv.page > 1){ dbv.page--; dbvLoadRows(); } };
$('#dbv-next').onclick = ()=>{ dbv.page++; dbvLoadRows(); };
$('#dbv-size').onchange = function(){ dbv.size = parseInt(this.value,10) || 50; dbv.page = 1; if(dbv.db && dbv.table) dbvLoadRows(); };

/* 冒烟辅助：?autoclick=<元素id> 页面加载完自动点击一次（headless 回归用，把结果写进 title） */
{
  const m = location.search.match(/autoclick=([\w-]+)/);
  if(m){
    const b = document.getElementById(m[1]);
    if(!b){ document.title = 'AUTOCLICK MISS ' + m[1]; }
    else{
      try{ b.click();
        document.title = 'AUTOCLICK OK ' + m[1] + (m[1] === 'btn-dbview' && $('#dbview') ? ' OPEN=' + ($('#dbview').style.display !== 'none' ? 'yes' : 'no') : '') + (window.__errs && window.__errs.length ? ' ERRS=' + window.__errs.join(' ; ') : '');
      }catch(e){ document.title = 'AUTOCLICK ERR ' + e.message + ' | ' + ((e.stack||'').split('\n')[1]||'').trim(); }
    }
  }
}

/* 清理后台进程：停止 EnvKit 管理的服务 + 报告端口占用（v3.0 T1.9：不再结束第三方进程） */
$('#btn-clean-proc').onclick = async ()=>{
  const go = await askConfirm('清理后台进程',
    '将执行：\n· 停止 EnvKit 管理的前端 / 后端服务（只停 EnvKit 自己派生的进程树）\n· 等待端口真正释放，并报告仍被占用的端口与占用进程（PID）\n\n' +
    '第三方进程只报告、不自动结束——误杀比杀不掉严重得多。确需结束，请在「端口诊断」里人工确认后处理。\n完成后可重新启动服务。', '开始');
  if(!go) return;
  try{ await post('/api/program/cleanup'); }catch(e){}
  setTimeout(()=>{ try{ pollState(); pollHealth(); pollProg(); }catch(e){} }, 2000);
};
$('#btn-elevate').onclick = async ()=>{ await post('/api/elevate'); };

const ACTIONS = {'db-test':['st-db-test','未检测'],'init-db':['st-db-test','未检测'],'web-install':['st-web-install','前端 未执行'],'backend-tidy':['st-web-install','前端 未执行'],'web-start':['st-web-start','未运行'],'backend-start':['st-backend-start','未运行']};
function renderProg(st){
  const show=(id,label,state)=>{ const el=document.getElementById(id); if(!el) return; let cls='idle';
    /* status 是后端给的三态以上结论（running/ok/warn/fail/stopped）。
       没有它才退回旧的 ok 推断——旧存档/旧后端里没有这个字段。
       warn = 已执行但没验到（例如还在编译），stopped = 正常停止：
       这两种以前都会被画成红色"失败"，用户会去修一个没坏的服务。 */
    if(state.running) cls='run';
    else if(state.status==='ok') cls='ok';
    else if(state.status==='stopped') cls='idle';
    else if(state.status==='warn') cls='warn';
    else if(state.ok)cls='ok'; else if(state.msg)cls='fail';
    el.className='status '+cls; el.textContent=label+(state.msg && state.msg!==label ? (' · '+state.msg) : ''); };
  if(st['db-test']) show('st-db-test', st['db-test'].running?'检测中':(st['db-test'].ok?'连接正常':'失败'), st['db-test']);
  else if(st['init-db']) show('st-db-test', st['init-db'].running?'执行中':(st['init-db'].ok?'已就绪':'失败'), st['init-db']);
  else show('st-db-test','未检测',{});
  if(st['web-install']) show('st-web-install',t('前端 {0}', st['web-install'].running?t('安装中'):(st['web-install'].ok?t('已安装'):t('失败'))), st['web-install']);
  else if(st['backend-tidy']) show('st-web-install',t('后端 {0}', st['backend-tidy'].running?t('整理中'):(st['backend-tidy'].ok?t('已整理'):t('失败'))), st['backend-tidy']);
  else show('st-web-install','依赖 未执行',{});
  show('st-web-start', stateLabel(st['web-start']), st['web-start']||{});
  show('st-backend-start', stateLabel(st['backend-start']), st['backend-start']||{});
  petTasksSync(st);   // 桌宠联动：运行中任务 → 进度环；失败 → 红标；重活完成 → 庆祝
  progResSync();      // 进程资源（内部节流，失败静默——旧后端没有这个端点也不该报错）
}
function stateLabel(s){
  if(!s) return '未运行';
  if(s.running) return '运行中';
  /* 停止/未复验都是正常结局，只有真失败才说"失败"（详见后端 ProgStatus.Status）。 */
  if(s.status==='stopped') return t('已停止');
  if(s.status==='warn') return t('已启动 · 未通过复验');
  return s.ok?t('已启动'):t('失败');
}
async function pollProg(){ try{ renderProg(await (await fetch('/api/program/state')).json()); }catch(e){} }

/* ---------- 服务进程资源（/api/proc/stats） ----------
   采样在后端每 2 秒一次，前端只读快照。这里再节流一次：
   pollProg 本身就是 2s 一轮，两处叠加不做限制的话会变成 2s 两次请求。
   注意只画"在跑"的进程——进程停了就清空，不显示上一次的数字冒充当前值。 */
let resAt = 0;
function fmtUptime(s){
  if(!s) return '—';
  const d = Math.floor(s/86400), h = Math.floor((s%86400)/3600), m = Math.floor((s%3600)/60);
  if(d) return t('{0} 天 {1} 小时', d, h);
  if(h) return t('{0} 小时 {1} 分', h, m);
  return t('{0} 分', m);
}
function sparkline(hist, w, h){
  if(!hist || hist.length < 2) return '';
  const min = Math.min.apply(null, hist), max = Math.max.apply(null, hist);
  const span = Math.max(0.5, max - min);
  const pts = hist.map(function(v,i){
    const x = (i/(hist.length-1)*w).toFixed(1);
    const y = (h - (v-min)/span*(h-2) - 1).toFixed(1);
    return x+','+y;
  }).join(' ');
  return '<svg class="spark" viewBox="0 0 '+w+' '+h+'" preserveAspectRatio="none"><polyline points="'+pts+'"/></svg>';
}
function paintRes(key, p){
  const el = document.getElementById('sr-'+key);
  if(!el) return;
  if(!p || !p.alive || typeof p.rss_mb !== 'number'){ el.innerHTML=''; el.title=''; return; }
  let html = sparkline(p.hist, 74, 16);
  let txt = t('内存 {0} MB', p.rss_mb.toFixed(1));
  if(p.cpu_pct > 0) txt += ' · ' + t('CPU {0}%', p.cpu_pct.toFixed(1));
  html += '<span>' + txt + '</span>';
  if(p.growing) html += '<span class="grow">' + t('2 分钟涨 {0} MB', Number(p.growth_mb||0).toFixed(0)) + '</span>';
  el.innerHTML = html;
  el.title = t('线程 {0} · 句柄 {1} · 已运行 {2}', p.threads||0, p.handles||0, fmtUptime(p.uptime_sec||0));
}
async function progResSync(){
  const now = Date.now();
  if(now - resAt < 1800) return;
  resAt = now;
  let j;
  try{ j = await (await fetch('/api/proc/stats')).json(); }catch(e){ return; }
  const map = {};
  (j.procs||[]).forEach(function(p){ map[p.key] = p; });
  paintRes('web', map['web']);
  paintRes('backend', map['backend']);
}

/* ---------- 系统事件（Windows 事件日志） ----------
   用途：进程闪退时，EnvKit 只能说"exit status 1"；真正的崩溃模块名在
   应用程序日志里（Application Error / .NET Runtime）。这里把最近 40 条
   严重/错误事件列出来，命中本项目栈关键词的标黄——标黄只代表"描述里提到了它"，
   不代表"就是它崩的"，所以原样给出命中词让人自己判断。 */
function slItem(e){
  const d = document.createElement('div');
  d.className = 'sitem' + (e.related ? ' rel' : '');
  const h = document.createElement('div');
  h.className = 'sitem-h';
  const id = document.createElement('span');
  id.className = 'sitem-id';
  id.textContent = (e.level === 1 ? t('严重') : t('错误')) + ' ' + e.id;
  h.appendChild(id);
  h.appendChild(document.createTextNode((e.time||'') + ' · ' + (e.log||'') + ' · ' + (e.source||'')));
  d.appendChild(h);
  const p = document.createElement('div');
  p.className = 'sitem-txt';
  p.textContent = e.text || t('（该事件没有可读描述）');
  d.appendChild(p);
  if(e.related && e.hint){
    const tip = document.createElement('div');
    tip.className = 'sitem-hint';
    tip.textContent = t('描述里提到：{0}', e.hint);
    d.appendChild(tip);
  }
  return d;
}
async function syslogLoad(force){
  const box = document.getElementById('syslog-list');
  const meta = document.getElementById('syslog-meta');
  if(!box) return;
  box.textContent = t('正在读取事件日志…');
  let j;
  try{ j = await (await fetch('/api/win/events' + (force ? '?force=1' : ''))).json(); }
  catch(e){ box.textContent = t('读取失败：{0}', e.message); return; }
  box.textContent = '';
  const evs = j.events || [];
  if(!j.ok && !evs.length){
    box.textContent = t('读取不到事件日志：{0}', j.err || t('未知原因'));
    if(meta) meta.textContent = '';
    return;
  }
  if(!evs.length) box.textContent = t('最近没有严重或错误级事件。');
  evs.forEach(function(e){ box.appendChild(slItem(e)); });
  if(meta){
    meta.textContent = t('共 {0} 条 · 读取于 {1}', evs.length, new Date((j.fetched||0)*1000).toLocaleTimeString()) +
      (j.err ? ' · ' + t('部分日志读取失败') : '');
  }
}
$('#btn-syslog').onclick = function(){ $('#syslog').classList.add('show'); syslogLoad(false); };
$('#syslog-close').onclick = function(){ $('#syslog').classList.remove('show'); };
$('#syslog-refresh').onclick = function(){ syslogLoad(true); };
$('#syslog').addEventListener('click', function(e){ if(e.target.id === 'syslog') $('#syslog').classList.remove('show'); });

/* ---------- 启动 ---------- */
(async function init(){
  /* 读取配置是页面唯一的启动期真实等待：产品头像呼吸环只在它进行时出现，
     finally 保证一定收尾（loadConfig 抛错也不会留一个永远转的头像）。
     不造等待、不做首屏 loading —— 见设计 §7。 */
  var endCfgWait = (typeof window.EKAvatarWait === 'function')
    ? window.EKAvatarWait(document.getElementById('app-logo'), t('正在读取配置…'))
    : function(){};
  try{ await loadConfig(); } finally { endCfgWait(); }
  auditBind();
  // v2.3 N4：守护状态的手动刷新
  const gb = $('#guard-refresh');
  if(gb) gb.onclick = ()=> loadGuardStatus();
  memBind();
  fetch('/api/check',{method:'POST'});
  pollHealth(); pollState(); pollProg();
  /* 首次引导：只看这台浏览器有没有看过（ek-guide-v1），不写后端配置、
     不阻塞上面的初始化。window.EKOnboard 由 90-onboard.js 定义；本 init 已经
     await 过 loadConfig()，整条内联脚本此时已执行完，不会撞 TDZ。
     自动化环境不自动播：全屏 overlay 会挡住既有 CDP 回归脚本对主界面控件的
     真实鼠标点击。判据用 navigator.webdriver **或** UA 里的 HeadlessChrome ——
     实测本项目 --headless=new 只开 remote-debugging-port 时 webdriver 仍是 false，
     单靠 webdriver 判不住；真实浏览器两者皆非，引导照常播。
     「关于 > 重新观看介绍」两种环境都可用。 */
  try{
    var automated = !!(navigator.webdriver || /HeadlessChrome|PhantomJS/i.test(navigator.userAgent));
    if(!automated && window.EKOnboard && !window.EKOnboard.hasSeen())
      window.EKOnboard.open('first');
  }catch(e){}
  setInterval(()=>{
    if(document.hidden) return;   // 页面切走时暂停 2s×3 的状态轮询，回来后下一轮自动恢复
    pollHealth(); pollState(); pollProg();
  }, 2000);
})();

