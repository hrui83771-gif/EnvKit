/* ---------- 列表真实增删重排的 FLIP 过渡（auto-animate 0.9.0 / MIT） ----------
   只挂到「数据真的会增删」的容器：AI 对话消息逐条追加、配置备份列表重排、
   检测/安装结果卡片刷新。装饰层、静态列表、桌宠一律不挂 —— 挂了也不动，
   等于给界面加一层无意义的假动画。

   两个必须显式处理的前提：
   1) 上游只发 ESM，页面里那份是本项目包过 IIFE 的版本，全局名 window.autoAnimate；
      没加载成功就整段跳过，页面照常用（不能因为少个库就报错白屏）。
   2) 「减少动态效果」开启时不挂。库自己也会尊重，但这里再加一道，
      和引导页描线动画用同一口径。 */
(function(){
  var IDS = ['ai-msgs', 'cfg_backups', 'detect-cards', 'install-cards'];

  function reduced(){
    try{ return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches); }
    catch(e){ return false; }
  }

  function init(){
    /* 先把模板里的 data-ek-icon 占位换成 SVG（05-icons.js）。
       只换一次：ekIconFill 换完就删属性，重复调用不会重复插节点。 */
    if(typeof ekIconFill === 'function') ekIconFill(document);
    if(!window.autoAnimate || reduced()) return 0;
    var n = 0;
    IDS.forEach(function(id){
      var el = document.getElementById(id);
      if(!el || el.dataset.aa === '1') return;
      try{
        window.autoAnimate(el, {duration:220, easing:'ease-in-out'});
        el.dataset.aa = '1';
        n++;
      }catch(e){ /* 单个容器失败不影响其余 */ }
    });
    return n;
  }

  window.ekMotionInit = init;
  window.ekMotionTargets = IDS.slice();
  if(document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
