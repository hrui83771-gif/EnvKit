/* ---------- v4.0 Phase 3/4：Apply 与最小快照 ----------
   Apply 不自造“修复逻辑”：计划完全来自 Doctor drift；
   前端只负责勾选和安全项确认，执行白名单在后端。
   快照恢复复用 postConfirm（服务端 428 + 真风险文案）。 */

var ekTxnBusy = false;
var ekLastApplyPlan = null;
var ekContractMode = '';

function ekTxnErr(prefix, e){
  return prefix + '：' + (e && e.message ? e.message : String(e));
}

async function ekTxnJSON(url, opt){
  var r = await fetch(url, opt || {});
  var txt = await r.text();
  var j = null;
  try{ j = txt ? JSON.parse(txt) : {}; }catch(e){ throw new Error(txt || ('HTTP ' + r.status)); }
  if(!r.ok){
    var err = new Error(j.error || j.reason || ('HTTP ' + r.status));
    err.status = r.status; err.body = j;
    throw err;
  }
  return j;
}

function ekTxnBind(){
  var plan = document.getElementById('btn-apply-plan');
  if(plan && !plan.__ekBound){ plan.__ekBound = true; plan.addEventListener('click', ekApplyPlan); }
  var run = document.getElementById('btn-apply-run');
  if(run && !run.__ekBound){ run.__ekBound = true; run.addEventListener('click', ekApplyRun); }
  var mode = document.getElementById('btn-contract-enforce');
  if(mode && !mode.__ekBound){ mode.__ekBound = true; mode.addEventListener('click', ekContractEnforce); }
  var ro = document.getElementById('btn-contract-readonly');
  if(ro && !ro.__ekBound){ ro.__ekBound = true; ro.addEventListener('click', ekContractReadonly); }
  var results = document.getElementById('apply-result');
  if(results && !results.__ekBound){
    results.__ekBound = true;
    results.addEventListener('click', function(ev){
      var b = ev.target && ev.target.closest ? ev.target.closest('[data-rollback-id]') : null;
      if(b) ekCheckpointRestore(b.getAttribute('data-rollback-id'));
    });
  }
  var cp = document.getElementById('btn-cp-create');
  if(cp && !cp.__ekBound){ cp.__ekBound = true; cp.addEventListener('click', ekCheckpointCreate); }
  var list = document.getElementById('cp-list');
  if(list && !list.__ekBound){
    list.__ekBound = true;
    list.addEventListener('click', function(ev){
      var b = ev.target && ev.target.closest ? ev.target.closest('[data-cp-act]') : null;
      if(!b) return;
      if(b.getAttribute('data-cp-act') === 'restore') ekCheckpointRestore(b.getAttribute('data-cp-id'));
      if(b.getAttribute('data-cp-act') === 'delete') ekCheckpointDelete(b.getAttribute('data-cp-id'));
    });
  }
  ekCheckpointList();
  ekContractModeLoad();
}

async function ekContractModeLoad(){
  try{
    var j = await ekTxnJSON('/api/contract');
    ekContractMode = j.mode || 'off';
  }catch(e){
    ekContractMode = '';
  }
  ekContractModeRender();
}

function ekContractModeRender(){
  var st = document.getElementById('st-contract-mode');
  var btn = document.getElementById('btn-contract-enforce');
  var ro = document.getElementById('btn-contract-readonly');
  if(!st) return;
  if(ekContractMode === 'enforce'){
    st.className = 'status ok'; st.textContent = t('执行模式 enforce');
    if(btn){ btn.disabled = true; btn.textContent = t('已启用执行'); }
    if(ro){ ro.disabled = false; }
  }else if(ekContractMode === 'readonly'){
    st.className = 'status warn'; st.textContent = t('只读模式 readonly');
    if(btn){ btn.disabled = false; btn.textContent = t('启用执行（enforce）'); }
    if(ro){ ro.disabled = true; }
  }else if(ekContractMode === 'off'){
    st.className = 'status idle'; st.textContent = t('契约模式 off');
    if(btn){ btn.disabled = true; btn.textContent = t('先启用契约'); }
    if(ro){ ro.disabled = true; }
  }else{
    st.className = 'status idle'; st.textContent = t('契约模式未加载');
    if(btn){ btn.disabled = true; btn.textContent = t('启用执行（enforce）'); }
    if(ro){ ro.disabled = true; }
  }
}

