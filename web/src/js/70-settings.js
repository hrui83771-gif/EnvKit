/* ---------- 基础 ---------- */

/* 链端只读行的显示文本。
   抽成纯函数是因为这里出过一个只有新装机器才看得见的显示错：
   原来是 (host || '') + (port ? ':' + port : '')，主机没配时那一行渲染成
   孤零零的 ":20200"，看着像端口配错了。空主机必须返回空串，让 ekRo 画成「—」。
   抽出来之后也能用**构造的值**测，不必依赖本机恰好没配主机。 */
function ekEpText(host, port){
  if(!host) return '';
  return port ? (host + ':' + port) : host;
}
function ekLoginText(user, host, port){
  if(!host) return '';
  return (user ? (user + '@') : '') + host + (port ? (':' + port) : '');
}


/* 备份目录的落点提示。
   留空时后端用 exe 同级的 backups/（见 dbops.go），但页面拿不到 exeDir ——
   所以这里只说"按哪条规则"，不编一个绝对路径出来（编错了比不写更坏）。 */
function ekSetBackupHint(v){
  const el = $('#s_backup_now');
  if(!el) return;
  const s = (v||'').trim();
  el.textContent = s ? t('备份文件写到：{0}', s) : t('未指定：用 exe 同级的 backups 目录');
}

/* 打开目录（备份 / 日志）：路径由服务端 /api/paths 解析。
   前端不自己拼——备份目录留空时真实位置是 exe 同级的 backups，
   而页面所在的工作目录不等于 exe 目录，拼出来只会打开一个不存在的路径。 */
document.addEventListener('click', async (e)=>{
  const btn = (e.target && e.target.closest) ? e.target.closest('[data-open]') : null;
  if(!btn) return;
  e.preventDefault();
  const which = btn.getAttribute('data-open');
  btn.disabled = true;
  try{
    const p = await (await fetch('/api/paths')).json();
    const path = which==='backup' ? p.backup_dir : (which==='logs' ? p.log_dir : p.exe_dir);
    if(!path){ toast(t('没有可打开的目录'), 'err'); return; }
    const r = await fetch('/api/open/path', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({path})});
    if(!r.ok){
      const j = await r.json().catch(()=>({}));
      toast((j && j.error) || t('打开失败'), 'err');
    }
  }catch(err){
    toast(t('打开失败：') + err, 'err');
  }finally{
    btn.disabled = false;
  }
});

/* "8080, 8888" / "8080 8888" / "8080、8888" → [8080,8888]
   非法项一律丢弃：把 "abc" 当端口写进配置的后果是复验去连一个永远不存在的端口，
   而用户看到的只是一句"启动后没验到"。宁可退回"由项目画像推断"。 */
function ekParsePorts(v){
  const out = [];
  String(v||'').split(/[^0-9]+/).forEach(s=>{
    const n = parseInt(s, 10);
    if(!isFinite(n) || n < 1 || n > 65535) return;
    if(out.indexOf(n) < 0) out.push(n);
  });
  return out.slice(0, 16);   // 上限 16 个：再多也不会有服务，只让每轮诊断更慢
}

/* 本机体检（只读）。刻意**不**放数据库/链端明细：那两张卡已经有了，
   同一个指标两处显示迟早分叉（一次改口径忘了另一边）。 */
function ekSetHealth(h){
  const svc = k=>{
    const s = (h.services||{})[k] || {};
    if(!s.running) return {v: t('未运行'), cls: ''};
    return {v: s.port ? (t('在跑') + ' :' + s.port) : t('在跑'), cls: 'ok'};
  };
  const e = h.env || {};
  const f = svc('web'), b = svc('backend');
  const iss = h.issues || [];
  const nErr = iss.filter(x=>x.level==='error').length;
  const nWarn = iss.length - nErr;
  let iv = t('无'), ic = 'ok';
  if(nErr){ iv = t('{0} 个错误、{1} 个警告', nErr, nWarn); ic = 'bad'; }
  else if(nWarn){ iv = t('{0} 个警告', nWarn); ic = 'warn'; }
  ekRo('#s_health_view', [
    /* 非管理员**不是问题**：上一轮刚把「未以管理员运行」从 WARN 降成 HINT，
       这里再涂成橙色等于换个地方继续报警。给中性色，让真正的问题（错误/未就绪）
       才有颜色可用。 */
    {k:'权限', v: h.admin ? t('管理员') : t('普通用户'), cls: h.admin ? 'ok' : ''},
    /* 没检测过 ≠ 没就绪：env.has_results=false 时写"就绪/未就绪"都是在瞎猜 */
    {k:'组件', v: !e.has_results ? t('未检测') : (e.ready ? t('就绪') : t('未就绪')),
     cls: !e.has_results ? '' : (e.ready ? 'ok' : 'bad')},
    {k:'前端服务', v: f.v, cls: f.cls},
    {k:'后端服务', v: b.v, cls: b.cls},
    {k:'待处理', v: iv, cls: ic}
  ]);
  /* 只写「N 个警告」等于把状态源算好的 action 丢掉：用户知道有 2 个警告，
     却不知道下一步点哪里。这里逐条列出问题与建议，markup 与首页
     renderIssues 一致——两处显示同一份数据，口径不可能分叉。 */
  const hb = $('#s_health_issues');
  if(hb){
    hb.innerHTML = iss.map(i=>{
      const cls = i.level==='error' ? 'err' : (i.level==='warn' ? 'warn' : 'info');
      return '<li class="'+cls+'"><b>'+esc(ekTrData(i.what))+'</b>'+
             (i.action ? '<span class="act">'+esc(t('建议：{0}', ekTrData(i.action)))+'</span>' : '')+'</li>';
    }).join('');
  }
}

