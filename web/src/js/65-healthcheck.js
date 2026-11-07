/* 65-healthcheck.js —— 全链路体检（v3.9 P0）
 *
 * 只读编排在后端 healthcheck.go 完成；这里只负责「发起 → 轮询进度 → 渲染一页结论」。
 * 失败项的按钮复用 40-ops.js 的 issueActInfo 白名单：只点页面上已有的控件，
 * 不新开执行路径，权限闸与 428 确认卡一行没动。
 *
 * 报告正文里的 Evidence 是后端拼好的中文原文，标 data-no-i18n 保护；
 * 界面标签走 t()，并在 ek:lang 时用当前语言整段重渲染。 */

let hcLast = null, hcTimer = null, hcTicks = 0;

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

function hcRender(rep){
  hcLast = rep || null;
  const prog = $('#hc-prog'), verdict = $('#hc-verdict'), items = $('#hc-items');
  const meta = $('#hc-meta'), summary = $('#hc-summary');
  if(!prog || !verdict || !items) return;

  const running = rep && rep.status === 'running';
  prog.style.display = running ? '' : 'none';
  if(running){
    const pct = rep.total > 0 ? Math.min(100, Math.round(rep.done * 100 / rep.total)) : 0;
    $('#hc-fill').style.width = pct + '%';
    $('#hc-prog-text').textContent = t('正在检查：{0}（{1}/{2}）', rep.progress, rep.done, rep.total);
  }

  if(!rep || rep.status === 'idle' || running){
    verdict.style.display = 'none';
    items.innerHTML = running
      ? '<div class="hc-empty">' + esc(t('按顺序重新跑一遍检测，日志会跟着更新；不会启动服务、不会建库、不会自动修复。')) + '</div>'
      : '<div class="hc-empty">' + esc(t('还没跑过体检。点「全链路体检」开始。')) + '</div>';
    meta.textContent = '';
    return;
  }

  verdict.style.display = '';
  verdict.className = 'hc-verdict ' + (rep.deliverable ? 'ok' : 'bad');
  verdict.innerHTML = '<b>' + esc(hcVerdictText(rep)) + '</b>' +
    '<div class="hc-concl">' + esc(rep.conclusion || '') + '</div>';
  meta.textContent = t('{0} · v{1}', rep.finished_at || '', rep.version || '');
  if(summary) summary.textContent = hcVerdictText(rep) + ' · ' + hcStatsText(rep) +
    (rep.finished_at ? ' · ' + rep.finished_at : '');

  // 证据是后端原文：标 data-no-i18n 防止自动翻译把「Access denied」之类翻坏
  items.setAttribute('data-no-i18n', '');
  let html = '';
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
    hcRender(rep);
    if(rep.status === 'done'){
      clearInterval(hcTimer); hcTimer = null;
      toast(rep.deliverable ? t('体检完成：可以交付') : t('体检完成：有项目不通过'), rep.deliverable ? 'ok' : 'err');
      return;
    }
  }catch(e){
    clearInterval(hcTimer); hcTimer = null;
    toast(t('体检进度读取失败：') + e, 'err');
    return;
  }
  // 3 分钟还没结束就是不对劲，别让进度条永远转
  if(hcTicks > 225){
    clearInterval(hcTimer); hcTimer = null;
    toast(t('体检超时，请查看系统日志'), 'err');
  }
}

async function hcStart(){
  if(hcTimer) return;
  hcOpen();
  hcTicks = 0;
  hcRender({status:'running', progress:t('准备中'), done:0, total:22});
  try{
    const r = await fetch('/api/healthcheck', {method:'POST'});
    if(r.status === 409){
      const msg = await r.text();
      toast(t('暂时不能开始：') + msg, 'err');
      const cur = await (await fetch('/api/healthcheck')).json();
      hcRender(cur);
      return;
    }
    if(!r.ok) throw new Error('HTTP ' + r.status);
  }catch(e){
    toast(t('体检启动失败：') + e, 'err');
    hcRender(hcLast || {status:'idle'});
    return;
  }
  clearInterval(hcTimer);
  hcTimer = setInterval(hcPoll, 800);
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
document.addEventListener('ek:lang', ()=>{ if(hcLast) hcRender(hcLast); });
// 打开面板时把最近一次结论读回来，不用每次重跑
(async ()=>{
  try{
    const rep = await (await fetch('/api/healthcheck')).json();
    if(rep && rep.status === 'done') hcRender(rep);
  }catch(e){}
})();
