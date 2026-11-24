/* ---------- v4.0 Phase 2：环境契约 Doctor（只读） ----------
   Doctor 只把「契约要求 vs 当前实际」摆出来：不启动、不停止、不修复。
   前端不自动运行：数据库检查最长 12 秒，一进设置就扫会让页面像卡住；
   用户点「运行诊断」才开始，结果按四态着色，drift 项排在最前。 */

var EK_DOC_STATUS = {
  ok:          {cls:'ok',          label:'一致'},
  partial:     {cls:'warn',        label:'部分可核实'},
  drift:       {cls:'fail',        label:'有偏差'},
  blocked:     {cls:'fail',        label:'无法诊断'},
  no_contract: {cls:'idle',        label:'没有契约'}
};
var EK_DOC_CHECK = {
  ok:          {cls:'s-ok',          label:'通过'},
  drift:       {cls:'s-drift',       label:'不一致'},
  undeclared:  {cls:'s-undeclared',  label:'未声明'},
  unknown:     {cls:'s-unknown',     label:'无法核实'},
  skip:        {cls:'s-ok',          label:'不适用'}
};
var ekDoctorBusy = false;

function ekDoctorBind(){
  var btn = document.getElementById('btn-doctor-run');
  if(btn && !btn.__ekBound){
    btn.__ekBound = true;
    btn.addEventListener('click', function(){ ekDoctorRun(); });
  }
}

async function ekDoctorRun(){
  if(ekDoctorBusy) return;
  var btn = document.getElementById('btn-doctor-run');
  var st = document.getElementById('st-doctor');
  var hint = document.getElementById('h-doctor');
  var sum = document.getElementById('doctor-sum');
  var list = document.getElementById('doctor-checks');
  var meta = document.getElementById('doctor-meta');
  ekDoctorBusy = true;
  if(btn){ btn.disabled = true; btn.textContent = t('诊断中…'); }
  if(st){ st.className = 'status run'; st.textContent = t('诊断中'); }
  if(hint) hint.textContent = t('只读；不会改动环境');
  if(sum) sum.innerHTML = '';
  if(list) list.innerHTML = '<div class="doc-empty">' + esc(t('正在逐项比对契约与实际…')) + '</div>';
  if(meta) meta.innerHTML = '';

  var ctl = new AbortController();
  var timer = setTimeout(function(){ ctl.abort(); }, 30000);
  try{
    var r = await fetch('/api/contract/doctor', {signal:ctl.signal});
    var txt = await r.text();
    var j;
    try{ j = JSON.parse(txt); }catch(e){ throw new Error(txt || ('HTTP ' + r.status)); }
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    ekDoctorRender(j);
  }catch(e){
    if(st){ st.className = 'status fail'; st.textContent = t('诊断失败'); }
    if(list) list.innerHTML = '<div class="doc-empty">' + esc(e.name === 'AbortError' ? t('诊断超时（30 秒）') : (e.message || String(e))) + '</div>';
    if(sum) sum.innerHTML = '';
    toast(t('Doctor 诊断失败：{0}', e.message || String(e)), 'err');
  }finally{
    clearTimeout(timer);
    ekDoctorBusy = false;
    if(btn){ btn.disabled = false; btn.textContent = t('重新诊断'); }
  }
}

function ekDoctorRender(rep){
  rep = rep || {};
  var st = document.getElementById('st-doctor');
  var hint = document.getElementById('h-doctor');
  var sum = document.getElementById('doctor-sum');
  var list = document.getElementById('doctor-checks');
  var meta = document.getElementById('doctor-meta');
  var s = EK_DOC_STATUS[rep.status] || {cls:'idle', label:rep.status || t('未知')};

  if(st){ st.className = 'status ' + s.cls; st.textContent = t(s.label); }
  if(hint) hint.textContent = rep.conclusion || '';

  var rows = [];
  if(rep.contract_path) rows.push({k:t('契约文件'), v:rep.contract_path});
  if(rep.project_root) rows.push({k:t('项目根'), v:rep.project_root});
  if(rep.checked_at) rows.push({k:t('检查时间'), v:String(rep.checked_at).replace('T',' ').slice(0,19)});
  if(rep.error) rows.push({k:t('错误'), v:rep.error, cls:'warn'});
  ekRo(meta, rows);

  var c = rep.summary || {};
  var chips = [];
  if(c.ok) chips.push('<span class="s-ok">' + esc(t('通过 {0}', c.ok)) + '</span>');
  if(c.drift) chips.push('<span class="s-drift">' + esc(t('不一致 {0}', c.drift)) + '</span>');
  if(c.undeclared) chips.push('<span class="s-undeclared">' + esc(t('未声明 {0}', c.undeclared)) + '</span>');
  if(c.unknown) chips.push('<span class="s-unknown">' + esc(t('无法核实 {0}', c.unknown)) + '</span>');
  if(c.skip) chips.push('<span>' + esc(t('不适用 {0}', c.skip)) + '</span>');
  if(sum) sum.innerHTML = chips.join('');

  var checks = (rep.checks || []).slice();
  /* drift 先看；同组内保持后端顺序（契约 → 工具 → 服务 → 数据库 → 密钥）。 */
  var rank = {drift:0, unknown:1, undeclared:2, ok:3, skip:4};
  checks.sort(function(a,b){
    var d = (rank[a.status] == null ? 9 : rank[a.status]) - (rank[b.status] == null ? 9 : rank[b.status]);
    return d;
  });

  if(!checks.length){
    list.innerHTML = '<div class="doc-empty">' + esc(rep.conclusion || t('没有可显示的检查项')) + '</div>';
    return;
  }
  list.innerHTML = checks.map(function(ch){
    var cs = EK_DOC_CHECK[ch.status] || {cls:'', label:ch.status || t('未知')};
    var kv = '';
    if(ch.expected) kv += '<div class="doc-kv">' + esc(t('期望：')) + esc(ekTrData(ch.expected)) + '</div>';
    if(ch.actual) kv += '<div class="doc-kv">' + esc(t('实际：')) + esc(ekTrData(ch.actual)) + '</div>';
    var detail = ch.detail ? '<div class="doc-detail">' + esc(ekTrData(ch.detail)) + '</div>' : '';
    var ev = (ch.evidence && ch.evidence.length)
      ? '<div class="doc-evidence">' + esc(ch.evidence.join(' · ')) + '</div>' : '';
    return '<div class="doc-check ' + cs.cls + '">' +
      '<div class="doc-top"><b>' + esc(ekTrData(ch.title || ch.id || '')) + '</b>' +
      '<span class="doc-badge ' + cs.cls + '">' + esc(t(cs.label)) + '</span></div>' +
      kv + detail + ev + '</div>';
  }).join('');
}

if(document.readyState === 'loading'){
  document.addEventListener('DOMContentLoaded', ekDoctorBind);
}else{
  ekDoctorBind();
}