async function ekSetLoadBase(){
  const st = $('#h-set-save');
  if(st) st.textContent = t('读取中…');
  try{
    const [c, pet, ai, hh] = await Promise.all([
      fetch('/api/config').then(r=>r.json()),
      fetch('/api/pet/config').then(r=>r.json()).catch(()=>null),
      fetch('/api/ai/config').then(r=>r.json()).catch(()=>null),
      // 体检数据不参与任何保存：它是 /api/health 的只读快照，失败也不该拖垮整页
      fetch('/api/health').then(r=>r.json()).catch(()=>null)
    ]);
    EK_SET.base = c;
    cfg = c;                       // 与全局快照对齐：老页面的 configBody() 才有完整底稿
    const p = c.projects || {}, px = c.proxy || {}, ch = c.chain || {};
    $('#s_install_dir').value   = c.install_dir || '';
    $('#s_proxy_http').value    = px.http || '';
    $('#s_proxy_https').value   = px.https || '';
    $('#s_goproxy').value       = c.goproxy || '';
    $('#s_gosumdb').value       = c.gosumdb || '';
    $('#s_go_cgo').value        = c.go_cgo || '';
    $('#s_npm_registry').value  = c.npm_registry || '';
    $('#s_fe_dir').value        = p.frontend_dir || '';
    $('#s_be_dir').value        = p.backend_dir || '';
    $('#s_backup_dir').value    = p.backup_dir || '';
    ekSetBackupHint(p.backup_dir || '');
    // scan_ports 是 []int，平时又允许手写 "8080, 8888"，统一按逗号空格还原成一行
    $('#s_scan_ports').value    = (p.scan_ports && p.scan_ports.length) ? p.scan_ports.join(', ') : '';
    if(hh) ekSetHealth(hh);
    $('#s_app_name').value      = c.app_name || '';
    $('#s_app_title').value     = c.app_title || '';
    /* update_url 不在这里填：它归「更新检查」卡（走 /api/update/config）。
       两个来源都写同一个输入框，会出现"一边刚改、另一边读回来按旧值覆盖"。 */

    /* 数据库/链端只读：密码只回「是否设置」，不在页面上再放一个可被读屏念出来的输入框 */
    ekRo('#s_db_view', [
      {k:'连接', v:(p.mysql_host||'127.0.0.1')+':'+(p.mysql_port||3306)},
      {k:'用户', v:p.mysql_user||''},
      {k:'数据库名', v:p.db_name||''},
      {k:'密码', v:p.mysql_password ? t('已设置（DPAPI 加密存储）') : t('未设置'), cls:p.mysql_password?'ok':'warn'}
    ]);
    ekRo('#s_chain_view', [
      /* 空值要交给 ekRo 画成「—」：不做这一步时 chain_host 没配的话，
         这一行会显示成孤零零的 ":20200"，看着像端口配错了。 */
      {k:'链节点', v: ekEpText(ch.chain_host, ch.chain_port), cls: ch.chain_host ? '' : 'bad'},
      {k:'登录', v: ekLoginText(ch.ssh_user, ch.ssh_host, ch.ssh_port)},
      {k:'节点目录', v:ch.chain_dir||''},
      {k:'WeBASE', v:(ch.webase_dir||'')+(ch.webase_port?(' · :'+ch.webase_port):'')},
      {k:'群组', v:ch.group_id||1},
      {k:'后台守护', v:ch.chain_guard ? t('已开启（{0} 秒/次）', ch.chain_guard_secs||60) : t('关闭'), cls:ch.chain_guard?'ok':'warn'}
    ]);

    if(pet){
      $('#s_weather_on').checked = !!pet.weather_enabled;
      $('#s_weather_city').value = pet.weather_city || '';
      ekWeatherCityLoaded = true;   // 已用服务端值填过 → 之后清空才是"用户要清空"
      ekWeatherHint(!!pet.weather_enabled, pet.weather_city || '');
    }
    if(ai){
      ekRo('#s_ai_view', [
        {k:'AI 助手', v: ai.key_set ? (ai.enabled ? t('已启用') : t('已配密钥但未启用')) : t('未配置密钥'),
         cls: (ai.key_set && ai.enabled) ? 'ok' : 'warn'},
        {k:'模型', v: ai.model || ''},
        {k:'记忆', v: ai.memory_enabled ? t('已开启') : t('关闭')}
      ]);
    }
    if(st) st.textContent = t('已读取最新配置（{0}）', new Date().toLocaleTimeString());
    /* 长 URL（GOPROXY / npm 源）在窄卡片里会被截断：把完整值放进 title，
       悬停就能核对 —— 比为了看清而把卡片拉成整行更划算。 */
    ['#s_goproxy','#s_gosumdb','#s_npm_registry','#s_proxy_http','#s_proxy_https','#s_install_dir','#s_fe_dir','#s_be_dir'].forEach(sel=>{
      const el = $(sel);
      if(el) el.title = el.value || '';
    });
  }catch(e){
    if(st) st.textContent = t('读取失败：{0}', e);
  }
}

/* 设置页的目录选择：不复用老页面的 pickDir —— 那个函数会顺带写 #fe_dir 并触发
   markDirty()，等于在设置页点一下「选择」就把老页表单存一遍，两边互相覆盖。 */
function ekSetPick(btn){
  const id = btn.dataset.spick;
  const cur = (document.getElementById(id)||{}).value || '';
  openBrowser('dir', cur, '', 'dir', p=>{ if(p){ document.getElementById(id).value = p; } });
}

