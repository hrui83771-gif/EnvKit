/* 65-healthcheck.js —— 全链路体检（v3.9）
 *
 * 只读编排在后端 healthcheck.go 完成；这里只负责「发起 → 轮询进度 → 渲染一页结论」。
 * 失败项的按钮复用 40-ops.js 的 issueActInfo 白名单：只点页面上已有的控件，
 * 不新开执行路径，权限闸与 428 确认卡一行没动。
 *
 * 报告正文里的 Evidence 是后端拼好的中文原文，标 data-no-i18n 保护；
 * 界面标签走 t()，并在 ek:lang 时用当前语言整段重渲染。
 *
 * 进度回放：后端每项完成时留一条真实痕迹（steps，含 done/title/at_ms）。
 * 前 8 项是本地文件检查，几毫秒跑完，800ms 的轮询会把它们一次跨过去。
 * 展示层按最小驻留逐条回放这些**真实发生过**的阶段：不编项数、不提前报完成，
 * 结论卡只在后端真 done 且队列播完后出现。补时有总上限，不会拖慢整体。 */

let hcLast = null, hcTimer = null, hcTicks = 0;
let hcHist = [];                 // /api/uistate 给的历史摘要（最新在前）
const HC_STEP_MIN = 260;         // 每个阶段至少看得见这么久
const HC_PLAY_BUDGET = 2600;     // 快速阶段的总补时上限，超过就跟真值
let hcCursor = 0, hcPlayTimer = null, hcBudget = 0;

const HC_GROUPS = [
  ['env',     ()=>t('环境与组件')],
  ['project', ()=>t('项目配置')],
  ['db',      ()=>t('数据库')],
  ['chain',   ()=>t('链端')],
  ['service', ()=>t('服务与端口')],
  ['runtime', ()=>t('运行期')],
];
const HC_MARK = {
  pass: ()=>t('通过'), fail: ()=>t('不通过'), skip: ()=>t('跳过'),
};

function hcOpen(){
  $('#hc').classList.add('show');
  // 每次打开先看结论，不要停在上次滚动到的位置
  const b = $('#hc-body'); if(b) b.scrollTop = 0;
}
function hcClose(){ $('#hc').classList.remove('show'); }

function hcVerdictText(rep){
  return rep.deliverable ? t('现在可以交付') : t('暂不能交付');
}
function hcStatsText(rep){
  return t('{0} 项通过 / {1} 项不通过 / {2} 项跳过', rep.pass, rep.fail, rep.skip);
}

/* ---------- 进度：只画「真实发生过」的阶段 ---------- */
function hcPaintProgress(step, total){
  const prog = $('#hc-prog'), fill = $('#hc-fill'), txt = $('#hc-prog-text');
  if(prog) prog.style.display = '';
  // width 永不超过后端真值：step 来自 steps 留痕，不是编出来的
  if(fill) fill.style.width = (total > 0 ? Math.min(100, Math.round(step.done * 100 / total)) : 0) + '%';
  if(txt){
    txt.textContent = t('正在检查：{0}（{1}/{2}）', step.title, step.done, total);
    txt.classList.remove('hc-swap'); void txt.offsetWidth; txt.classList.add('hc-swap');
  }
  const v = $('#hc-verdict'), items = $('#hc-items'), meta = $('#hc-meta');
  if(v) v.style.display = 'none';
  if(meta) meta.textContent = '';
  if(items) items.innerHTML = '<div class="hc-empty">' +
    esc(t('按顺序重新跑一遍检测，日志会跟着更新；不会启动服务、不会建库、不会自动修复。')) + '</div>';
}

function hcStopPlay(){
  if(hcPlayTimer){ clearTimeout(hcPlayTimer); hcPlayTimer = null; }
}

function hcOnDone(rep){
  if(hcTimer){ clearInterval(hcTimer); hcTimer = null; }
  toast(rep.deliverable ? t('体检完成：可以交付') : t('体检完成：有项目不通过'),
        rep.deliverable ? 'ok' : 'err');
  hcRefreshUIState();
}

