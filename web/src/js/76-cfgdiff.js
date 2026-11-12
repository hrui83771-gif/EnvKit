/* ---------- 配置导入前：把「到底改了哪几行」摆出来 ----------
   数据来源只有两处，都在浏览器本地、都是真的：
     旧 = GET /api/config/export 拿到的当前 config.json 原文；
     新 = 用户刚选中、已通过 JSON.parse 的导入文件原文。
   两边先递归按键排序再比较，这样缩进和键顺序不会制造假差异 ——
   「把对判成错」比不显示差异更糟。

   拿不到旧值就如实跳过预览、走原来的导入流程，绝不拿 cfg 运行时对象
   或任何猜出来的内容凑一份"旧配置"。后端不新增任何返回配置全文的端点。
   diff2html 只吃 unified patch 文本，输出进 innerHTML 前由上游模板转义；
   CDP 门禁另外用带 <script> 的配置值复验一次，确保没有活的元素被造出来。 */
function ekCfgSort(v){
  if(Array.isArray(v)) return v.map(ekCfgSort);
  if(v && typeof v === 'object'){
    var o = {};
    Object.keys(v).sort().forEach(function(k){ o[k] = ekCfgSort(v[k]); });
    return o;
  }
  return v;
}
function ekCfgNorm(text){ return JSON.stringify(ekCfgSort(JSON.parse(text)), null, 2); }

function ekCfgDiffHTML(oldText, newText){
  var a = ekCfgNorm(oldText), b = ekCfgNorm(newText);
  if(a === b) return {same:true, changed:0, patch:'', html:''};
  var patch = window.Diff.createTwoFilesPatch('config.json', 'config.json', a, b, '', '', {context:3});
  // 变更行数从 patch 本身数，不数渲染后的 class —— 客观事实优先于措辞
  var changed = patch.split('\n').filter(function(l){
    return /^[+-]/.test(l) && !/^(\+\+\+|---)/.test(l);
  }).length;
  var html = window.Diff2Html.html(patch, {
    outputFormat:'side-by-side', drawFileList:false, matching:'lines'
  });
  return {same:false, changed:changed, patch:patch, html:html};
}
window.ekCfgDiffHTML = ekCfgDiffHTML;

var ekdfBox = null;
function ekdfEnsureBox(){
  if(ekdfBox) return ekdfBox;
  ekdfBox = document.createElement('div');
  ekdfBox.className = 'modal';
  ekdfBox.id = 'ek-cfgdiff';
  ekdfBox.innerHTML =
    '<div class="mbox ekdf-box">' +
      '<div class="mhead">' + esc(t('配置导入前 · 逐行差异')) + '</div>' +
      '<div class="ekdf-sub" id="ekdf-sub"></div>' +
      '<div class="ekdf-body" id="ekdf-body"></div>' +
      '<div class="mfoot"><span class="hint" id="ekdf-n"></span>' +
        '<button class="ghost" id="ekdf-cancel">' + esc(t('取消')) + '</button>' +
        '<button id="ekdf-ok">' + esc(t('确认导入')) + '</button>' +
      '</div>' +
    '</div>';
  document.body.appendChild(ekdfBox);
  return ekdfBox;
}
/* 切语言后按新语言重建，别让弹窗停在旧语言 */
document.addEventListener('ek:lang', function(){
  if(ekdfBox){ ekdfBox.remove(); ekdfBox = null; }
});

/* 返回 Promise<boolean>：true = 继续导入，false = 用户取消。
   ESC 由 20-shell.js 的统一处理关掉 .modal.show，这里用 MutationObserver
   兜住那条路径 —— 否则 Promise 永远不 resolve，await 会卡死。 */
function ekCfgConfirmImport(text, fileName){
  return new Promise(function(resolve){
    if(!window.Diff || !window.Diff2Html){ resolve(true); return; }
    var settled = false, mo = null;

    function done(v){
      if(settled) return;
      settled = true;
      if(mo) mo.disconnect();
      if(ekdfBox) ekdfBox.classList.remove('show');
      resolve(v);
    }

    function show(oldText){
      var d;
      try{ d = ekCfgDiffHTML(oldText, text); }
      catch(e){ done(true); return; }   // 比较失败：按原流程，不编造差异

      var box = ekdfEnsureBox();
      box.querySelector('#ekdf-sub').textContent =
        t('当前配置 → 待导入文件：{0}', fileName) + ' · ' +
        t('按键排序后比较，只显示真实变更的字段');
      box.querySelector('#ekdf-body').innerHTML = d.same
        ? '<div class="ekdf-same">' + esc(t('两份配置内容相同，没有需要变更的字段')) + '</div>'
        : d.html;
      box.querySelector('#ekdf-n').textContent = d.same ? '' : t('共 {0} 处差异', d.changed);
      box.querySelector('#ekdf-cancel').onclick = function(){ done(false); };
      box.querySelector('#ekdf-ok').onclick = function(){ done(true); };
      box.onclick = function(ev){ if(ev.target === box) done(false); };

      mo = new MutationObserver(function(){
        if(ekdfBox && !ekdfBox.classList.contains('show')) done(false);
      });
      mo.observe(box, {attributes:true, attributeFilter:['class']});
      box.classList.add('show');
    }

    fetch('/api/config/export?t=' + encodeURIComponent(window.__EK_TOKEN__))
      .then(function(r){ if(!r.ok) throw new Error('HTTP ' + r.status); return r.text(); })
      .then(show)
      .catch(function(){
        if(typeof toast === 'function') toast(t('读取当前配置失败，已跳过差异预览'), 'err');
        done(true);
      });
  });
}