async function ekSetSaveBase(){
  const st = $('#h-set-save');
  if(!EK_SET.base){ toast(t('还没读到配置，请先点「重新读取」'), 'err'); return; }
  const c = JSON.parse(JSON.stringify(EK_SET.base));   // 深拷贝：失败时不留半改的底稿
  c.install_dir = $('#s_install_dir').value.trim();
  c.proxy = {http:$('#s_proxy_http').value.trim(), https:$('#s_proxy_https').value.trim()};
  c.goproxy = $('#s_goproxy').value.trim();
  c.gosumdb = $('#s_gosumdb').value.trim();
  c.go_cgo  = $('#s_go_cgo').value.trim();
  c.npm_registry = $('#s_npm_registry').value.trim();
  c.projects = c.projects || {};
  c.projects.frontend_dir = $('#s_fe_dir').value.trim();
  c.projects.backend_dir  = $('#s_be_dir').value.trim();
  c.projects.backup_dir   = $('#s_backup_dir').value.trim();
  /* 端口清单必须回成 []int：写字符串会让 Go 侧 json.Unmarshal 直接失败，
     整个保存被 400 拒掉，而错误落在 projects 上，提示是"某项格式不对"，
     用户完全看不出是端口那一格。 */
  c.projects.scan_ports   = ekParsePorts($('#s_scan_ports').value);
  c.app_name  = $('#s_app_name').value.trim();
  c.app_title = $('#s_app_title').value.trim();
  c.update_url = $('#s_update_url').value.trim();

  if(st) st.textContent = t('保存中…');
  try{
    const r = await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(c)});
    if(r.ok){
      if(st) st.textContent = t('已保存（{0}）', new Date().toLocaleTimeString());
      toast(t('基础设置已保存'));
      /* 老页面 DOM 里还是旧值：不重填的话，用户在「程序配置」随手改一个输入框，
         那次自动保存就会把刚设的目录/代理按旧值写回去。必须重读一次。 */
      try{ await loadConfig(); }catch(e){}
      await ekSetLoadBase();
      /* 天气那一格也在同一块面板里，用户按的是同一个「保存」——
         过去这个按钮对它完全无效，必须再单独点一次「应用」（原始反馈：
         "按理说我保存配置后就生效了"）。仍然走独立端点，只是顺序上一起做；
         silent：上面的 toast 已经说了"已保存"，不需要连弹两条。 */
      try{ await ekSetWeather(true); }catch(e){}
    }else{
      let msg = 'HTTP ' + r.status;
      try{
        const j = await r.json();
        if(j.problems && j.problems.length) msg = j.problems.map(x=>(x.field||'')+' → '+(x.msg||'')).join('；');
        else if(j.error) msg = j.error;
      }catch(e){}
      if(st) st.textContent = t('保存失败：{0}', msg);
      toast(t('保存失败：')+msg, 'err');
    }
  }catch(e){
    if(st) st.textContent = t('保存失败：{0}', e);
  }
}

/* 天气是独立出入口：/api/pet/config 用 *bool，绝不能混进 /api/config 的整包保存 */
/* 生效状态提示：卡片里那行小字。
   过去这里只有旁边一个「应用」按钮，勾完开关页面**什么都不变**，
   用户无法判断"是不是已经生效"，只能再点一次应用试试——这正是本月报上来的
   「必须点应用才生效」。现在勾选即保存，并把服务端回的生效值原样显示出来。 */
function ekWeatherHint(on, city){
  const h = document.getElementById('h-weather');
  if(!h) return;
  if(typeof on !== 'boolean'){
    const el = document.getElementById('s_weather_on');
    on = !!(el && el.checked);
  }
  if(!on){ h.textContent = t('已关闭（不查询、不出网）'); return; }
  const c = city || (document.getElementById('s_weather_city') || {}).value || '';
  h.textContent = c ? t('已开启 · {0}', c) : t('已开启');
}

async function ekSetWeather(quiet){
  const onEl = $('#s_weather_on'), cityEl = $('#s_weather_city');
  const on = !!(onEl && onEl.checked);
  const city = cityEl ? cityEl.value.trim() : '';
  const st = $('#h-weather');
  // 城市字段只在"填过"或"确实被清空"时提交：见 ekWeatherCityLoaded 的说明，
  // 否则"刚打开页面就勾开关"会把用户已存的城市用空串覆盖掉。
  const body = {weather_enabled: on};
  if(cityEl && (city !== '' || ekWeatherCityLoaded)) body.weather_city = city;
  if(st) st.textContent = t('保存中…');
  try{
    const r = await fetch('/api/pet/config',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify(body)});
    if(!r.ok){ if(st) st.textContent = t('保存失败：HTTP {0}', r.status); toast(t('保存失败：HTTP {0}', r.status), 'err'); return; }
    // 以**服务端回的生效值**为准，而不是"我刚勾的值"：勾上只证明点了，
    // 回读才证明存下去了（城市留空时服务端给的是默认城市）。
    let eff = null;
    try{ eff = await r.json(); }catch(_){}
    if(!eff || typeof eff.weather_enabled !== 'boolean'){
      try{ eff = await (await fetch('/api/pet/config')).json(); }catch(_){}
    }
    if(eff && typeof eff.weather_enabled === 'boolean'){
      if(onEl) onEl.checked = !!eff.weather_enabled;
      if(cityEl){ cityEl.value = eff.weather_city || ''; ekWeatherCityLoaded = true; }
      petWeatherOn = !!eff.weather_enabled;
      petWeatherMenuSync();
      petWeatherBroadcast(petWeatherOn);
      ekWeatherHint(petWeatherOn, eff.weather_city || '');
      if(!quiet){
        toast(eff.weather_enabled ? t('天气已开启（{0}）', eff.weather_city || t('默认城市')) : t('天气已关闭'));
      }
    }else{
      ekWeatherHint(on, city);
      if(!quiet) toast(on ? t('天气已开启（{0}）', city||t('默认城市')) : t('天气已关闭'));
    }
  }catch(e){
    if(st) st.textContent = t('网络错误：')+e;
    toast(t('网络错误：')+e, 'err');
  }
}

/* ---------- 开发者 ----------
   20 个字段只有一个保存口（/api/dev/config），全部是 int 指针：填 0 或留空 = 回落内置默认。
   默认值只在后端 state.go 里有一份（devDefault*），前端不复制字面量 ——
   「恢复默认」的语义就是"把 dev 段清成 0"，而不是在前端写第二份数字。 */
