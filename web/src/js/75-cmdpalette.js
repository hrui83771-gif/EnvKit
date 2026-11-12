/* ---------- Ctrl+K 命令面板：只索引真实存在的控件 ----------
   索引不是手写清单，是打开面板时现扫 DOM：主导航、带 id 的按钮、各面板的
   章节标题、以及 EKTour 的 11 步导览。因此条目数量会随界面真实变化，
   「搜到却点不到」在结构上不可能出现。

   执行动作只有两种：goto(panel) / el.click()。点的是页面上本来就在的控件，
   破坏性操作照样弹 postConfirm、照样吃服务端 428 —— 命令面板不是新的权限入口，
   也没有任何「按名字执行任意命令」的能力。AI 工具清单刻意不进索引。 */
(function(){
  var box = null, input = null, list = null, countEl = null;
  var all = [], filtered = [], sel = 0, keySource = 'none';

  function ic(n, s){ return typeof ekIcon === 'function' ? ekIcon(n, s) : ''; }
  function txt(el){ return (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 60); }
  function panelOf(el){
    var p = el.closest ? el.closest('.panel') : null;
    return p && /^panel-/.test(p.id) ? p.id.slice(6) : '';
  }
  function excluded(el){
    if(el.closest('#onboard') || el.closest('#ek-cmd') || el.closest('#ek-cfgdiff')) return true;
    if(el.hasAttribute && el.hasAttribute('data-food')) return true;   // 桌宠投喂不是功能控件
    return false;
  }

  function collect(){
    var out = [];
    document.querySelectorAll('.nav[data-panel]').forEach(function(el){
      var s = txt(el); if(!s) return;
      var pn = el.getAttribute('data-panel');
      out.push({group:'page', glabel:t('页面'), text:s, hint:pn,
        sel:'.nav[data-panel="' + pn + '"]',
        icon:'chevron-right', act:function(){ goto(pn); }});
    });
    document.querySelectorAll('button[id]').forEach(function(el){
      if(excluded(el) || el.disabled) return;
      var s = txt(el);
      if(!s || !/[\u4e00-\u9fa5A-Za-z0-9]/.test(s)) return;
      var pn = panelOf(el);
      out.push({group:'act', glabel:t('操作'), text:s,
        hint:pn ? t('所在页面：{0}', pn) : el.id,
        sel:'#' + el.id,
        icon:'corner-down-left', act:function(){ if(pn) goto(pn); el.click(); }});
    });
    document.querySelectorAll('.panel h2, .panel h3').forEach(function(el){
      var s = txt(el), pn = panelOf(el);
      if(!s || !pn || el.closest('#onboard')) return;
      out.push({group:'sec', glabel:t('章节'), text:s, hint:t('所在页面：{0}', pn),
        icon:'list', act:function(){ goto(pn); el.scrollIntoView({block:'center', behavior:'auto'}); }});
    });
    if(window.EKTour && Array.isArray(window.EKTour.STEPS)){
      out.push({group:'tour', glabel:t('导览'),
        text:t('跟着做一遍：{0} 步控件教学', window.EKTour.STEPS.length),
        hint:'EKTour', icon:'command',
        act:function(){ if(window.EKTour.start) window.EKTour.start(); }});
    }
    return out;
  }

  function build(){
    box = document.createElement('div');
    box.className = 'modal';
    box.id = 'ek-cmd';
    box.innerHTML =
      '<div class="mbox ekcp-box">' +
        '<div class="ekcp-search">' + ic('search', 18) +
          '<input id="ekcp-q" type="text" autocomplete="off" spellcheck="false" placeholder="' +
            esc(t('搜索页面、按钮、章节…')) + '" />' +
          '<kbd>Ctrl K</kbd>' +
        '</div>' +
        '<div class="ekcp-list" id="ekcp-list"></div>' +
        '<div class="mfoot ekcp-foot">' +
          '<span class="hint" id="ekcp-count"></span>' +
          '<span class="ekcp-keys">' + ic('corner-down-left', 14) + esc(t('打开')) +
            ' · ↑↓ ' + esc(t('选择')) + ' · Esc ' + esc(t('关闭')) + '</span>' +
        '</div>' +
      '</div>';
    document.body.appendChild(box);
    input = document.getElementById('ekcp-q');
    list = document.getElementById('ekcp-list');
    countEl = document.getElementById('ekcp-count');

    input.addEventListener('input', function(){ sel = 0; render(); });
    input.addEventListener('keydown', function(ev){
      if(ev.key === 'ArrowDown'){ ev.preventDefault(); sel = Math.min(sel + 1, filtered.length - 1); render(); }
      else if(ev.key === 'ArrowUp'){ ev.preventDefault(); sel = Math.max(sel - 1, 0); render(); }
      else if(ev.key === 'Enter'){ ev.preventDefault(); runIndex(sel); }
    });
    list.addEventListener('click', function(ev){
      var row = ev.target.closest && ev.target.closest('[data-i]');
      if(row) runIndex(Number(row.getAttribute('data-i')));
    });
    box.addEventListener('click', function(ev){ if(ev.target === box) close(); });
  }

  function render(){
    var q = (input.value || '').trim().toLowerCase();
    filtered = all.filter(function(it){
      if(!q) return true;
      return (it.text + ' ' + it.hint + ' ' + it.glabel).toLowerCase().indexOf(q) >= 0;
    }).slice(0, 60);
    if(sel >= filtered.length) sel = Math.max(0, filtered.length - 1);
    var html = '', lastGroup = '';
    filtered.forEach(function(it, i){
      if(it.group !== lastGroup){
        html += '<div class="ekcp-grp">' + esc(it.glabel) + '</div>';
        lastGroup = it.group;
      }
      html += '<div class="ekcp-row' + (i === sel ? ' on' : '') + '" data-i="' + i + '">' +
        '<span class="ekcp-ic">' + ic(it.icon, 16) + '</span>' +
        '<span class="ekcp-tx"><b>' + esc(it.text) + '</b><small>' + esc(it.hint) + '</small></span>' +
        '<span class="ekcp-go">' + ic('arrow-right', 14) + '</span></div>';
    });
    if(!filtered.length) html = '<div class="ekcp-empty">' + esc(t('没有匹配的控件')) + '</div>';
    list.innerHTML = html;
    countEl.textContent = t('共 {0} 项可跳转的控件', all.length);
    var on = list.querySelector('.ekcp-row.on');
    if(on && on.scrollIntoView) on.scrollIntoView({block:'nearest'});
  }

  function open(){
    if(!box) build();
    all = collect();
    filtered = all.slice();
    sel = 0;
    input.value = '';
    render();
    box.classList.add('show');
    setTimeout(function(){ try{ input.focus(); }catch(e){} }, 0);
  }
  function close(){ if(box) box.classList.remove('show'); }
  function toggle(){ if(box && box.classList.contains('show')) close(); else open(); }

  function runIndex(i){
    var it = filtered[i];
    if(!it) return;
    close();
    // 先收面板再执行：否则目标控件可能被自己的遮罩挡住（真实点不到）
    setTimeout(function(){ try{ it.act(); }catch(e){} }, 0);
  }

  function bindKeys(){
    if(!window.hotkeys) return false;
    // hotkeys-js 默认在输入框里不触发任何键；这里只放行带 Ctrl/Cmd 的组合键，
    // 保证在搜索框里按 Ctrl+K 也能重新聚焦，同时不抢普通输入。
    window.hotkeys.filter = function(ev){
      var el = ev.target || ev.srcElement, tag = el && el.tagName;
      if(tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (el && el.isContentEditable))
        return !!(ev.ctrlKey || ev.metaKey);
      return true;
    };
    window.hotkeys('ctrl+k, command+k', function(ev){
      ev.preventDefault(); toggle(); return false;
    });
    keySource = 'hotkeys';
    return true;
  }

  if(!bindKeys()){
    // 库没加载时保留最基本的可用性；CDP 会单独断言全局 hotkeys 必须存在，
    // 所以这条降级不会把「vendor 没嵌进 exe」掩盖过去。
    keySource = 'fallback';
    document.addEventListener('keydown', function(ev){
      if((ev.ctrlKey || ev.metaKey) && String(ev.key).toLowerCase() === 'k'){
        ev.preventDefault(); toggle();
      }
    });
  }

  window.EKCmd = {
    open: open, close: close, toggle: toggle, runIndex: runIndex,
    items: function(){ return all.slice(); },
    shown: function(){ return filtered.map(function(it){ return {text:it.text, group:it.group, hint:it.hint}; }); },
    collect: collect,
    state: function(){ return {
      open: !!(box && box.classList.contains('show')),
      total: all.length, shown: filtered.length, selected: sel,
      query: input ? input.value : '', keySource: keySource
    }; }
  };
})();