async function ekContractEnforce(){
  if(ekTxnBusy || ekContractMode === 'enforce') return;
  try{
    var r = await postConfirm('/api/contract/mode', {mode:'enforce'},
      t('启用环境事务执行'), t('切换后 Apply 可以执行建库、启动服务和导入 schema；高风险动作仍需逐项确认，执行前自动快照。'));
    if(!r){ return; }
    var j = await r.json();
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    ekContractMode = j.mode || 'enforce';
    ekContractModeRender();
    ekApplySyncRun();
    toast(t('已切换到 enforce，可执行环境修复'), 'ok');
    if(ekLastApplyPlan) ekApplyPlan();
  }catch(e){
    toast(ekTxnErr(t('切换契约模式失败'), e), 'err');
  }
}

async function ekContractReadonly(){
  if(ekTxnBusy || ekContractMode !== 'enforce') return;
  try{
    var j = await ekTxnJSON('/api/contract/mode', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body:JSON.stringify({mode:'readonly'})
    });
    ekContractMode = j.mode || 'readonly';
    ekContractModeRender();
    ekApplySyncRun();
    var row = document.getElementById('apply-run-row');
    if(row) row.style.display = 'none';
    toast(t('已切回 readonly，执行按钮已关闭'), 'ok');
  }catch(e){
    toast(ekTxnErr(t('切换契约模式失败'), e), 'err');
  }
}

function ekApplyBadge(risk){
  if(risk === 'confirm') return '<span class="doc-badge s-drift">' + esc(t('需确认')) + '</span>';
  if(risk === 'blocked') return '<span class="doc-badge s-undeclared">' + esc(t('不自动修')) + '</span>';
  return '<span class="doc-badge s-ok">' + esc(t('安全')) + '</span>';
}

async function ekApplyPlan(){
  if(ekTxnBusy) return;
  ekTxnBusy = true;
  var btn = document.getElementById('btn-apply-plan');
  var st = document.getElementById('st-apply');
  var list = document.getElementById('apply-actions');
  var sum = document.getElementById('apply-sum');
  var row = document.getElementById('apply-run-row');
  var res = document.getElementById('apply-result');
  if(btn){ btn.disabled = true; btn.textContent = t('生成中…'); }
  if(st){ st.className = 'status run'; st.textContent = t('生成中'); }
  if(sum) sum.innerHTML = '';
  if(res) res.innerHTML = '';
  if(row) row.style.display = 'none';
  if(list) list.innerHTML = '<div class="doc-empty">' + esc(t('正在把 Doctor 的偏差翻译成修复计划…')) + '</div>';
  try{
    var p = await ekTxnJSON('/api/contract/apply/plan');
    ekLastApplyPlan = p;
    if(p.mode) ekContractMode = p.mode;
    ekContractModeRender();
    ekApplyRenderPlan(p);
  }catch(e){
    ekLastApplyPlan = null;
    if(st){ st.className = 'status fail'; st.textContent = e.status === 409 ? t('不可执行') : t('生成失败'); }
    if(list) list.innerHTML = '<div class="doc-empty">' + esc(e.message) + '</div>';
    toast(ekTxnErr(t('生成修复计划失败'), e), 'err');
  }finally{
    ekTxnBusy = false;
    if(btn){ btn.disabled = false; btn.textContent = t('重新生成计划'); }
  }
}