/* 回放队列。后端可能一次给出十几条近乎同时完成的阶段，这里按真实顺序
 * 逐条展示，每条至少驻留 HC_STEP_MIN 毫秒；预算耗尽就跳过剩余直接跟真值。 */
function hcPlayNext(){
  hcPlayTimer = null;
  const rep = hcLast;
  const steps = (rep && Array.isArray(rep.steps)) ? rep.steps : [];
  if(hcCursor >= steps.length || hcBudget <= 0){
    hcCursor = steps.length;                       // 剩余阶段不再重播
    if(rep && rep.status === 'done'){ hcRender(rep); hcOnDone(rep); }
    else if(rep && rep.status === 'running'){
      hcPaintProgress({done: rep.done, title: rep.progress}, rep.total || 22);
    }
    return;
  }
  const s = steps[hcCursor++];
  hcPaintProgress(s, rep.total || 22);
  const nx = steps[hcCursor];
  const gap = nx ? Math.max(0, Number(nx.at_ms) - Number(s.at_ms)) : 0;
  const wait = Math.max(HC_STEP_MIN, Math.min(gap, 500));
  hcBudget -= wait;
  hcPlayTimer = setTimeout(hcPlayNext, wait);
}

/* hcFeed 是轮询与启动的统一入口：决定「现在画进度」还是「现在出结论」。 */
function hcFeed(rep){
  if(!rep){ return; }
  hcLast = rep;
  if(rep.status === 'idle'){ hcStopPlay(); hcRender(rep); return; }
  const steps = Array.isArray(rep.steps) ? rep.steps : [];
  if(steps.length > hcCursor && !hcPlayTimer && hcBudget > 0){
    hcPlayNext();
  } else if(!hcPlayTimer){
    if(rep.status === 'running'){
      hcPaintProgress({done: rep.done, title: rep.progress}, rep.total || 22);
    }else{
      hcRender(rep);
    }
  }
  // done 但还在回放：结论卡交给 hcPlayNext 播完后再出，不提前报完成
  if(rep.status === 'done' && !hcPlayTimer && hcCursor >= steps.length &&
     !(hcLast && hcLast.__doneShown)){
    hcRender(rep); hcOnDone(rep);
  }
}