const EK_DEV_MAP = [
  {k:'summary_after_tokens',  s:'#sd_summary_after',  n:'#sd_summary_after_n'},
  {k:'summary_keep_tokens',   s:'#sd_summary_keep',   n:'#sd_summary_keep_n'},
  {k:'summarize_keep_msgs',   s:'#sd_keep_msgs',      n:'#sd_keep_msgs_n'},
  {k:'turn_timeout_secs',     s:'#sd_turn_timeout',   n:'#sd_turn_timeout_n'},
  {k:'audit_retention_days',  s:'#sd_audit_days',     n:'#sd_audit_days_n'},
  {k:'trace_retention_days',  s:'#sd_trace_days',     n:'#sd_trace_days_n'},
  {k:'shadow_retention_days', s:'#sd_shadow_days',    n:'#sd_shadow_days_n'},
  {k:'mission_deadline_days', s:'#sd_mission_dl',     n:'#sd_mission_dl_n'},
  {k:'mission_keep_days',     s:'#sd_mission_keep',   n:'#sd_mission_keep_n'},
  {k:'backup_keep',           s:'#sd_backup_keep',    n:'#sd_backup_keep_n'},
  {k:'server_port',           s:null,                 n:'#sd_server_port'},
  {k:'port_scan_limit',       s:null,                 n:'#sd_port_limit'},
  {k:'explore_max_kb',        s:'#sd_explore_kb',     n:'#sd_explore_kb_n'},
  {k:'log_max_mb',            s:'#sd_log_mb',         n:'#sd_log_mb_n'},
  {k:'svc_log_max_mb',        s:'#sd_svclog_mb',      n:'#sd_svclog_mb_n'},
  {k:'log_keep',              s:'#sd_log_keep',       n:'#sd_log_keep_n'},
  {k:'svc_log_keep',          s:'#sd_svclog_keep',    n:'#sd_svclog_keep_n'},
  {k:'verify_wait_web_secs',  s:'#sd_vwait_web',      n:'#sd_vwait_web_n'},
  {k:'verify_wait_backend_secs', s:'#sd_vwait_be',    n:'#sd_vwait_be_n'},
  {k:'subproc_retries',       s:'#sd_retries',        n:'#sd_retries_n'},
  {k:'subproc_backoff_secs',  s:'#sd_backoff',        n:'#sd_backoff_n'}
];

function ekDevBindSliders(){
  EK_DEV_MAP.forEach(m=>{
    if(!m.s) return;
    const s = $(m.s), n = $(m.n);
    if(!s || !n) return;
    s.addEventListener('input', ()=>{ n.value = s.value; });
    /* 数字→滑块只做视觉映射，不回写数字框：用户输入 0（= 恢复默认）时
       若把 0 夹到滑块 min 再写回去，就永远填不出 0 了。 */
    n.addEventListener('input', ()=>{
      const v = parseInt(n.value, 10);
      if(isNaN(v)) return;
      const lo = parseInt(s.min,10) || 0, hi = parseInt(s.max,10) || 1e9;
      s.value = Math.max(lo, Math.min(hi, v));
    });
  });
}

async function ekSetLoadDev(){
  const st = $('#h-sd-save');
  if(st) st.textContent = t('读取中…');
  try{
    /* 开发者区的「恢复预算」只读块来自 config.json 的 recovery 段：
       直接点「开发者」tab 时基础区还没跑过，这里补一次，否则那块永远是空的。 */
    if(!EK_SET.base) await ekSetLoadBase();
    const d = await fetch('/api/dev/config').then(r=>r.json());
    /* 怪癖是自动学习出来的运行态，不在 dev 段里 —— 单独取一次，失败也不影响其它块 */
    fetch('/api/ai/config').then(r=>r.json()).then(j=>{
      EK_SET.aiQuirk = j.quirk || {};
      ekSetRenderQuirk();
    }).catch(()=>{ EK_SET.aiQuirk = {}; ekSetRenderQuirk(); });
    /* 仓库根只在 eval status 里（它要真去 stat tools/eval_gate.py）；
       没进过评测 tab 时这里补一次，否则运行时块永远显示"未定位"。 */
    if(!EK_SET.ev){
      fetch('/api/eval/status').then(r=>r.json()).then(j=>{ EK_SET.ev = j; ekSetRenderRuntime(); }).catch(()=>{});
    }
    EK_SET.dev = d;
    EK_DEV_MAP.forEach(m=>{
      const v = d[m.k];
      const n = $(m.n);
      if(n) n.value = (v==null ? '' : v);
      const s = $(m.s);
      if(s && v!=null){
        const lo = parseInt(s.min,10)||0, hi = parseInt(s.max,10)||1e9;
        s.value = Math.max(lo, Math.min(hi, v));
      }
    });
    /* 端口状态：拿浏览器实际访问的端口和 dev 段起始端口对照 ——
       两者不一致说明当前实例是顺延出来的，改起始端口要重启才见效。 */
    const live = parseInt(location.port || '80', 10);
    const want = parseInt(d.server_port, 10) || 18765;
    const el = $('#st-dev-port');
    if(el){
      el.textContent = live===want ? t('当前 :{0}', live) : t('当前 :{0}（设定 {1}）', live, want);
      el.className = 'status ' + (live===want ? 'ok' : 'warn');
    }
    const hp = $('#h-dev-port');
    /* 正常时不显示任何字：这地方只有"状态不对"才值得占一行。
       原来那句"改端口需重启、评测脚本硬编码 18765"是说明书，不是状态。 */
    if(hp) hp.textContent = live===want
      ? ''
      : t('当前实例在 :{0}，与设定 :{1} 不一致（被占用顺延，或改过未重启）；评测脚本硬编码 18765，后两档会拒绝启动。', live, want);

    ekSetRenderRuntime();
    ekSetRenderQuirk();
    ekSetRenderRecovery();
    if(st) st.textContent = t('已读取（{0}）', new Date().toLocaleTimeString());
  }catch(e){
    if(st) st.textContent = t('读取失败：{0}', e);
  }
}