function ekApplyRenderPlan(p){
  p = p || {};
  var st = document.getElementById('st-apply');
  var list = document.getElementById('apply-actions');
  var sum = document.getElementById('apply-sum');
  var row = document.getElementById('apply-run-row');
  var actions = p.actions || [];
  if(p.mode) ekContractMode = p.mode;
  var hint = document.getElementById('h-apply');
  if(p.mode === 'readonly' && hint) hint.textContent = t('readonly 只生成计划；要执行请先切到 enforce');
  else if(p.mode === 'enforce' && hint) hint.textContent = t('enforce：执行前自动快照，失败停止后续动作');
  var safe = 0, confirm = 0, blocked = 0;
  actions.forEach(function(a){
    if(a.risk === 'blocked' || !a.available) blocked++;
    else if(a.risk === 'confirm') confirm++;
    else safe++;
  });
  if(st){
    st.className = 'status ' + (p.ok ? (actions.length ? 'warn' : 'ok') : 'fail');
    st.textContent = p.ok ? (actions.length ? t('有 {0} 项可处理', actions.length) : t('无需处理')) : t('不可执行');
  }
  if(sum){
    var chips = [];
    if(safe) chips.push('<span class="s-ok">' + esc(t('安全 {0}', safe)) + '</span>');
    if(confirm) chips.push('<span class="s-drift">' + esc(t('需确认 {0}', confirm)) + '</span>');
    if(blocked) chips.push('<span class="s-undeclared">' + esc(t('不自动修 {0}', blocked)) + '</span>');
    sum.innerHTML = chips.join('');
  }
  if(!p.ok){
    list.innerHTML = '<div class="doc-empty">' + esc(p.error || t('没有可用的契约')) + '</div>';
    return;
  }
  if(!actions.length){
    list.innerHTML = '<div class="doc-empty">' + esc(p.conclusion || t('没有需要 Apply 的偏差')) + '</div>';
    return;
  }
  list.innerHTML = actions.map(function(a){
    var selectable = a.available && a.risk !== 'blocked';
    var box = selectable
      ? '<input type="checkbox" class="ap-check" data-apply-id="' + esc(a.id) + '"' + (a.risk === 'safe' ? ' checked' : '') + ' />'
      : '<span class="ap-block">×</span>';
    var reason = a.reason ? '<div class="doc-detail">' + esc(a.reason) + '</div>' : '';
    return '<div class="doc-check ' + (selectable ? (a.risk === 'confirm' ? 's-drift' : 's-ok') : 's-undeclared') + '">' +
      '<div class="doc-top">' + box + '<b style="flex:1">' + esc(a.title) + '</b>' + ekApplyBadge(a.risk) + '</div>' +
      '<div class="doc-kv">' + esc(t('目标：')) + esc(a.target || '') + '</div>' +
      reason + '</div>';
  }).join('');
  // 绑定勾选变化：没有勾选就禁用执行按钮
  list.querySelectorAll('.ap-check').forEach(function(cb){
    cb.addEventListener('change', ekApplySyncRun);
  });
  if(row) row.style.display = (ekContractMode === 'enforce' && (safe + confirm > 0)) ? '' : 'none';
  ekApplySyncRun();
}

function ekApplySelected(){
  var ids = [];
  document.querySelectorAll('#apply-actions .ap-check:checked').forEach(function(cb){
    ids.push(cb.getAttribute('data-apply-id'));
  });
  return ids;
}

function ekApplySyncRun(){
  var btn = document.getElementById('btn-apply-run');
  if(!btn) return;
  var n = ekApplySelected().length;
  btn.disabled = n === 0 || ekTxnBusy || ekContractMode !== 'enforce';
  if(ekContractMode !== 'enforce'){
    btn.textContent = t('只读模式不能执行');
  }else{
    btn.textContent = n ? t('执行选中项（{0}）', n) : t('执行选中项');
  }
}

async function ekApplyRun(){
  if(ekTxnBusy) return;
  if(ekContractMode !== 'enforce'){ toast(t('readonly 只能生成计划；请先切换到 enforce'), 'warn'); return; }
  var ids = ekApplySelected();
  if(!ids.length){ toast(t('请至少勾选一个动作'), 'warn'); return; }
  var st = document.getElementById('st-apply');
  var out = document.getElementById('apply-result');
  ekTxnBusy = true;
  ekApplySyncRun();
  if(st){ st.className = 'status run'; st.textContent = t('执行中'); }
  if(out) out.innerHTML = '<div class="doc-empty">' + esc(t('正在执行并重新诊断…')) + '</div>';
  try{
    /* 让服务端决定要不要 428：前端不猜哪项高危，风险文案由后端按真实动作生成。 */
    var r = await postConfirm('/api/contract/apply', {selected:ids},
      t('执行环境修复'), t('仅执行选中的白名单动作；不会运行契约里的任意命令。'));
    if(!r){ // 用户取消二次确认
      if(st){ st.className = 'status idle'; st.textContent = t('未执行'); }
      if(out) out.innerHTML = '';
      return;
    }
    var txt = await r.text();
    var j; try{ j = JSON.parse(txt); }catch(e){ throw new Error(txt || ('HTTP ' + r.status)); }
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    ekApplyRenderResult(j);
  }catch(e){
    if(st){ st.className = 'status fail'; st.textContent = t('执行失败'); }
    if(out) out.innerHTML = '<div class="doc-empty">' + esc(e.message) + '</div>';
    toast(ekTxnErr(t('环境修复失败'), e), 'err');
  }finally{
    ekTxnBusy = false;
    ekApplySyncRun();
  }
}

