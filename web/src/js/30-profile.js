/* ---------- 个人中心：昵称 / 头像 / 快捷设置 ----------
   存 localStorage 而不是 config.json：这是"这台浏览器上的我"，不是环境配置；
   也不进 exe 的分发包。头像上传在 canvas 里裁成 128×128，避免原图把 localStorage 撑爆。 */
let ekProfile = { name: '', avatar: '🐟' };
try{
  const p = JSON.parse(localStorage.getItem('ek-profile') || 'null');
  if(p && typeof p === 'object'){
    if(typeof p.name === 'string') ekProfile.name = p.name.slice(0, 16);
    if(typeof p.avatar === 'string' && p.avatar) ekProfile.avatar = p.avatar;
  }
}catch(_){}

function ekProfileSave(){ try{ localStorage.setItem('ek-profile', JSON.stringify(ekProfile)); }catch(_){} }

function ekProfilePaint(){
  const paint = el=>{
    if(!el) return;
    el.innerHTML = '';
    if(/^data:image\//.test(ekProfile.avatar)){
      const img = document.createElement('img');
      img.alt = ''; img.src = ekProfile.avatar; el.appendChild(img);
    } else {
      el.textContent = ekProfile.avatar || '🐟';
    }
  };
  paint(document.getElementById('profile-avatar'));
  paint(document.getElementById('profile-avatar-big'));
  const nm = document.getElementById('profile-name');
  if(nm) nm.textContent = ekProfile.name || t('我');
  // 预选头像高亮：上传自定义图时没有预设命中，全部取消高亮
  document.querySelectorAll('#profile-presets [data-avatar]').forEach(b=>{
    b.classList.toggle('on', b.dataset.avatar === ekProfile.avatar);
  });
  const inp = document.getElementById('profile-nick');
  if(inp && document.activeElement !== inp) inp.value = ekProfile.name;
}

/* 开场问候文案抽成函数：单测/CDP 可以直接读它，不必等 1.2 秒看气泡。 */
function ekGreetText(){
  const h = new Date().getHours();
  const g = t(h<5?'夜深了，注意休息':h<11?'早上好':h<14?'中午好':h<18?'下午好':'晚上好');
  const nm = (ekProfile.name || '').trim();
  return nm ? t('{0}，{1}！环境很健康', g, nm) : t('{0}，环境很健康', g);
}

/* 天气问候句：把「今天什么天 + 多少度 + 什么季节」揉成一句人话。
   单独成函数是为了 CDP 能直接喂假数据断言（不必真去查 wttr.in）。
   顺序是"先看天再看温度"——下不下雨决定今天要不要带伞，比"多少度"更有用。 */
function ekWeatherLine(w){
  if(!w) return '';
  const desc = String(w.desc || '').trim();
  const temp = parseInt(String(w.temp_c == null ? '' : w.temp_c).replace(/[^\d-]/g, ''), 10);
  const ok = isFinite(temp);
  const m = new Date().getMonth() + 1;
  const summer = m >= 6 && m <= 8, winter = m === 12 || m <= 2;
  if(/雷/.test(desc)) return t('在打雷，能不出门就别出去。');
  if(/雪/.test(desc)) return t('下雪了，路上滑，慢点走。');
  if(/雨/.test(desc)) return t('外面在下雨，伞带上。');
  if(/雾|霾/.test(desc)) return t('外面有{0}，窗户关一下。', desc);
  if(!ok) return desc ? t('今天{0}，出门看一眼天。', desc) : '';
  /* 季节 + 温度的矛盾组合单独说一句：冬天 26°、夏天 5° 这种，
     泛泛的"加件外套"就不如直接吐槽一句像人。 */
  if(winter && temp >= 24) return t('{0}°？冬天这么暖，有点不对劲。', temp);
  if(summer && temp <= 8) return t('{0}°，夏天这么冷，你确定没看错？', temp);
  if(temp >= 35) return t('{0}°，这温度能把鱼烤干，别在外面待着。', temp);
  if(temp >= 30) return t('{0}°，热，水别断。', temp);
  if(temp >= 24) return t('{0}°，{1}，还算舒服。', temp, desc || t('天气不错'));
  if(temp >= 16) return t('{0}°，{1}，正合适。', temp, desc || t('天气不错'));
  if(temp >= 8)  return t('{0}°，有点凉，加件外套。', temp);
  if(temp >= 0)  return t('{0}°，冷，多穿一件。', temp);
  return t('{0}°，零下，裹好再出门。', temp);
}