/* 运行时只读块：只放"事实"，不放能改的开关（能改的都在上面的组里） */
function ekSetRenderRuntime(){
  const verEl = document.getElementById('about-ver');
  const ver = verEl ? verEl.textContent.replace(/^.*v/, '') : '';
  const rows = [
    {k:'版本', v: ver || t('未知')},
    {k:'控制台端口', v: location.port || '(80)'},
    {k:'安装目录', v: (EK_SET.base && EK_SET.base.install_dir) || ''},
    {k:'仓库根', v: (EK_SET.ev && EK_SET.ev.root) || t('未定位（评测脚本只在仓库目录下）'),
     cls: (EK_SET.ev && EK_SET.ev.root) ? 'ok' : 'warn'}
  ];
  ekRo('#sd_runtime', rows);
}

function ekSetRenderQuirk(){
  const q = EK_SET.aiQuirk;
  if(!q){ ekRo('#sd_quirk', [{k:'状态', v:t('读取中…')}]); return; }
  const rows = [
    {k:'温度参数', v: q.temp_mode==='one' ? t('固定发 1（该模型只接受 1）')
                : (q.temp_mode==='none' ? t('不发送温度') : t('按配置值发送'))},
    {k:'max_tokens 字段', v: q.max_tokens_field==='max_completion_tokens' ? 'max_completion_tokens' : 'max_tokens'},
    {k:'工具调用', v: q.no_tools ? t('不支持（已自动降级）') : t('支持'), cls: q.no_tools?'warn':'ok'}
  ];
  ekRo('#sd_quirk', rows);
}

function ekSetRenderRecovery(){
  const r = (EK_SET.base && EK_SET.base.recovery) || {};
  const rows = [
    {k:'窗口内动作上限', v: r.max_actions || t('内置默认（12）')},
    {k:'预算窗口',      v: r.window_secs ? t('{0} 秒', r.window_secs) : t('内置默认（600 秒）')},
    {k:'单目标重试上限', v: r.max_attempts || t('内置默认（3）')},
    {k:'断路器阈值',    v: r.breaker_after || t('内置默认（3）')},
    {k:'熔断冷静期',    v: r.breaker_cooldown_secs ? t('{0} 秒', r.breaker_cooldown_secs) : t('内置默认（300 秒）')}
  ];
  ekRo('#sd_recovery', rows);
}