function ekApplyRenderResult(run){
  run = run || {};
  var st = document.getElementById('st-apply');
  var out = document.getElementById('apply-result');
  if(st){
    st.className = 'status ' + (run.ok ? 'ok' : 'fail');
    st.textContent = run.ok ? t('执行完成') : t('有动作失败');
  }
  var html = '';
  var tx = run.transaction;
  if(tx){
    var db = tx.database_included ? t('已含数据库') : t('仅配置与现场');
    html += '<div class="doc-check ' + (run.ok ? 's-ok' : 's-drift') + '">' +
      '<div class="doc-top"><b>' + esc(t('执行前快照')) + '</b><span class="doc-badge ' + (run.ok ? 's-ok' : 's-drift') + '">' + esc(tx.checkpoint_id || '') + '</span></div>' +
      '<div class="doc-kv">' + esc(db) + ' · ' + esc(ekBytes((tx.checkpoint || {}).size || 0)) + '</div>' +
      '<div class="doc-detail">' + esc(tx.rollback_note || '') + '</div>' +
      '<div class="row" style="margin-top:6px"><button class="ghost" data-rollback-id="' + esc(tx.checkpoint_id || '') + '">' + esc(t('回滚到执行前')) + '</button></div>' +
      '</div>';
  }
  var steps = run.applied || [];
  html += steps.map(function(s){
    var ev = (s.evidence && s.evidence.length)
      ? '<div class="doc-evidence">' + esc(s.evidence.join(' · ')) + '</div>' : '';
    return '<div class="doc-check ' + (s.ok ? 's-ok' : 's-drift') + '">' +
      '<div class="doc-top"><b>' + esc(s.title || s.id) + '</b><span class="doc-badge ' + (s.ok ? 's-ok' : 's-drift') + '">' + esc(s.ok ? t('完成') : t('失败')) + '</span></div>' +
      '<div class="doc-kv">' + esc(s.msg || '') + '</div>' + ev + '</div>';
  }).join('');
  var skipped = run.skipped || [];
  if(skipped.length){
    html += '<div class="doc-empty">' + esc(t('有动作失败，以下 {0} 项已停止未执行：{1}', skipped.length, skipped.map(function(s){ return s.title || s.id; }).join('、'))) + '</div>';
  }
  out.innerHTML = html;
  if(run.doctor && typeof ekDoctorRender === 'function') ekDoctorRender(run.doctor);
}

async function ekCheckpointList(){
  var list = document.getElementById('cp-list');
  if(!list) return;
  try{
    var j = await ekTxnJSON('/api/contract/checkpoints');
    var rows = j.checkpoints || [];
    if(!rows.length){
      list.innerHTML = '<div class="doc-empty">' + esc(t('还没有快照。先做一个，再改环境。')) + '</div>';
      return;
    }
    list.innerHTML = rows.map(function(c){
      var db = c.db_included
        ? t('数据库 {0} · {1} 表 · {2}', c.db_name || '', c.table_count || 0, ekBytes(c.db_size || 0))
        : t('不含数据库');
      var svc = t('服务 {0} 个，运行 {1} 个', c.services || 0, c.running || 0);
      return '<div class="cp-item">' +
        '<div class="cp-main"><b>' + esc(c.label || c.id) + '</b>' +
        '<span class="hint">' + esc(String(c.created_at || '').replace('T',' ').slice(0,19)) + ' · ' + esc(ekBytes(c.size || 0)) + '</span></div>' +
        '<div class="cp-meta">' + esc(db) + ' · ' + esc(svc) + '</div>' +
        '<div class="cp-act"><button class="ghost" data-cp-act="restore" data-cp-id="' + esc(c.id) + '">' + esc(t('恢复')) + '</button>' +
        '<button class="ghost danger" data-cp-act="delete" data-cp-id="' + esc(c.id) + '">' + esc(t('删除')) + '</button></div>' +
        '</div>';
    }).join('');
  }catch(e){
    list.innerHTML = '<div class="doc-empty">' + esc(ekTxnErr(t('读取快照失败'), e)) + '</div>';
  }
}