/* ---------- 最终报告 ---------- */
function hcRender(rep){
  hcLast = rep || null;
  const prog = $('#hc-prog'), verdict = $('#hc-verdict'), items = $('#hc-items');
  const meta = $('#hc-meta'), summary = $('#hc-summary');
  if(!prog || !verdict || !items) return;

  if(!rep || rep.status === 'idle' || rep.status === 'running'){
    prog.style.display = rep && rep.status === 'running' ? '' : 'none';
    verdict.style.display = 'none';
    items.innerHTML = '<div class="hc-empty">' +
      esc(t('还没跑过体检。点「全链路体检」开始。')) + '</div>';
    meta.textContent = '';
    return;
  }
  hcLast.__doneShown = true;
  prog.style.display = 'none';

  verdict.style.display = '';
  verdict.className = 'hc-verdict ' + (rep.deliverable ? 'ok' : 'bad');
  verdict.innerHTML = '<b>' + esc(hcVerdictText(rep)) + '</b>' +
    '<div class="hc-concl">' + esc(rep.conclusion || '') + '</div>';
  meta.textContent = t('{0} · v{1}', rep.finished_at || '', rep.version || '');
  if(summary) summary.textContent = hcVerdictText(rep) + ' · ' + hcStatsText(rep) +
    (rep.finished_at ? ' · ' + rep.finished_at : '') +
    (rep.restored ? ' · ' + t('上次运行留下的报告') : '');

  // 证据是后端原文：标 data-no-i18n 防止自动翻译把「Access denied」之类翻坏
  items.setAttribute('data-no-i18n', '');
  let html = '';
  // 恢复出来的旧报告必须自报身份，不能冒充刚跑完的结论
  if(rep.restored){
    html += '<div class="hc-restored">' +
      esc(t('这是上次运行留下的报告（{0}），点「全链路体检」重新跑一次才是当前状态。', rep.finished_at || '')) +
      '</div>';
  }
  const prev = hcHist.find(h => h && h.finished_at !== rep.finished_at) || null;
  if(prev){
    const d = Number(prev.fail || 0) - Number(rep.fail || 0);
    const line = d > 0 ? t('比上次少了 {0} 项不通过', d)
               : d < 0 ? t('比上次多了 {0} 项不通过', -d)
                       : t('与上次持平：{0} 项不通过', rep.fail);
    html += '<div class="hc-hist">' + esc(line) +
      (prev.finished_at ? '<span>' + esc(t('上次：{0}', prev.finished_at)) + '</span>' : '') +
      '</div>';
  }
  for(const [key, label] of HC_GROUPS){
    const list = (rep.items || []).filter(i=>i.group === key);
    if(!list.length) continue;
    const bad = list.filter(i=>i.status === 'fail').length;
    html += '<div class="hc-group"><span>' + esc(label()) + '</span>' +
            '<span class="hc-gmeta">' + esc(t('{0} 项', list.length)) +
            (bad ? ' · <b class="hc-bad">' + esc(t('{0} 项不通过', bad)) + '</b>' : '') +
            '</span></div>';
    for(const it of list){
      const mk = HC_MARK[it.status] ? HC_MARK[it.status]() : it.status;
      html += '<div class="hc-item ' + esc(it.status) + '">' +
        '<div class="hc-line"><span class="hc-badge ' + esc(it.status) + '">' + esc(mk) + '</span>' +
        '<b>' + esc(it.title) + '</b></div>' +
        '<div class="hc-detail">' + esc(it.detail || '') + '</div>';
      const acts = (it.acts || []).map(a=>{
        const inf = (typeof issueActInfo === 'function') ? issueActInfo(a) : null;
        return inf ? '<button class="mini" data-hc-act="' + esc(a) + '">' + esc(inf.label) + '</button>' : '';
      }).join('');
      if(acts) html += '<div class="hc-acts">' + acts + '</div>';
      html += '</div>';
    }
  }
  items.innerHTML = html || '<div class="hc-empty">' + esc(t('这次没有产生任何检查项。')) + '</div>';
}

async function hcPoll(){
  hcTicks++;
  try{
    const r = await fetch('/api/healthcheck');
    if(!r.ok) throw new Error('HTTP ' + r.status);
    const rep = await r.json();
    hcFeed(rep);
    if(rep.status === 'done'){
      // 结论与停止轮询统一由 hcFeed / hcPlayNext 收口，这里不再提前 return
      return;
    }
  }catch(e){
    clearInterval(hcTimer); hcTimer = null;
    toast(t('体检进度读取失败：') + e, 'err');
    return;
  }
  // 3 分钟还没结束就是不对劲，别让进度条永远转（200ms 一次 = 900 tick）
  if(hcTicks > 900){
    clearInterval(hcTimer); hcTimer = null;
    toast(t('体检超时，请查看系统日志'), 'err');
  }
}

async function hcStart(){
  if(hcTimer) return;
  hcOpen();
  hcTicks = 0; hcCursor = 0; hcBudget = HC_PLAY_BUDGET;
  hcStopPlay();
  hcFeed({status:'running', progress:t('准备中'), done:0, total:22, steps:[]});
  try{
    const r = await fetch('/api/healthcheck', {method:'POST'});
    if(r.status === 409){
      const msg = await r.text();
      toast(t('暂时不能开始：') + msg, 'err');
      const cur = await (await fetch('/api/healthcheck')).json();
      hcFeed(cur);
      return;
    }
    if(!r.ok) throw new Error('HTTP ' + r.status);
  }catch(e){
    toast(t('体检启动失败：') + e, 'err');
    hcFeed(hcLast || {status:'idle'});
    return;
  }
  clearInterval(hcTimer);
  hcTimer = setInterval(hcPoll, 200);
  hcPoll();
}