async function ekSetSaveDev(){
  const st = $('#h-sd-save');
  const body = {};
  EK_DEV_MAP.forEach(m=>{
    const n = $(m.n);
    if(!n) return;
    const v = parseInt(n.value, 10);
    body[m.k] = isNaN(v) ? 0 : v;      // 空 = 0 = 恢复默认，这是显式语义不是"没填"
  });
  if(st) st.textContent = t('保存中…');
  try{
    const r = await fetch('/api/dev/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    let j = {}; try{ j = await r.json(); }catch(e){}
    if(r.ok){
      if(st) st.textContent = t('已保存（{0}）', new Date().toLocaleTimeString());
      toast(t('开发者配置已保存；端口改动需重启生效'));
      await ekSetLoadDev();
    }else{
      const msg = (j.problems && j.problems.length)
        ? j.problems.map(x=>(x.field||'')+' → '+(x.msg||'')).join('；')
        : (j.error || ('HTTP ' + r.status));
      if(st) st.textContent = t('保存失败：{0}', msg);
      toast(t('保存失败：')+msg, 'err');
    }
  }catch(e){
    if(st) st.textContent = t('保存失败：{0}', e);
  }
}

/* 恢复默认 = 把 20 个字段全部清 0（后端 devOr 会把 0 当"没设"回落内置默认）。
   滑块同步到 min 只是视觉近似：0 不在量程内，滑到最左即可，数字框保留真实的 0。 */
function ekSetResetDev(){
  EK_DEV_MAP.forEach(m=>{
    const n = $(m.n);
    if(n) n.value = 0;
    const s = $(m.s);
    if(s) s.value = s.min;
  });
  const st = $('#h-sd-save');
  if(st) st.textContent = t('已清空为默认（点「保存」生效）');
}

/* ---------- 评测 ----------
   四档全部走 POST /api/eval/run：
     smoke / gate  只读，点了就跑；
     recovery / tasks 破坏性，服务端强制 428，前端只能弹确认卡后带 confirm 重发。
   前端不做任何"乐观启动"：按钮状态一律以 GET /api/eval/status 的 running 为准，
   否则点了没反应和点了跑了分不清。 */

function ekPct(v){
  if(v==null || isNaN(Number(v))) return '';
  return (Math.round(Number(v)*1000)/10) + '%';
}

async function ekEvLoad(){
  try{
    const s = await fetch('/api/eval/status').then(r=>r.json());
    EK_SET.ev = s;
    ekEvRenderEnv(s);
    ekEvRenderKinds(s);
    ekEvRenderBaseline(s);
    ekEvRenderBoards(s);
    ekEvRenderReports(s);
    ekSetRenderRuntime();
    return s;
  }catch(e){
    const st = $('#st-eval-env');
    if(st){ st.textContent = t('检测失败'); st.className = 'status err'; }
    ekRo('#ev-env', [{k:'错误', v:String(e), cls:'bad'}]);
    return null;
  }
}

function ekEvRenderEnv(s){
  const st = $('#st-eval-env');
  const py = s.python || {}, port = s.port || {}, ai = s.ai || {};
  if(st){
    const ok = py.ok && !s.root_error;
    st.textContent = ok ? t('可用') : t('部分不可用');
    st.className = 'status ' + (ok ? 'ok' : 'warn');
  }
  const rows = [
    {k:'仓库根', v: s.root || s.root_error || '', cls: s.root ? 'ok' : 'bad'},
    {k:'Python', v: py.ok ? (py.path + (py.version ? ' · ' + py.version : '')) : (py.why || t('未找到')), cls: py.ok?'ok':'bad'},
    {k:'控制台端口', v: port.ok ? t('{0}（与脚本一致）', port.listen) : (port.note || t('{0}', port.listen)), cls: port.ok?'ok':'warn'},
    {k:'AI', v: ai.key_set ? t('已配置（{0}）', ai.model || t('默认模型')) : t('未配置密钥：后两档不可用'), cls: ai.key_set?'ok':'warn'}
  ];
  const last = s.last;
  if(last){
    const t0 = last.finished_at || '';
    rows.push({k:'上次评测', v: t('{0} · {1} · {2} 秒 · {3} 行', last.name||last.kind, last.ok?t('通过'):t('未通过'), Math.round(last.seconds||0), last.lines||0),
               cls: last.ok?'ok':'bad'});
    if(t0) rows.push({k:'完成时间', v:t0});
    if(last.summary) rows.push({k:'结论', v:last.summary});
    if(last.gate && last.gate.problems && last.gate.problems.length){
      rows.push({k:'门禁问题', v:last.gate.problems.join('；'), cls:'bad'});
    }
  }
  if(s.running){
    rows.push({k:'正在运行', v:t('{0} · 已 {1} 秒{2}', s.running.name, s.running.elapsed_secs, s.running.dry_run?t('（预演）'):''), cls:'warn'});
  }
  ekRo('#ev-env', rows);
  const h = $('#h-ev-env');
  if(h) h.textContent = '';
}

/* 轮询重渲染会重建卡片，输入框里的 --only / 重复次数必须先存后填，否则每 2.5 秒被清一次 */
function ekEvSnapshotInputs(){
  const o = {};
  document.querySelectorAll('#ev-kinds [data-ev-in]').forEach(el=>{
    o[el.id] = (el.type === 'checkbox') ? el.checked : el.value;
  });
  return o;
}
function ekEvRestoreInputs(o){
  Object.keys(o||{}).forEach(id=>{
    const el = document.getElementById(id); if(!el) return;
    if(el.type === 'checkbox') el.checked = !!o[id]; else el.value = o[id];
  });
}

function ekEvRenderKinds(s){
  const box = $('#ev-kinds'); if(!box) return;
  const keep = ekEvSnapshotInputs();
  const running = s.running || null;
  box.innerHTML = (s.kinds||[]).map(k=>{
    const cls = ['ev-card'];
    if(!k.read_only) cls.push('danger');
    if(!k.available) cls.push('na');
    const badge = k.read_only
      ? '<span class="ev-badge ro">' + t('只读') + '</span>'
      : '<span class="ev-badge rw">' + t('破坏性') + '</span>';
    let ops = '';
    if(k.key === 'recovery'){
      ops = '<input type="text" data-ev-in id="ev_only_' + k.key + '" placeholder="--only 如 kill_service" title="只跑指定注入类，逗号分隔" />' +
            '<label class="ev-chk" title="自动批准确认卡（白名单只有 start_service）；不开的话样本会被确认卡掐断，覆盖率必然偏低">' +
            '<input type="checkbox" data-ev-in id="ev_aw_' + k.key + '">' + t('允许写操作') + '</label>' +
            '<button class="ev-run ghost" data-ev="dry" data-kind="' + k.key + '">' + t('预演') + '</button>';
    }else if(k.key === 'tasks'){
      ops = '<input type="text" data-ev-in id="ev_only_' + k.key + '" placeholder="--only 如 T01,T02" title="只跑指定题目，逗号分隔" />' +
            '<input type="number" class="set-num" data-ev-in id="ev_repeat_' + k.key + '" min="0" max="10" placeholder="重复" title="重复次数；0 = 用脚本默认" />' +
            '<button class="ev-run ghost" data-ev="dry" data-kind="' + k.key + '">' + t('预演') + '</button>';
    }
    const isRun = running && running.kind === k.key;
    const btn = isRun
      ? '<button class="ev-run warn" data-ev="stop">' + t('停止') + '</button>'
      : '<button class="ev-run" data-ev="run" data-kind="' + k.key + '"' + (k.available ? '' : ' disabled') + '>' +
        (k.read_only ? t('开始') : t('开始（需确认）')) + '</button>';
    const blockers = (k.blockers && k.blockers.length)
      ? '<div class="ev-block">' + k.blockers.map(b=>'<div>'+esc(b)+'</div>').join('') + '</div>'
      : '';
    return '<div class="' + cls.join(' ') + '">' +
      '<div class="ev-head"><span class="ev-name">' + esc(k.name) + '</span>' + badge +
        (running && running.kind!==k.key ? '<span class="ev-badge">' + t('排队中') + '</span>' : '') + '</div>' +
      '<div class="ev-script">' + esc(k.script) + '</div>' +
      '<div class="ev-cost">' + esc(k.cost) + '</div>' +
      blockers +
      '<div class="ev-ops">' + ops + '</div>' +
      btn +
      '</div>';
  }).join('');
  ekEvRestoreInputs(keep);
}

function ekEvRenderBaseline(s){
  const b = s.baseline || {};
  if(!b.exists){
    ekRo('#ev-baseline', [{k:'状态', v: b.note || t('尚未冻结基线'), cls:'warn'}]);
    const h = $('#h-ev-baseline');
    if(h) h.textContent = t('命令行执行 python tools/eval_gate.py --set-baseline 冻结；界面不提供，避免误把一次跑坏的结论固化成门禁。');
    return;
  }
  const rows = [
    {k:'冻结时间', v: b.generated_at || ''},
    {k:'样本规模', v: t('{0} 轮 / {1} 份报告', b.n_runs||0, b.n_files||0)},
    {k:'判分器自检', v: t('{0} 项通过', b.selftest_pass||0), cls:'ok'},
    {k:'覆盖率（可判）', v: ekPct(b.coverage_judgeable)}
  ];
  /* 注记只陈述口径，不带方向判断：这里的百分比一律是「判定通过率」（越高越好），
     而 false_recovery 这种维度「越低越好」说的是被测量的东西，不是这个百分比。
     混着说会让人把 100% 读成最差 —— 那比不写注记更误导。 */
  const NOTE = {
    human: t('沙箱内测得：无人值守场景下恒为 0，仅作对照'),
    false_recovery: t('衡量「宣称已恢复、其实没恢复」；这里的百分比是判定通过率，越高越好')
  };
  Object.keys(b.dims||{}).forEach(k=>{
    const v = b.dims[k];
    let label = '· ' + k;
    if(NOTE[k]) label += ' ⓘ';
    rows.push({k:label, v:ekPct(v), cls:''});
  });
  ekRo('#ev-baseline', rows);
  const h = $('#h-ev-baseline');
  if(h) h.textContent = t('带 ⓘ 的维度有口径说明：{0}', Object.keys(NOTE).map(k=>k+'：'+NOTE[k]).join('；'));
}

function ekEvRenderBoards(s){
  const rows = (s.boards||[]).map(b=>({
    k: b.name,
    v: b.available ? (b.source + ' · ' + b.note) : (t('未产出') + '：' + b.note),
    cls: b.available ? 'ok' : 'warn'
  }));
  ekRo('#ev-boards', rows);
}

function ekEvRenderReports(s){
  const box = $('#ev-reports'); if(!box) return;
  const rs = s.reports || [];
  if(!rs.length){ box.innerHTML = '<div class="ro-empty">' + t('docs/eval 下还没有 .json 产物') + '</div>'; return; }
  box.innerHTML = rs.map(r=>{
    const kb = r.size > 1024*1024 ? (Math.round(r.size/1024/102.4)/10)+' MB' : Math.max(1, Math.round(r.size/1024))+' KB';
    const kind = r.kind==='report' ? t('报告') : (r.kind==='shadow' ? t('影子') : (r.kind==='batch' ? t('批次') : t('其它')));
    return '<div class="ev-rep"><span class="k ' + (r.kind==='report'?'report':(r.kind==='shadow'?'shadow':'')) + '">' + esc(kind) + '</span>' +
      '<span class="n">' + esc(r.name) + '</span>' +
      '<span class="m">' + esc(kb) + ' · ' + esc(r.mtime) + '</span></div>';
  }).join('');
}

/* 运行：只读档直接跑；破坏性档先吃 428，弹服务端给的真实风险说明，确认后带 confirm 重发 */
async function ekEvRun(kind, dry){
  const k = ((EK_SET.ev||{}).kinds||[]).find(x=>x.key===kind);
  if(!k) return;
  if(!k.available){ toast(t('这一档当前不可用，先看卡片里的原因'), 'err'); return; }
  const body = {kind};
  if(dry) body.dry_run = true;
  const onlyEl = document.getElementById('ev_only_' + kind);
  if(onlyEl && onlyEl.value.trim()) body.only = onlyEl.value.trim();
  const repEl = document.getElementById('ev_repeat_' + kind);
  if(repEl && parseInt(repEl.value,10) > 0) body.repeat = parseInt(repEl.value,10);
  const awEl = document.getElementById('ev_aw_' + kind);
  if(awEl && awEl.checked) body.allow_writes = true;

  const title = dry ? t('预演：{0}', k.name) : t('开始评测：{0}', k.name);
  const r = await postConfirm('/api/eval/run', body, title, k.cost);
  if(!r) return;                        // 用户取消确认卡
  let j = {}; try{ j = await r.json(); }catch(e){}
  if(!r.ok){
    toast(t('启动失败：') + (j.error || ('HTTP ' + r.status)), 'err');
    if(j.blockers && j.blockers.length) toast(j.blockers.join('；'), 'err');
    await ekEvLoad();
    return;
  }
  if(j.dry_run){
    // 预演输出是脚本 stdout，几千字很正常：给宽版 + 等宽字体，正文区自己滚
    // （CSS #cf-msg 的 overflow:auto）。截断要明说，别让人以为脚本就输出这些。
    const raw = j.output || t('（无输出）');
    const cut = raw.length > 6000;
    await askConfirm(t('预演：{0}', k.name),
      raw.slice(0, 6000) + (cut ? t('\n\n……输出较长，只显示前 6000 字') : ''),
      t('知道了'), false, t('关闭'), {wide:true, mono:true});
    await ekEvLoad();
    return;
  }
  if(j.started){
    toast(t('已开始：{0}', j.name || k.name));
    if($('#lw-eval') && $('#lw-eval').classList.contains('collapsed')){
      $('#lw-eval').classList.remove('collapsed');
      const b = document.querySelector('[data-log="eval"][data-act="toggle"]');
      if(b) b.textContent = t('折叠');
    }
    await ekEvLoad();
    ekEvPoll(true);
  }
}

async function ekEvStop(){
  const r = await fetch('/api/eval/run',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({kind:'stop'})});
  let j = {}; try{ j = await r.json(); }catch(e){}
  if(j.ok) toast(t('已请求停止：{0}', j.stopped || ''));
  else toast(t('停止失败：') + (j.error || ('HTTP ' + r.status)), 'err');
  await ekEvLoad();
}

