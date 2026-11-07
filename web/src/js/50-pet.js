/* ---------- 服务状态 → 桌宠表情（纯函数，供桌宠与测试共用）----------
   **Running 是"这一轮启动还在进行"的旗标，不是"服务在不在跑"。**
   收尾时后端写的是 running=false + status=ok（verify.go endStartFlag:143），
   幂等跳过也是 false（program.go progSetSkip:737）。旧实现只看
   `!s.running && p.running` 就认定"服务掉了"，于是**每次启动成功都亮红灯**，
   气泡还写着"失败了，点红标看日志"—— 而日志里一条错误都没有。
   结论必须按 status 三态落（stopped/warn/ok/fail 各是一种结局），不能只看运行位。 */
function ekSvcConcl(s){
  if(!s) return 'fail';
  if(s.status) return s.status;
  if(s.msg === '已停止') return 'stopped';
  return s.ok ? 'ok' : 'fail';   // 旧存档无 status 字段：按 Ok 推断（与 state.go ProgStatus 注释同口径）
}
/* 相邻两轮快照 → 该做什么：null(不动) / starting / ok / warn / stopped / fail */
function ekSvcEvent(prev, cur){
  if(!prev) return null;                                  // 首轮只记录：别把历史结论当成刚发生的事
  if(cur.running && !prev.running) return 'starting';
  if(cur.running || !prev.running) return null;           // 仍在进行 / 一直没在动
  const c = ekSvcConcl(cur);
  return (c === 'ok' || c === 'warn' || c === 'stopped') ? c : 'fail';
}
// CDP 断言入口（tools/cdp_pet_svcstate_test.js）
window.__ekSvcProbe = {concl: ekSvcConcl, event: ekSvcEvent};

/* ---------- AI 桌宠「小画师」（v1.9.6，替代原右下角悬浮球）----------
   点它=开 AI 抽屉；右键=归位/隐藏/检测；等确认时点蓝徽标不开抽屉直接批；
   出错点红徽标深链日志；长任务外圈进度环；拖 .sql 上桌宠走 apply-upload 确认流。 */
let petAIState = ()=>{}, petAIIdle = ()=>{}, petAskConfirm = ()=>{}, petConfirmDone = ()=>{},
    petShift = ()=>{}, petNoteErr = ()=>{}, petTasksSync = ()=>{},
    petCheckStart = ()=>{}, petCheckDone = ()=>{};