function hcCopy(){
  if(!hcLast || hcLast.status !== 'done'){ toast(t('先跑一次体检再复制'), 'err'); return; }
  const lines = [hcVerdictText(hcLast) + ' · ' + hcStatsText(hcLast), '', t('结论：') + (hcLast.conclusion || '')];
  for(const [key, label] of HC_GROUPS){
    const list = (hcLast.items || []).filter(i=>i.group === key && i.status === 'fail');
    if(!list.length) continue;
    lines.push('', label() + '：');
    for(const it of list) lines.push('· ' + it.title + ' — ' + (it.detail || ''));
  }
  const text = lines.join('\n');
  const done = ()=>toast(t('结论已复制'), 'ok');
  if(navigator.clipboard && navigator.clipboard.writeText){
    navigator.clipboard.writeText(text).then(done, ()=>hcCopyFallback(text, done));
  }else hcCopyFallback(text, done);
}
function hcCopyFallback(text, done){
  const ta = document.createElement('textarea');
  ta.value = text; ta.style.position='fixed'; ta.style.opacity='0';
  document.body.appendChild(ta); ta.select();
  try{ document.execCommand('copy'); done(); }catch(e){ toast(t('复制失败，请用导出 Markdown'), 'err'); }
  ta.remove();
}

/* 刷新/重启后照样知道「配置改过没复检」和上一轮体检结果。
 * 后端是唯一真相源，前端那个 let 只做展示。 */
async function hcRefreshUIState(){
  try{
    const u = await (await fetch('/api/uistate')).json();
    hcHist = Array.isArray(u.hc_history) ? u.hc_history : [];
    if(typeof markCfgStale === 'function') markCfgStale(!!u.cfg_stale);
    if(hcLast && hcLast.status === 'done' && $('#hc-items')) hcRender(hcLast);
  }catch(e){}
}

$('#btn-healthcheck').onclick = hcStart;
$('#hc-close').onclick = hcClose;
$('#hc').addEventListener('click', e=>{ if(e.target.id === 'hc') hcClose(); });
$('#hc-copy').onclick = hcCopy;
$('#hc-export').onclick = ()=>{
  if(!hcLast || hcLast.status !== 'done'){ toast(t('先跑一次体检再导出'), 'err'); return; }
  location.href = '/api/healthcheck/export?t=' + encodeURIComponent(window.__EK_TOKEN__);
};
// 失败项的动作按钮：先收起弹窗，再点页面上已有的那个控件（切面板 / 聚焦 / 问 AI…）
document.addEventListener('click', e=>{
  const b = e.target.closest && e.target.closest('[data-hc-act]');
  if(!b) return;
  const inf = (typeof issueActInfo === 'function') ? issueActInfo(b.getAttribute('data-hc-act')) : null;
  if(!inf){ toast(t('这个动作不被允许'), 'err'); return; }
  hcClose();
  try{ inf.run(null); }catch(err){ toast(t('操作失败：') + err, 'err'); }
});
// 切语言时报告整段重渲染（段名/徽标/按钮都走 t()，证据原文保持后端给的形态）
document.addEventListener('ek:lang', ()=>{
  if(!hcLast) return;
  if(hcLast.status === 'running'){
    const steps = hcLast.steps || [];
    const s = steps[Math.max(0, hcCursor - 1)];
    hcPaintProgress(s || {done:hcLast.done, title:hcLast.progress}, hcLast.total || 22);
  }else hcRender(hcLast);
});
// 不挂 focus 同步：检测是后端 goroutine，中途切窗口回读会把「待复检」
// 又亮回来（明明用户已经点了复检）。只在页面加载与体检完成时对齐真值，
// 刷新页面自然拿到后端结果，状态不会来回跳。
// 打开面板时把最近一次结论读回来，不用每次重跑
(async ()=>{
  await hcRefreshUIState();
  try{
    const rep = await (await fetch('/api/healthcheck')).json();
    if(rep && rep.status === 'done') hcRender(rep);
  }catch(e){}
})();
