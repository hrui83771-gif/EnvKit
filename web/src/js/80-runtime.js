/* ---------- 全局通知 ---------- */

async function ekSetLoadNotify(){
  try{
    const n = await fetch('/api/notify/config').then(r=>r.json());
    EK_EXT.notify = {muted: !!n.muted};
    // 开关名字就叫「全局静音」，所以开关"开"必须等于 muted=true。
    // 曾经这里写成 !n.muted（勾上=通知开），标签和状态正好相反 ——
    // 用户看到的"亮着"其实是通知开着，点一下反而把自己静音了。
    $('#s_notify_mute').checked = !!n.muted;
    ekSetRenderNotifyState();
  }catch(e){
    EK_EXT.notify = null;
    ekSetRenderNotifyState(e);
  }
}

/* 状态并进测试按钮旁边那一格。
   两个来源（告警地址 / 是否静音）都要看：静音的优先 —— 那时候"已配置地址"
   是个危险的安慰，告警根本不会发出去。 */
function ekSetRenderNotifyState(err){
  const el = $('#s_alert_state');
  if(!el) return;
  if(err){ el.className = 'hint'; el.textContent = t('读取失败：{0}', err); return; }
  if(EK_EXT.notify && EK_EXT.notify.muted){
    el.className = 'hint bad';
    el.textContent = t('已静音：气泡与 Webhook 都不会发');
    return;
  }
  el.className = 'hint';
  const a = EK_EXT.alert || {};
  el.textContent = a.webhook ? t('已配置，会 POST 告警') : t('未配置，只弹气泡');
}