(function(){
  ekProfilePaint();
  const btn = document.getElementById('btn-profile');
  const pop = document.getElementById('profile-pop');
  if(btn && pop){
    btn.onclick = e=>{ e.stopPropagation(); pop.hidden = !pop.hidden; };
    document.addEventListener('click', e=>{
      if(!pop.hidden && !pop.contains(e.target) && !btn.contains(e.target)) pop.hidden = true;
    });
    document.addEventListener('keydown', e=>{ if(e.key === 'Escape') pop.hidden = true; });
    // 工具类动作点完就收；语言/主题留在面板里（用户常连着切）
    ['btn-av','btn-avclean','btn-elevate'].forEach(id=>{
      const b = document.getElementById(id);
      if(b) b.addEventListener('click', ()=>{ pop.hidden = true; });
    });
  }
  const nick = document.getElementById('profile-nick');
  if(nick) nick.addEventListener('input', ()=>{
    ekProfile.name = nick.value.trim().slice(0, 16);
    ekProfileSave(); ekProfilePaint();
  });
  document.querySelectorAll('#profile-presets [data-avatar]').forEach(b=>{
    b.onclick = ()=>{ ekProfile.avatar = b.dataset.avatar; ekProfileSave(); ekProfilePaint(); };
  });
  const file = document.getElementById('profile-avatar-file');
  if(file) file.addEventListener('change', ()=>{
    const f = file.files && file.files[0];
    if(!f) return;
    const rd = new FileReader();
    rd.onload = ()=>{
      const img = new Image();
      img.onload = ()=>{
        const s = Math.min(img.width, img.height) || 1;
        const cv = document.createElement('canvas');
        cv.width = 128; cv.height = 128;
        const cx = cv.getContext('2d');
        cx.drawImage(img, (img.width - s) / 2, (img.height - s) / 2, s, s, 0, 0, 128, 128);
        let url = cv.toDataURL('image/png');
        if(url.length > 200000) url = cv.toDataURL('image/jpeg', 0.85);
        ekProfile.avatar = url; ekProfileSave(); ekProfilePaint();
      };
      img.onerror = ()=> toast(t('这张图读不出来，换一张试试'), 'err');
      img.src = rd.result;
    };
    rd.readAsDataURL(f);
    file.value = '';
  });
  const reset = document.getElementById('profile-avatar-reset');
  if(reset) reset.onclick = ()=>{ ekProfile.avatar = '🐟'; ekProfileSave(); ekProfilePaint(); };

})();


/* ---------- 内嵌 SSH 终端（xterm.js + WebSocket 桥接） ---------- */
let term=null, fitAddon=null, termWS=null, termRO=null;
function termFit(){ try{ const w=document.getElementById('term-wrap'); if(!w || !w.offsetParent) return; if(fitAddon) fitAddon.fit(); }catch(e){} }
$('#btn-term-connect').onclick = ()=>{
  if(!term){
    term = new Terminal({cursorBlink:true, fontSize:13, fontFamily:'Consolas,"Courier New",monospace',
      theme:{background:'#10161f', foreground:'#d7dde6', cursor:'#9ad1a8', selectionBackground:'#2f7d4f55'}});
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open(document.getElementById('term'));
    term.onData(d=>{ if(termWS && termWS.readyState===1) termWS.send(new TextEncoder().encode(d)); });
    term.onResize(({cols,rows})=>{ if(termWS && termWS.readyState===1) termWS.send(JSON.stringify({type:'resize',cols,rows})); });
    termRO = new ResizeObserver(()=>termFit());
    termRO.observe(document.getElementById('term-wrap'));
    window.addEventListener('resize', termFit);
  }
  document.getElementById('term-card').style.display='';
  termSetCollapsed(false);   // 已收起状态下点「连接终端」＝重新展开
  const host=$('#ch_ssh_host').value.trim(), user=$('#ch_ssh_user').value.trim();
  document.getElementById('term-title').textContent = (user||'root')+'@'+(host||'未配置主机')+' — ssh';
  setTimeout(termFit, 30);
  if(termWS && (termWS.readyState===0 || termWS.readyState===1)) return;
  const proto=location.protocol==='https:'?'wss':'ws';
  termWS = new WebSocket(proto+'://'+location.host+'/api/chain/term?t='+encodeURIComponent(window.__EK_TOKEN__));
  termWS.binaryType='arraybuffer';
  termWS.onopen=()=>{ term.writeln('\x1b[32m[已连接 '+(user||'root')+'@'+host+']\x1b[0m'); termFit(); term.focus(); };
  termWS.onmessage=ev=>{ term.write(ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data); };
  termWS.onclose=()=>{ term.writeln('\x1b[33m[连接已关闭]\x1b[0m'); termWS=null; };
  termWS.onerror=()=>{ term.writeln('\x1b[31m[连接错误，请检查 SSH 配置]\x1b[0m'); };
};
function termDisconnect(){ if(termWS) termWS.close(); }
/* 「断开」只剩终端标题栏那一颗：配置/操作分离后独立按钮收进标题栏，不再单列 */
$('#btn-term-close2').onclick = ()=>{ termDisconnect(); document.getElementById('term-card').classList.remove('full'); };
$('#btn-term-clear').onclick = ()=>{ if(term) term.clear(); };
$('#btn-term-full').onclick = ()=>{ document.getElementById('term-card').classList.toggle('full'); setTimeout(termFit, 40); };

