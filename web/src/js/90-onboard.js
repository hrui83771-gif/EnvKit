/* ---------- 首次引导 / 产品导览 + P1-4 操作教学（v4） ----------
   设计：docs/DESIGN-onboarding-2026-11-06.md
   红线：全部演示数据是内置静态常量，**不碰任何接口、不写配置、
   不触发真实确认卡/权限逻辑、不执行任何命令**。P1-4 只高亮，不代点不代跑检测。
   依赖：Swiper 12（翻页）、Anime.js v4 UMD（动画）、driver.js（控件高亮），
   三者都从同域 /vendor/ 加载；任一缺失都降级成「静态但可用」，不白屏。
   注意：本文件按 90- 前缀最后拼入，事件一律委托 document + closest()。

   v4 相对 v3 的变化：
   - 8 屏不再同构，各自一套构图（字标 / 散落汇聚 / 时间轴 / 终端+报告 / 控制台 / 对话 / 阶梯 / 章节地图）
   - 头像从 5 处收敛到片头 1 处；AI 屏改成克隆主界面真实桌宠 #petSvg
   - AI 屏与检测屏加入打字机（typeText），所有文本都能被"跳过动画"一次性补全
   - 顶栏头像进度环换成细进度条 + 章节名

   v5 变化（本轮）：
   - 首次也播完整 8 屏，不再有"首次精选 5 屏"这一层
   - 每屏补了一层客观细节：开场 HUD 四个数字 + 剖面轮转高亮、汇聚屏"5 → 1"计数、
     时间轴自动前进 + 前置/耗时、检测终端进度条 + 报告结论行、日志侧栏分区计数与轮转、
     AI 屏保留多轮历史 + trace 耗时 + 桌宠状态联动、权限屏"直接做/要确认/不做"三栏清单、
     收尾屏每章一句摘要 + 总览 */