async function ekSetSaveNotify(){
  const muted = $('#s_notify_mute').checked;   // 开关开 = 静音开
  try{
    const r = await fetch('/api/notify/config',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({muted: muted})});
    if(!r.ok){ toast(t('保存失败：') + (await r.text()).trim(), 'err'); return; }
    const j = await r.json();
    EK_EXT.notify = {muted: !!j.muted};
    ekSetRenderNotifyState();
    toast(j.muted ? t('已开启全局静音') : t('通知已恢复'));
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

/* ---------- 告警通知 ---------- */

async function ekSetLoadAlert(){
  try{
    const a = await fetch('/api/alerts/config').then(r=>r.json());
    EK_EXT.alert = a;
    $('#s_alert_hook').value = a.webhook || '';
    $('#s_alert_cd').value = a.cooldown_secs || 300;
    ekSetRenderNotifyState();
  }catch(e){
    $('#s_alert_state').textContent = t('读取失败：{0}', e);
  }
}

async function ekSetSaveAlert(){
  const hook = $('#s_alert_hook').value.trim();
  const cd = parseInt($('#s_alert_cd').value, 10);
  const body = {webhook: hook};
  if(!isNaN(cd) && cd >= 0) body.cooldown_secs = cd;
  try{
    const r = await fetch('/api/alerts/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(!r.ok){ toast(t('保存失败：') + (await r.text()).trim(), 'err'); return; }
    await ekSetLoadAlert();
    toast(t('告警设置已保存'));
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

async function ekSetAlertTest(){
  const btn = $('#btn-s-alert-test');
  const label = btn ? btn.textContent : '';
  if(btn){ btn.disabled = true; btn.textContent = t('发送中…'); }
  try{
    const r = await fetch('/api/alerts/test',{method:'POST'});
    const j = await r.json().catch(()=>({}));
    if(!r.ok){ toast(t('发送失败：HTTP {0}', r.status), 'err'); }
    else if(j.webhook){ toast(t('已发送到 {0}', j.host || t('已配置地址'))); }
    else { toast(t('已弹桌面气泡（未配置 Webhook，不外连）')); }
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
  finally{ if(btn){ btn.disabled = false; btn.textContent = label || t('发一条测试告警'); } }
}

/* ---------- 定时行动 ---------- */

async function ekSetLoadMissions(){
  try{
    const m = await fetch('/api/missions/config').then(r=>r.json());
    EK_EXT.ms = m;
    /* enabled 是 *bool：老配置里没这一项时，返回的 JSON 里干脆没有这个字段。
       缺字段 = 默认开，所以不能用 !!m.enabled（undefined 会被读成"关"）。 */
    $('#s_ms_on').checked = (m.enabled !== false);
    $('#s_ms_patrol').value = m.patrol_interval_min || 60;
    $('#s_ms_brief').value = m.briefing_at || '08:30';
    $('#s_ms_inbound').checked = !!m.inbound_hook;
  }catch(e){
    /* 读失败就保持界面原样：宁可显示旧值，也不要把开关自动拨到某个位置 */
  }
}

async function ekSetSaveMissions(){
  const n = parseInt($('#s_ms_patrol').value, 10);
  const body = {
    enabled: $('#s_ms_on').checked,
    inbound_hook: $('#s_ms_inbound').checked,
    briefing_at: $('#s_ms_brief').value.trim()
  };
  if(!isNaN(n) && n >= 0) body.patrol_interval_min = n;
  try{
    const r = await fetch('/api/missions/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(!r.ok){ toast(t('保存失败：') + (await r.text()).trim(), 'err'); return; }
    await ekSetLoadMissions();
    toast(t('定时行动设置已保存'));
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

/* ---------- 手机访问（v3.6.2） ----------
   手机上能做什么，由服务端的路由表决定（另一个 mux，只有 6 条路由），
   不靠前端的 if。这里只是开关和查看。

   v3.6.2 调整：
   · 开关从「程序配置」卡片迁到顶栏「个人中心」下（#pp_lan_on），
     **唯一写入口**在这一处 —— 同一个开关有两处写入口必然对不上（MEMORY 第 2 条）。
   · 配对信息（配对码 / 网址 / 注意项 / 二维码）走弹窗 #lanpair。 */

async function ekSetLoadLan(){
  try{
    const j = await fetch('/api/lan/config').then(r=>r.json());
    EK_EXT.lan = j;
    /* 个人中心里的开关与副标题 */
    const sw = document.getElementById('pp_lan_on');
    if(sw) sw.checked = !!j.enabled;
    const sub = document.getElementById('pp_lan_sub');
    if(sub){
      if(!j.enabled) sub.textContent = t('关');
      else if(!j.running) sub.textContent = t('已开启但端口没在监听');
      else sub.textContent = t('已开启 · 手机访问 {0}', (j.addrs && j.addrs[0]) || t('（没找到可连的地址）'));
    }
    const pwState = document.getElementById('s_lan_pw_state');
    if(pwState){
      pwState.textContent = j.has_password
        ? t('已设置密码（可与配对码二选一）') : t('未设置密码');
    }
    const pwClear = document.getElementById('btn-lan-pw-clear');
    if(pwClear) pwClear.style.display = j.has_password ? '' : 'none';
    const portEl = document.getElementById('s_lan_port');
    /* 端口输入框只在**用户没动过**时才回填。
       反过来（无条件回填）会抹掉用户正在输入的值 —— 他改到一半，
       一次后台刷新就把他的输入冲掉了。 */
    if(portEl && !portEl.dataset.touched) portEl.value = j.port || '';
    const pwEl = document.getElementById('s_lan_pw');
    if(pwEl) pwEl.value = '';
    ekSetRenderLanAddrs(j);
    ekSetRenderLanDevs(j.devices || [], '#lp_devs');
    ekLanPairPaint(j);
  }catch(e){
    ekRo('#s_lan_addr', [{k:t('状态'), v:t('读取失败：{0}', e), cls:'bad'}]);
  }
}

function ekSetRenderLanAddrs(j){
  const rows = [];
  if(!j.enabled){
    rows.push({k:t('状态'), v:t('未开启（在顶栏「个人中心」下打开）'), cls:''});
  }else if(!j.running){
    /* 开关开着但没监听：端口被占 / 监听失败。这种情况必须说清楚，
       否则用户会一直拿手机连一个根本没有在听的端口。 */
    rows.push({k:t('状态'), v:t('已开启但端口未在监听（看日志：可能被占用）'), cls:'bad'});
  }else{
    const addrs = j.addrs || [];
    if(addrs.length){
      addrs.forEach((a,i)=>rows.push({k: i===0 ? t('手机访问') : '', v:a, cls:'ok'}));
    }else{
      rows.push({k:t('状态'), v:t('没检测到手机能连上的地址（这台电脑没连 WiFi/网线？）'), cls:'warn'});
    }
  }
  /* 配置端口改了但还没重启：上面列的地址按**实际**监听端口生成，
     这里必须说清楚，否则用户会以为"改了端口没生效 = 改坏了"。 */
  if(j.pending_restart){
    rows.push({k:t('端口'), v:t('已改为 {0}，重启 EnvKit 后生效（当前仍用 {1}）', j.port, j.listen_port), cls:'warn'});
  }
  ekRo('#s_lan_addr', rows);
}

/* ---------- 防火墙诊断 ---------- */

/* 初始提示。**不自动查**：诊断要跑两次 PowerShell（读规则 + 读网络类别），
   每次打开设置页都跑太贵，用户也未必关心。 */
function ekSetLanFWIdle(){
  const box = document.getElementById('s_lan_fw');
  if(!box || box.dataset.loaded) return;
  ekRo(box, [{k:t('防火墙'), v:t('点「检查防火墙」看 Windows 是否放行这个端口')}]);
}

/* 手机连不上的第一嫌疑不是 EnvKit，而是 Windows 防火墙 ——
   本机访问 200、端口在监听，包却停在系统那一层，从程序里完全看不出来。
   这里把规则状态、当前网络类别和该做什么一起给出来，避免用户到处试。 */
async function ekSetLoadLanFW(){
  const box = document.getElementById('s_lan_fw');
  if(box) box.dataset.loaded = '1';
  try{
    const r = await fetch('/api/lan/fw');
    const txt = await r.text();
    let j = {};
    try{ j = JSON.parse(txt); }catch(_){}
    if(!r.ok) throw new Error((j && j.msg) || txt.trim() || ('HTTP ' + r.status));
    ekSetRenderLanFW(j.diag || {});
  }catch(e){
    ekRo('#s_lan_fw', [{k:t('防火墙'), v:t('诊断失败：{0}', e), cls:'bad'}]);
  }
}

function ekSetRenderLanFW(d){
  const box = document.getElementById('s_lan_fw');
  if(box) box.dataset.loaded = '1';
  const rows = [];
  rows.push({
    k: t('防火墙'),
    v: d.allowed ? t('已放行 TCP/{0}', d.port) : t('未放行 TCP/{0}', d.port),
    cls: d.allowed ? 'ok' : 'bad'
  });
  if(d.network_name){
    rows.push({k:t('网络'), v: d.network_name + (d.network_category ? '（' + d.network_category + '）' : ''), cls:''});
  }
  if(d.hint) rows.push({k:t('原因'), v:t(d.hint), cls: d.allowed ? '' : 'warn'});
  if(d.advice) rows.push({k:t('建议'), v:t(d.advice), cls:''});
  ekRo('#s_lan_fw', rows);
  /* 已放行就别再留按钮：重复点只会再弹一次 UAC。 */
  const btn = document.getElementById('btn-lan-fw');
  if(btn) btn.style.display = d.allowed ? 'none' : '';
}

/* 放行按钮。POST 是**同步等确认**的（最长 90 秒，等用户在 UAC 窗口点「是」），
   所以按钮期间必须禁用并改文案，否则用户会以为点了没反应而反复点。 */
async function ekSetLanFWAllow(){
  const btn = document.getElementById('btn-lan-fw');
  if(btn){ btn.disabled = true; btn.textContent = t('放行中…（在 UAC 窗口点「是」）'); }
  try{
    const r = await fetch('/api/lan/fw',{method:'POST'});
    const txt = await r.text();
    let j = {};
    try{ j = JSON.parse(txt); }catch(_){}
    if(!r.ok) throw new Error((j && j.msg) || txt.trim() || ('HTTP ' + r.status));
    ekSetRenderLanFW(j.diag || {});
    if(j.diag && j.diag.allowed) toast(t('防火墙已放行'));
    else toast(t('放行失败'), 'err');
  }catch(e){
    toast(t('放行失败：{0}', e), 'err');
  }finally{
    if(btn){
      btn.disabled = false;
      btn.textContent = t('放行防火墙（需要管理员权限）');
    }
  }
}

function ekLanAgo(ts){
  if(!ts) return '';
  const s = Math.max(0, Math.floor(Date.now()/1000) - ts);
  if(s < 60) return t('刚刚');
  if(s < 3600) return Math.floor(s/60) + t(' 分钟前');
  if(s < 86400) return Math.floor(s/3600) + t(' 小时前');
  return Math.floor(s/86400) + t(' 天前');
}

function ekSetRenderLanDevs(list, sel){
  const box = document.querySelector(sel || '#s_lan_devs');
  if(!box) return;
  box.innerHTML = '';
  if(!list.length){
    const e = document.createElement('div');
    e.className = 'ro-empty';
    e.textContent = t('还没有手机配对过');
    box.appendChild(e);
    return;
  }
  list.forEach(d=>{
    const row = document.createElement('div'); row.className = 'lan-dev';
    const nm = document.createElement('span'); nm.className = 'nm';
    nm.textContent = d.name || t('未知设备');
    const tm = document.createElement('span'); tm.className = 'tm';
    tm.textContent = ekLanAgo(d.last);
    const b = document.createElement('button'); b.className = 'ghost';
    b.textContent = t('移除');
    b.onclick = async ()=>{
      if(!confirm(t('移除后这台手机要重新配对，继续？'))) return;
      try{
        await fetch('/api/lan/kick',{method:'POST',headers:{'Content-Type':'application/json'},
          body:JSON.stringify({id:d.id})});
        ekSetLoadLan();
        toast(t('已移除该设备'));
      }catch(e){ toast(t('网络错误：')+e, 'err'); }
    };
    row.appendChild(nm); row.appendChild(tm); row.appendChild(b);
    box.appendChild(row);
  });
}

async function ekSetSaveLan(patch){
  try{
    const r = await fetch('/api/lan/config',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify(patch)});
    const j = await r.json().catch(()=>({}));
    if(!r.ok){
      toast((j && j.msg) || t('保存失败'), 'err');
      return null;
    }
    EK_EXT.lan = j;
    return j;
  }catch(e){ toast(t('网络错误：')+e, 'err'); return null; }
}

/* 个人中心开关。开启成功后**自动弹出配对窗** ——
   用户开这个开关的目的就是"让手机连上"，开完还让他自己去设置页找地址，
   等于把下一步藏起来了。 */
async function ekLanToggle(on){
  if(on){
    const j = await ekSetSaveLan({enabled:true});
    if(!j){ ekLanPaintSwitch(false); return; }
    await ekSetLoadLan();
    toast(t('手机访问已开启'));
    ekLanPairOpen();
    return;
  }
  const j = await ekSetSaveLan({enabled:false});
  if(!j){ ekLanPaintSwitch(true); return; }
  await ekSetLoadLan();
  toast(t('手机访问已关闭'));
}

/* 开关 + 副标题。单独抽出来：失败回滚时要用，不能只改 checked 不改文案。 */
function ekLanPaintSwitch(on){
  const sw = document.getElementById('pp_lan_on');
  if(sw) sw.checked = !!on;
  const sub = document.getElementById('pp_lan_sub');
  if(sub && !on) sub.textContent = t('关');
}

/* 配对弹窗内容 */
function ekLanPairPaint(j){
  j = j || EK_EXT.lan || {};
  const code = document.getElementById('lp_code');
  if(code) code.textContent = j.pair_code || '——';
  const url = document.getElementById('lp_url');
  if(url){
    const a = (j.addrs && j.addrs[0]) || '';
    url.textContent = a ? a + (a.indexOf('?') >= 0 ? '&' : '?') + 'code=' + (j.pair_code||'') : t('（没找到手机能连的地址）');
  }
  const qr = document.getElementById('lp_qr');
  if(qr){
    const ok = j.enabled && (j.addrs || []).length > 0;
    qr.style.display = ok ? '' : 'none';
    if(ok) ekLanLoadQR(qr, '/api/lan/qr?t=' + Date.now());
  }
  ekLanPaintCaps(j);
  ekLanPaintOTP(j);
}

/* 三个能力开关：画的是**实际生效值**（eff_*），不是 raw。
   raw 为 null 表示用户从没设过（老配置），此时生效值是代码里的缺省
   （看屏幕开、剪贴板关、终端关）。画 raw 会让"从没设过"显示成"全关"，
   而实际截屏是开着的 —— 那是最容易让人误判的一种不一致。 */
function ekLanPaintCaps(j){
  const rows = [
    ['lp_shot', 'eff_shot'],
    ['lp_clip', 'eff_clip'],
    ['lp_term', 'eff_term'],
  ];
  for(const [id, key] of rows){
    const el = document.getElementById(id);
    if(el) el.checked = !!j[key];
  }
  /* 这台机器没有 ConPTY（Win10 1809 以下）时别让用户打开一个必然失败的开关 */
  const termRow = document.getElementById('lp_term_row');
  if(termRow && j.conpty === false){
    termRow.classList.add('disabled');
    const box = document.getElementById('lp_term');
    if(box){ box.checked = false; box.disabled = true; }
  }
}

/* 二次验证：开关 + 密钥二维码 + 密钥原文（口令器扫不出时手输） */
function ekLanPaintOTP(j){
  const on = document.getElementById('lp_otp_on');
  if(on) on.checked = !!j.require_otp;
  const key = document.getElementById('lp_otp_key');
  if(key) key.textContent = j.otp_secret || t('（还没有密钥，打开开关会自动生成）');
  const box = document.getElementById('lp_otp_box');
  const qimg = document.getElementById('lp_otp_qr');
  const show = !!j.require_otp && !!j.otp_secret;
  if(box) box.style.display = show ? '' : 'none';
  if(show && qimg) ekLanLoadQR(qimg, '/api/lan/qr?otp=1&t=' + Date.now());
}

/* 加载二维码。
   **不能直接写 <img src="/api/lan/qr">** ——
   <img> 发请求不会带 X-EnvKit-Token 头，而主控制台的接口要令牌，
   结果就是一片碎图标（用户看到的是"二维码坏了"）。
   所以走 fetch（带令牌头）→ 转成 data URL。

   为什么用 data URL 而不是 objectURL：
   **CSP 的 img-src 是 `'self' data:`，不含 blob:** ——
   用 URL.createObjectURL 会被 CSP 拦掉，img.onerror 触发，
   用户看到的还是"二维码坏了"（本项目 AI 图片附件踩过同一个坑，见 app.js 5941 行）。
   data URL 在 img-src 白名单里，不用改 CSP。 */
async function ekLanLoadQR(qr, url){
  try{
    const r = await fetch(url || ('/api/lan/qr?t=' + Date.now()));
    if(!r.ok) throw new Error('HTTP ' + r.status);
    const blob = await r.blob();
    const rd = new FileReader();
    rd.onload = ()=>{ qr.src = rd.result; };
    rd.onerror = ()=>{ qr.alt = t('二维码加载失败'); };
    rd.readAsDataURL(blob);
  }catch(e){
    qr.alt = t('二维码加载失败（{0}）', e);
  }
}

function ekLanPairOpen(){
  const m = document.getElementById('lanpair');
  if(!m) return;
  m.classList.add('show');
  ekSetLoadLan();
}

function ekLanPairClose(){
  const m = document.getElementById('lanpair');
  if(m) m.classList.remove('show');
}

async function ekSetLanRotate(){
  try{
    const r = await fetch('/api/lan/rotate',{method:'POST'});
    if(!r.ok){ toast(t('更换失败'), 'err'); return; }
    const j = await r.json();
    await ekSetLoadLan();
    toast(t('配对码已更换，旧码立即失效'));
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

async function ekSetLanSetPw(){
  const pw = $('#s_lan_pw').value;
  if(pw.length < 6){ toast(t('密码至少 6 位'), 'err'); return; }
  const j = await ekSetSaveLan({password:pw});
  if(!j) return;
  await ekSetLoadLan();
  toast(t('密码已设置，手机上可以用它代替配对码'));
}

async function ekSetLanClearPw(){
  if(!confirm(t('清除后手机只能用配对码进入，继续？'))) return;
  const j = await ekSetSaveLan({clear_password:true});
  if(!j) return;
  await ekSetLoadLan();
  toast(t('密码已清除'));
}

async function ekSetLanPort(){
  const el = $('#s_lan_port');
  const n = parseInt(el.value, 10);
  if(isNaN(n) || n < 1024 || n > 65535){ toast(t('端口需在 1024-65535'), 'err'); return; }
  const j = await ekSetSaveLan({port:n});
  if(!j) return;
  /* 存成功才算"用户已经定下这个值"，之后后台刷新不再覆盖它。 */
  el.dataset.touched = '1';
  /* 已经开着且端口不同 → 得重启；开关关着时下一次开启就会用新端口，不必吓唬用户。 */
  toast(j.pending_restart ? t('端口已保存，重启 EnvKit 后生效') : t('端口已保存'));
  await ekSetLoadLan();
}

/* ---------- 更新检查 ---------- */

async function ekSetLoadUpdate(){
  try{
    const u = await fetch('/api/update/config').then(r=>r.json());
    EK_EXT.upd = u;
    $('#s_upd_on').checked = !!u.enabled;
    $('#s_update_url').value = u.url || '';
    $('#s_upd_interval').value = u.interval_hours || 0;
    $('#s_upd_ignore').value = u.ignore_version || '';
    ekSetRenderUpdate(u);
  }catch(e){
    ekRo('#s_upd_state', [{k:'状态', v:t('读取失败：{0}', e), cls:'bad'}]);
  }
}

/* 状态行单独一个函数：保存后只更新这块，不重填输入框（否则正在输入的内容会被冲掉） */
function ekSetRenderUpdate(u){
  const r = (u && u.result) || {};
  let st = t('尚未检查'), cls = '';
  if(r.has_new && r.ignored){ st = t('有新版本 {0}，已按设置不再提示', r.latest); cls = 'warn'; }
  else if(r.has_new){ st = t('有新版本 {0}', r.latest); cls = 'warn'; }
  else if(r.error){ st = r.error; cls = 'warn'; }
  else if(r.latest){ st = t('已是最新版本'); cls = 'ok'; }
  ekRo('#s_upd_state', [
    {k:'当前版本', v: r.current || ''},
    {k:'最新版本', v: r.latest || ''},
    {k:'上次检查', v: r.at || ''},
    {k:'状态', v: st, cls: cls}
  ]);
}

async function ekSetSaveUpdate(recheck){
  const n = parseInt($('#s_upd_interval').value, 10);
  const body = {
    enabled: $('#s_upd_on').checked,
    ignore_version: $('#s_upd_ignore').value.trim(),
    url: $('#s_update_url').value.trim(),
    recheck: !!recheck
  };
  if(!isNaN(n) && n >= 0) body.interval_hours = n;
  try{
    const r = await fetch('/api/update/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(!r.ok){ toast(t('保存失败：') + (await r.text()).trim(), 'err'); return; }
    const u = await r.json();
    EK_EXT.upd = u;
    /* 同步全局快照：基础保存条也会带 update_url 出去，不同步就会用旧值盖回来 */
    if(cfg) cfg.update_url = u.url || '';
    ekSetRenderUpdate(u);
    if(recheck){
      toast(t('已发起检查，几秒后刷新状态'));
      /* 后端是异步重查（httpShort 最长 4 秒，同步等会卡住按钮） */
      setTimeout(()=>{
        fetch('/api/update/config').then(x=>x.json()).then(v=>ekSetRenderUpdate(v)).catch(()=>{});
      }, 2500);
    }
  }catch(e){ toast(t('网络错误：')+e, 'err'); }
}

/* 绑定：输入框用 change（失焦/回车才触发），按钮用委托 */
(function(){
  const w = document.getElementById('panel-settings');
  if(!w) return;
  const auto = (id, fn) => { const el = document.getElementById(id); if(el) el.addEventListener('change', fn); };
  auto('s_alert_hook', ekSetSaveAlert);
  auto('s_alert_cd', ekSetSaveAlert);
  auto('s_notify_mute', ekSetSaveNotify);
  auto('s_ms_on', ekSetSaveMissions);
  auto('s_ms_patrol', ekSetSaveMissions);
  auto('s_ms_brief', ekSetSaveMissions);
  auto('s_ms_inbound', ekSetSaveMissions);
  auto('s_upd_on', ()=>ekSetSaveUpdate(false));
  auto('s_update_url', ()=>ekSetSaveUpdate(false));
  auto('s_upd_interval', ()=>ekSetSaveUpdate(false));
  auto('s_upd_ignore', ()=>ekSetSaveUpdate(false));
  /* 手机访问：端口用 change（改完即存）。开关不在这里 ——
     v3.6.2 已迁到顶栏「个人中心」（#pp_lan_on），那里是唯一写入口。 */
  auto('s_lan_port', ekSetLanPort);
  /* 备份目录只改"以后备份写到哪"，本身不单独落盘（点保存才存）。
     提示跟着输入实时变，否则改完还得再点一次保存才知道会生效到哪。 */
  (function(){
    const el = document.getElementById('s_backup_dir');
    if(el) el.addEventListener('input', ()=> ekSetBackupHint(el.value));
  })();
  document.addEventListener('click', ev=>{
    const el = ev.target;
    if(!el || !el.id) return;
    if(el.id === 'btn-s-alert-test') ekSetAlertTest();
    else if(el.id === 'btn-s-upd-check') ekSetSaveUpdate(true);
    else if(el.id === 'btn-lan-pw') ekSetLanSetPw();
    else if(el.id === 'btn-lan-pw-clear') ekSetLanClearPw();
    else if(el.id === 'btn-lan-fw') ekSetLanFWAllow();
    else if(el.id === 'btn-lan-fw-check') ekSetLoadLanFW();
    else if(el.id === 'btn-lan-open' || el.id === 'btn-lan-open2') ekLanPairOpen();
    else if(el.id === 'lp_rotate') ekSetLanRotate();
    else if(el.id === 'lp-close') ekLanPairClose();
    else if(el.id === 'lp_copy'){
      const u = document.getElementById('lp_url');
      const txt = u ? u.textContent : '';
      if(!txt) return;
      navigator.clipboard.writeText(txt).then(
        ()=> toast(t('网址已复制')),
        ()=> toast(t('复制失败，请手动选中网址'), 'err'));
    }
  });
})();

/* ---------- 手机访问：个人中心开关与配对弹窗（v3.6.2） ---------- */
(function(){
  const sw = document.getElementById('pp_lan_on');
  if(sw){
    sw.addEventListener('change', ()=>{
      ekLanToggle(sw.checked);
    });
  }
  /* 弹窗点背景关闭（点 .mbox 内部不关）。 */
  const m = document.getElementById('lanpair');
  if(m){
    m.addEventListener('click', e=>{
      if(e.target === m) ekLanPairClose();
    });
  }
  document.addEventListener('keydown', e=>{
    if(e.key === 'Escape' && m && m.classList.contains('show')) ekLanPairClose();
  });
  /* 打开个人中心时刷新一次状态（开关可能被别人改过）。 */
  const btn = document.getElementById('btn-profile');
  if(btn) btn.addEventListener('click', ()=>{ setTimeout(()=>ekSetLoadLan(), 60); });

  /* **页面一进来就把开关状态读回来**。
     旧实现只在打开「程序配置」页时调 ekSetLoadLan，于是个人中心里那个开关
     一开始显示的是未勾选 —— 用户看到"关着"，实际是开着的；
     他一碰就把它关了（真实会发生的事，不是理论风险）。
     开关在个人中心下，状态就必须自己负责读，不能指望别的面板顺带刷新。 */
  ekSetLoadLan();

  /* v3.6.2：手机端本地能力开关 + 二次验证。
     一律「先存、再整块重画」：存失败时要回到服务端的真实值，
     只改勾选框会让用户以为已经改成了（而实际没落盘）。 */
  const capBind = [
    ['lp_shot', 'allow_shot', '看屏幕'],
    ['lp_clip', 'allow_clip', '剪贴板'],
    ['lp_term', 'allow_term', '终端'],
  ];
  for(const [id, key, label] of capBind){
    const el = document.getElementById(id);
    if(!el) continue;
    el.addEventListener('change', async ()=>{
      const patch = {};
      patch[key] = el.checked;
      /* 终端是"什么都能干"的入口，单独给一次确认 —— 它不是普通开关。 */
      if(key === 'allow_term' && el.checked &&
         !confirm(t('打开后，手机上可以在电脑上执行任意命令（等同你本人操作）。继续？'))){
        el.checked = false;
        return;
      }
      const j = await ekSetSaveLan(patch);
      await ekSetLoadLan();
      if(!j) return;
      toast(el.checked ? t('手机上已开启：{0}', label) : t('手机上已关闭：{0}', label));
    });
  }

  const otpOn = document.getElementById('lp_otp_on');
  if(otpOn){
    otpOn.addEventListener('change', async ()=>{
      if(otpOn.checked && !confirm(t('打开后，新手机配对时必须再输口令器里的 6 位验证码。已经配对的手机不受影响。继续？'))){
        otpOn.checked = false;
        return;
      }
      const j = await ekSetSaveLan({require_otp: otpOn.checked});
      await ekSetLoadLan();
      if(j && otpOn.checked) toast(t('二次验证已开启：用口令器扫右边的二维码'));
    });
  }

  const otpNew = document.getElementById('lp_otp_new');
  if(otpNew){
    otpNew.addEventListener('click', async ()=>{
      if(!confirm(t('重新生成密钥后，已经扫过旧密钥的口令器立即失效，需要重新扫。继续？'))) return;
      const j = await ekSetSaveLan({new_otp:true});
      await ekSetLoadLan();
      if(j) toast(t('已生成新密钥，请用口令器重新扫码'));
    });
  }
})();