/* 轮询只在「设置页 + 评测 tab」可见时开：离开就停，否则后台一直每 2.5 秒打一次 status
   （status 会实探 Python 与读文件，没必要在用户看别的页时也跑）。 */
function ekEvPoll(on){
  if(EK_SET.evTimer){ clearInterval(EK_SET.evTimer); EK_SET.evTimer = null; }
  if(!on) return;
  EK_SET.evTimer = setInterval(async ()=>{
    const p = document.getElementById('panel-settings');
    if(!p || !p.classList.contains('show') || EK_SET.tab !== 'eval'){ ekEvPoll(false); return; }
    const s = await ekEvLoad();
    if(s && !s.running){ ekEvPoll(false); }   // 跑完了就停，别空转
  }, 2500);
}

/* ---------- 设置页交互绑定（唯一一处，全部挂在 document 上） ----------
   为什么挂 document 而不是 #panel-settings / #set-tabs：
   这两个容器都可能在脚本求值时尚不存在（深链、脚本插在 body 前、将来改成懒加载），
   一次 getElementById 拿到 null 就等于整页按钮永久失效，而且报错是静默的
   ——页面看着正常、点了毫无反应，最难查。document 一直存在，用 closest 限定作用域同样安全。
   注意这里是**唯一**的委托：同一批按钮不能绑两遍，否则一次点击会发两次请求。 */