/* 收起/展开：只切 UI，不动 WebSocket——SSH 会话在后台继续，展开后无需重连 */
let termCollapsed=false;
function termSetCollapsed(c){
  termCollapsed=c;
  document.getElementById('term-card').classList.toggle('collapsed', c);
  const b=$('#btn-term-collapse'); if(b) b.textContent = c ? t('展开') : t('收起');
  if(!c) setTimeout(termFit, 40);   // 展开后重新适配尺寸（隐藏期间 ResizeObserver 被 termFit 跳过）
}
$('#btn-term-collapse').onclick = ()=> termSetCollapsed(!termCollapsed);

/* ---------- SSH 密码显示切换 ---------- */
$('#ch_pass_eye').onclick = ()=>{
  const p=document.getElementById('ch_ssh_pass');
  const show = p.type==='password';
  p.type = show ? 'text' : 'password';
  document.getElementById('ch_pass_eye').textContent = show ? '隐藏' : '显示';
};

/* ---------- 杀软白名单 ---------- */
$('#btn-av').onclick = async ()=>{
  if(await askConfirm('加入杀软白名单','将通过 UAC 申请管理员权限，把 EnvKit 所在目录和 EnvKit.exe\n加入 Windows Defender 排除项，防止 go / npm / mysqldump 子进程被误拦。\n\n弹出 UAC 窗口时请点「是」。','加入')) post('/api/av-exclude');
};
(async ()=>{ try{
  const s = await (await fetch('/api/av-exclude')).json();
  const b = document.getElementById('btn-av');
  if(s.defender===false){ b.textContent='杀软：第三方'; b.disabled=true; }
  else if(s.excluded){ b.textContent='已加白名单'; b.disabled=true; }
}catch(e){} })();

/* ---------- 深度清除：仅在检测到 WROberlay 残留时才显示按钮 ---------- */
(async ()=>{ try{
  const r = await (await fetch('/api/av-clean')).json();
  if(r && r.threat) document.getElementById('btn-avclean').style.display = '';
}catch(e){} })();
/* --------------------------------------------------------------------- */
$('#btn-avclean').onclick = async ()=>{
  const okGo = await askConfirm('深度清除流氓软件残留',
    '目标：PUA:Win32/Softcnapp 家族（WROberlay*.dll 注入型广告软件）。\n' +
    '它会向新启动的进程注入 DLL，导致 go / npm / mysqldump 随机「零输出秒退」。\n\n' +
    '将执行：\n' +
    '· 调用 Defender 清除已检测威胁\n' +
    '· 删除 C:\\Windows 下所有 WROberlay* 文件（被占用则改名 .quarantine）\n' +
    '· 清除相关 CLSID 与图标覆盖注册项\n' +
    '· 重启资源管理器以卸载已加载的 DLL\n\n' +
    '建议用「以管理员身份运行」的 EnvKit 执行，可免 UAC 弹窗。完成后重启一次电脑。',
    '开始清除');
  if(okGo){
    try{ await post('/api/av-clean'); }catch(e){}
    setTimeout(()=>{ try{ goto('start'); }catch(e){} }, 300);
  }
};