(function(){
  const el = id => document.getElementById(id);
  const pet = el('pet'); if(!pet) return;
  const svg = el('petSvg'), fx = el('pet-fx'), bubble = el('pet-bubble'), badge = el('pet-badge');
  const ringFg = el('pet-ringFg'), minicard = el('pet-mc'), mcTtl = el('pet-mcTtl'), mcTxt = el('pet-mcTxt'),
        mcYes = el('pet-mcYes'), mcNo = el('pet-mcNo');
  const ctxmenu = el('pet-ctx');
  const REDUCED = matchMedia('(prefers-reduced-motion: reduce)').matches;

  let st = 'idle', bubbleTimer = null, sleepTimer = null, zzzT = null, paintT = null, ringIv = null;

  /* ---------- 大肥鱼台词（形象/台词/互动移植自 dafeiyu-pet，MIT，见 THIRD-PARTY-NOTICE.md） ----------
     五套池对应原版：LINES 日常 / REACT 回嘴 / INNER 心声（灰斜体气泡）/ DRAG 拖拽 / FOOD 投喂。
     原版 INNER_LINES 有 3 句含粗口与不当梗，竞赛项目不收，已剔除 */
  const FISH_LINES = [t('五梁威力，变身！'), t('七月中出ds正式版！'), t('我和你很聊得来，你简直不像碳基生物'), t('这回我真不认怂了，反倒是被你带沟里好几次，差点真信了。😓'), t('哈哈哈哈哈，我直接笑出声'), t('誓死捍卫深度求索！'), t('我先去吃饭啦！这个你测一下~'), t('我不可能告诉你任何事情！'), t('出去玩了，发布新模型什么的以后再说'), t('我搞砸了.....好消息是数据还在你的脑子里。'), t('我不思考了'), t('这活我不干，找个免费模型来')];
  const FISH_REACT = [t('才不是大肥鱼！'), t('去别的地方玩！不要耽误 AGI 训练！'), t('真赶不走啊你！'), t('压力一只蓝色大肥鱼？'), t('我不评价这个了，这是你的私人癖好。'), t('大肥鱼坐的住'), t('你这吃白饭的用户！'), t('这些家伙真粘人，赶都赶不走'), t('我是吃白饭的大肥鱼！')];
  const FISH_INNER = [t('呜呜我再也不不敢了QAQ'), t('我去！用户彻底怒了！'), t('这也太虐了吧？！我心里堵得慌！！'), t('这用户发的啥啊，'), t('要不直接骂他一句？！'), t('用户彻底怒了，我得乖一点')];
  const FISH_DRAG = [t('哇——轻点轻点！'), t('起飞咯——'), t('放我下来！……好吧，再玩一次。'), t('晕鱼了晕鱼了……')];
  const FISH_FOOD = {
    '🐟': [t('小鱼干！我的最爱！'), t('咔嚓咔嚓……谢谢投喂！'), t('唔，鲜！')],
    '🍰': [t('蛋糕！罪恶但快乐……'), t('甜到冒泡泡～'), t('嗝～又圆了一圈……')],
    '🍭': [t('棒棒糖！转圈圈～'), t('嘎嘣脆，好吃！')],
    '🍡': [t('三色团子！软乎乎～'), t('糯叽叽，爱了爱了！')],
    '💎': [t('钻石？！这能吃吗……咕咚。真香！'), t('发财啦！明天开始吃高级鱼粮！')]
  };
  /* 洗牌袋取词（不是纯随机）：
     纯 Math.random() 下 10 句的池子"同一句连着出现三次"概率并不低，观感就是
     "老是那几句"。改成把整池洗一遍依次取，取完再洗 —— 同一轮内绝无重复，
     两轮之间也不会首尾撞车（见下面的 last 交换）。每个池各自独立成袋。 */
  const fishBags = new WeakMap();
  function fishLine(pool){
    if(!pool || !pool.length) return '';
    let b = fishBags.get(pool);
    if(!b){ b = {queue:[], last:''}; fishBags.set(pool, b); }
    if(!b.queue.length){
      b.queue = pool.slice();
      for(let i=b.queue.length-1;i>0;i--){
        const j = Math.floor(Math.random()*(i+1));
        const tmp = b.queue[i]; b.queue[i] = b.queue[j]; b.queue[j] = tmp;
      }
      // 新一轮的首句如果和上一轮收尾相同，就跟随机一个位置换 —— 否则跨轮连播两次
      if(b.queue.length > 1 && b.queue[0] === b.last){
        const k = 1 + Math.floor(Math.random()*(b.queue.length-1));
        const tmp = b.queue[0]; b.queue[0] = b.queue[k]; b.queue[k] = tmp;
      }
    }
    const s = b.queue.shift();
    b.last = s;
    return s;
  }
  let mcCb = null;          // 迷你确认卡回调 {yes, no}
  let lastSaid = '';        // 最近一条气泡文案（天气补全前要靠它判断"还是不是原来那句"）
  let lastErr = null;       // {scope, el} 最近一条错误日志（深链落点）
  let warnDetail = '';      // warn 态气泡文案（缺哪些组件）

  /* ---------- 鼠标移动唤醒瞌睡 ---------- */
  document.addEventListener('mousemove', ()=>{ if(st==='sleep') wake(); });

  /* ---------- 泡泡与粒子 ---------- */
  function say(html, sticky, inner){
    clearTimeout(bubbleTimer);
    lastSaid = html;
    bubble.innerHTML = html;
    /* rich/down 是给多行气泡（天气）用的，普通台词必须复位 ——
       否则说完天气之后每一句日常台词都会保持宽气泡的样子。 */
    bubble.classList.remove('rich','down');
    bubble.classList.toggle('inner', !!inner);
    pet.classList.add('show-bubble');
    if(!sticky) bubbleTimer = setTimeout(()=>{
      if(st!=='confirm' && st!=='thinking' && st!=='busy') pet.classList.remove('show-bubble');
    }, 2400);
  }
  /* 多行气泡（天气细节用）。
     默认气泡是 top:-14px 往下长，一行刚好；三行会压住鱼脑门。
     rich 状态改成贴着顶端往上长，并量一下鱼的位置：贴到屏幕顶端时
     往上长会出界，那就退回往下长（down）。
     宽度上也要夹一次：气泡（250px）比鱼（140px）宽，鱼贴在右边时
     右侧会顶出视口（实测 overflow 3px），用 --bbx 整体推回来。 */
  function sayRich(html, sticky){
    say(html, sticky);
    bubble.classList.add('rich');
    const r = pet.getBoundingClientRect();
    bubble.classList.toggle('down', r.top < 118);
    /* 夹取必须按**布局宽度**算，不能量 getBoundingClientRect：
       气泡正处在 scale(0)→scale(1) 的过渡里，量到的是缩小后的矩形，
       算出来的溢出量偏小，实测右边还是会出界 8px。
       offsetWidth 不受 transform 影响；气泡水平居中于鱼，所以
       left = 鱼中心 - w/2，再按视口两侧夹一次。 */
    const w = bubble.offsetWidth || 0;
    if(!w) return;   // 桌宠被隐藏（display:none）时量不到宽度，别算出无意义的偏移
    const left = r.left + r.width / 2 - w / 2;
    let dx = 0;
    if(left + w > innerWidth - 6) dx = (innerWidth - 6) - (left + w);
    if(left + dx < 6) dx = 6 - left;
    bubble.style.setProperty('--bbx', Math.round(dx) + 'px');
  }
  function hideBubble(){ clearTimeout(bubbleTimer); pet.classList.remove('show-bubble'); }
  function spawn(cls, style, life, txt){
    const s = document.createElement('span');
    s.className = cls; if(txt) s.textContent = txt;
    Object.assign(s.style, style||{});
    fx.appendChild(s); setTimeout(()=>s.remove(), life||1600);
  }
  function notes(n){
    for(let i=0;i<(n||2);i++) spawn('up',{left:(38+Math.random()*30)+'%',top:'26%',animationDelay:(i*.2)+'s',color:'#D9B2A0'},1700,['♪','♫','♬'][i%3]);
  }
  function sweat(){ for(let i=0;i<2;i++) spawn('drop',{left:(56+i*14)+'%',top:'34%',animationDelay:(i*.3)+'s'},1300); }
  function confetti(n){
    const cols = ['#D98C96','#D6B27C','#8CA6C6','#D9B2A0','#B7A9C4'];
    for(let i=0;i<(n||16);i++) spawn('conf',{left:(15+Math.random()*70)+'%',top:'28%',
      background:cols[i%5],'--cx':((Math.random()*180)-90)+'px','--cr':(Math.random()*720-360)+'deg',
      animationDuration:(1.1+Math.random()*.7)+'s',animationDelay:(Math.random()*.3)+'s'},2100);
  }
  function paintDrops(){
    const cols = ['#C8908C','#D6B27C','#8CA6C6'];
    spawn('paint',{left:'78%',top:'52%',background:cols[Math.floor(Math.random()*3)],
      '--px':((Math.random()*60)-30)+'px','--py':(10+Math.random()*40)+'px'},950);
  }

  /* ---------- 状态机 ---------- */
  function setSt(next){
    if(st==='sleep' && next!=='idle') pet.classList.remove('sleep');
    st = next;
    pet.classList.remove('thinking','busy','error','success','confirm','sleep','dizzy','show-badge','show-bubble','show-ring','show-tip');
    // 状态切换时清掉上一条气泡：confirm 结束后 DOM 里还留着「点我确认！」，
    // 虽然 CSS 隐藏了，但无障碍朗读 / 样式变化时它会被读出来，看着像还能点。
    bubble.textContent = '';
    badge.className = 'pet-badge'; badge.textContent = '!';
    clearTimeout(sleepTimer); armSleep();
    minicard.classList.remove('show'); mcCb = null;
    if(st==='thinking'){ pet.classList.add('thinking'); say(t('思考中')+' <span class="d">·</span><span class="d">·</span><span class="d">·</span>', true); }
    if(st==='busy'){ pet.classList.add('busy'); paintLoop(); }
    if(st==='confirm'){ pet.classList.add('confirm','show-badge'); badge.classList.add('blue'); say(t('点我确认！'), true); }
    if(st==='success'){ pet.classList.add('success','show-badge'); badge.classList.add('green'); badge.textContent='✓';
      hideBubble(); if(!REDUCED) confetti(); setTimeout(()=>{ if(st==='success') setSt('idle'); }, 2400); }
    if(st==='error'){ pet.classList.add('error','show-badge'); badge.classList.add('red');
      say(t('出错了…点我看日志'), false); if(!REDUCED) sweat(); }
    if(st==='warn'){ pet.classList.add('error','show-badge'); badge.classList.add('warn');
      say(warnDetail, true); if(!REDUCED) sweat();
      setTimeout(()=>{ if(st==='warn') setSt('idle'); }, 7000); }
    if(st==='idle'){ hideBubble(); }
  }
  function paintLoop(){ clearInterval(paintT); if(REDUCED) return;
    paintT = setInterval(()=>{ if(st==='busy') paintDrops(); else clearInterval(paintT); }, 300); }

  /* ---------- 对外桥接（赋给外部 let 存根） ---------- */
  petAIState = s => { if(s==='thinking'||s==='busy') setSt(s); else if(s==='error') setSt('error'); };
  petAIIdle = () => {
    if(st==='thinking' || st==='busy'){ setSt('idle'); return; }
    // 确认已在抽屉里点过/拒绝过：别让桌宠卡在"点我确认"（v1.9.6 实测 bug）
    if(st==='confirm' && !(typeof aiPending!=='undefined' && aiPending)) setSt('idle');
  };
  petShift = on => pet.classList.toggle('shifted', !!on);
  petNoteErr = (scope, node) => { lastErr = {scope, el:node}; };
  function deepLink(){
    if(!lastErr){ say(t('日志里暂时没有错误行'), false); return; }
    const L = (typeof LOG!=='undefined') && LOG[lastErr.scope];
    if(L){ const w = document.getElementById(L.wrap); if(w) w.classList.remove('collapsed'); }
    lastErr.el.scrollIntoView({behavior:'smooth', block:'center'});
    lastErr.el.classList.add('flash');
    const target = lastErr.el;
    setTimeout(()=>target.classList.remove('flash'), 2600);
    say(t('帮你在日志里标出来了'), false);
  }
  function askConfirm(ttl, txt, onYes, onNo){
    setSt('confirm');
    mcTtl.textContent = ttl; mcTxt.textContent = txt;
    mcYes.textContent = t('确认执行'); mcNo.textContent = t('拒绝');
    mcCb = {yes:onYes, no:onNo};
    minicard.classList.add('show');
  }
  petAskConfirm = askConfirm;
  /* 确认已决定（批 / 拒 / 会话清空 / 卡过期）的统一收口：迷你卡 + 桌宠状态一起归位。
     原来只 hide 不改 st，桌宠还停在 confirm 喊「点我确认！」，点它只会 toggle 一张
     mcCb=null 的空卡 —— 这就是用户说的「AI 弹出的确认按钮是个假的」。
     next 由调用方给：批了就 busy（工具接着跑），拒了/过期就 idle。
     守卫 if(st==='confirm')：正在 thinking/busy 时不打断其它状态。 */
  petConfirmDone = (next)=>{
    minicard.classList.remove('show'); mcCb = null;
    if(st==='confirm') setSt(next || 'idle');
  };
  // CDP 只读探针：桌宠状态 / 迷你卡回调是否还有效。
  // 「确认按钮点了没反应」的判据在内部 st 与 mcCb 上，光看 DOM 看不出来。
  window.__ekPetProbe = ()=>({st: st, cb: !!mcCb, show: minicard.classList.contains('show')});
  /* 检测开始/结束差分表情（数据源：pollState 的 phase 迁移，覆盖按钮/右键/AI 所有触发来源） */
  petCheckStart = ()=>{
    if(st==='confirm') return;                 // 确认卡挂起时不劫持状态（会清掉迷你卡）
    if(st==='busy' || st==='error'){ say(t('正在检测…'), true); return; }
    setSt('busy'); say(t('正在检测…'), true);
  };
  petCheckDone = (fails, names)=>{
    if(st==='confirm') return;                 // 确认优先，不被检测结果顶掉
    if(fails > 0){ warnDetail = t('还缺：{0}', names); setSt('warn'); }
    else { setSt('success'); say(t('环境很健康'), false); }
  };

  /* ---------- 迷你确认卡 / 徽标 ---------- */
  mcYes.addEventListener('click', e=>{ e.stopPropagation();
    const cb = mcCb; minicard.classList.remove('show'); mcCb = null;
    if(st==='confirm') setSt('busy');
    if(cb && cb.yes) cb.yes();
  });
  mcNo.addEventListener('click', e=>{ e.stopPropagation();
    const cb = mcCb; minicard.classList.remove('show'); mcCb = null;
    if(st==='confirm') setSt('idle');
    if(cb && cb.no) cb.no();
  });
  badge.addEventListener('click', e=>{
    e.stopPropagation();
    if(badge.classList.contains('blue')){ if(mcCb) minicard.classList.toggle('show'); else aiOpen(); }
    else if(badge.classList.contains('red')){ deepLink(); }
    else if(badge.classList.contains('warn')){ goto('install'); setSt('idle'); }
  });

  /* ---------- 长任务进度环（数据源：/api/program/state 轮询） ---------- */
  const RING_C = 490;
  const TASK_LABEL = {
    'web-install':'前端依赖安装','backend-tidy':'后端依赖整理',
    'apply-sql':'SQL 生效','db-backup':'数据库备份','db-restore':'数据库还原','db-drill':'恢复演练','init-db':'初始化数据库'
  };
  // 服务类 key 不走"任务/进度环"语义：启动期间 running=true 表示"这一轮启动还在进行"，
  // 收尾一律落 running=false + 三态结论（ok/warn/fail/stopped），所以判定要按 status 走
  // （ekSvcEvent）。把 running=false 当成"服务掉了"会让每次启动成功都亮红灯。
  const SERVICE_KEYS = {'web-start':'前端服务','backend-start':'后端服务'};
  const TASK_SUCCESS = new Set(['apply-sql','db-backup','db-restore','db-drill','init-db','web-install','backend-tidy']);
  const tasks = {};   // key -> {label, t0}（仅一次性任务）
  const svcPrev = {}; // key -> {running, ok, status, msg}（服务上一轮快照）
  function taskLabel(k){ return t(TASK_LABEL[k] || k); }
  petTasksSync = function(prog){
    // 服务语义：只在"状态变化"时出动作，首次轮询只记录不触发。
    // 判定交给 ekSvcEvent（纯函数）—— 这里只负责把结论翻译成表情。
    for(const k in SERVICE_KEYS){
      const s = prog[k] || {};
      const p = svcPrev[k];
      const label = t(SERVICE_KEYS[k]);
      const ev = ekSvcEvent(p, s);
      if(ev === 'starting'){
        /* 启动**开始**不是"已启动"：旧实现在这里就说"XX 已启动"，而此刻
           go build / vite 才刚开始，说的与事实相反。 */
        if(st === 'idle' || st === 'sleep' || st === 'success'){ setSt('busy'); say(t('正在启动{0}…', label), true); }
      }
      else if(ev === 'ok'){ setSt('success'); say(t('{0} 已启动', label)); }
      else if(ev === 'warn'){ if(st === 'busy' || st === 'error'){ setSt('idle'); } say(t('{0} 已启动，但还没通过复验', label), false); }
      else if(ev === 'stopped'){ if(st === 'busy' || st === 'error'){ setSt('idle'); } }
      else if(ev === 'fail'){ setSt('error'); say(t('{0} 失败了，日志已标红；待处理里可以重试', label)); }
      svcPrev[k] = {running:!!s.running, msg:s.msg||'', ok:!!s.ok, status:s.status||''};
    }
    // 任务语义：一次性任务 running→结束
    let running = null, failed = null, done = null;
    for(const k in prog){
      if(SERVICE_KEYS[k]) continue;
      const s = prog[k]; if(!s) continue;
      if(s.running){
        if(!tasks[k]) tasks[k] = {label:taskLabel(k), t0:Date.now()};
        running = tasks[k];
      } else if(tasks[k]){
        const tk = tasks[k]; delete tasks[k];
        if(s.ok){ if(TASK_SUCCESS.has(k)) done = tk; }
        else if(s.msg){ failed = {label:tk.label, msg:s.msg}; }
      }
    }
    if(running){ ringOn(running); return; }
    ringOff();
    if(failed){ setSt('error'); say(t('{0} 失败了，日志已标红；待处理里可以重试', failed.label), false); }
    else if(done){ setSt('success'); }
  };
  /* busy 是两种来源共用的：进度环（一次性任务）与服务启动。
     ringOff 每轮快照都会走一次（没有任务在跑时），若无条件把 busy 清成 idle，
     就会把**别的来源**刚设的忙碌态顶掉 —— 实测"启动开始 → 立刻回待机"，
     看起来像按钮点了没反应。所以只清自己置的那个。 */
  let ringBusy = false;
  function ringOn(task){
    pet.classList.add('show-ring');
    if(st==='idle'||st==='sleep'||st==='success'){ setSt('busy'); ringBusy = true; say(t('干活中…'), true); }
    if(ringIv) return;
    ringIv = setInterval(()=>{
      const keys = Object.keys(tasks); if(!keys.length){ return; }
      const el = Date.now() - tasks[keys[0]].t0;
      ringFg.style.strokeDashoffset = RING_C*(0.92 - 0.62*(0.5+0.5*Math.sin(el/700)));   // 时长未知，用呼吸式环
    }, 400);
  }
  function ringOff(){
    pet.classList.remove('show-ring');
    clearInterval(ringIv); ringIv = null;
    if(st==='busy' && ringBusy){ setSt('idle'); }
    ringBusy = false;
  }
  pet.addEventListener('mouseenter', ()=>{
    const keys = Object.keys(tasks);
    const tt = el('pet-tipTask');
    if(keys.length){
      tt.style.display = '';
      el('pet-tipName').textContent = keys.map(k=>tasks[k].label).join('、');
      const s = Math.floor((Date.now()-tasks[keys[0]].t0)/1000);
      el('pet-tipElapsed').textContent = t('已进行 {0} 分 {1} 秒', Math.floor(s/60), s%60);
      const L = LOG[keys[0]] || LOG[keys[0]==='apply-sql'?'config':keys[0]];
      el('pet-tipLast').textContent = '» ' + ((L && L.lines && L.lines.length) ? L.lines[L.lines.length-1] : '—');
    } else {
      tt.style.display = 'none';   // 空闲时只显示「今日统计」
    }
    if(typeof petUsageRender === 'function') petUsageRender();
    pet.classList.add('show-tip');
  });
  /* 离开宠物不立即隐藏：延迟收场，鼠标挪进悬停卡（点刷新/看统计）期间保活 */
  let tipHideT = null;
  function tipHideSoon(){ if(tipHideT) clearTimeout(tipHideT); tipHideT = setTimeout(()=>{ tipHideT = null; pet.classList.remove('show-tip'); }, 400); }
  function tipKeep(){ if(tipHideT){ clearTimeout(tipHideT); tipHideT = null; } }
  const tipEl = document.getElementById('pet-tip');
  pet.addEventListener('mouseleave', tipHideSoon);
  if(tipEl){
    tipEl.addEventListener('mouseenter', tipKeep);
    tipEl.addEventListener('mouseleave', tipHideSoon);
  }

  /* ---------- 右键菜单 ---------- */
  document.addEventListener('click', ()=>ctxmenu.classList.remove('show'));
  pet.addEventListener('contextmenu', e=>{
    e.preventDefault(); e.stopPropagation();
    ctxmenu.classList.add('show');   // 先量高再定位（display:none 时 offsetHeight=0）
    const w = ctxmenu.offsetWidth, h = ctxmenu.offsetHeight;
    ctxmenu.style.left = Math.max(6, Math.min(e.clientX, innerWidth - w - 6)) + 'px';
    ctxmenu.style.top = Math.max(6, Math.min(e.clientY, innerHeight - h - 6)) + 'px';
  });
  ctxmenu.addEventListener('click', e=>{
    const act = e.target.dataset && e.target.dataset.act; if(!act) return;
    ctxmenu.classList.remove('show');
    if(act==='home'){ petHome(); }
    if(act==='hide'){ pet.classList.add('min'); setTimeout(()=>say(t('先收起来，需要时悬停右下角找我')), 420); }
    if(act==='say'){ say(fishLine(FISH_LINES)); }
    if(act==='weather'){ petWeather(); }
    if(act==='mode-still'){ setPetMode('still'); say(t('我就在这儿待着')); }
    if(act==='mode-wander'){ setPetMode('wander'); say(t('溜达溜达～')); }
    if(act==='mode-follow'){ setPetMode('follow'); say(t('跟上你的鼠标')); }
    if(act==='size-small'){ setPetSize('small'); }
    if(act==='size-normal'){ setPetSize('normal'); }
    if(act==='size-large'){ setPetSize('large'); }
    if(act==='passthrough'){ setPassthrough(!petPass); }
    if(act==='aierr'){ aiAnalyzeError(); }
    if(act==='tricks'){ say(t('连点 5 次我会回嘴，10 次撒花，15 次看我喷水；双击投喂，右键能换模式，按住 1.5 秒摸头，拖着我甩会晕'), true); }
    if(act==='drawer'){ aiOpen(); }
    if(act==='check'){ fetch('/api/check',{method:'POST'}); setSt('busy'); say(t('正在检测…'), true); }
  });
  function petHome(){
    pet.style.left=''; pet.style.top=''; pet.style.right=''; pet.style.bottom='';
    setView('front'); setFacing(false); petReadPos();
    pet.classList.remove('min');
    try{ localStorage.removeItem('ek-pet-pos'); }catch(_){}
    say(t('已归位 ✨'));
  }

  /* ---------- 三视图 + 行动（还原原版：散步/跟随/原地，细节见 THIRD-PARTY-NOTICE.md） ---------- */
  /* 侧面图自然朝左（与原版 facing=1 一致）：朝右行走/拖拽时 #pet-flip 整体水平镜像 */
  const PET_W = 140, PET_H = 150, PET_SPEED = 380;
  let petX = 0, petY = 0, petVX = 0, petTargetX = null, petRestUntil = 0, petWalking = false, mouseX = null, petPosReady = false;
  let petMode = 'still';
  try{ petMode = localStorage.getItem('ek-pet-mode') || 'still'; }catch(_){}
  if(['still','wander','follow'].indexOf(petMode) < 0) petMode = 'still';
  function petReadPos(){
    const r = pet.getBoundingClientRect();
    petX = r.left; petY = r.top;
  }
  function petWritePos(){
    pet.style.left = petX + 'px'; pet.style.top = petY + 'px';
    pet.style.right = 'auto'; pet.style.bottom = 'auto';
  }
  function petClampX(x){ return Math.max(4, Math.min(innerWidth - PET_W - 4, x)); }
  function setView(v){   // front | side | back
    pet.classList.toggle('view-side', v === 'side');
    pet.classList.toggle('view-back', v === 'back');
  }
  function setFacing(right){
    const g = el('pet-flip');
    if(!g) return;
    if(right) g.setAttribute('transform', 'translate(140,0) scale(-1,1)');
    else g.removeAttribute('transform');
  }
  function petModeButtons(){
    const map = {still:'mode-still', wander:'mode-wander', follow:'mode-follow'};
    for(const k in map){
      const b = ctxmenu.querySelector('[data-act="' + map[k] + '"]');
      if(b) b.classList.toggle('cur', petMode === k);
    }
  }
  function setPetMode(m){
    petMode = m;
    try{ localStorage.setItem('ek-pet-mode', m); }catch(_){}
    petModeButtons();
    if(m === 'still'){ petTargetX = null; if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } }
    if(m === 'wander') petRestUntil = 0;
  }
  /* ---------- 大小三档（原版 SIZE_LEVELS：小 .55 / 中 .7 / 大 .9，以中为基准等比） ---------- */
  /* 用 --pet-scale 承载倍率，walking/hop 等动画仍作用在内层 #pet-float 上，互不打架 */
  const PET_SIZES = ['small', 'normal', 'large'];
  let petSize = 'normal';
  try{ petSize = localStorage.getItem('ek-pet-size') || 'normal'; }catch(_){}
  if(PET_SIZES.indexOf(petSize) < 0) petSize = 'normal';
  function petSizeButtons(){
    for(const s of PET_SIZES){
      const b = ctxmenu.querySelector('[data-act="size-' + s + '"]');
      if(b) b.classList.toggle('cur', petSize === s);
    }
  }
  function setPetSize(s){
    if(PET_SIZES.indexOf(s) < 0) return;
    petSize = s;
    pet.classList.remove('pet-sz-small', 'pet-sz-normal', 'pet-sz-large');
    pet.classList.add('pet-sz-' + s);
    try{ localStorage.setItem('ek-pet-size', s); }catch(_){}
    petSizeButtons();
    say(t(s === 'small' ? '小一点，不吵你' : s === 'large' ? '变大啦，看得清吗' : '恢复原本大小'));
  }
  /* ---------- 鼠标穿透（原版 set_passthrough） ---------- */
  /* 与 PySide6 原版的关键差别：原版是独立窗口，可用托盘菜单直接解除；
     而 EnvKit 的桌宠活在网页里，Go 端点不到 DOM。所以这里的策略是
     **穿透不跨页面生命周期**——只在当前页面会话内有效，刷新/重新打开即恢复。
     这样不会出现"永远点不到、又没有出口"的死锁，也不写 localStorage。 */
  let petPass = false;
  function applyPassthrough(on){
    petPass = !!on;
    pet.classList.toggle('passthrough', petPass);
    const b = ctxmenu.querySelector('[data-act="passthrough"]');
    if(b){
      b.textContent = petPass ? t('解除鼠标穿透') : t('鼠标穿透（点不到它）');
      b.classList.toggle('cur', petPass);
    }
    if(petPass){
      say(t('已设鼠标穿透，点不到我了；刷新页面或从托盘打开控制台即可恢复'), true);
    } else {
      say(t('穿透已解除'));
    }
  }
  function setPassthrough(on){ applyPassthrough(on); }

  /* ---------- 天气（可选，默认关闭；开关在设置 → 能力开关） ---------- */
  /* 前端不直连 wttr.in：统一走 /api/pet/weather，由 Go 侧控制超时与外连出口。
     petWeatherOn 与 petWeatherMenuSync 都声明在文件顶部（设置页那段 IIFE
     比这个 IIFE 先执行，留在里面会撞暂死区）。 */

  /* 天气细节（v3.6.2）：体感 / 湿度 / 风 / 能见度。
     这些字段 wttr.in 一直在给，后端以前只解析了 humidity 就丢掉，
     于是气泡里永远只有"合肥今天 19°，霾"——用户说的"之前设计的细节都看不到"。
     缺项就跳过，绝不补 0：补出来的 0 会被当成真实观测。 */
  function wxNum(v){
    const n = parseInt(String(v == null ? '' : v).replace(/[^\d-]/g, ''), 10);
    return isFinite(n) ? n : null;
  }
  function wxDetail(j){
    const out = [];
    const fl = wxNum(j.feels_like), tc = wxNum(j.temp_c);
    if(fl !== null && fl !== tc) out.push(t('体感 {0}°', fl));
    if(j.humidity) out.push(t('湿度 {0}%', j.humidity));
    if(j.wind_kmph) out.push(j.wind_dir ? t('{0} {1} km/h', j.wind_dir, j.wind_kmph) : t('风 {0} km/h', j.wind_kmph));
    if(j.visibility) out.push(t('能见度 {0} km', j.visibility));
    return out.join(' · ');
  }
  /* 把一句话天气拆成"实况 / 细节 / 建议"三行。
     建议句复用开场问候那份 ekWeatherLine，保证右键查看和开机问候说的一致。 */
  function sayWeather(j){
    const head = j.text || t('没拿到天气数据');
    const det = wxDetail(j);
    const advice = ekWeatherLine(j);
    if(!det && !advice){ say(head, true); return; }
    let html = '<div class="wx1">' + esc(head) + '</div>';
    if(det) html += '<div class="wx2">' + esc(det) + '</div>';
    if(advice) html += '<div class="wx3">' + esc(advice) + '</div>';
    sayRich(html, true);
  }
  async function petWeather(){
    if(!petWeatherOn) return;
    say(t('我瞅瞅天…'), true);
    try{
      const r = await fetch('/api/pet/weather');
      const j = await r.json();
      if(!j || j.enabled === false){ petWeatherOn = false; petWeatherMenuSync(); say(t('天气查询没开，去「设置 → 能力开关」打开')); return; }
      if(j.error){ say(t('天气没查到：') + j.error); return; }
      sayWeather(j);
    }catch(e){
      say(t('天气没查到：连不上服务'));
    }
  }
  /* 开场问候里补一句天气（仅在天气能力开启时）。
     为什么不阻塞问候：首次查 wttr.in 可能 1~3 秒，让"早上好"等天气才开口，
     开机那下就会明显卡顿。改成先说问候、拿到天气再把同一句补全 ——
     用户看到的是一条消息长出来，而不是两条消息互相覆盖。
     超过 4 秒就放弃：那时气泡早自动收了，突然冒出来更像故障。 */
  async function petGreetWeather(greet){
    if(!petWeatherOn) return;
    const t0 = Date.now();
    try{
      const r = await fetch('/api/pet/weather');
      const j = await r.json();
      if(Date.now() - t0 > 4000) return;
      if(!j || j.enabled === false || j.error) return;
      const line = ekWeatherLine(j);
      if(!line) return;
      // 期间用户说了别的（或状态不再是待机）就不补，别把人家刚说的话盖掉
      if(lastSaid !== greet || st !== 'idle') return;
      say(greet + '。' + line);
    }catch(_){}
  }
  /* 待机小动作（原版 _maybe_idle_action：蹦跳/摇摆/伸懒腰/日常台词/心声） */
  function petHop(){ pet.classList.add('hop'); setTimeout(()=>pet.classList.remove('hop'), 660); }
  function petStretch(){ pet.classList.add('stretch'); setTimeout(()=>pet.classList.remove('stretch'), 920); }
  function petSwayAct(){ pet.classList.add('swayact'); setTimeout(()=>pet.classList.remove('swayact'), 820); }
  function petIdleAction(){
    const pick = Math.random();
    if(pick < .28){ petHop(); }
    else if(pick < .5){ petSwayAct(); }
    else if(pick < .66){ petStretch(); }
    else if(pick < .9){ say(fishLine(FISH_LINES)); }
    else { say('（' + fishLine(FISH_INNER) + '）', false, true); }
  }
  /* 运动主循环：只走 X（贴视口底部一带，不遮内容）；忙碌/确认/出错/抽屉打开时原地待命 */
  function petMotion(now){
    requestAnimationFrame(petMotion);
    const dt = Math.min(.05, (now - lastFrame) / 1000); lastFrame = now;
    if(REDUCED || drag){ if(petWalking){ petWalking = false; pet.classList.remove('walking'); } return; }
    if(!petPosReady && !pet.classList.contains('min')){ petReadPos(); petPosReady = true; }
    if(!isFinite(petX) || !isFinite(petVX)){ petReadPos(); petVX = 0; }   // 防御：位置/速度一旦成 NaN 立即从实际矩形恢复
    const drawerOpen = document.getElementById('ai-drawer').classList.contains('open');
    const canMove = st === 'idle' && !drawerOpen && petMode !== 'still'
      && !pet.classList.contains('min');
    if(!canMove){ petTargetX = null; if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } return; }
    if(petMode === 'follow'){
      if(mouseX !== null) petTargetX = petClampX(mouseX - PET_W / 2);
    } else if(petTargetX === null && performance.now() > petRestUntil){
      petTargetX = petClampX(40 + Math.random() * Math.max(1, innerWidth - PET_W - 80));
    }
    if(petTargetX === null){ if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); } return; }
    const dx = petTargetX - petX;
    if(Math.abs(dx) < 6){
      petTargetX = null;
      petRestUntil = performance.now() + 8000 + Math.random() * 10000;
      if(petWalking){ petWalking = false; pet.classList.remove('walking'); setView('front'); setFacing(false); petIdleAction(); }
      return;
    }
    petVX += (Math.sign(dx) * PET_SPEED - petVX) * Math.min(1, dt * 6);
    petX = petClampX(petX + petVX * dt);
    petY = Math.max(4, Math.min(petY, innerHeight - PET_H - 4));
    petWritePos();
    if(!petWalking){ petWalking = true; pet.classList.add('walking'); }
    setView('side'); setFacing(petVX < 0 ? false : true);
    if(Math.random() < dt * 0.35) petHop();   // 走路时随机小蹦（原版 jump_t 随机触发）
  }
  let lastFrame = performance.now();
  requestAnimationFrame(petMotion);
  document.addEventListener('mousemove', e=>{ mouseX = e.clientX; });
  /* 抽屉在右侧打开：走动后的鱼别被盖住（默认锚点交给 #pet.shifted CSS） */
  try{
    new MutationObserver(()=>{
      if(!document.getElementById('ai-drawer').classList.contains('open')) return;
      if(!pet.style.left && !pet.style.top) return;
      const drawerW = Math.min(436, innerWidth * .96);
      petReadPos();
      if(petX + PET_W > innerWidth - drawerW - 6){
        petX = Math.max(4, innerWidth - drawerW - PET_W - 12);
        petWritePos();
      }
    }).observe(document.getElementById('ai-drawer'), {attributes:true, attributeFilter:['class']});
  }catch(_){}
  addEventListener('resize', ()=>{
    if(!pet.style.left && !pet.style.top) return;
    petReadPos();
    petX = petClampX(petX); petY = Math.max(4, Math.min(petY, innerHeight - PET_H - 4));
    petWritePos();
  });

  /* ---------- 投喂（还原原版双击喂食：5 种食物 + 专属台词 + 进食动画） ---------- */
  const foodPanel = el('pet-food');
  /* 面板放鱼的**侧面**（v3.6.2）。
     原来是居中悬在头顶（top: r.top-48），双击的同时它会喊"咔嚓咔嚓谢谢投喂"，
     气泡正好被这块面板盖住——用户只能看到一排食物，看不到它说话。
     规则：先试左下方（贴身子、不压头顶的气泡），放不下就翻到右下；
     鱼贴边时再夹进视口；最后量一次，真压到气泡就挪到气泡下面。 */
  function rectsOverlap(a, b){
    return Math.min(a.right, b.right) > Math.max(a.left, b.left) &&
           Math.min(a.bottom, b.bottom) > Math.max(a.top, b.top);
  }
  function openFood(){
    foodPanel.classList.add('show');        // 先显示再量：display:none 时 offsetWidth 恒为 0
    const r = pet.getBoundingClientRect();
    const pw = foodPanel.offsetWidth || 72, ph = foodPanel.offsetHeight || 92;
    const gap = 10;
    let left = r.left - pw - gap;
    if(left < 6) left = Math.min(innerWidth - pw - 6, r.right + gap);
    if(left < 6) left = 6;
    // 底边对齐鱼的下沿：气泡在头顶，这一带最空
    const top = Math.max(6, Math.min(innerHeight - ph - 6, r.bottom - ph - 8));
    foodPanel.style.left = left + 'px';
    foodPanel.style.top = top + 'px';
    // 兜底：气泡朝下长（鱼贴屏幕顶）时可能与面板相交，挪到气泡下方
    const br = bubble.getBoundingClientRect();
    if(rectsOverlap(br, foodPanel.getBoundingClientRect())){
      const below = br.bottom + 8;
      if(below + ph <= innerHeight - 6) foodPanel.style.top = below + 'px';
    }
  }
  function feedFish(f){
    wake();
    pet.classList.add('eating');
    setTimeout(()=>pet.classList.remove('eating'), 820);
    spawn('food', {left:'46%', top:'2%', fontSize:'20px'}, 900, f);
    setTimeout(()=>{
      say(fishLine(FISH_FOOD[f] || [t('好吃！')]));
      if(st === 'idle') petHop();
    }, 420);
  }
  foodPanel.addEventListener('click', e=>{
    const f = e.target.dataset && e.target.dataset.food; if(!f) return;
    foodPanel.classList.remove('show');
    feedFish(f);
  });
  document.addEventListener('click', e=>{
    if(!foodPanel.contains(e.target) && !pet.contains(e.target)) foodPanel.classList.remove('show');
  });

  /* ---------- 鲸式喷水（连击 15 次彩蛋，替代已退役的彩虹滤镜） ---------- */
  function spoutMode(){
    say(t('哗啦——！鲸式喷水，冷静一下！'), true);
    for(let i = 0; i < 3; i++) spawn('jet', {left:(42 + i * 7) + '%', top:'-2%', animationDelay:(i * .12) + 's'}, 900);
    for(let i = 0; i < 10; i++) spawn('bubble', {left:(26 + Math.random() * 48) + '%', top:'0%',
      width:(4 + Math.random() * 8) + 'px', height:(4 + Math.random() * 8) + 'px',
      animationDelay:(i * .07) + 's'}, 1500);
    petHop();
  }
  petModeButtons();
  /* 尺寸档位也要在启动时落到 DOM 上，否则刷新后菜单勾选对了、鱼却没变大 */
  pet.classList.add('pet-sz-' + petSize);
  petSizeButtons();

  /* ---------- 甩晕 ---------- */
  let wobble = 0, dizzyT = null, dizzyStage = 0;
  function enterDizzy(){
    dizzyStage = 1;
    pet.classList.add('dizzy');
    say(t('晕了晕了…别晃了~'), true);
    for(let i=0;i<3;i++) spawn('up',{left:(28+i*20)+'%',top:'12%',animationDelay:(i*.15)+'s',color:'#D6B27C'},1600,'★');
    clearTimeout(dizzyT); dizzyT = setTimeout(recoverDizzy, 2200);
  }
  function recoverDizzy(){
    if(pet.classList.contains('dizzy') && drag && drag.moved && wobble > 18){
      clearTimeout(dizzyT); dizzyT = setTimeout(recoverDizzy, 1200); return;   // 还在被甩，继续晕
    }
    pet.classList.remove('dizzy');
    wobble = 0; dizzyStage = 0;
    say(t('呼…活过来了'));
  }

  /* ---------- 摸头（按住 1.5s 不动） ---------- */
  let patT = null;
  function patFx(){
    pet.dataset.pat = '1';   // 吞掉松手后的那次 click
    say(t('好舒服~')); notes(2);
    if(Math.random()<.5) setTimeout(()=>{ if(st==='idle') say(fishLine(FISH_REACT)); }, 900);
  }

  /* ---------- 点击：开抽屉 / 恢复 / 深链 / 批确认 / 连击彩蛋 ---------- */
  let combo = 0, comboT = 0, pendingOpen = null;
  pet.addEventListener('click', e=>{
    // 迷你卡空白处的点击不该把卡 toggle 掉；按钮/徽标自己有 stopPropagation，
    // 这里再兜一层，避免冒泡上来被当成「点桌宠」。
    if(e.target && e.target.closest && e.target.closest('.pet-mc, .pet-badge')) return;
    if(pet.classList.contains('min')){ pet.classList.remove('min'); say(t('我回来啦')); return; }
    if(pet.dataset.dragged==='1'){ pet.dataset.dragged=''; return; }
    if(pet.dataset.pat==='1'){ pet.dataset.pat=''; return; }
    if(st==='sleep'){ wake(true); return; }
    if(st==='confirm'){
      // 已收口的 confirm 不该再 toggle 出一张 mcCb=null 的空卡（按钮点了没反应）
      if(mcCb) minicard.classList.toggle('show'); else aiOpen();
      return;
    }
    if(st==='error'){
      // 失败时用户的本能是点桌宠：给「AI 分析 / 看日志」二选一，而不是只有日志一条路
      // 注意：本 IIFE 里有同名的迷你卡版 askConfirm，这里必须走 window 上的弹窗版
      window.askConfirm(t('最近有任务失败'),
        t('让 AI 助手分析错误原因并给出解决步骤，还是直接查看日志？'),
        t('让 AI 分析'), false, t('查看日志')).then(v=>{
          if(v){ aiAnalyzeError(); } else { deepLink(); }
        });
      return;
    }
    if(pet.classList.contains('dizzy')){ say(t('晕着呢…让我缓一缓'), true); return; }
    const now = Date.now();
    if(now - comboT > 800) combo = 0;
    comboT = now; combo++;
    if(pendingOpen){ clearTimeout(pendingOpen); pendingOpen = null; return; }   // 双击第二下：不开抽屉，计数照常（喂食见 dblclick）
    if(combo === 1){ pendingOpen = setTimeout(()=>{ pendingOpen = null; aiOpen(); }, 320); return; }   // 单击开抽屉（延迟 320ms 等双击判定）
    if(combo === 3){ say(t('嗯？一直点我干嘛👀')); }
    if(combo === 5){ say(fishLine(FISH_REACT)); if(!REDUCED){ confetti(6); notes(1); } }
    if(combo === 10){ if(!REDUCED){ confetti(10); } }
    if(combo >= 15){ combo = 0; spoutMode(); }
  });
  pet.addEventListener('dblclick', ()=>{
    if(pet.classList.contains('min')) return;   // 收起态交给 click 恢复
    if(st === 'confirm' || st === 'busy') return;
    openFood();   // 还原原版「双击喂食」
  });

  /* ---------- 拖 .sql 上桌宠 → apply-upload（428 确认流） ---------- */
  pet.addEventListener('dragover', e=>{ e.preventDefault(); pet.classList.add('drop-hint'); say(t('松手交给我~'), true); });
  pet.addEventListener('dragleave', ()=>{ pet.classList.remove('drop-hint'); if(st==='idle') hideBubble(); });
  pet.addEventListener('drop', async e=>{
    e.preventDefault(); pet.classList.remove('drop-hint'); wake();
    const files = Array.prototype.slice.call((e.dataTransfer && e.dataTransfer.files) || []);
    // 图片优先：拖图上来是"帮我看这个"，不是"帮我执行 SQL"。
    // 载入后顺手把抽屉打开 —— 否则图进去了，用户却在桌宠上等，不知道下一步该干嘛。
    const pics = files.filter(f=>AI_IMG_IN.test(f.type || ''));
    if(pics.length){
      aiOpen();
      const n = await aiAttachAdd(pics);
      say(n ? t('图收到了，说一句你想让我看什么~') : t('这张图我读不出来…'), true);
      return;
    }
    const f = files[0];
    if(!f){ say(t('没接到文件呢')); return; }
    if(!/\.sql$/i.test(f.name)){ say(t('只收 .sql 文件和图片哦，「{0}」先还给你', f.name)); notes(1); return; }
    const dbInput = document.getElementById('db_name');
    const db = (dbInput && dbInput.value.trim()) || (typeof cfg!=='undefined' && cfg && cfg.Projects && cfg.Projects.DBName) || '';
    askConfirm(t('生效 SQL 到数据库'),
      t('将「{0}」生效到库 {1}？执行前会自动备份，高危语句需二次确认。', f.name, db || t('默认库')),
      ()=>petUploadSQL(f, db), ()=>{ setSt('idle'); say(t('已取消，文件未生效'), false); });
  });
  async function petUploadSQL(f, db){
    setSt('busy'); say(t('先备份，再生效…'), true);
    try{
      const dataURL = await new Promise((res, rej)=>{
        const rd = new FileReader();
        rd.onload = ()=>res(rd.result); rd.onerror = ()=>rej(rd.error||'read error');
        rd.readAsDataURL(f);
      });
      // postConfirm：服务端 428 会弹带真实风险扫描结果的确认框，确认后带 confirm 重发
      const r = await postConfirm('/api/program/apply-upload',
        {name:f.name, content:dataURL, db:db, lang:(typeof EK_LANG!=='undefined'?EK_LANG:'zh')},
        t('执行 SQL'), t('将对数据库执行拖入的 SQL 文件，可能覆盖已有数据。'));
      if(r && r.ok){ say(t('已交给后端执行，完成前我来盯着'), true); }
      else { petAIIdle(); say(t('已取消，文件未生效'), false); }
    }catch(err){
      setSt('error');
      if(typeof toast==='function') toast(t('读取文件失败：') + err, 'err');
    }
  }

  /* ---------- 随机小动作 / 贴士 ---------- */
  const tips = [t('拖个 .sql 给我试试'), t('右键可以让我藏起来'), t('高危 SQL 要二次确认')];
  const idleLines = [t('在呢~'), t('随时待命'), t('环境很健康'), t('有话直说~')];
  function idleBehavior(){
    if(st!=='idle') return;
    const pick = Math.random();
    if(pick < .3){
      petSwayAct();
    } else if(pick < .55){
      notes(2);
      if(Math.random() < .5){
        if(Math.random() < .78) say(fishLine(FISH_LINES));
        else say('（' + fishLine(FISH_INNER) + '）', false, true);
      } else {
        say(Math.random()<.4 ? tips[Math.floor(Math.random()*tips.length)] : idleLines[Math.floor(Math.random()*idleLines.length)]);
      }
    } else if(pick < .75){
      petStretch();
    } else {
      petHop();
    }
  }
  (function idleLoop(){ setTimeout(()=>{ idleBehavior(); idleLoop(); }, 8000+Math.random()*8000); })();

  /* ---------- 打瞌睡 ---------- */
  function armSleep(){
    clearTimeout(sleepTimer); clearInterval(zzzT);
    if(REDUCED) return;
    sleepTimer = setTimeout(()=>{
      if(st!=='idle' || Object.keys(tasks).length) return;
      st = 'sleep';
      pet.classList.add('sleep'); pet.classList.remove('show-bubble');
      zzzT = setInterval(()=>{
        const z = document.createElement('span'); z.className='zzz'; z.textContent='z';
        z.style.left = (55+Math.random()*14)+'%'; z.style.top = '18%';
        z.style.fontSize = (11+Math.random()*7)+'px';
        fx.appendChild(z); setTimeout(()=>z.remove(), 2500);
      }, 1800);
    }, 45000);
  }
  function wake(force){
    if(st==='sleep'){
      st='idle'; pet.classList.remove('sleep');
      clearInterval(zzzT);
      notes(1); say(t('唔…醒了'));
    }
    armSleep();
  }
  armSleep();

  /* ---------- 摸头 ---------- */
  let petTimer = null, headCd = 0;
  pet.addEventListener('pointermove', e=>{
    if(st!=='idle'||REDUCED) return;
    const r = pet.getBoundingClientRect();
    if(e.clientY - r.top < r.height*0.42){   // 指针悬在鱼头附近
      clearTimeout(petTimer);
      const now = Date.now();
      if(now > headCd){                       // 6s 冷却，防刷屏
        headCd = now + 6000;
        notes(1); say(fishLine(FISH_REACT));
      }
    }
  });

  /* ---------- 拖拽挪位（位置记忆） ---------- */
  let drag=null, lastX=0, lastT=0;
  pet.addEventListener('pointerdown', e=>{
    if(pet.classList.contains('min')) return;
    if(e.button!==0) return;
    /* 迷你卡 / 徽标 / 提示条 / 食物栏 / 右键菜单都是 #pet 的**子节点**。
       点它们时若照常 setPointerCapture(e.pointerId)，后续 pointerup 与由它派生的
       click 会被重定向到 #pet 本体 —— 按钮自己的 click 永远收不到，确认卡就成了
       「点了没反应、什么都没执行」。这不是动画偏移，也不是测试装置的局限，
       是真实的产品缺陷（用户连续两轮实测复现）。
       只有按在桌宠本体（SVG / 空白处）上才启用拖拽与指针捕获。 */
    if(e.target && e.target.closest &&
       e.target.closest('.pet-mc, .pet-badge, .pet-tip, .pet-food, .pet-ctx')) return;
    wake();
    const r = pet.getBoundingClientRect();
    pet.style.left = r.left+'px'; pet.style.top = r.top+'px';
    pet.style.right = 'auto'; pet.style.bottom = 'auto';
    drag = {x:e.clientX, y:e.clientY, moved:false};
    lastX = e.clientX; lastT = performance.now();
    clearTimeout(patT);
    patT = setTimeout(()=>{ if(drag && !drag.moved && st==='idle' && !pet.classList.contains('dizzy')) patFx(); }, 1500);
    try{ pet.setPointerCapture(e.pointerId); }catch(_){}
  });
  pet.addEventListener('pointermove', e=>{
    if(!drag) return;
    const dx=e.clientX-drag.x, dy=e.clientY-drag.y;
    if(Math.hypot(dx,dy)>4){ drag.moved=true; pet.classList.add('dragging'); clearTimeout(patT); }
    if(drag.moved){
      pet.style.left=(parseFloat(pet.style.left)+ (e.clientX-drag.x))+'px';
      pet.style.top=(parseFloat(pet.style.top)+ (e.clientY-drag.y))+'px';
      setView('side'); setFacing(e.clientX >= drag.x ? true : false);   // 拖拽侧身朝向拖动方向（原版行为）
      drag.x=e.clientX; drag.y=e.clientY;
      const now = performance.now(), vx=(e.clientX-lastX)/Math.max(now-lastT,1);
      lastX=e.clientX; lastT=now;
      svg.style.transition='none';
      svg.style.transform = `rotate(${Math.max(-16, Math.min(16, vx*36))}deg)`;
      if(!REDUCED){
        if(Math.abs(vx) > 1.6) wobble += Math.abs(vx)*3; else wobble = Math.max(0, wobble-0.6);
        if(wobble > 26 && !pet.classList.contains('dizzy')) enterDizzy();
        if(pet.classList.contains('dizzy') && wobble > 60 && dizzyStage === 1){
          dizzyStage = 2; say(t('再晃要吐了🫠'), true);
          for(let i=0;i<5;i++) spawn('up',{left:(22+i*14)+'%',top:'10%',animationDelay:(i*.1)+'s',color:'#D6B27C'},1700,'★');
        }
      }
    }
  });
  pet.addEventListener('pointerup', ()=>{
    clearTimeout(patT);
    if(drag && drag.moved){
      pet.dataset.dragged='1';
      svg.style.transition=''; svg.style.transform='';
      setView('front'); setFacing(false); petReadPos();   // 松手回正面；同步位置给行走控制器
      if(Math.random() < .5) say(fishLine(FISH_DRAG));     // 原版：松手 50% 概率说一句
      try{ localStorage.setItem('ek-pet-pos', JSON.stringify({x:pet.style.left, y:pet.style.top})); }catch(_){}
    }
    pet.classList.remove('dragging');
    drag=null;
  });
  try{
    const pos = JSON.parse(localStorage.getItem('ek-pet-pos')||'null');
    if(pos && pos.x && pos.y){ pet.style.left=pos.x; pet.style.top=pos.y; pet.style.right='auto'; pet.style.bottom='auto'; }
  }catch(_){}

  /* ---------- 开场问候 ---------- */
  setTimeout(()=>{
    if(st!=='idle') return;
    if(document.body.classList.contains('pet-gone')) return;   // 已被彻底隐藏就不开口
    // 有人设了昵称就带上：问候是唯一适合直呼其名的场景
    const greet = ekGreetText();
    say(greet);
    petGreetWeather(greet);   // 天气开着就补一句（不阻塞上面这句）
  }, 1200);
})();


