/* ---------- 自愈态势 + 影子晋升（2026-11-16，功能 ①②） ----------
   数据源只有 /api/guard/status 的 timeline / shadow 字段，全部是追加式审计
   与 shadow-*.jsonl 的真实聚合。本文件不写状态、不造记录、不发起动作。

   三条口径必须守住：
   1. 影子条目 result=ok 是"评估通过"，不是"恢复成功" —— 每条都带未执行标记；
   2. 空列表 = 尚无记录，不是"守护没开" —— 状态徽标另说配置；
   3. 后端中文原文走 ekTrData，不经过 t() 的字典缺词会原样露出。 */
(function(){
  const KIND = {
    recover:    {label:'自动重启',   cls:'ok'},
    verified:   {label:'复验通过',   cls:'ok'},
    unverified: {label:'未验到',     cls:'warn'},
    failed:     {label:'复验失败',   cls:'err'},
    cascade:    {label:'关联压制',   cls:'warn'},
    shadow:     {label:'影子评估',   cls:'info'},
    other:      {label:'守护记录',   cls:'muted'}
  };
  const ROUTE = {
    grandfathered: {label:'基线放行', cls:'ok'},
    promoted:      {label:'已晋升',   cls:'ok'},
    ready:         {label:'待签字',   cls:'warn'},
    pending:       {label:'积累中',   cls:'muted'}
  };
  let state = null;   // 最近一次成功读取的视图，语言切换时重绘用

  function badge(label, cls){
    return '<span class="heal-badge ' + esc(cls || 'muted') + '">' + esc(label) + '</span>';
  }
  function when(ts){ return String(ts || '').slice(5, 16); }

  function renderTimeline(d){
    const box = document.getElementById('heal-timeline');
    if(!box) return;
    const items = (d && d.timeline) || [];
    if(!items.length){
      box.innerHTML = '<div class="muted" style="font-size:12px">尚无守护记录。守护开启并跑过一轮后，这里会按时间列出恢复、复验、关联压制与影子评估。</div>';
      return;
    }
    let h = '';
    for(const it of items){
      const k = KIND[it.kind] || KIND.other;
      // 展开区放 detail / verify 全文；用 <details> 原生交互，不引第三方。
      const full = [it.detail, it.verify].filter(Boolean).map(x=>ekTrData(x)).join(' · ');
      h += '<details class="heal-row">' +
             '<summary>' +
               '<span class="heal-t">' + esc(when(it.ts)) + '</span>' +
               badge(t(k.label), k.cls) +
               '<span class="heal-a">' + esc(ekTrData(it.action || '')) + '</span>' +
               '<span class="heal-g">' + esc(ekTrData(it.target || '')) + '</span>' +
               (it.shadow_only ? badge(t('未执行'), 'info') : '') +
             '</summary>' +
             (full ? '<div class="heal-detail">' + esc(full) + '</div>' : '<div class="heal-detail muted">—</div>') +
           '</details>';
    }
    box.innerHTML = h;
  }

  function renderShadow(d){
    const box = document.getElementById('heal-shadow');
    const tip = document.getElementById('heal-shadow-tip');
    if(!box) return;
    const sh = (d && d.shadow) || {};
    const list = sh.strategies || [];
    const agg = {};
    for(const a of (sh.aggregates || [])) agg[a.strategy] = a;
    if(tip) tip.textContent = list.length ? t('共 {0} 条策略', list.length) : '';
    if(!list.length){
      box.innerHTML = '<div class="muted" style="font-size:12px">' +
        esc(ekTrData(sh.empty_note || '当前没有配置了恢复策略的可守护服务。')) + '</div>';
      return;
    }
    const need = sh.need || 20;
    let h = '';
    for(const s of list){
      const r = ROUTE[s.route] || ROUTE.pending;
      const a = agg[s.strategy] || {total:0, pass:0, pass_rate:-1};
      const rate = a.pass_rate < 0 ? '—' : Math.round(a.pass_rate) + '%';
      const blockers = (s.blockers || []).map(x=>'<li>' + esc(ekTrData(x)) + '</li>').join('');
      // 进度条宽度只由连续通过数计算，失败一次即清零（后端 shadowPassesOf 的口径）
      const pct = Math.min(100, Math.round(100 * (s.passes || 0) / need));
      h += '<div class="heal-card">' +
             '<div class="heal-card-hd">' +
               '<b>' + esc(ekTrData(s.strategy)) + '</b>' +
               badge(t(r.label), r.cls) +
               '<span class="heal-g">' + esc(ekTrData((s.approved_by || ''))) + '</span>' +
             '</div>' +
             '<div class="heal-bar"><i style="width:' + pct + '%"></i></div>' +
             '<div class="heal-meta">' +
               t('连续通过 {0} / {1}', s.passes || 0, need) + ' · ' +
               t('累计 {0}/{1}（{2}）', a.pass, a.total, rate) +
               (a.last_ts ? ' · ' + t('最近 {0}', when(a.last_ts)) : '') +
             '</div>' +
             (blockers ? '<ul class="heal-blockers">' + blockers + '</ul>' : '') +
           '</div>';
    }
    box.innerHTML = h;
  }

  function renderStatus(d){
    const s = (d && d.status) || {};
    const badgeEl = document.getElementById('heal-status-badge');
    const noteEl = document.getElementById('heal-status-note');
    if(badgeEl){
      let label = t('未配置'), cls = 'muted';
      if(!s.host){ label = t('未配置'); }
      else if(s.last_unreachable > 0){ label = t('连不上'); cls = 'warn'; }
      else if(s.enabled && s.last_check){ label = t('运行中'); cls = 'ok'; }
      else if(s.enabled){ label = t('已开启'); }
      badgeEl.innerHTML = badge(label, cls);
    }
    if(noteEl){
      // 状态说明是后端拼好的中文，按原样翻；"尚未完成第一轮"要说清楚不是没在跑。
      noteEl.textContent = ekTrData(s.note || '');
    }
  }

  async function loadHeal(){
    let d = null;
    try{ d = await (await fetch('/api/guard/status')).json(); }
    catch(e){
      const box = document.getElementById('heal-timeline');
      if(box) box.innerHTML = '<div class="muted" style="font-size:12px">' + esc(t('读取失败')) + '</div>';
      return;
    }
    state = d;
    renderStatus(d);
    renderTimeline(d);
    renderShadow(d);
  }
  window.loadHeal = loadHeal;
  window.ekHealState = ()=> state;   // CDP 断言与调试用，只读引用

  const rf = document.getElementById('heal-refresh');
  if(rf) rf.onclick = ()=> loadHeal();
  // 语言切换后重绘：applyI18n 只翻静态节点，动态生成的中文要重走 ekTrData。
  document.addEventListener('ek:lang', ()=>{ if(state){ renderStatus(state); renderTimeline(state); renderShadow(state); } });
})();
