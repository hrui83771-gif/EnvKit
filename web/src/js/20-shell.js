/* ---------- API 令牌：给所有 /api 请求自动带上随机令牌（防其它网页 CSRF） ---------- */
(function(){
  let reloading = false;
  const orig = window.fetch.bind(window);
  window.fetch = function(input, init){
    try{
      init = Object.assign({}, init || {});
      const url = typeof input === 'string' ? input : (input && input.url) || '';
      if(url.startsWith('/api/') || url.startsWith(location.origin + '/api/')){
        init.headers = Object.assign({}, init.headers || {}, {'X-EnvKit-Token': window.__EK_TOKEN__});
      }
    }catch(e){}
    return orig(input, init).then(res=>{
      try{
        const url = typeof input === 'string' ? input : (input && input.url) || '';
        if(res.status === 403 && (url.startsWith('/api/') || url.startsWith(location.origin + '/api/')) && !reloading){
          reloading = true;
          alert('EnvKit 已重启，本页令牌已过期，点击确定自动刷新页面。');
          location.reload();
        }
      }catch(e){}
      return res;
    });
  };
})();

/* ---------- 非阻塞 toast：替代正常流程里的 alert（"应用已重启需刷新"这类阻断式提示除外） ---------- */
function toast(msg, cls){
  const box = document.getElementById('toasts'); if(!box) return;
  const d = document.createElement('div'); d.className = 'toast' + (cls ? ' ' + cls : ''); d.textContent = msg;
  box.appendChild(d);
  setTimeout(()=>{ d.classList.add('out'); setTimeout(()=>d.remove(), 350); }, 4200);
  while(box.children.length > 4) box.firstChild.remove();
}
/* esc：把服务端/外部数据插值进 innerHTML 前转义（日志内容会回流进模型参数，不可信任） */
function esc(s){ return String(s == null ? '' : s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }

/* ---------- 日志（按 scope 分流 + 工具条） ---------- */
/* 没有级别标签的行只能按字面判断（子进程裸输出没有 [WARN]/[FAIL] 可读）。
   **两个消费者必须共用这一个正则**：一处是"这行在界面上标不标红"，
   另一处是"这行要不要收进『最近错误』发给 AI"。各用一套就会出现
   「日志红着、AI 说没错误」或者「AI 说一堆错、屏幕上一条红的都没有」。
   带级别标签的行**不**走这里 —— 级别就是权威口径。 */
const BARE_ERR_RE = /(fatal|panic|失败|错误|无法|不存在|拒绝|refused|denied|EADDRINUSE|exit status|cannot)/i;

// ekRefreshLogBars 按当前语言重排行数标签（切换语言时调用；日志正文保持原文）
function ekRefreshLogBars(){
  if(typeof LOG === 'undefined') return;
  for(const k in LOG){
    const L = LOG[k]; if(!L) continue;
    const bar = document.getElementById(L.bar);
    if(bar) bar.textContent = t('{0} 行', L.n || 0);
  }
  ekSyncLogToggle();
}

/* 折叠按钮的文案必须由**当前状态**推导，不能照抄标记层：
   日志默认是 collapsed，按钮却写死「折叠」，用户看到的就是
   "日志已经收起 + 按钮写着折叠"，点一次反而展开——要点两次才顺。
   文案统一走 t()，否则英文界面里点一下会突然变回中文。 */
function ekSyncLogToggle(){
  if(typeof LOG === 'undefined') return;
  document.querySelectorAll('[data-log][data-act="toggle"]').forEach(b=>{
    const L = LOG[b.dataset.log]; if(!L) return;
    const w = document.getElementById(L.wrap); if(!w) return;
    b.textContent = w.classList.contains('collapsed') ? t('展开') : t('折叠');
  });
}

const LOG = {
  install:{el:$('#log-install'), bar:'lm-install', dot:'ld-install', wrap:'lw-install', n:0, auto:true, light:false, lines:[]},
  config:{el:$('#log-config'), bar:'lm-config', dot:'ld-config', wrap:'lw-config', n:0, auto:true, light:false, lines:[]},
  web:{el:$('#log-web'), bar:'lm-web', dot:'ld-web', wrap:'lw-web', n:0, auto:true, light:false, lines:[]},
  backend:{el:$('#log-backend'), bar:'lm-backend', dot:'ld-backend', wrap:'lw-backend', n:0, auto:true, light:false, lines:[]},
  chain:{el:$('#log-chain'), bar:'lm-chain', dot:'ld-chain', wrap:'lw-chain', n:0, auto:true, light:false, lines:[]},
  // 评测日志：后端 info(scEval,...) 直接按 scope=eval 流进来，不需要单独的轮询接口
  eval:{el:$('#log-eval'), bar:'lm-eval', dot:'ld-eval', wrap:'lw-eval', n:0, auto:true, light:false, lines:[]}
};
function appendLog(scope, src, line){
  let keys;
  if(scope==='sys') keys = ['install','config','chain','web','backend'];
  else if(scope==='start') keys = src ? [src] : ['web','backend'];
  else keys = [scope];
  keys.forEach(k=>{
    const L = LOG[k]; if(!L) return;
    const div = document.createElement('div'); div.className='ln';
    const m = line.match(/\[(\w+)\]/);
    if(m){ const lvl=m[1];
      if(['FAIL','ERROR'].includes(lvl)) div.classList.add('err');
      else if(['WARN','HINT'].includes(lvl)) div.classList.add('warn');
      else if(lvl==='OK') div.classList.add('ok');
      else if(lvl==='INFO') div.classList.add('INFO'); }
    /* 无日志级别标签的行：按内容兜底识别错误/警告（新手看不懂日志，先让错误自己"跳出来"） */
    if(!m){
      if(BARE_ERR_RE.test(line)) div.classList.add('err');
      else if(/(警告|warn|hint|重试)/i.test(line)) div.classList.add('warn');
    }
    div.textContent = line;
    L.el.appendChild(div); L.n++; L.lines.push(line);
    const bar = document.getElementById(L.bar); if(bar) bar.textContent = t('{0} 行', L.n);
    const dot = document.getElementById(L.dot);
    if(dot){ dot.className='dot'; if(lvlOf(line)==='err') dot.classList.add('err'); else if(lvlOf(line)==='ok') dot.classList.add('ok'); }
    if(L.auto) L.el.scrollTop = L.el.scrollHeight;
  });
}
function lvlOf(line){ const m=line.match(/\[(\w+)\]/); if(!m) return ''; const l=m[1];
  if(['FAIL','ERROR'].includes(l)) return 'err'; if(['WARN','HINT'].includes(l)) return 'warn'; if(l==='OK') return 'ok'; return ''; }
const es = new EventSource('/api/events?t=' + encodeURIComponent(window.__EK_TOKEN__));
es.onmessage = e => { try{ const m=JSON.parse(e.data); appendLog(m.scope, m.src, m.line); }catch(_){} };
let esErrs = 0;
es.onopen = ()=>{ esErrs = 0; };   // 重连成功即复位计数，避免累计误杀
es.onerror = () => { if(++esErrs >= 6){ es.close(); alert('与 EnvKit 的连接已断开（可能已重启），点击确定刷新页面。'); location.reload(); } };

document.addEventListener('click', ev=>{
  const b = ev.target.closest('[data-log]'); if(!b) return;
  const k=b.dataset.log, act=b.dataset.act, L=LOG[k];
  if(act==='toggle'){ const w=document.getElementById(L.wrap); w.classList.toggle('collapsed'); b.textContent = w.classList.contains('collapsed') ? t('展开') : t('折叠'); }
  else if(act==='clear'){ L.el.innerHTML=''; L.n=0; L.lines=[]; document.getElementById(L.bar).textContent=t('{0} 行', 0); }
  else if(act==='copy'){ navigator.clipboard.writeText(L.lines.join('\n')); b.textContent=t('已复制'); setTimeout(()=>b.textContent=t('复制'),1200); }
  else if(act==='download'){ const blob=new Blob([L.lines.join('\n')],{type:'text/plain'}); const a=document.createElement('a'); a.href=URL.createObjectURL(blob); a.download='envkit-'+k+'.log'; a.click(); }
  else if(act==='light'){ L.light=!L.light; L.el.classList.toggle('light', L.light); b.classList.toggle('on', L.light); }
  else if(act==='auto'){ L.auto=!L.auto; b.classList.toggle('on', L.auto); }
});

/* ---------- 路由 ---------- */
function goto(panel){
  document.querySelectorAll('.nav').forEach(x=>x.classList.toggle('active', x.dataset.panel===panel));
  document.querySelectorAll('.panel').forEach(p=>p.classList.toggle('show', p.id==='panel-'+panel));
  // 切进来才拉，避免每次启动都读一遍文件（v2.7：审计面板同时拉看板统计）
  if(panel==='audit'){ loadAudit(); loadStats(); }
  // 设置页每次进入都重读：它和「程序配置」「链端校验」共用同一份 config.json，
  // 缓存一份只在打开时读的值，切回来就会显示过期的目录/端口。
  if(panel==='settings'){ ekSettingsEnter(); }
}
document.querySelectorAll('.nav').forEach(n=>n.addEventListener('click',()=>goto(n.dataset.panel)));
document.querySelectorAll('[data-goto]').forEach(c=>c.addEventListener('click',()=>goto(c.dataset.goto)));
// 深链：#config / #chain / #audit … 托盘右键「程序配置」直接落到对应面板。
// 用 goto 且不模拟点击：新标签页打开时也能一次到位。
function ekApplyHash(){
  const h = (location.hash || '').replace(/^#/, '');
  if(h && document.getElementById('panel-' + h)) goto(h);
}
ekApplyHash();
window.addEventListener('hashchange', ekApplyHash);
$('#next-install').onclick = ()=>goto('config');
$('#next-config').onclick = ()=>goto('chain');
$('#next-chain').onclick = ()=>goto('start');

/* ---------- 配置 + 自动保存 ---------- */
function syncStartDirs(){
  $('#fe_dir2').value = (cfg.projects&&cfg.projects.frontend_dir)||'';
  $('#be_dir2').value = (cfg.projects&&cfg.projects.backend_dir)||'';
}
function projectsBody(){
  return {
    frontend_dir:$('#fe_dir').value.trim(), backend_dir:$('#be_dir').value.trim(),
    mysql_host:$('#mysql_host').value.trim(), mysql_port:parseInt($('#mysql_port2').value)||3306,
    mysql_user:$('#mysql_user').value.trim(), mysql_password:$('#mysql_pass').value,
    db_name:$('#db_name').value.trim(), sql_file:$('#sql_path').value.trim(),
    scan_ports:(cfg.projects&&cfg.projects.scan_ports)||[],
    web_scripts:(cfg.projects&&cfg.projects.web_scripts)||[],
    // v3.7.1：备份目录在「设置 → 基础」里可改，本页没有对应输入框。
    // 表单提交是**整体替换 Projects**，不回传就被抹成空 —— 与 scan_ports /
    // web_scripts 同一套处理：页面不显示，但必须原样带过去。
    backup_dir:(cfg.projects&&cfg.projects.backup_dir)||'',
    // v2.1 启动方式覆盖项：留空 = 服务端按 package.json / 项目结构推断。
    // 必须显式回传，漏了会被 mergeFormConfig 抹掉（表单提交是整体替换 Projects）。
    web_script:($('#cfg_web_script')||{}).value ? $('#cfg_web_script').value.trim() : '',
    backend_file:($('#cfg_backend_file')||{}).value ? $('#cfg_backend_file').value.trim() : ''
  };
}
function chainBody(){
  return {
    ssh_host:$('#ch_ssh_host').value.trim(), ssh_port:parseInt($('#ch_ssh_port').value)||22,
    ssh_user:$('#ch_ssh_user').value.trim(), ssh_password:$('#ch_ssh_pass').value, ssh_key:$('#ch_ssh_key').value.trim(),
    chain_host:$('#ch_host').value.trim(), chain_port:parseInt($('#ch_port').value)||20200,
    chain_dir:$('#ch_dir').value.trim(), chain_start:$('#ch_start').value.trim(),
    webase_dir:$('#ch_wdir').value.trim(), webase_start:$('#ch_wstart').value.trim(),
    webase_port:parseInt($('#ch_wport').value)||5002, group_id:parseInt($('#ch_group').value)||1,
    chain_autorecover:$('#ch_autorecover').checked,
    chain_guard:$('#ch_guard').checked,
    chain_guard_secs:parseInt($('#ch_guard_secs').value)||60,
    chain_notify:$('#ch_notify').checked
  };
}
function configBody(){
  const comps = (cfg.components||[]).map((c,i)=>({...c, url:(document.getElementById('compurl-'+i)||{}).value||c.url}));
  return {
    app_name:cfg.app_name, app_title:cfg.app_title,
    install_dir:$('#install_dir').value,
    proxy:{http:$('#proxy_http').value, https:$('#proxy_https').value},
    goproxy:$('#goproxy').value, gosumdb:cfg.gosumdb||'', go_cgo:cfg.go_cgo||'', npm_registry:$('#npm_registry').value,
    mysql:{password:$('#mysql_password').value, port:parseInt($('#mysql_port').value)||3306, service_name:$('#mysql_service').value},
    projects:projectsBody(), chain:chainBody(), components:comps,
    /* ai 由「AI 助手」页单独保存（/api/ai/config）：这里必须原样回传，
       否则程序配置页的自动保存会把 API Key 抹掉（后端也有兜底合并） */
    ai:cfg.ai
    /* pet（桌宠天气）**不回传**：它同样由独立接口 /api/pet/config 保存，
       且表单页没有对应控件。后端 mergeFormConfig 会无条件保留 cur.Pet，
       两侧都别改 —— 少了任何一侧，改一下"安装目录"就会把天气开关抹成关闭 */
  };
}
function markDirty(){
  if(!loaded) return;
  dirty = true;
  $('#save-state').textContent = '未保存…';
  $('#save-state2').textContent = '未保存…';
  $('#save-state3').textContent = '未保存…';
  clearTimeout(saveTimer);
  saveTimer = setTimeout(saveNow, 800);
}
async function saveNow(){
  if(!loaded || !dirty) return;
  const body = configBody();
  const fail = (msg)=>{ ['save-state','save-state2','save-state3'].forEach(id=>{ const el=$('#'+id); el.textContent=msg; el.title=msg; }); };
  try{
    const r = await fetch('/api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(r.ok){ cfg = body; dirty = false; $('#save-state').textContent=t('已保存'); $('#save-state2').textContent=t('已保存'); $('#save-state3').textContent=t('已保存'); syncStartDirs(); loadLaunchPlan(); }
    else if(r.status===400){
      // 硬校验拒绝：把第一个问题亮在保存状态上，完整列表放悬浮提示
      let msg = t('保存失败');
      try{ const j = await r.json(); if(j.problems && j.problems.length){ msg = t('保存失败：{0}', j.problems[0].msg); const all = j.problems.map(p=>p.field+' → '+p.msg).join('\n'); fail(msg); setTimeout(()=>{ ['save-state','save-state2','save-state3'].forEach(id=>{ $('#'+id).title=all; }); },0); return; } }catch(e){}
      fail(msg);
    }
    else { fail(t('保存失败')); }
  }catch(e){ fail(t('保存失败')); }
}
/* 有未保存改动时关页前提醒（自动保存有 800ms 窗口期，期间关页会静默丢失） */
window.addEventListener('beforeunload', e=>{
  if(dirty){ e.preventDefault(); e.returnValue = ''; }
});
async function loadConfig(){
  cfg = await (await fetch('/api/config')).json();
  $('#title').textContent = cfg.app_name || 'EnvKit';
  $('#subtitle').textContent = cfg.app_title || '统一开发环境助手';
  $('#install_dir').value = cfg.install_dir||'';
  $('#proxy_http').value = (cfg.proxy&&cfg.proxy.http)||'';
  $('#proxy_https').value = (cfg.proxy&&cfg.proxy.https)||'';
  $('#goproxy').value = cfg.goproxy||'';
  $('#npm_registry').value = cfg.npm_registry||'';
  $('#mysql_password').value = cfg.mysql?.password||'';
  $('#mysql_port').value = cfg.mysql?.port||'';
  $('#mysql_service').value = cfg.mysql?.service_name||'';
  $('#fe_dir').value = cfg.projects?.frontend_dir||'';
  $('#be_dir').value = cfg.projects?.backend_dir||'';
  // v2.1 启动方式覆盖项回填
  if($('#cfg_web_script')) $('#cfg_web_script').value = cfg.projects?.web_script||'';
  if($('#cfg_backend_file')) $('#cfg_backend_file').value = cfg.projects?.backend_file||'';
  $('#mysql_host').value = cfg.projects?.mysql_host||'';
  $('#mysql_port2').value = cfg.projects?.mysql_port||'';
  $('#mysql_user').value = cfg.projects?.mysql_user||'';
  $('#mysql_pass').value = cfg.projects?.mysql_password||'';
  $('#db_name').value = cfg.projects?.db_name||'';
  $('#sql_path').value = cfg.projects?.sql_file||'';
  const C = cfg.chain||{};
  $('#ch_ssh_host').value=C.ssh_host||''; $('#ch_ssh_port').value=C.ssh_port||22;
  $('#ch_ssh_user').value=C.ssh_user||''; $('#ch_ssh_pass').value=C.ssh_password||''; $('#ch_ssh_key').value=C.ssh_key||'';
  $('#ch_host').value=C.chain_host||''; $('#ch_port').value=C.chain_port||20200;
  $('#ch_dir').value=C.chain_dir||''; $('#ch_start').value=C.chain_start||'';
  $('#ch_wdir').value=C.webase_dir||''; $('#ch_wstart').value=C.webase_start||'';
  $('#ch_wport').value=C.webase_port||5002; $('#ch_group').value=C.group_id||1;
  $('#ch_autorecover').checked = !!C.chain_autorecover;
  $('#ch_guard').checked = !!C.chain_guard;
  $('#ch_guard_secs').value = C.chain_guard_secs||60;
  $('#ch_notify').checked = C.chain_notify!==false;
  $('#cp-20200').textContent = C.chain_port||20200;
  $('#cp-5002').textContent = C.webase_port||5002;
  syncStartDirs();
  psRenderChips(); wlRender();
  loadGuardStatus();
  // 启动页的脚本选择器：留空 = 由服务端按 package.json 推断（v2.1）。
  // 旧版本在这里硬填 'serve'，导致"项目没有 serve 脚本"时必然失败，
  // 而且这个值只存在 localStorage 里，AI 读不到。
  try{ $('#web_script').value = localStorage.getItem('ek-web-script') || ''; }catch(_){ $('#web_script').value = ''; }
  const box = $('#comp-urls'); box.innerHTML='';
  (cfg.components||[]).forEach((c,i)=>{
    const h=document.createElement('div'); h.style.margin='6px 0';
    const lb=document.createElement('label'); lb.textContent = (c.name||'') + ' ' + (c.version||''); h.appendChild(lb);
    const inp=document.createElement('input'); inp.value=c.url||''; inp.id='compurl-'+i; inp.dataset.cfg='1';
    h.appendChild(inp); box.appendChild(h);
  });
  document.querySelectorAll('[data-cfg]').forEach(el=>{
    if(el.dataset.bound) return; el.dataset.bound='1';
    el.addEventListener('input', markDirty);
    el.addEventListener('change', ()=>{ if(el.id==='be_dir') detectSQL(); });
  });
  loaded = true;
  refreshGuards();
  detectSQL();
  loadLaunchPlan();
}

/* ---------- 内置浏览器 ---------- */
let bm = { mode:'dir', path:'', ext:'', onPick:null, native:'dir' };
function winJoin(p,n){ return p.replace(/[\\\/]+$/,'') + '\\' + n; }
function openBrowser(mode, initial, ext, nativeKind, onPick, remote){
  bm = { mode, path:'', ext:ext||'', onPick, native:nativeKind, remote:!!remote };
  $('#bm-title').textContent = (remote? '远程目录（Linux · 经 SSH）：' : '') + (mode==='file' ? '选择文件' : '选择文件夹');
  $('#bm-pick').style.display = mode==='file' ? 'none' : '';
  $('#bm-native').style.display = nativeKind ? '' : 'none';
  $('#browser').classList.add('show');
  loadLS(initial||'');
}
function joinPath(p,n){ return bm.remote ? (p.replace(/\/+$/,'') + '/' + n) : winJoin(p,n); }
function closeBrowser(){ $('#browser').classList.remove('show'); }
function bmRow(icon,label,fn){ const d=document.createElement('div'); d.className='mrow';
  const ic=document.createElement('span'); ic.textContent=icon; ic.style.opacity='.7';
  d.appendChild(ic); d.appendChild(document.createTextNode(label)); d.onclick=fn; return d; }
function emptyRow(t){ const d=document.createElement('div'); d.className='mempty'; d.textContent=t; return d; }
async function loadLS(path){
  let d;
  try{
    if(bm.remote){
      d = await (await fetch('/api/chain/ls',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({path:path||''})})).json();
    } else {
      const q = new URLSearchParams({path:path||'', mode:bm.mode, ext:bm.ext||''});
      d = await (await fetch('/api/ls?'+q)).json();
    }
  }catch(e){ return; }
  bm.path = d.path||'';
  $('#bm-path').value = d.path || (bm.remote ? '远程主机 · 选择根目录' : '此电脑');
  const list=$('#bm-list'); list.innerHTML='';
  if(!d.path){
    (d.drives||[]).forEach(dr=>list.appendChild(bmRow('■', dr, ()=>loadLS(dr))));
    if(d.error) list.appendChild(emptyRow(d.error));
    return;
  }
  const upTarget = (d.parent && d.parent !== d.path) ? d.parent : '';
  list.appendChild(bmRow('↑', t('（上一级）'), ()=>loadLS(upTarget)));
  (d.dirs||[]).forEach(n=>list.appendChild(bmRow('▸', n, ()=>loadLS(joinPath(d.path,n)))));
  (d.files||[]).forEach(n=>list.appendChild(bmRow('·', n, ()=>{ bm.onPick && bm.onPick(joinPath(d.path,n)); closeBrowser(); })));
  if(d.error) list.appendChild(emptyRow(d.error));
  if(!(d.dirs||[]).length && !(d.files||[]).length && !d.error) list.appendChild(emptyRow('空目录'));
}
function pickRemoteDir(inputId){
  const cur = $('#'+inputId).value.trim();
  openBrowser('dir', cur, '', '', p=>{ $('#'+inputId).value = p; markDirty(); refreshGuards(); }, true);
}
$('#bm-up').onclick = ()=>loadLS(bm.path ? bm.path : '');
$('#bm-cancel').onclick = closeBrowser;
$('#bm-pick').onclick = ()=>{ if(bm.path && bm.onPick) bm.onPick(bm.path); closeBrowser(); };
$('#bm-native').onclick = async ()=>{
  const url = bm.mode==='file' ? '/api/pick-file' : '/api/pick-dir';
  const r = await fetch(url,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({initial:bm.path||'', filter:bm.mode==='file'?'SQL 文件 (*.sql)|*.sql|所有文件 (*.*)|*.*':''})});
  const d = await r.json();
  if(d.path){ bm.onPick && bm.onPick(d.path); closeBrowser(); }
};
document.addEventListener('keydown', e=>{ if(e.key==='Escape'){ closeBrowser(); closeConfirm(false); $('#about').classList.remove('show'); } });

/* ---------- 二次确认 ---------- */
let cfResolveFn = null;
// opts：{wide: 更宽的弹窗, mono: 正文用等宽字}。评测「预演」塞的是脚本 stdout，
// 窄框 + 比例字体会让几千字变成一大坨，所以单独给一个宽版。
function askConfirm(title, msg, okText, danger, cancelText, opts){
  $('#cf-title').textContent = title;
  const cm = $('#cf-msg');
  /* opts.html：msg 是**已净化**的 HTML（目前只有晨报用 aiMd 渲染，走同一套白名单）。
     纯文本走 textContent —— 直接把模型/文件内容塞 innerHTML 是被禁止的。
     Markdown 里的表格/标题需要 normal 换行；普通文本仍靠 pre-wrap 保留换行。 */
  if(opts && opts.html){
    cm.innerHTML = msg;
    cm.classList.add('md-body');
    cm.style.whiteSpace = 'normal';
  } else {
    cm.textContent = msg;
    cm.classList.remove('md-body');
    cm.style.whiteSpace = 'pre-wrap';
  }
  $('#cf-ok').textContent = okText || '确定';
  $('#cf-cancel').textContent = cancelText || '取消';
  const box = document.querySelector('#confirm .mbox');
  box.classList.toggle('danger', !!danger);
  // 每次都显式覆盖，避免上一次的宽/等宽样式留到下一个确认框
  box.classList.toggle('wide', !!(opts && opts.wide));
  box.classList.toggle('mono', !!(opts && opts.mono));
  $('#cf-msg').scrollTop = 0;   // 长文本从第一行开始看，别停在上次滚到的位置
  $('#confirm').classList.add('show');
  return new Promise(res=>{ cfResolveFn = res; });
}
function closeConfirm(v){ $('#confirm').classList.remove('show'); document.querySelector('#confirm .mbox').classList.remove('danger'); if(cfResolveFn){ cfResolveFn(v); cfResolveFn = null; } }
$('#cf-ok').onclick = ()=>closeConfirm(true);
$('#cf-cancel').onclick = ()=>closeConfirm(false);
// 点遮罩空白处也能关。长内容弹窗里「找不到关闭按钮」时，这是最后一条退路
// （ESC 同样有效，见上面的 keydown 监听）。
$('#confirm').addEventListener('click', e=>{ if(e.target.id==='confirm') closeConfirm(false); });

/* ---------- 关于 / 免责声明 ---------- */
$('#btn-about').onclick = ()=>$('#about').classList.add('show');
$('#about-close').onclick = ()=>$('#about').classList.remove('show');
$('#about').addEventListener('click', e=>{ if(e.target.id==='about') $('#about').classList.remove('show'); });

/* ---------- 退出程序（可选连带停服务） ---------- */
$('#btn-exit-app').onclick = ()=>$('#exit-app').classList.add('show');
$('#exit-cancel').onclick = ()=>$('#exit-app').classList.remove('show');
$('#exit-app').addEventListener('click', e=>{ if(e.target.id==='exit-app') $('#exit-app').classList.remove('show'); });
$('#exit-confirm').onclick = async ()=>{
  const stopSvc = $('#exit-stop-svc').checked;
  const btn = $('#exit-confirm');
  btn.disabled = true; btn.textContent = t('正在退出…');
  try{
    await fetch('/api/program/exit', {method:'POST', headers:{'Content-Type':'application/json'},
      body: JSON.stringify({stop_services: stopSvc})});
  }catch(e){}
  $('#exit-app').classList.remove('show');
  // 全屏告别层：服务端即将退出，页面会失联，别让用户对着报错的页面发呆
  const ov = document.createElement('div');
  ov.style.cssText = 'position:fixed;inset:0;background:rgba(8,12,18,.92);z-index:9999;display:flex;align-items:center;justify-content:center';
  const box = document.createElement('div');
  box.style.cssText = 'color:#e8eaed;font-size:14px;text-align:center;line-height:2';
  const h = document.createElement('div');
  h.style.cssText = 'font-size:15px;font-weight:700;margin-bottom:6px';
  h.textContent = t('EnvKit 已退出');
  const p = document.createElement('div');
  p.textContent = t('本页面已与服务失去连接，可以关闭此标签页。');
  box.appendChild(h); box.appendChild(p); ov.appendChild(box);
  document.body.appendChild(ov);
  setTimeout(()=>{ try{ btn.disabled = false; btn.textContent = t('退出'); }catch(e){} }, 3000);
};

/* ---------- 主题切换 ---------- */
function applyTheme(mode){   // 注意：参数不能叫 t，会遮蔽全局翻译函数 t()
  document.body.dataset.theme = mode;
  try{ localStorage.setItem('ek-theme', mode); }catch(e){}
  const b = document.getElementById('btn-theme'); if(b) b.textContent = (mode==='dark') ? t('亮色') : t('暗色');
}
(function(){ let t='light'; try{ t = localStorage.getItem('ek-theme') || 'light'; }catch(e){} applyTheme(t); })();
(function(){
  const b = document.getElementById('btn-lang');
  if(b) b.onclick = ()=> ekSetLang(EK_LANG === 'en' ? 'zh' : 'en', true);
  ekSetLang(EK_LANG, false);
})();
$('#btn-theme').onclick = ()=> applyTheme(document.body.dataset.theme==='dark' ? 'light' : 'dark');

