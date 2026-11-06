/* ---------- 首次引导 / 产品导览 + P1-4 操作教学（v3） ----------
   设计：docs/DESIGN-onboarding-2026-11-06.md
   红线：全部演示数据是内置静态常量，**不 fetch 任何 /api/\*、不写配置、
   不触发真实确认卡/权限逻辑、不执行任何命令**。P1-4 只高亮，不代点不代跑检测。
   依赖：Swiper 12（翻页）、Anime.js v4 UMD（动画）、driver.js（控件高亮），
   三者都从同域 /vendor/ 加载；任一缺失都降级成「静态但可用」，不白屏。
   注意：本文件按 90- 前缀最后拼入，事件一律委托 document + closest()。 */

(function(){
  'use strict';

  var OB = document.getElementById('onboard');
  if(!OB) return;

  var KEY = 'ek-guide-v1';
  var FIRST_RUN_SLIDES = [0, 1, 2, 5, 7];
  var BRAND_MS = 1200;
  var TOTAL = 8;
  var REDUCED = !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  var AM = window.anime || null;

  function q(sel){ return OB.querySelector(sel); }
  function qa(sel){ return Array.prototype.slice.call(OB.querySelectorAll(sel)); }
  function has(o, k){ return !!(o && typeof o[k] === 'function'); }

  /* ---------- 静态演示数据（不来自接口，不写回任何地方） ---------- */
  var STEP_TEXT = [
    {b:'环境安装', p:'检测 Go / Node.js / MySQL 的版本和安装位置，缺什么补什么，国内镜像下载会校验 SHA256。'},
    {b:'程序配置', p:'填数据库、建库建表、导入导出配置；连不上会直接告诉你是哪一项不通。'},
    {b:'链端校验', p:'检查 FISCO-BCOS 节点与 WeBASE-Front 是否在、区块高度和客户端版本是否正常。'},
    {b:'程序启动', p:'先后端再前端一键拉起，日志按来源分栏，进程树由 EnvKit 统一回收。'}
  ];
  var DETECT = [
    {n:'Go',        v:'1.27.3',    k:'ok',   s:'正常'},
    {n:'Node.js',   v:'22.22.2',   k:'ok',   s:'正常'},
    {n:'MySQL 8',   v:'8.0.36',    k:'ok',   s:'正常'},
    {n:'GOPROXY',   v:'goproxy.cn',k:'ok',   s:'正常'},
    {n:'FISCO 节点', v:'20200',    k:'warn', s:'待校验'},
    {n:'磁盘剩余',   v:'18.4 GB',  k:'ok',   s:'正常'}
  ];
  var SCOPES = {
    install:['[INFO] 检测 Go 1.27.3 ... 正常','[INFO] 检测 Node.js 22.22.2 ... 正常','[INFO] GOPROXY 已指向国内镜像'],
    config: ['[INFO] 读取 config.json','[INFO] 数据库连接测试通过','[INFO] 建表脚本已就绪'],
    chain:  ['[INFO] 节点 20200 可达','[INFO] WeBASE-Front 5002 可达','[WARN] 区块高度为 0，可能是新链'],
    start:  ['[INFO] 后端监听 :8888','[INFO] 前端监听 :5173','[INFO] 进程树已登记'],
    sys:    ['[INFO] 日志按日轮转','[INFO] 配置原子写入完成','[INFO] 单实例锁已获取']
  };
  var PERMS = [
    {b:'只读', p:'能看状态、日志、检测报告、轨迹与审计；不需要确认，永远不会改动任何东西。'},
    {b:'诊断', p:'在只读基础上能跑检测、读数据库结构与抽样数据；不需要确认，仍不写入。'},
    {b:'受限写', p:'能改配置、执行备份复验等可逆操作；需要一次确认，且只能走白名单里的固定动作。'},
    {b:'审批后动作', p:'启停服务、链端操作等；破坏性操作走 428 + 红色确认框，不会执行任意命令。'}
  ];
  var ANSWERS = {
    read:'服务状态：后端已运行、前端已运行、链端校验 2/2 通过。（演示数据）',
    write:'「重启后端」属于写操作，会先弹一张确认卡问过你才动 —— 这就是演示的那张卡。'
  };
  var CHAPTERS = ['开场','为什么存在','四步走','状态','日志','AI 与桌宠','安全边界','收尾'];

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
  var scope = 'install';
  var stepIdx = 0;
  var permIdx = 0;

  function seen(){ try{ return localStorage.getItem(KEY) === '1'; }catch(e){ return false; } }
  function mark(){ try{ localStorage.setItem(KEY, '1'); }catch(e){} }

  function avatarSrc(){
    var g = document.getElementById('app-logo');
    if(g && g.src) return g.src;
    try{ return typeof APP_ICON !== 'undefined' ? APP_ICON : ''; }catch(e){ return ''; }
  }
  function paintAvatars(){
    var src = avatarSrc();
    ['ob-brand-avatar','ob-guide-avatar','ob-hero-avatar','ob-pet-avatar','ob-end-avatar'].forEach(function(id){
      var e = document.getElementById(id);
      if(e && src) e.src = src;
    });
  }

  /* Anime.js v4 的 API 与 v3 不同（animate / stagger / y，不是 anime()/translateY）。
     全部包 try/catch：库缺失或 API 变体不匹配时，页面仍然是完整可读的静态导览。 */
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

  /* ---------- 品牌片头（1.2 秒，只在首次自动播时出现） ---------- */
  var BRAND_SELS = '.ob-brand-glow,.ob-brand-ava,.ob-brand-word,.ob-brand-tag,.ob-brand-ver,.ob-brand-inner';

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
    fx(q('.ob-brand-glow'),  {opacity:[0,.28], scale:[.6,1.15], duration:520});
    fx(q('.ob-brand-ava'),   {opacity:[0,1], scale:[.82,1], duration:400, delay:120});
    fx(q('.ob-brand-word'),  {opacity:[0,1], y:[10,0], duration:380, delay:460});
    fx(q('.ob-brand-tag'),   {opacity:[0,1], y:[8,0],  duration:340, delay:550});
    fx(q('.ob-brand-ver'),   {opacity:[0,1], duration:300, delay:640});
    fx(q('.ob-brand-inner'), {opacity:[1,0], y:[0,-12], duration:230, delay:940});
    brandTimer = setTimeout(endBrand, BRAND_MS);
  }

  /* ---------- 每屏入场 / 进度 / 播报 ---------- */
  function enterAnim(slide){
    if(REDUCED || !slide) return;
    var els = slide.querySelectorAll('.ob-kicker,.ob-h,.ob-lead,.ob-stage,.ob-actions,.ob-end-note');
    if(!els.length) return;
    fx(els, {opacity:[0,1], y:[14,0], delay:stagger(70), duration:470});
  }

  function setProgress(){
    var arc = q('#ob-guide-arc');
    if(arc){
      var n = slides.length || 1;
      arc.style.strokeDashoffset = String(131.95 * (1 - (cur + 1) / n));
    }
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
    var s = slides[i];
    setProgress();
    enterAnim(s);
    if(s) initDemo(Number(s.getAttribute('data-idx')));
  }

  /* ---------- 8 屏静态演示（纯 DOM，不碰接口） ---------- */
  function initDemo(idx){
    if(inited[idx]) return;
    inited[idx] = true;
    if(idx === 2) showStep(0);
    if(idx === 3) renderDetect(true);
    if(idx === 4) renderScope('install');
    if(idx === 6) showPerm(0);
    if(idx === 7) renderChapters();
    if(idx === 1) setTimeout(function(){ mergeFlow(); }, REDUCED ? 0 : 420);
  }

  function showStep(i){
    stepIdx = i;
    qa('#ob-track .ob-step').forEach(function(b){
      b.classList.toggle('on', Number(b.getAttribute('data-step')) === i);
    });
    var d = q('#ob-step-desc');
    if(d) d.innerHTML = '<b>' + t(STEP_TEXT[i].b) + '</b><p>' + t(STEP_TEXT[i].p) + '</p>';
  }

  function renderDetect(blank){
    var box = q('#ob-detect-rows');
    if(!box) return;
    box.innerHTML = '';
    DETECT.forEach(function(r){
      var d = document.createElement('div');
      d.className = 'ob-row' + (blank ? '' : ' lit ' + r.k);
      d.innerHTML = '<i class="dot"></i><span class="nm">' + r.n + '</span>' +
                    '<span class="vl">' + r.v + '</span><span class="st">' + t(r.s) + '</span>';
      box.appendChild(d);
    });
    var badge = q('#ob-detect-badge');
    if(badge){ badge.classList.remove('on'); badge.textContent = ''; }
  }

  function renderScope(k){
    scope = k;
    qa('#ob-scopes button').forEach(function(b){
      b.classList.toggle('on', b.getAttribute('data-scope') === k);
    });
    var box = q('#ob-log-lines');
    if(!box) return;
    box.innerHTML = '';
    (SCOPES[k] || []).forEach(function(line){
      var d = document.createElement('div');
      if(line.indexOf('[WARN]') === 0 || line.indexOf('[ERROR]') === 0) d.className = 'err';
      else if(line.indexOf('正常') >= 0 || line.indexOf('通过') >= 0) d.className = 'ok';
      d.textContent = line;
      box.appendChild(d);
    });
  }

  function renderChapters(){
    var g = q('#ob-chapter-grid');
    if(!g) return;
    g.innerHTML = '';
    slides.forEach(function(s, i){
      var n = Number(s.getAttribute('data-idx'));
      var b = document.createElement('button');
      b.setAttribute('data-idx', String(i));
      b.setAttribute('data-jump', String(i));
      b.textContent = String(n + 1);
      b.title = t(CHAPTERS[n]);
      g.appendChild(b);
    });
    setProgress();
  }

  function mergeFlow(){
    var beam = q('#ob-beam') || q('.ob-beam');
    var card = q('.ob-new-card');
    if(beam){ beam.classList.remove('flow'); void beam.offsetWidth; beam.classList.add('flow'); }
    if(card){ card.classList.remove('lit'); setTimeout(function(){ card.classList.add('lit'); }, REDUCED ? 0 : 420); }
  }

  function showPerm(i){
    permIdx = i;
    qa('#ob-perms button').forEach(function(b){
      b.classList.toggle('on', Number(b.getAttribute('data-perm')) === i);
    });
    var d = q('#ob-perm-desc');
    if(d) d.innerHTML = '<b>' + t(PERMS[i].b) + '</b> · ' + t(PERMS[i].p);
  }

  /* ---------- 开关 ---------- */
  function buildSwiper(){
    if(typeof window.Swiper !== 'function') return null;
    try{
      return new window.Swiper('#ob-swiper', {
        slidesPerView: 1, loop: false, speed: REDUCED ? 0 : 520,
        effect: 'fade', fadeEffect: {crossFade: true},
        allowTouchMove: true,
        keyboard: {enabled: false},                 /* 键盘统一由下面自己管，片头期间也要拦 */
        mousewheel: {enabled: true, forceToAxis: true, releaseOnEdges: true, thresholdDelta: 25},
        pagination: {el: '#ob-dots', clickable: true},
        on: {slideChange: function(s){ onSlide(s.activeIndex); }}
      });
    }catch(e){ return null; }
  }

  function setSlides(list){
    ALL_SLIDES.forEach(function(s){ if(s.parentNode) s.parentNode.removeChild(s); });
    list.forEach(function(s){ WRAP.appendChild(s); });
  }

  function tourAvailable(){
    return !!(window.driver && window.driver.js && typeof window.driver.js.driver === 'function');
  }

  function open(m){
    mode = (m === 'first') ? 'first' : 'replay';
    returnFocus = document.activeElement;
    inited = {};
    if(sw){ try{ sw.destroy(true, true); }catch(e){} sw = null; }
    slides = (mode === 'first') ? FIRST_RUN_SLIDES.map(function(i){ return ALL_SLIDES[i]; }) : ALL_SLIDES.slice();
    setSlides(slides);

    paintAvatars();
    var tourBtn = q('#ob-cta-tour');
    if(tourBtn) tourBtn.hidden = !tourAvailable();

    OB.classList.add('show');
    document.body.classList.add('ob-active');
    /* Swiper 必须在容器可见之后 init，否则算出来高度是 0 */
    sw = buildSwiper();
    cur = 0;
    setProgress();
    enterAnim(slides[0]);
    initDemo(Number(slides[0].getAttribute('data-idx')));

    var skip = q('#ob-skip');
    if(skip && typeof skip.focus === 'function') skip.focus();

    if(mode === 'first' && !REDUCED) playBrand();
    else endBrand();
  }

  function close(){
    endBrand();
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
    {element:'#guide',       popover:{title:t('四步导航'), description:t('主流程就是这四步：安装、配置、链端、启动。点任一段可以直接跳过去。'), side:'bottom'}},
    {element:'#pg-detect',   popover:{title:t('先看检测'), description:t('第一步是检测本机组件，结果会直接显示在这里。'), side:'bottom'}},
    {element:'#btn-check',   popover:{title:t('手动复检'), description:t('装完或改完配置，点这里重新检测。这一步留给你自己点，引导不会代跑。'), side:'top'}},
    {element:'#log-install', popover:{title:t('日志分区'), description:t('安装过程的输出按来源分栏，出错时先来这里看。'), side:'top'}}
  ];

  var EKTour = {
    STEPS: TOUR_STEPS,
    start: function(){
      if(!tourAvailable()) return false;
      var steps = TOUR_STEPS.filter(function(s){ return !!document.querySelector(s.element); });
      if(!steps.length) return false;
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

  /* ---------- 事件委托（跨 IIFE，一律挂 document + closest） ---------- */
  document.addEventListener('click', function(e){
    var replay = e.target.closest && e.target.closest('#about-replay');
    if(replay){ open('replay'); return; }

    if(!OB.classList.contains('show')) return;
    if(brandArmed && OB.contains(e.target)){ endBrand(); return; }

    var b = e.target.closest ? e.target.closest('button') : null;
    if(!b || !OB.contains(b)) return;
    var id = b.id;
    var jump = b.getAttribute('data-jump');

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
      document.dispatchEvent(new CustomEvent('ek:tour-start', {detail:{source:'onboard-finish'}}));
      return;
    }
    if(id === 'ob-merge-replay'){ mergeFlow(); return; }

    if(id === 'ob-track-run'){
      qa('#ob-track .ob-step').forEach(function(s, i){
        setTimeout(function(){ s.classList.add('done'); }, REDUCED ? 0 : i * 220);
      });
      return;
    }
    var step = b.getAttribute('data-step');
    if(step !== null){ showStep(Number(step)); return; }

    if(id === 'ob-detect-run'){
      renderDetect(true);
      qa('#ob-detect-rows .ob-row').forEach(function(row, i){
        setTimeout(function(){
          var r = DETECT[i];
          row.classList.add('lit', r.k);
        }, REDUCED ? 0 : 160 + i * 190);
      });
      var badge = q('#ob-detect-badge');
      setTimeout(function(){ if(badge){ badge.textContent = t('5 项正常 · 1 项待校验'); badge.classList.add('on'); } },
        REDUCED ? 0 : 160 + DETECT.length * 190);
      return;
    }

    var sc = b.getAttribute('data-scope');
    if(sc !== null){ renderScope(sc); return; }
    if(id === 'ob-log-insert'){
      var box = q('#ob-log-lines');
      if(box){
        var d = document.createElement('div');
        d.className = 'err';
        d.textContent = t('[ERROR] 演示异常：端口 8888 被占用（这条是演示数据）');
        box.appendChild(d);
        box.scrollTop = box.scrollHeight;
        var rec = q('#ob-recent-err');
        if(rec){ rec.classList.add('hot'); var sp = rec.querySelector('span'); if(sp) sp.textContent = t('端口 8888 被占用'); }
      }
      return;
    }

    var question = b.getAttribute('data-q');
    if(question !== null){
      var bub = q('#ob-bubble');
      if(bub) bub.textContent = t(ANSWERS[question]);
      var cf = q('#ob-demo-confirm');
      if(cf) cf.hidden = (question !== 'write');
      var pet = q('#ob-pet');
      if(pet){ pet.classList.add('blink'); setTimeout(function(){ pet.classList.remove('blink'); }, 180); }
      return;
    }
    if(id === 'ob-demo-ok' || id === 'ob-demo-cancel'){
      var c = q('#ob-demo-confirm');
      if(c) c.hidden = true;
      if(id === 'ob-demo-ok'){
        var bb = q('#ob-bubble');
        if(bb) bb.textContent = t('演示结束：真实操作仍会走原来的权限闸门，这里不会执行任何动作。');
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