(function(){
  'use strict';

  var OB = document.getElementById('onboard');
  if(!OB) return;

  var KEY = 'ek-guide-v1';
  /* v5：首次也完整播 8 屏（原来只播 [0,1,2,5,7] 五屏精选）。
     常量保留成显式列表，是为了让「首次选集」这件事仍然是一个可断言的点：
     真要回到精简版只改这行，测试会立刻告诉你屏数和顺序变了。 */
  var FIRST_RUN_SLIDES = [0, 1, 2, 3, 4, 5, 6, 7];
  var BRAND_MS = 1400;
  var TOTAL = 8;
  var REDUCED = !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  var AM = window.anime || null;

  /* qa() 是**在引导层内部**查的（OB.querySelectorAll），拿它查 .modal 永远查不到。
     弹窗在主页面文档里，必须用 document 查。 */
  function closeModals(){
    Array.prototype.forEach.call(document.querySelectorAll('.modal.show'), function(m){
      m.classList.remove('show');
    });
  }

  function q(sel){ return OB.querySelector(sel); }
  function qa(sel){ return Array.prototype.slice.call(OB.querySelectorAll(sel)); }
  function has(o, k){ return !!(o && typeof o[k] === 'function'); }
  function pad(n){ return (n < 10 ? '0' : '') + n; }

  /* 全局翻译器故意跳过 CODE / PRE（真实日志、命令回显必须原文照出，见 10-i18n.js 的 EK_SKIP）。
     但引导里的报错行是**演示数据**，不是真实输出：跳过它，EN 模式下这屏就会留一句中文。
     所以这里单独补一次，用 t() 直译 —— 换回中文时 t() 会沿 EK_ZH_OF 反查，重复调用也幂等。 */
  function trCode(){
    qa('code,pre').forEach(function(el){
      var s = (el.textContent || '').trim();
      if(!s) return;
      var v = t(s);
      if(v !== s) el.textContent = v;
    });
  }

  /* ---------- 背景粒子层 ----------
     一个 canvas 画几十个慢速漂移的点，近则连线，指针靠近会被推开。
     没有引 tsParticles 那类库（≈90KB）：这一层只画点和线，引库换不来效果，
     只换来一个要跟着升级、还得从 /vendor/ 同域加载的外部依赖。
     减少动态偏好下只画一帧静态分布 —— 有画面，不动。 */
  var FX = (function(){
    var cv = q('#ob-fx');
    if(!cv || !cv.getContext) return {start:function(){}, stop:function(){}, theme:function(){}};
    var ctx = cv.getContext('2d');
    var W = 0, H = 0, raf = 0, on = false, dots = [];
    var cA = '#8CA6C6', cB = '#D9B2A0';
    var ptr = {x:-9999, y:-9999};
    var LINK = 132, LINK2 = LINK * LINK, PUSH = 150, PUSH2 = PUSH * PUSH;

    function cssVar(name, fb){
      var v = '';
      try{ v = getComputedStyle(OB).getPropertyValue(name) || ''; }catch(e){}
      return v.trim() || fb;
    }
    function theme(){
      /* 不用 --brand2：那个值在浅色底上几乎看不见。这两个变量在 CSS 里
         按主题各给了一套（浅色偏深、暗色偏亮）。 */
      cA = cssVar('--ob-fx-a', '#6E86A8');
      cB = cssVar('--ob-fx-b', '#C08B72');
    }
    function seed(){
      var n = Math.round(W * H / 19000);
      n = Math.max(22, Math.min(72, n));
      dots = [];
      for(var i = 0; i < n; i++){
        dots.push({
          x: Math.random() * W,
          y: Math.random() * H,
          vx: (Math.random() - .5) * .26,
          vy: (Math.random() - .5) * .26,
          r: 1 + Math.random() * 1.7,
          ph: Math.random() * Math.PI * 2,
          sp: .6 + Math.random() * 1.1,
          /* 少数点用暖色：全蓝就是星空壁纸，掺一点陶土色才和整页的水彩暖调对上 */
          warm: Math.random() < .26
        });
      }
    }
    function resize(){
      var r = cv.getBoundingClientRect();
      W = Math.max(1, Math.round(r.width));
      H = Math.max(1, Math.round(r.height));
      var dpr = Math.min(2, window.devicePixelRatio || 1);
      cv.width = Math.round(W * dpr);
      cv.height = Math.round(H * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      seed();
    }
    function draw(ts){
      if(!on) return;
      var t = ts || 0, i, j, a, b, dx, dy, d2, d;
      ctx.clearRect(0, 0, W, H);
      for(i = 0; i < dots.length; i++){
        a = dots[i];
        a.x += a.vx; a.y += a.vy;
        dx = a.x - ptr.x; dy = a.y - ptr.y;
        d2 = dx * dx + dy * dy;
        if(d2 < PUSH2 && d2 > .01){
          d = Math.sqrt(d2);
          var f = (1 - d / PUSH) * .9;
          a.x += dx / d * f * 3;
          a.y += dy / d * f * 3;
        }
        if(a.x < -24) a.x = W + 24; else if(a.x > W + 24) a.x = -24;
        if(a.y < -24) a.y = H + 24; else if(a.y > H + 24) a.y = -24;
      }
      ctx.lineWidth = 1;
      ctx.strokeStyle = cA;
      for(i = 0; i < dots.length; i++){
        a = dots[i];
        for(j = i + 1; j < dots.length; j++){
          b = dots[j];
          dx = a.x - b.x; dy = a.y - b.y;
          d2 = dx * dx + dy * dy;
          if(d2 > LINK2) continue;
          ctx.globalAlpha = (1 - Math.sqrt(d2) / LINK) * .42;
          ctx.beginPath();
          ctx.moveTo(a.x, a.y);
          ctx.lineTo(b.x, b.y);
          ctx.stroke();
        }
      }
      for(i = 0; i < dots.length; i++){
        a = dots[i];
        var tw = .55 + .45 * Math.sin(t / 900 * a.sp + a.ph);
        ctx.globalAlpha = .34 + .5 * tw;
        ctx.fillStyle = a.warm ? cB : cA;
        ctx.beginPath();
        ctx.arc(a.x, a.y, a.r * (.8 + .35 * tw), 0, Math.PI * 2);
        ctx.fill();
      }
      ctx.globalAlpha = 1;
      if(!REDUCED) raf = requestAnimationFrame(draw);
    }
    function onMove(e){
      var r = cv.getBoundingClientRect();
      ptr.x = e.clientX - r.left;
      ptr.y = e.clientY - r.top;
    }
    function onLeave(){ ptr.x = ptr.y = -9999; }
    function start(){
      if(on) return;
      resize(); theme();
      window.addEventListener('resize', resize);
      OB.addEventListener('pointermove', onMove);
      OB.addEventListener('pointerleave', onLeave);
      on = true;
      draw(0);
    }
    function stop(){
      on = false;
      if(raf) cancelAnimationFrame(raf);
      raf = 0;
      window.removeEventListener('resize', resize);
      OB.removeEventListener('pointermove', onMove);
      OB.removeEventListener('pointerleave', onLeave);
      try{ ctx.clearRect(0, 0, W, H); }catch(e){}
    }
    return {start:start, stop:stop, theme:theme};
  })();

  /* ---------- 顶栏开关：中英 / 明暗 ----------
     调的是主界面那两个函数本身（ekSetLang / applyTheme），所以"和软件内同步"
     不是额外做一次同步，而是本来就只有一份状态。 */
  var ICON_MOON = '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M6.4 1.9a6.3 6.3 0 0 0 7.7 7.7A6.6 6.6 0 1 1 6.4 1.9Z" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/></svg>';
  var ICON_SUN = '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="3" fill="none" stroke="currentColor" stroke-width="1.3"/><path d="M8 1.3v1.5M8 13.2v1.5M1.3 8h1.5M13.2 8h1.5M3.3 3.3l1.1 1.1M11.6 11.6l1.1 1.1M12.7 3.3l-1.1 1.1M4.4 11.6l-1.1 1.1" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg>';

  function isEN(){ return document.documentElement.getAttribute('lang') === 'en'; }
  function isDark(){ return document.body.dataset.theme === 'dark'; }

  function syncTools(){
    var lb = q('#ob-lang'), tb = q('#ob-theme');
    if(lb){
      /* 按钮写"点了会变成什么"：EN 模式下写"中文"。与主界面 #btn-lang 同一套写法 */
      lb.textContent = isEN() ? '中文' : 'EN';
      var lt = t(isEN() ? '切换为中文' : '切换为英文');
      lb.title = lt; lb.setAttribute('aria-label', lt);
    }
    if(tb){
      tb.innerHTML = isDark() ? ICON_SUN : ICON_MOON;
      var tt = t(isDark() ? '切换亮色' : '切换暗色');
      tb.title = tt; tb.setAttribute('aria-label', tt);
    }
  }


  /* ---------- 静态演示数据（不来自接口，不写回任何地方） ---------- */
  var CHAPTERS = ['开场','为什么存在','四步走','状态','日志','AI 与桌宠','安全边界','收尾'];
  var CHAPTER_SUM = [
    '五件事，一个入口',
    '凭什么说修好了',
    '四步各有输入与产出',
    '检测给结论，不给猜测',
    '日志按来源分区',
    '说人话，动手前先问',
    '四档权限逐级放开',
    '从安装页开始'
  ];

  /* 开场右侧叠层剖面的轮转说明：焦点到哪一块，下面这句就换成哪一句 */
  var SLAB_NOTES = [
    '环境安装 · Go / Node.js / MySQL 一次装齐',
    '程序配置 · 连接、建库、导入导出都在这一页',
    '链端校验 · 节点与 WeBASE 单独一条结论',
    '同一个入口，同一份状态'
  ];

  /* w = 这一步会往磁盘上写什么。四步里只有"链端校验"是纯只读的，
     把它写出来比只说"有输入产出"有用——用户真正担心的是"会不会动我的机器"。 */
  var STEP_TEXT = [
    {b:'环境安装', p:'检测 Go / Node.js / MySQL 的版本和安装位置，缺什么补什么，国内镜像下载会校验 SHA256。',
     i:'无 · 只读本机 PATH 与版本号', o:'一份可执行的安装计划',
     pre:'Windows 10 / 11 · 有管理员权限', cost:'首次约 3–5 分钟',
     w:'C:\\EnvKit\\tools 下的解压目录 · 当前用户的 PATH 与 GOPROXY', ro:false},
    {b:'程序配置', p:'填数据库、建库建表、导入导出配置；连不上会直接告诉你是哪一项不通。',
     i:'数据库地址与账号', o:'config.json + 初始化好的库表',
     pre:'环境安装已完成 · MySQL 在跑', cost:'约 1 分钟',
     w:'config.json（原子写入，旧配置留快照）· 目标库的表结构', ro:false},
    {b:'链端校验', p:'检查 FISCO-BCOS 节点与 WeBASE-Front 是否在、区块高度和客户端版本是否正常。',
     i:'节点端口与证书', o:'链端健康结论 + 区块高度',
     pre:'能 SSH 到链端主机', cost:'约 20 秒',
     w:'不写任何东西：只读端口、证书与区块高度', ro:true},
    {b:'程序启动', p:'先后端再前端一键拉起，日志按来源分栏，进程树由 EnvKit 统一回收。',
     i:'前端产物 + 后端二进制', o:'两个监听端口 + 可托管的进程树',
     pre:'配置与链端都已就绪', cost:'约 15 秒',
     w:'两个子进程 · 按日轮转的日志文件 · 进程树登记', ro:false}
  ];

  /* 检测项按"属于哪一类"分组，而不是平铺成一张表。
     g 就是组名，渲染时按 GROUPS 的顺序建三张卡。 */
  var DETECT = [
    {n:'Go',         v:'1.27.3',    p:'C:\\EnvKit\\tools\\go',        k:'ok',   s:'正常', g:'运行时'},
    {n:'Node.js',    v:'22.22.2',   p:'C:\\Program Files\\nodejs',    k:'ok',   s:'正常', g:'运行时'},
    {n:'MySQL 8',    v:'8.0.36',    p:'localhost:3306',               k:'ok',   s:'正常', g:'数据与磁盘'},
    {n:'磁盘剩余',   v:'18.4 GB',   p:'D:\\BCGD\\FarmTrace',          k:'ok',   s:'正常', g:'数据与磁盘'},
    {n:'GOPROXY',    v:'goproxy.cn',p:'环境变量已生效',                k:'ok',   s:'正常', g:'网络与链端'},
    {n:'FISCO 节点', v:':20200',    p:'127.0.0.1 · 未响应',           k:'warn', s:'待校验', g:'网络与链端'}
  ];
  var GROUPS = ['运行时', '数据与磁盘', '网络与链端'];

  /* 每个分区给到 6–7 行：只放 5 行时控制台下半截是空的，像没加载完 */
  var SCOPES = {
    install:['[INFO] 检测 Go 1.27.3 ... 正常','[INFO] 检测 Node.js 22.22.2 ... 正常','[INFO] GOPROXY 已指向国内镜像','[INFO] MySQL 8.0.36 服务在线','[INFO] 依赖校验完成，无缺失组件','[INFO] 复用已有 C:\\EnvKit\\tools 目录','[INFO] 安装计划已生成，等待执行'],
    config: ['[INFO] 读取 config.json','[INFO] 数据库连接测试通过','[INFO] 建表脚本已就绪','[INFO] 配置原子写入完成','[INFO] 已有配置快照，可回滚','[INFO] 前端产物校验通过（13 个片段）'],
    chain:  ['[INFO] 节点 20200 可达','[INFO] WeBASE-Front 5002 可达','[WARN] 区块高度为 0，可能是新链','[INFO] 客户端版本 2.11.0','[INFO] 群组 1 已就绪','[INFO] 证书有效期剩余 312 天','[INFO] 链端健康结论已写入状态行'],
    start:  ['[INFO] 后端监听 :8888','[INFO] 前端监听 :5173','[INFO] 进程树已登记','[INFO] 健康检查 200 OK','[INFO] 日志采集已挂载','[INFO] 托盘图标已显示'],
    sys:    ['[INFO] 日志按日轮转','[INFO] 配置原子写入完成','[INFO] 单实例锁已获取','[INFO] 托盘初始化成功','[INFO] 审计库已就绪','[INFO] 上次退出为正常关机']
  };

  /* 权限四档：ok = 不需要确认就能做，ask = 要先点一次确认，no = 根本不做。
     这三栏是这一屏的"细节"所在：光说"四档权限"很抽象，列出来才看得懂边界在哪。 */
  var PERMS = [
    {b:'只读', p:'能看状态、日志、检测报告、轨迹与审计。不需要确认，永远不会改动任何东西。',
     ok:['看运行状态','读日志与报告','查审计与轨迹'], ask:[], no:['改配置','启停服务']},
    {b:'诊断', p:'在只读基础上能跑检测、读数据库结构与抽样数据。不需要确认，仍然不写入。',
     ok:['跑环境检测','读库表结构','抽样只读'], ask:[], no:['写入任何配置']},
    {b:'受限写', p:'能改配置、执行备份复验等可逆操作。需要一次确认，且只能走白名单里的固定动作。',
     ok:['备份复验','导出配置快照'], ask:['改 config.json','建库建表'], no:['执行任意命令']},
    {b:'审批后动作', p:'启停服务、链端操作等。破坏性操作走 428 + 红色确认框；不会执行任意命令。',
     ok:['白名单内的启停'], ask:['重启后端','链端启停'], no:['任意 shell','未登记的脚本']}
  ];

  /* AI 屏的对话脚本：trace 是"工具调用"提示，answer 是最终回答。
     写类问句的 answer 以「确认卡」开头 —— 打字机逐字输出时，250ms 内已经能看到关键词，
     同时确认卡是同步弹出的（真实交互也是先弹卡再解释）。 */
  var AI_SCRIPT = {
    read: {
      me:'现在服务都起来了吗？',
      trace:'读取运行状态 · 3 项检查',
      answer:'后端在跑、前端在跑，链端两项校验都过了。要我把最近十条日志摘出来吗？',
      ms:'0.42s'
    },
    write: {
      me:'帮我重启一下后端',
      trace:'解析意图 · 命中白名单动作',
      answer:'确认卡在这里：你点了才执行。真实操作仍走权限闸门。',
      ms:'0.31s'
    },
    explain: {
      me:'这段报错是什么意思？',
      trace:'检索日志与错误码 · 2 处命中',
      answer:'端口被占用：8888 被上一个实例占着。先结束残留进程，再点启动。',
      ms:'0.66s'
    }
  };
  var AI_AFTER = '权限闸门照旧生效，这里不会执行任何动作。';

  /* 桌宠状态跟着对话走 —— 主界面那边也是这样一句一句变的 */
  var PET_STATE = {
    idle:'待机 · 已连接',
    think:'思考中 · 读取状态',
    wait:'待你确认 · 不会自己动手',
    done:'已就绪 · 可以继续问'
  };

  /* 桌宠的内置台词，逐句照抄主界面（50-pet.js 的 FISH_LINES / FISH_FOOD 与 say()）。
     演示页不另编台词 —— 另编了就只是"看起来像"，接过来才叫同一个角色。
     每个动作两句话：先出声，再补一句它同时会说的内容（天气是查之前/查到后）。
     d 是可选的补充说明，没有就只显示台词本身。 */
  var PET_ACT = {
    say:     {l:['我不思考了', '我先去吃饭啦！这个你测一下~', '大肥鱼坐的住']},
    feed:    {l:['小鱼干！我的最爱！'], d:'双击它投喂，给的东西不同台词也不同'},
    weather: {l:['我瞅瞅天…', '合肥今天 19° · 湿度 42° · 体感 17°'],
              d:'只有天气会出网，开关默认是关的'},
    err:     {l:['出错了…点我看日志', '帮你在日志里标出来了'],
              d:'红色告警时点它，会直接跳到那条错误行'}
  };

  /* ---------- 运行时状态 ---------- */
  var ALL_SLIDES = qa('.ob-slide');
  var WRAP = OB.querySelector('.swiper-wrapper');
  var mode = 'replay';
  var slides = [];
  var cur = 0;
  var sw = null;
  var brandTimer = null;
  var brandArmed = false;
  var returnFocus = null;
  var inited = {};
  var timers = [];
  var typingCancel = null;
  var petMounted = false;
  var slabIdx = 0;

  function seen(){ try{ return localStorage.getItem(KEY) === '1'; }catch(e){ return false; } }
  function mark(){ try{ localStorage.setItem(KEY, '1'); }catch(e){} }

  /* 所有延时都登记，切屏/关闭时统一清掉，避免上一屏的回调打到下一屏 */
  function later(fn, ms){
    var id = setTimeout(fn, REDUCED ? 0 : ms);
    timers.push(id);
    return id;
  }
  function clearTimers(){
    timers.forEach(clearTimeout);
    timers = [];
  }

  function avatarSrc(){
    var g = document.getElementById('app-logo');
    if(g && g.src) return g.src;
    try{ return typeof APP_ICON !== 'undefined' ? APP_ICON : ''; }catch(e){ return ''; }
  }

  /* ---------- Anime.js v4：只在库存在且用户没要求减弱动效时动 ---------- */
  function fx(target, params){
    if(!target || REDUCED || !has(AM, 'animate')) return;
    try{ AM.animate(target, params); }catch(e){}
  }
  function stagger(v){
    try{ return has(AM, 'stagger') ? AM.stagger(v) : 0; }catch(e){ return 0; }
  }
  function resetStyle(sel){
    qa(sel).forEach(function(e){ e.removeAttribute('style'); });
  }

  /* ---------- 打字机 ----------
     返回前会取消上一次未完成的打字（并把那段补全），保证同一时刻只有一处光标。 */
  function typeText(el, text, speed, done){
    endType();
    if(!el) return;
    if(REDUCED){ el.textContent = text; if(done) done(); return; }
    el.textContent = '';
    el.classList.add('typing');
    var i = 0;
    var iv = setInterval(function(){
      i += 1;
      el.textContent = text.slice(0, i);
      if(i >= text.length){
        clearInterval(iv);
        typingCancel = null;
        el.classList.remove('typing');
        if(done) done();
      }
    }, speed || 20);
    typingCancel = function(){
      clearInterval(iv);
      typingCancel = null;
      el.textContent = text;
      el.classList.remove('typing');
      /* 被打断也要把 done 跑完：否则 data-live 标不上，这段回答就没法被冻结进历史 */
      if(done) done();
    };
  }
  function endType(){ if(typingCancel) typingCancel(); }

  /* ---------- 桌宠：克隆主界面真实形象，而不是拿头像 + 两个圆点假装 ----------
     #pet 在引导期间被 body.ob-active 隐藏，但克隆出来的是独立节点，不受影响。
     克隆后必须抹掉所有 id，否则会和主界面元素撞 id、也会继承 #pet-img 的状态。 */
  function mountPet(){
    if(petMounted) return;
    var slot = q('#ob-pet-slot');
    var src = document.getElementById('petSvg');
    if(!slot || !src) return;
    var c = src.cloneNode(true);
    c.removeAttribute('id');
    c.setAttribute('width', '190');
    c.setAttribute('height', '204');
    Array.prototype.forEach.call(c.querySelectorAll('[id]'), function(n){ n.removeAttribute('id'); });
    slot.appendChild(c);
    petMounted = true;
  }

  /* ---------- 品牌片头 ---------- */
  var BRAND_SELS = '.ob-brand-glow,.ob-brand-ava,.ob-brand-word,.ob-brand-rule,.ob-brand-tag,.ob-brand-inner';

  function endBrand(){
    if(!brandArmed) return;
    brandArmed = false;
    if(brandTimer){ clearTimeout(brandTimer); brandTimer = null; }
    var b = q('#ob-brand');
    if(b) b.classList.remove('play');
    document.removeEventListener('keydown', onBrandKey, true);
    resetStyle(BRAND_SELS);
    var skip = q('#ob-skip');
    if(skip && typeof skip.focus === 'function') skip.focus();
  }

  function onBrandKey(e){
    if(!brandArmed) return;
    e.preventDefault();
    e.stopPropagation();
    endBrand();
  }

  function playBrand(){
    var b = q('#ob-brand');
    if(!b) return;
    resetStyle(BRAND_SELS);
    b.classList.add('play');
    brandArmed = true;
    document.addEventListener('keydown', onBrandKey, true);
    fx(q('.ob-brand-glow'), {opacity:[0,.55], scale:[.7,1.06], duration:900, ease:'outQuad'});
    fx(q('.ob-brand-ava'),  {opacity:[0,1], scale:[.86,1], duration:520, delay:60});
    fx(q('.ob-brand-word'), {opacity:[0,1], y:[14,0], letterSpacing:['.04em','-.045em'], duration:560, delay:260});
    fx(q('.ob-brand-rule'), {opacity:[0,.7], scaleX:[0,1], duration:420, delay:520});
    fx(q('.ob-brand-tag'),  {opacity:[0,1], y:[8,0], duration:380, delay:600});
    fx(q('.ob-brand-inner'),{opacity:[1,0], y:[0,-14], duration:260, delay:1120});
    brandTimer = setTimeout(endBrand, BRAND_MS);
  }

  /* ---------- 每屏入场：不同屏动不同的块，避免"整页一起淡入"的塑料感 ---------- */
  function enterAnim(slide){
    if(REDUCED || !slide) return;
    var els = slide.querySelectorAll(
      '.ob-kicker,.ob-h,.ob-display,.ob-tagline,.ob-lead,.ob-quiet,.ob-actions,' +
      '.ob-hero-art,.ob-recheck,.ob-timeline,.ob-detail,.ob-detect,.ob-console,.ob-chat,.ob-perms,.ob-chapter-grid');
    if(!els.length) return;
    fx(els, {opacity:[0,1], y:[16,0], delay:stagger(62), duration:520, ease:'outQuad'});
  }

  /* ---------- 顶栏进度 + 播报 ---------- */
  function setProgress(){
    var n = slides.length || 1;
    var bar = q('#ob-progress-bar');
    if(bar) bar.style.width = ((cur + 1) / n * 100) + '%';

    var s = slides[cur];
    var idx = s ? Number(s.getAttribute('data-idx')) : 0;
    var no = q('#ob-sec-no'), nm = q('#ob-sec-name');
    /* 章节号写成 03 / 08：一眼能算出还剩几屏，比纯数字有进度感 */
    if(no) no.textContent = pad(idx + 1) + ' / ' + pad(slides.length);
    if(nm) nm.textContent = t(CHAPTERS[idx]);

    var live = q('#ob-live');
    if(live) live.textContent = t('第 {0} 屏，共 {1} 屏', cur + 1, slides.length);

    var prev = q('#ob-prev'), next = q('#ob-next');
    if(prev) prev.disabled = (cur === 0);
    if(next) next.disabled = (cur >= slides.length - 1);

    qa('#ob-chapter-grid button').forEach(function(b){
      b.classList.toggle('on', Number(b.getAttribute('data-idx')) === cur);
    });
  }

  function onSlide(i){
    cur = i;
    clearTimers();
    endType();
    var s = slides[i];
    setProgress();
    enterAnim(s);
    if(i === 1) bpDraw();          /* 每次进 02 屏都把图纸重画一遍（真实逐条描线） */
    if(s) initDemo(Number(s.getAttribute('data-idx')));
    if(s) autoplay(Number(s.getAttribute('data-idx')));
  }

  /* 回到某一屏就重新开始它自己的循环演示（离开时 clearTimers 已经把上一轮的链断掉） */
  function autoplay(idx){
    if(REDUCED) return;
    if(idx === 0){ slabIdx = 0; later(rotateSlab, 260); }
  }

  /* ---------- 各屏演示 ---------- */
  function initDemo(idx){
    if(inited[idx]) return;
    inited[idx] = true;
    if(idx === 2) showStep(0);
    if(idx === 3) runDetect();      /* 进去就扫一遍：空终端 + 全灰的报告行像坏掉了 */
    if(idx === 4) renderScope('install');
    if(idx === 5) mountPet();
    if(idx === 6) showPerm(0);
    if(idx === 7) renderChapters();
    if(idx === 1) renderCrit(rcPin);
  }

  /* 03 时间轴：进度线 + 详情（做什么 / 输入 / 产出 / 前置 / 耗时）
     没有"打勾"：这一屏一次也没真的跑过，勾号只会让人以为已经做完了。
     自动前进也去掉——它和打勾是同一个假进度，读说明的时候还会抢走焦点。 */
  function showStep(i){
    var n = STEP_TEXT.length;
    qa('#ob-track .ob-step').forEach(function(b){
      b.classList.toggle('on', Number(b.getAttribute('data-step')) === i);
    });
    var fill = q('#ob-tl-fill');
    if(fill) fill.style.width = (i / (n - 1) * 75) + '%';   /* rail 从 12.5% 到 87.5% */
    var d = q('#ob-step-desc');
    if(!d) return;
    var x = STEP_TEXT[i];
    d.innerHTML = '<b>' + t(x.b) + '</b><p>' + t(x.p) + '</p>' +
      '<div class="ob-io">' +
      '<span><em>' + t('输入') + '</em>' + t(x.i) + '</span>' +
      '<span><em>' + t('产出') + '</em>' + t(x.o) + '</span>' +
      '<span><em>' + t('前置') + '</em>' + t(x.pre) + '</span>' +
      '<span><em>' + t('预计耗时') + '</em>' + t(x.cost) + '</span>' +
      '</div>' +
      '<div class="ob-writes' + (x.ro ? ' ro' : '') + '"><em>' +
        t(x.ro ? '只读' : '会写') + '</em><span>' + t(x.w) + '</span></div>';
  }


  /* 01 开场：叠层剖面轮流被点亮，下面那句说明跟着走 */
  function focusSlab(i){
    var order = [q('.ob-slab.s1'), q('.ob-slab.s2'), q('.ob-slab.s3'), q('.ob-slab.s4')];
    order.forEach(function(el, k){ if(el) el.classList.toggle('focus', k === i); });
    var note = q('#ob-art-note');
    if(note) note.textContent = t(SLAB_NOTES[i] || SLAB_NOTES[0]);
  }

  function rotateSlab(){
    focusSlab(slabIdx);
    slabIdx = (slabIdx + 1) % SLAB_NOTES.length;
    later(rotateSlab, 2600);
  }

  /* 04 检测：先按组建设置，再逐条点亮。
     结论条是整屏的锚点，所以状态全部挂在它身上（数字 / 六段条 / 判断 / 下一步）。 */
  function renderDetect(blank){
    var box = q('#ob-detect-rows');
    if(!box) return;
    box.innerHTML = '';
    GROUPS.forEach(function(g){
      var rows = DETECT.filter(function(r){ return r.g === g; });
      if(!rows.length) return;
      var card = document.createElement('div');
      card.className = 'ob-grp';
      var head = document.createElement('div');
      head.className = 'ob-grp-h';
      head.innerHTML = '<b>' + t(g) + '</b><span>' + rows.length + '</span>';
      card.appendChild(head);
      rows.forEach(function(r){
        var d = document.createElement('div');
        d.className = 'ob-row' + (blank ? '' : ' lit ' + r.k);
        d.innerHTML = '<i class="dot"></i><span class="nm">' + t(r.n) + '</span>' +
                      '<span class="vl">' + r.v + '</span>' +
                      '<span class="st">' + t(r.s) + '</span>' +
                      '<span class="path">' + r.p + '</span>';
        card.appendChild(d);
      });
      box.appendChild(card);
    });

    /* 六段条先摆成全灰的短刻度：一进来就能看出"要检 6 项"，而不是空着 */
    var segs = q('#ob-segs');
    if(segs){
      segs.innerHTML = '';
      DETECT.forEach(function(){ segs.appendChild(document.createElement('i')); });
    }
    var term = q('#ob-detect-cmd');
    if(term) term.innerHTML = '';
    var foot = q('#ob-report-foot');
    if(foot) foot.classList.remove('on');
    var vd = q('#ob-verdict');
    if(vd) vd.classList.remove('hot');
    var score = q('#ob-vd-score');
    if(score) score.textContent = '\u2013';
    var msg = q('#ob-vd-msg');
    if(msg) msg.textContent = t('还没跑检测');
    var prog = q('#ob-term-prog i');
    if(prog) prog.style.width = '0%';
    var pct = q('#ob-term-pct');
    if(pct) pct.textContent = t('就绪');
  }

  /* 数字从 0 滚到 5：比直接贴一个 5 更像"刚算出来的" */
  function countUp(el, to, ms){
    if(!el) return;
    if(REDUCED){ el.textContent = String(to); return; }
    var start = null;
    function tick(now){
      if(start === null) start = now;
      var p = Math.min(1, (now - start) / ms);
      el.textContent = String(Math.round(to * (1 - Math.pow(1 - p, 3))));
      if(p < 1) requestAnimationFrame(tick);
    }
    requestAnimationFrame(tick);
  }

  function runDetect(){
    renderDetect(true);
    var term = q('#ob-detect-cmd');
    var lines = [
      ['$ envkit check --all', 'pr'],
      ['> 读取组件版本与安装位置 …', ''],
      ['> 校验数据库与链端连通性 …', ''],
      ['OK 6 项检查完成，1 项待校验', 'wn']
    ];
    if(term){
      term.innerHTML = '';
      lines.forEach(function(L, i){
        later(function(){
          var d = document.createElement('div');
          d.className = 'on ' + L[1];
          d.textContent = L[0];
          term.appendChild(d);
        }, i * 210);
      });
    }
    /* 终端顶部那条 2px 进度：让"正在跑"有个一眼可见的物理量 */
    var prog = q('#ob-term-prog i');
    var pct = q('#ob-term-pct');
    if(pct) pct.textContent = t('扫描中…');
    if(prog){ prog.style.width = '0%'; void prog.offsetWidth; prog.style.width = '100%'; }

    /* 行和段块同拍点亮：第 N 段就是第 N 行，两边对得上才说得通 */
    qa('#ob-detect-rows .ob-row').forEach(function(row, i){
      later(function(){ row.classList.add('lit', DETECT[i].k); }, 420 + i * 150);
    });
    qa('#ob-segs i').forEach(function(s, i){
      later(function(){ s.className = DETECT[i].k; }, 420 + i * 150);
    });

    later(function(){
      if(pct) pct.textContent = '1.8s · 6 项';
      var vd = q('#ob-verdict');
      if(vd) vd.classList.add('hot');
      countUp(q('#ob-vd-score'), 5, 300);
      var msg = q('#ob-vd-msg');
      if(msg) msg.textContent = t('1 项待校验：链端 FISCO 节点 127.0.0.1:20200 未响应');
      var foot = q('#ob-report-foot');
      if(foot){
        var sp = foot.querySelector('span');
        if(sp) sp.textContent = t('先跑配置，再复核链端');
        foot.classList.add('on');
      }
    }, 420 + DETECT.length * 150);
  }

  /* 05 日志：分区切换 + 行号由 CSS counter 生成（不影响 textContent） */
  var SCOPE_LABEL = {install:'安装', config:'配置', chain:'链端', start:'启动', sys:'系统'};

  function renderScope(k){
    qa('#ob-scopes button').forEach(function(b){
      b.classList.toggle('on', b.getAttribute('data-scope') === k);
    });
    renderScopeStats(k);
    var box = q('#ob-log-lines');
    if(!box) return;
    box.innerHTML = '';
    (SCOPES[k] || []).forEach(function(line){
      var d = document.createElement('div');
      if(line.indexOf('[WARN]') === 0 || line.indexOf('[ERROR]') === 0) d.className = 'wn';
      else if(line.indexOf('正常') >= 0 || line.indexOf('通过') >= 0 || line.indexOf('OK') >= 0) d.className = 'ok';
      d.textContent = line;
      box.appendChild(d);
    });
  }

  /* 侧栏那一列分区计数：把"五个分区各有多少行"变成能看见的数字 */
  function renderScopeStats(cur){
    var st = q('#ob-scope-stats');
    if(!st) return;
    st.innerHTML = '';
    Object.keys(SCOPES).forEach(function(key){
      var d = document.createElement('i');
      if(key === cur) d.className = 'on';
      d.innerHTML = '<em>' + t(SCOPE_LABEL[key]) + '</em><b>' + SCOPES[key].length + '</b>';
      st.appendChild(d);
    });
  }

  /* 06 AI：用户气泡 → trace → 逐字回答（写类同步弹确认卡）
     v5 起保留历史：上一轮回答会被"冻结"成一条普通气泡，新回答继续在 #ob-bubble 里逐字打，
     这样连续问三句才像真的在对话，而不是每句都覆盖同一块。 */
  /* 06 的桌宠会说话：气泡贴在头顶，内容全部来自内置台词。
     sticky=true 时不自动收（"点我确认！"要一直挂着，和主界面一致）。 */
  var petSayTimer = null;

  function petSay(html, sticky, ms){
    var b = q('#ob-pet-say');
    if(!b) return;
    clearTimeout(petSayTimer);
    b.innerHTML = html;
    b.classList.add('show');
    if(!sticky){
      petSayTimer = setTimeout(function(){ b.classList.remove('show'); }, ms || 2600);
    }
  }

  function petSayHide(){
    clearTimeout(petSayTimer);
    var b = q('#ob-pet-say');
    if(b) b.classList.remove('show');
  }

  /* 气泡内容：台词本体加粗，补充说明放 <em>；没有补充说明就只留台词 */
  function petHTML(line, d){
    return '<b>' + t(line) + '</b>' + (d ? '<em>' + t(d) + '</em>' : '');
  }

  /* 内置动作：说的这句就是主界面会说的那句，没有为演示另写文案 */
  var petRot = 0;

  function petAct(k){
    var A = PET_ACT[k];
    if(!A) return;
    if(k === 'say'){
      petSay(petHTML(A.l[petRot++ % A.l.length], A.d), false, 3400);
      return;
    }
    if(k === 'feed'){
      petSay(petHTML(A.l[0], A.d), false, 3400);
      return;
    }
    /* 天气和看错误都是两步：先出声，再给结果 */
    petSay(petHTML(A.l[0]), true);
    later(function(){
      petSay(petHTML(A.l[1], A.d), false, 3800);
    }, 900);
  }

  function petState(k){
    var m = q('#ob-pet-msg');
    if(m) m.textContent = t(PET_STATE[k] || PET_STATE.idle);
    var chip = q('.ob-pet-state');
    if(chip){
      chip.classList.toggle('hot', k === 'wait');
      chip.classList.toggle('done', k === 'done');
    }
    /* 状态和气泡是同一个状态机的两个出口：芯片写"在干什么"，
       气泡说"它此刻会说什么"——主界面这两件事本来就是一起变的 */
    if(k === 'think') petSay('<b>' + t('思考中') + '</b><em>' + t('读日志、查状态，起手就来') + '</em>', true);
    else if(k === 'wait') petSay('<b>' + t('点我确认！') + '</b>', true);
    else if(k === 'done') petSay('<b>✓ ' + t('搞定') + '</b>', false, 2000);
    else petSayHide();
  }

  function resetChat(){
    var log = q('#ob-chat-log');
    if(log){
      Array.prototype.slice.call(log.children).forEach(function(n){
        if(n.id !== 'ob-bubble' && n.parentNode) log.removeChild(n);
      });
    }
    var bub = q('#ob-bubble');
    if(bub){
      bub.className = 'ob-bubble';
      bub.removeAttribute('data-live');
      bub.innerHTML = t('今天想看点什么？') + '<em>' + t('读日志、看状态、重启服务——都会先问你。') + '</em>';
    }
    petState('idle');
    showConfirm(false);
  }

  function freezeBubble(){
    var log = q('#ob-chat-log'), bub = q('#ob-bubble');
    if(!log || !bub) return;
    var txt = (bub.textContent || '').trim();
    if(bub.getAttribute('data-live') !== '1' || !txt) return;
    var f = document.createElement('div');
    f.className = 'ob-bubble frozen';
    f.textContent = txt;
    log.insertBefore(f, bub);
    bub.removeAttribute('data-live');
    bub.textContent = '';
  }

  /* 气泡多了会把面板顶爆：只留最近几轮，从最旧的开始丢 */
  function trimLog(){
    var log = q('#ob-chat-log');
    if(!log) return;
    var kids = Array.prototype.slice.call(log.children);
    while(kids.length > 9){
      var n = kids.shift();
      if(n.id === 'ob-bubble'){ kids.push(n); continue; }
      if(n.parentNode) log.removeChild(n);
    }
  }

  function askAI(kind){
    var script = AI_SCRIPT[kind];
    var log = q('#ob-chat-log');
    var bub = q('#ob-bubble');
    if(!script || !log || !bub) return;

    endType();
    freezeBubble();
    bub.classList.remove('me');
    bub.textContent = '';
    petState('think');

    var me = document.createElement('div');
    me.className = 'ob-bubble me';
    me.textContent = t(script.me);
    log.insertBefore(me, bub);

    var tr = document.createElement('div');
    tr.className = 'ob-trace';
    tr.innerHTML = '<i></i><b>' + t('调用中') + '</b><span>' + t(script.trace) + '</span>' +
                   '<em>' + script.ms + '</em>';
    log.insertBefore(tr, bub);
    requestAnimationFrame(function(){ tr.classList.add('on'); });

    /* 写类是"先拦后解释"，确认卡必须同步出现 */
    if(kind === 'write') showConfirm(true);

    var startTyping = function(){
      tr.classList.add('done');
      typeText(bub, t(script.answer), 18, function(){
        bub.setAttribute('data-live', '1');
        petState(kind === 'write' ? 'wait' : 'done');
      });
    };
    if(kind === 'write') startTyping();
    else later(startTyping, 420);

    trimLog();
    if(log.scrollHeight) log.scrollTop = log.scrollHeight;
  }

  function showConfirm(on){
    var cf = q('#ob-demo-confirm');
    if(cf) cf.hidden = !on;
  }

  /* 07 权限阶梯 */
  var PERM_COLS = [['ok','直接做'], ['ask','要确认'], ['no','不做']];

  function showPerm(i){
    /* 联锁图只认这一处写入口：选中的那一路点亮，其余保持淡线 */
    var pw = q('.ob-permwrap');
    if(pw) pw.setAttribute('data-perm', String(i));
    qa('#ob-perms button').forEach(function(b){
      b.classList.toggle('on', Number(b.getAttribute('data-perm')) === i);
    });
    var d = q('#ob-perm-desc');
    if(d) d.innerHTML = '<b>' + t(PERMS[i].b) + '</b>' + t(PERMS[i].p);

    var box = q('#ob-perm-acts');
    if(!box) return;
    box.innerHTML = '';
    PERM_COLS.forEach(function(col){
      var cell = document.createElement('div');
      cell.className = col[0];
      var items = PERMS[i][col[0]] || [];
      var html = '<em>' + t(col[1]) + '</em>';
      if(!items.length) html += '<span>—</span>';
      else items.forEach(function(x){ html += '<span>' + t(x) + '</span>'; });
      cell.innerHTML = html;
      box.appendChild(cell);
    });
  }

  /* 08 章节地图 */
  function renderChapters(){
    var g = q('#ob-chapter-grid');
    if(!g) return;
    g.innerHTML = '';
    slides.forEach(function(s, i){
      var n = Number(s.getAttribute('data-idx'));
      var b = document.createElement('button');
      b.setAttribute('data-idx', String(i));
      b.setAttribute('data-jump', String(i));
      b.innerHTML = '<span class="ob-ch-no">' + pad(n + 1) + '</span>' +
                    '<span class="ob-ch-nm">' + t(CHAPTERS[n]) + '</span>' +
                    '<span class="ob-ch-sum">' + t(CHAPTER_SUM[n]) + '</span>';
      g.appendChild(b);
    });
    setProgress();
  }

  /* 02 复线轨道：点任一站台 → 车头开过去，展开探测方式 / 通过阈值 / 来源。
     v10 把 v9 的「白卡片 + diff 表格」换成轨道 + 站台 + 车头，交互与字段不变。
     欢迎页首次打开并没有真实故障数据，播一段"修复过程"就是编的（v3.8 删
     「重播汇聚」是同一个理由）：车头是选中态的物理化，不是执行进度；它只在
     用户点击后移动，不自动巡游、不报通过、不发请求。字段名来自真实判定链。 */
  var RC_CRIT = [
    { probe: function(){ return t('向 127.0.0.1:8888 发起 TCP 连接'); },
      pass:  function(){ return t('连接建立，且 /api/health 返回 200'); },
      from:  function(){ return t('runtime/state（唯一计算源）'); } },
    { probe: function(){ return t('用配置里的账号连一次 farm 库'); },
      pass:  function(){ return t('握手成功，并能读到表结构'); },
      from:  function(){ return t('配置 · 数据库'); } },
    { probe: function(){ return t('读取节点当前区块高度'); },
      pass:  function(){ return t('高度比上一次大，且持续增长'); },
      from:  function(){ return t('日志 · scChain'); } }
  ];
  var rcPin = null;                      /* 展开中的判据序号，null = 收起 */
  var rcMoveTimer = null;
  /* 车头只跟随「当前查看的判据」：落点从真实盒模型量，不写死三站偏移。
     它不是进度条——不自动巡游、不亮完成灯、不发请求；quiet 供切语言重绘，
     只定位不重播行进。REDUCED 下也只定位。 */
  function rcDrive(k, quiet){
    var rows = q('#ob-rc-rows');
    var train = q('#ob-rc-train');
    if(!rows || !train) return;
    clearTimeout(rcMoveTimer);
    rows.classList.remove('moving');
    if(k === null){
      train.style.setProperty('--stop', '0px');
      rows.classList.remove('routed');
      return;
    }
    var stop = q('#ob-recheck .ob-rc-row[data-crit="' + k + '"]');
    if(!stop) return;
    var r = rows.getBoundingClientRect();
    var s = stop.getBoundingClientRect();
    train.style.setProperty('--stop', Math.round(s.top + s.height / 2 - r.top) + 'px');
    rows.classList.add('routed');
    if(quiet || REDUCED) return;
    void rows.offsetWidth;            /* 强制重排：连点同一站也能重新开一次 */
    rows.classList.add('moving');
    rcMoveTimer = setTimeout(function(){ rows.classList.remove('moving'); }, 700);
  }
  function renderCrit(k, quiet){
    var box = q('#ob-rc-detail');
    if(!box) return;
    qa('#ob-recheck .ob-rc-row').forEach(function(r){
      r.classList.toggle('on', k !== null && r.getAttribute('data-crit') === String(k));
    });
    rcDrive(k, quiet);
    if(k === null){
      box.innerHTML = '<div class="ob-rc-detail-empty">' +
        t('点判据，看它按什么规则测') + '</div>';
      return;
    }
    var d = RC_CRIT[k] || RC_CRIT[0];
    box.innerHTML = '<div class="ob-rc-dlist">' +
      '<div class="ob-rc-ditem"><u>' + t('探测方式') + '</u><span>' + d.probe() + '</span></div>' +
      '<div class="ob-rc-ditem"><u>' + t('通过阈值') + '</u><span>' + d.pass() + '</span></div>' +
      '<div class="ob-rc-ditem"><u>' + t('来源') + '</u><span>' + d.from() + '</span></div>' +
      '</div>';
  }

  /* 02 屏：进屏时把图纸"画"一遍。实线按真实长度描边（dashoffset 从 L 到 0），
     虚线 / 链线 / 填充 / 文字只淡入——给虚线再叠一层 dasharray 会把它的段长抹平，
     那几类就不参与描线。顺序＝SVG 里的书写顺序（图框→网格→接触网→车体→
     转向架→钢轨→尺寸→标注），所以看起来就是"先打框、再上主体、最后标注"。
     画完 cancel() 让元素回到 CSS 原样式，不留内联残留；不报完成、不改任何状态。 */
  var bpAnims = [];
  function bpDraw(){
    var svg = q('#ob-recheck .ob-rc-sheet');
    if(!svg || !svg.children) return;
    bpAnims.forEach(function(a){ try{ a.cancel(); }catch(e){} });
    bpAnims = [];
    if(REDUCED) return;
    var t0 = 0;
    Array.prototype.forEach.call(svg.children, function(el){
      var tag = el.tagName;
      if(tag === 'defs') return;
      if(!/^(path|rect|circle|line|polyline|text|use)$/.test(tag)) return;
      var cs = window.getComputedStyle(el);
      var delay = t0;
      var isText = tag === 'text';
      t0 += isText ? 2 : 6;
      if(isText){
        bpAnims.push(el.animate([{ opacity: 0 }, { opacity: 1 }],
          { duration: 300, delay: delay + 620, easing: 'linear', fill: 'backwards' }));
        return;
      }
      /* 实线才描边：虚线（.bp-d/.bp-cl）、pattern 填充块、<use> 弹簧组都只淡入 */
      var solid = cs.strokeDasharray === 'none' && cs.stroke !== 'none' &&
        !el.getAttribute('fill') && tag !== 'use';
      var len = 0;
      if(solid){
        try{ len = el.getTotalLength ? el.getTotalLength() : 0; }catch(e){ len = 0; }
      }
      if(!(len > 0)){
        bpAnims.push(el.animate([{ opacity: 0 }, { opacity: 1 }],
          { duration: 260, delay: delay, easing: 'linear', fill: 'backwards' }));
        return;
      }
      var a = el.animate(
        [{ strokeDasharray: len + ' ' + len, strokeDashoffset: len },
         { strokeDasharray: len + ' ' + len, strokeDashoffset: 0 }],
        { duration: Math.max(150, Math.min(900, len * 3)), delay: delay,
          easing: 'linear', fill: 'backwards' });
      a.onfinish = function(){ try{ a.cancel(); }catch(e){} };
      bpAnims.push(a);
    });
  }

  /* ---------- Swiper ---------- */
  function buildSwiper(){
    if(typeof window.Swiper !== 'function') return null;
    try{
      return new window.Swiper('#ob-swiper', {
        slidesPerView: 1, loop: false, speed: REDUCED ? 0 : 560,
        effect: 'fade', fadeEffect: {crossFade: true},
        allowTouchMove: true,
        keyboard: {enabled: false},
        mousewheel: {enabled: true, forceToAxis: true, releaseOnEdges: true, thresholdDelta: 25},
        pagination: {el: '#ob-dots', clickable: true},
        on: {slideChange: function(s){ onSlide(s.activeIndex); }}
      });
    }catch(e){ return null; }
  }

  function setSlides(list){
    ALL_SLIDES.forEach(function(s){ if(s.parentNode) s.parentNode.removeChild(s); });
    list.forEach(function(s){
      WRAP.appendChild(s);
      /* 场景角上的大号章节号（CSS 用 attr(data-no) 渲染），按屏号补零 */
      var sc = s.querySelector('.ob-scene');
      var n = Number(s.getAttribute('data-idx'));
      if(sc) sc.setAttribute('data-no', pad(n + 1));
    });
  }

  function tourAvailable(){
    return !!(window.driver && window.driver.js && typeof window.driver.js.driver === 'function');
  }

  /* ---------- 开关 ---------- */
  function open(m){
    mode = (m === 'first') ? 'first' : 'replay';
    returnFocus = document.activeElement;
    inited = {};
    clearTimers();
    endType();
    resetChat();
    showConfirm(false);
    if(sw){ try{ sw.destroy(true, true); }catch(e){} sw = null; }
    slides = (mode === 'first') ? FIRST_RUN_SLIDES.map(function(i){ return ALL_SLIDES[i]; }) : ALL_SLIDES.slice();
    setSlides(slides);

    var src = avatarSrc();
    var ba = q('#ob-brand-avatar');
    if(ba && src) ba.src = src;
    var tourBtn = q('#ob-cta-tour');
    if(tourBtn) tourBtn.hidden = !tourAvailable();

    OB.classList.add('show');
    document.body.classList.add('ob-active');
    syncTools();                    /* 主题/语言可能在主界面被改过，开屏先对齐一次 */
    FX.start();
    trCode();                       /* CODE/PRE 里是演示报错，全局翻译器不碰，这里补 */
    sw = buildSwiper();
    cur = 0;
    setProgress();
    enterAnim(slides[0]);
    initDemo(Number(slides[0].getAttribute('data-idx')));
    autoplay(Number(slides[0].getAttribute('data-idx')));

    var skip = q('#ob-skip');
    if(skip && typeof skip.focus === 'function') skip.focus();

    if(mode === 'first' && !REDUCED) playBrand();
    else endBrand();
  }

  function close(){
    endBrand();
    clearTimers();
    endType();
    FX.stop();
    if(sw){ try{ sw.destroy(true, true); }catch(e){} sw = null; }
    OB.classList.remove('show');
    document.body.classList.remove('ob-active');
    if(returnFocus && typeof returnFocus.focus === 'function') returnFocus.focus();
    returnFocus = null;
  }

  function finish(){
    if(mode === 'first') mark();
    close();
  }

  /* ---------- P1-4：4 步高亮教学，只指不点 ---------- */
  var TOUR_STEPS = [
    {element:'#guide',     popover:{title:t('四步导航'), description:t('主流程就是这四步：安装、配置、链端、启动。点任一段可以直接跳过去。'), side:'bottom'}},
    {element:'#pg-detect', popover:{title:t('先看检测'), description:t('第一步是检测本机组件，结果会直接显示在这里。'), side:'bottom'}},
    {element:'#btn-check', popover:{title:t('手动复检'), description:t('装完或改完配置，点这里重新检测。这一步留给你自己点，引导不会代跑。'), side:'top'}},
    {element:'#log-install',popover:{title:t('日志分区'), description:t('安装过程的输出按来源分栏，出错时先来这里看。'), side:'top'}}
  ];

  var EKTour = {
    STEPS: TOUR_STEPS,
    start: function(){
      if(!tourAvailable()) return false;
      var steps = TOUR_STEPS.filter(function(s){ return !!document.querySelector(s.element); });
      if(!steps.length) return false;
      /* 教学高亮的是主界面控件，任何还开着的弹窗都会把浮层顶偏 —— 先收掉 */
      closeModals();
      try{
        var d = window.driver.js.driver({steps: steps, showProgress: true, showButtons: ['next','previous','close'],
          nextBtnText: t('下一步'), prevBtnText: t('上一步'), doneBtnText: t('完成'),
          overlayOpacity: .55, smoothScroll: true});
        d.drive();
        return true;
      }catch(e){ return false; }
    }
  };
  window.EKTour = EKTour;

  document.addEventListener('ek:tour-start', function(){ EKTour.start(); });

  /* 引导开着的时候切语言：正文由 applyI18n 处理，CODE/PRE 只有这里补得上。
     02 的展开区是 JS 拼的，切语言必须按当前语言重新生成，否则会停在旧语言。 */
  document.addEventListener('ek:lang', function(){
    if(OB.classList.contains('show')){ trCode(); renderCrit(rcPin, true); syncTools(); }
  });
  /* 主题改变（无论是这里的按钮还是主界面）→ 粒子颜色跟着换 */
  document.addEventListener('ek:theme', function(){
    if(OB.classList.contains('show')){ syncTools(); FX.theme(); }
  });

  /* ---------- 事件委托（跨 IIFE，一律 document + closest） ---------- */
  document.addEventListener('click', function(e){
    var replay = e.target.closest && e.target.closest('#about-replay');
    if(replay){ open('replay'); return; }
    /* v3.8.1：个人中心里的「新手指引」。全项目原本只有「关于」一个入口，
       想再看一遍得先猜它藏在哪。 */
    var guide = e.target.closest && e.target.closest('#btn-guide');
    if(guide){ open('replay'); return; }

    if(!OB.classList.contains('show')) return;
    if(brandArmed && OB.contains(e.target)){ endBrand(); return; }

    /* 02 站台：点站台让车头进站并展开依据，再点一次收起回车库。
       必须在通用 button 分支之前 return，否则空 id 会掉进 switch 里空转。 */
    var crit = e.target.closest ? e.target.closest('#ob-recheck .ob-rc-crit') : null;
    if(crit){
      var ck = Number(crit.getAttribute('data-crit'));
      rcPin = (rcPin === ck) ? null : ck;
      renderCrit(rcPin);
      return;
    }

    var b = e.target.closest ? e.target.closest('button') : null;
    if(!b || !OB.contains(b)) return;
    var id = b.id;
    var jump = b.getAttribute('data-jump');

    /* 顶栏两个开关：直接调主界面的函数，关掉引导后软件本身也已经是切换后的状态 */
    if(id === 'ob-lang'){
      try{ ekSetLang(isEN() ? 'zh' : 'en', true); }catch(err){}
      syncTools();
      return;
    }
    if(id === 'ob-theme'){
      try{ applyTheme(isDark() ? 'light' : 'dark'); }catch(err){}
      syncTools();
      FX.theme();
      return;
    }

    if(jump !== null){ if(sw) sw.slideTo(Number(jump)); return; }

    if(id === 'ob-skip' || id === 'ob-hero-skip' || id === 'ob-cta-later'){ finish(); return; }
    if(id === 'ob-hero-next' || id === 'ob-next'){ if(sw) sw.slideNext(); else finish(); return; }
    if(id === 'ob-prev'){ if(sw) sw.slidePrev(); return; }
    if(id === 'ob-cta-install'){
      finish();
      try{ goto('install'); }catch(err){}
      return;
    }
    if(id === 'ob-cta-tour'){
      finish();
      /* 从「关于」里重看再点这个按钮时，#about 还开着。driver.js 会把浮层画在
         遮罩之上、却按底下那些控件的坐标定位 —— 于是浮层指着一个被弹窗盖住的
         位置，看起来就是"导航歪了"。先关掉所有弹窗，再开始教学。 */
      closeModals();
      document.dispatchEvent(new CustomEvent('ek:tour-start', {detail:{source:'onboard-finish'}}));
      return;
    }
    var pact = b.getAttribute('data-pact');
    if(pact !== null){ petAct(pact); return; }

    var step = b.getAttribute('data-step');
    if(step !== null){ showStep(Number(step)); return; }

    if(id === 'ob-detect-run'){ runDetect(); return; }

    var sc = b.getAttribute('data-scope');
    if(sc !== null){ renderScope(sc); return; }
    if(id === 'ob-log-insert'){
      var box = q('#ob-log-lines');
      if(box){
        var d = document.createElement('div');
        d.className = 'err flash';
        d.textContent = t('[ERROR] 演示异常：端口 8888 被占用（这条是演示数据）');
        box.appendChild(d);
        box.scrollTop = box.scrollHeight;
        var rec = q('#ob-recent-err');
        if(rec){ rec.classList.add('hot'); var sp = rec.querySelector('span'); if(sp) sp.textContent = t('端口 8888 被占用'); }
      }
      return;
    }

    var question = b.getAttribute('data-q');
    if(question !== null){ askAI(question); return; }
    if(id === 'ob-demo-ok' || id === 'ob-demo-cancel'){
      showConfirm(false);
      endType();
      var bb = q('#ob-bubble');
      if(id === 'ob-demo-ok'){
        /* 点了"知道了"才算这一轮收尾：桌宠的 ✓ 跟回答同时出现，
           而不是在用户还没点的时候就先撒花 */
        if(bb) typeText(bb, t(AI_AFTER), 16, function(){
          bb.setAttribute('data-live', '1');
          petState('done');
        });
      } else {
        petState('idle');
        if(bb) typeText(bb, t('那就不动，随时可以改主意。'), 16, function(){
          bb.setAttribute('data-live', '1');
        });
      }
      return;
    }

    var perm = b.getAttribute('data-perm');
    if(perm !== null){ showPerm(Number(perm)); return; }
  });

  /* 片头期间任意键只结束片头；之后 ESC 关、左右键翻页、Tab 循环 */
  function focusables(){
    return qa('button').filter(function(b){
      return !b.disabled && !b.hidden && b.offsetParent !== null;
    });
  }
  function trap(e){
    var list = focusables();
    if(!list.length) return;
    var first = list[0], last = list[list.length - 1];
    if(e.shiftKey && document.activeElement === first){ e.preventDefault(); last.focus(); }
    else if(!e.shiftKey && document.activeElement === last){ e.preventDefault(); first.focus(); }
  }
  document.addEventListener('keydown', function(e){
    if(!OB.classList.contains('show')) return;
    if(brandArmed){ e.preventDefault(); endBrand(); return; }
    if(e.key === 'Escape'){ e.preventDefault(); finish(); return; }
    if(e.key === 'ArrowLeft'){ e.preventDefault(); if(sw) sw.slidePrev(); return; }
    if(e.key === 'ArrowRight'){ e.preventDefault(); if(sw) sw.slideNext(); return; }
    if(e.key === 'Tab'){ trap(e); }
  });

  /* ---------- 对外接口 ---------- */
  window.EKOnboard = {
    open: open,
    close: close,
    FIRST_RUN_SLIDES: FIRST_RUN_SLIDES,
    hasSeen: seen,
    reset: function(){ try{ localStorage.removeItem(KEY); }catch(e){} },
    /* 真实等待场景的头像状态体（与 00-core.js EKAvatarWait 同一套实现，
       避免「加了个没样式的 class」这种空转）：有等待才显示，且必须能结束。 */
    wait: function(host, text){
      return (typeof window.EKAvatarWait === 'function')
        ? window.EKAvatarWait(host, text) : function(){};
    }
  };
})();