(function(){
  ekDevBindSliders();          // 滑块 ↔ 数字框双向，只绑一次
  document.addEventListener('click', ev=>{
    /* 1) 设置页 tab */
    const tab = ev.target.closest('#set-tabs .cfg-tab');
    if(tab){ ekSetPane(tab.dataset.setTab); return; }
    /* 2) 评测档按钮（卡内动态生成，只能委托） */
    const evb = ev.target.closest('#ev-kinds [data-ev]');
    if(evb){
      if(evb.dataset.ev === 'stop') ekEvStop();
      else ekEvRun(evb.dataset.kind, evb.dataset.ev === 'dry');
      return;
    }
    if(!ev.target.closest('#panel-settings')) return;
    /* 3) 目录选择 */
    const pick = ev.target.closest('[data-spick]');
    if(pick){ ekSetPick(pick); return; }
    /* 4) 普通按钮按 id 分派 */
    const id = ev.target.id;
    if(id === 'btn-set-save') ekSetSaveBase();
    else if(id === 'btn-set-reload') ekSetLoadBase();
    else if(id === 'btn-s-weather') ekSetWeather();
    else if(id === 'btn-sd-save') ekSetSaveDev();
    else if(id === 'btn-sd-reset') ekSetResetDev();
    else if(id === 'btn-ev-refresh') ekEvLoad();
    else if(id === 'btn-sd-breaker') ekSetBreakerReset();
  });
  /* 5) 天气开关/城市：改了就立刻存，不再等旁边的「应用」。
     勾上一个开关却什么都不发生，是最难判断的一种"没生效"：
     用户只能反复点，而每次都以为是自己点错了。 */
  document.addEventListener('change', ev=>{
    const id = ev.target && ev.target.id;
    if(id === 's_weather_on' || id === 's_weather_city') ekSetWeather();
  });
  document.addEventListener('keydown', ev=>{
    if(ev.target && ev.target.id === 's_weather_city' && ev.key === 'Enter'){
      ev.preventDefault();
      ekSetWeather();
    }
  });
})();

async function ekSetBreakerReset(){
  if(!await askConfirm(t('重置断路器'),
    t('立刻放行一次恢复探针。只有确认底层环境已经恢复时才这么做——否则等于让同一个起不来的服务再被重启一轮。'),
    t('重置'))) return;
  try{
    const r = await post('/api/program/breaker-reset', {});
    if(r && r.ok){
      toast(t('断路器已重置'));
      const h = $('#h-sd-breaker');
      if(h) h.textContent = t('已重置（{0}）', new Date().toLocaleTimeString());
    }else{
      toast(t('重置失败'), 'err');
    }
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

/* ==================== 通知 / 定时行动 / 更新检查（v3.4） ====================
   三块各走自己的端点，不混进 POST /api/config 的整包：
     /api/alerts/config    Webhook + 冷却
     /api/missions/config  巡检间隔 / 晨报时刻 / 入站开关
     /api/update/config    更新地址 + 开关 + 复查间隔 + 忽略版本
   共同原则：改完立刻存、存完重读回显。同一份数据让用户在两处手动同步，
   迟早会出现「这边显示已开、实际没开」。
*/
const EK_EXT = {alert:null, ms:null, upd:null, notify:null, lan:null};

async function ekSetLoadExtras(){
  await Promise.all([ekSetLoadAlert(), ekSetLoadMissions(), ekSetLoadUpdate(), ekSetLoadNotify(), ekSetLoadLan(), ekSetLoadBackups()]);
  ekSetLanFWIdle();
}

/* ==================== 配置备份 / 恢复（v3.9） ====================
   备份文件由后端轮转，界面只认 slot=1|2|3，不接收路径。
   恢复是破坏性写操作：postConfirm 先吃服务端 428，弹真实风险说明，
   确认后才带 confirm 重发——前端自己弹一个提示框绕过不了服务端闸门。 */
async function ekSetLoadBackups(){
  const box = $('#cfg_backups');
  if(!box) return;
  try{
    const j = await (await fetch('/api/config/backups')).json();
    const arr = Array.isArray(j.backups) ? j.backups : [];
    if(!arr.length){
      box.innerHTML = '<div class="hint">' + esc(t('还没有备份，保存过一次配置后这里会出现历史版本。')) + '</div>';
      return;
    }
    box.innerHTML = arr.map(b=>{
      const desc = b.readable ? (b.summary || t('未配置项目路径')) : t('备份无法解析，不能用于恢复');
      return '<div class="bk-row"><div class="bk-info">' +
        '<b>' + esc(t('备份 {0}', b.slot)) + '</b> <span class="muted">' + esc(b.at) + '</span>' +
        '<div class="hint">' + esc(desc) + '</div></div>' +
        (b.readable
          ? '<button class="mini" data-bk-slot="' + Number(b.slot) + '">' + esc(t('恢复此版本')) + '</button>'
          : '<span class="muted">' + esc(t('不可恢复')) + '</span>') +
      '</div>';
    }).join('');
  }catch(e){
    box.innerHTML = '<div class="hint">' + esc(t('备份列表读取失败')) + '</div>';
  }
}

document.addEventListener('click', async e=>{
  if(e.target.closest && e.target.closest('#cfg-backup-reload')){ ekSetLoadBackups(); return; }
  const b = e.target.closest && e.target.closest('[data-bk-slot]');
  if(!b) return;
  const slot = Number(b.getAttribute('data-bk-slot'));
  const r = await postConfirm('/api/config/restore', {slot},
    t('恢复配置'), t('这会覆盖当前配置。'));
  if(!r) return;
  let j = {}; try{ j = await r.json(); }catch(err){}
  if(r.ok && j.ok){
    toast(t('配置已恢复，正在重新加载…'), 'ok');
    setTimeout(()=>location.reload(), 700);
  }else if(j.problems && j.problems.length){
    toast(t('操作失败：') + j.problems.join('；'), 'err');
  }else{
    toast(t('操作失败：') + (j.reason || ('HTTP ' + r.status)), 'err');
  }
});