function ekBytes(n){
  n = Number(n) || 0;
  if(n < 1024) return n + ' B';
  if(n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(1) + ' MB';
}

async function ekCheckpointCreate(){
  if(ekTxnBusy) return;
  ekTxnBusy = true;
  var btn = document.getElementById('btn-cp-create');
  var st = document.getElementById('st-cp');
  var label = (document.getElementById('cp-label') || {}).value || '';
  var db = !!(document.getElementById('cp-db') || {}).checked;
  if(btn){ btn.disabled = true; btn.textContent = t('快照中…'); }
  if(st){ st.className = 'status run'; st.textContent = t('快照中'); }
  try{
    var j = await ekTxnJSON('/api/contract/checkpoint', {
      method:'POST', headers:{'Content-Type':'application/json'},
      body:JSON.stringify({label:label.trim(), database:db})
    });
    if(st){ st.className = 'status ok'; st.textContent = t('已创建'); }
    toast(t('快照 {0} 已创建', (j.checkpoint || {}).id || ''), 'ok');
    ekCheckpointList();
  }catch(e){
    if(st){ st.className = 'status fail'; st.textContent = t('创建失败'); }
    toast(ekTxnErr(t('创建快照失败'), e), 'err');
  }finally{
    ekTxnBusy = false;
    if(btn){ btn.disabled = false; btn.textContent = t('立即快照'); }
  }
}

async function ekCheckpointRestore(id){
  if(!id || ekTxnBusy) return;
  var st = document.getElementById('st-cp');
  try{
    var r = await postConfirm('/api/contract/checkpoint/restore', {id:id},
      t('恢复快照'), t('将恢复配置和数据库，当前状态会先留备份。'));
    if(!r) return;
    var j = await r.json();
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    if(st){ st.className = 'status ' + (j.ok ? 'ok' : 'fail'); st.textContent = j.ok ? t('已恢复') : t('恢复有失败'); }
    var out = document.getElementById('apply-result');
    if(out){
      var steps = j.steps || [];
      out.innerHTML = steps.map(function(s){
        return '<div class="doc-check ' + (s.ok ? 's-ok' : 's-drift') + '">' +
          '<div class="doc-top"><b>' + esc(s.title || s.id) + '</b><span class="doc-badge ' + (s.ok ? 's-ok' : 's-drift') + '">' + esc(s.ok ? t('完成') : t('失败')) + '</span></div>' +
          '<div class="doc-kv">' + esc(s.msg || '') + '</div></div>';
      }).join('');
    }
    if(j.contract_changed){ toast(j.contract_note || t('契约文件与快照不同，未覆盖'), 'warn'); }
    if(j.doctor && typeof ekDoctorRender === 'function') ekDoctorRender(j.doctor);
    toast(j.ok ? t('快照已恢复') : (j.error || t('恢复有失败')), j.ok ? 'ok' : 'err');
    ekCheckpointList();
  }catch(e){
    toast(ekTxnErr(t('恢复快照失败'), e), 'err');
  }
}

async function ekCheckpointDelete(id){
  if(!id || ekTxnBusy) return;
  try{
    var r = await postConfirm('/api/contract/checkpoint/delete', {id:id},
      t('删除快照'), t('删除后不能恢复；当前环境不受影响。'));
    if(!r) return;
    var j = await r.json();
    if(!r.ok) throw new Error(j.error || ('HTTP ' + r.status));
    toast(t('快照已删除'), 'ok');
    ekCheckpointList();
  }catch(e){
    toast(ekTxnErr(t('删除快照失败'), e), 'err');
  }
}

if(document.readyState === 'loading'){
  document.addEventListener('DOMContentLoaded', ekTxnBind);
}else{
  ekTxnBind();
}
