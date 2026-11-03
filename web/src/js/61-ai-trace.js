/* ============ 轨迹：历史视图（v3.7.3） ============
   这个文件补的是「轨迹」页此前缺的另一半。

   现状（v3.7.x）：轨迹页只渲染**浏览器内存里的本回合节点**——刷新即失。
   而轨迹其实一直在落盘（trace-YYYYMMDD.jsonl），后端的 traceQuery /
   traceSummary 早就写好了却没有任何调用点。于是这份数据只能喂给内部统计，
   人看不到"上周那次反复重启到底怎么走的"。

   现在两种视图并存：
     · 本回合  —— 默认，就是原来那块（SSE 实时节点）
     · 历史    —— 读盘上的轨迹：统计条 + 筛选 + 列表 + 单条时间轴

   ## 为什么用 var 而不是 let/const
   前端是「多个片段按文件名拼成一个 <script>」（见 tools/web_build.py），
   而本文件的 `aiTrMode` 会被 60-ai.js 里的 aiTraceRender 读到。
   60-ai.js 在拼接后**先执行**，若这里用 let 就会落进 TDZ：
   `node --check` 查不出，运行时是 ReferenceError。var 会被提升成 undefined，
   60-ai.js 侧按 `aiTrMode === 'hist'` 判断，undefined 自然走本回合分支。
   —— 这条坑项目里踩过，别再改回 let。

   ## 口径
   成败与结局**一律取自服务端字段**（outcome / verified / result），
   前端只做"把已有事实排成时间轴"这件事，不重新判定。
   与"徽章说验了就是真验了"是同一条原则。
*/

var aiTrMode = 'turn';       // 'turn' | 'hist'
var aiTrHist = null;         // {rows, stats, retention, err, loading}
var aiTrDetail = null;       // 单条轨迹的完整数据（null = 显示列表）
var aiTrBusy = false;

/* 阶段 → 中文名。与后端 trace.go / trace_phase.go 的常量一一对应，
   后端加了新阶段这里要跟着加，否则会掉回原始英文常量（不致命但难看）。 */
var AI_TR_PHASE = {
  plan: '规划', action: '动作', evidence: '证据', verify: '复验',
  failure: '失败', success: '达成', policy: '策略', human: '人工', truncated: '已截断'
};

function aiTrSwitchRow() {
  var wrap = document.createElement('div');
  wrap.className = 'pbar';
  wrap.style.marginBottom = '8px';
  var mk = function (mode, label) {
    var b = document.createElement('button');
    b.textContent = t(label);
    if (aiTrMode === mode) {
      b.style.color = 'var(--brand)';
      b.style.borderColor = 'var(--brand)';
    }
    b.onclick = function () {
      aiTrMode = mode;
      if (mode === 'turn') aiTrDetail = null;
      aiTraceRender();
      if (mode === 'hist') aiTrLoad(false);
    };
    return b;
  };
  wrap.appendChild(mk('turn', '本回合'));
  wrap.appendChild(mk('hist', '历史'));
  if (aiTrMode === 'hist') {
    var sp = document.createElement('span');
    sp.style.flex = '1';
    wrap.appendChild(sp);
    var re = document.createElement('button');
    re.textContent = t('刷新');
    re.onclick = function () { aiTrLoad(true); };
    wrap.appendChild(re);
  }
  return wrap;
}

/* 结局标签：颜色只表达"好/中/坏"三态，与页面既有图例同源。 */
function aiTrOutcomeTag(outcome) {
  var map = { success: ['ok', '成功'], failed: ['bad', '失败'],
              partial: ['warn', '部分'], aborted: ['mute', '中断'] };
  var m = map[outcome] || ['mute', outcome || '未判定'];
  var s = document.createElement('span');
  s.className = 'tr-tag ' + m[0];
  s.textContent = t(m[1]);
  return s;
}

function aiTrFmtWhen(s) {
  // "2026-11-01 10:00:00.000" → "11-01 10:00:00"（列表里年份是冗余的）
  if (!s || s.length < 19) return s || '—';
  return s.slice(5, 19);
}

function aiTrFmtDur(ms) {
  if (!ms) return '—';
  if (ms < 1000) return ms + 'ms';
  return (ms / 1000).toFixed(1) + 's';
}

function aiTrLoad(force) {
  if (aiTrBusy) return;
  if (aiTrHist && !force && !aiTrHist.err) { aiTrHistRender($ai('ai-pane-trace')); return; }
  aiTrBusy = true;
  aiTrHist = { rows: [], stats: null, loading: true };
  aiTrHistRender($ai('ai-pane-trace'));
  var q = [];
  if (aiTrHistFilter.outcome) q.push('outcome=' + encodeURIComponent(aiTrHistFilter.outcome));
  if (aiTrHistFilter.q) q.push('q=' + encodeURIComponent(aiTrHistFilter.q));
  if (aiTrHistFilter.onlybad) q.push('onlybad=1');
  q.push('n=' + (aiTrHistFilter.n || 30));
  fetch('/api/traces?' + q.join('&')).then(function (r) { return r.json(); }).then(function (d) {
    aiTrBusy = false;
    if (!d || !d.ok) throw new Error((d && d.error) || 'bad response');
    aiTrHist = { rows: d.traces || [], stats: d.stats || null, retention: d.retention_days };
    aiTrHistRender($ai('ai-pane-trace'));
  }).catch(function (e) {
    aiTrBusy = false;
    aiTrHist = { rows: [], err: String(e && e.message || e) };
    aiTrHistRender($ai('ai-pane-trace'));
  });
}

/* 筛选条件放在模块级：重绘不该把用户刚输入的词清掉。 */
var aiTrHistFilter = { outcome: '', q: '', onlybad: false, n: 30 };

function aiTrHistRender(box) {
  if (!box) return;
  box.textContent = '';
  box.appendChild(aiTrSwitchRow());

  if (aiTrDetail) { aiTrRenderDetail(box, aiTrDetail); return; }

  // ---- 筛选条 ----
  var bar = document.createElement('div');
  bar.className = 'pbar';
  var sel = document.createElement('select');
  sel.style.cssText = 'font-size:11px;padding:3px 6px;border:1px solid var(--line);border-radius:6px;background:var(--soft);color:var(--text);flex:none';
  [['', '全部结局'], ['success', '成功'], ['failed', '失败'], ['partial', '部分'], ['aborted', '中断']].forEach(function (o) {
    var op = document.createElement('option');
    op.value = o[0]; op.textContent = t(o[1]);
    if (aiTrHistFilter.outcome === o[0]) op.selected = true;
    sel.appendChild(op);
  });
  sel.onchange = function () { aiTrHistFilter.outcome = sel.value; aiTrLoad(true); };
  var inp = document.createElement('input');
  inp.placeholder = t('搜索目标或动作');
  inp.value = aiTrHistFilter.q;
  var timer = null;
  inp.oninput = function () {
    clearTimeout(timer);
    timer = setTimeout(function () { aiTrHistFilter.q = inp.value.trim(); aiTrLoad(true); }, 350);
  };
  var only = document.createElement('label');
  only.className = 'muted';
  only.style.cssText = 'display:flex;gap:4px;align-items:center;white-space:nowrap';
  var cb = document.createElement('input');
  cb.type = 'checkbox'; cb.checked = !!aiTrHistFilter.onlybad;
  cb.onchange = function () { aiTrHistFilter.onlybad = cb.checked; aiTrLoad(true); };
  only.appendChild(cb);
  only.appendChild(document.createTextNode(t('只看异常')));
  bar.appendChild(sel); bar.appendChild(inp); bar.appendChild(only);
  box.appendChild(bar);

  if (!aiTrHist || aiTrHist.loading) {
    var l = document.createElement('div'); l.className = 'muted';
    l.textContent = t('读取轨迹…');
    box.appendChild(l); return;
  }
  if (aiTrHist.err) {
    var e = document.createElement('div'); e.className = 'muted';
    e.textContent = t('读取失败') + '：' + aiTrHist.err;
    box.appendChild(e); return;
  }
  var rows = aiTrHist.rows || [];

  // ---- 统计条 ----
  if (aiTrHist.stats && aiTrHist.stats.total) {
    var st = aiTrHist.stats;
    var sum = document.createElement('div');
    sum.className = 'muted';
    sum.style.cssText = 'margin-bottom:8px;line-height:1.6;display:flex;flex-wrap:wrap;gap:2px 8px';
    var bits = [
      t('最近 {0} 条', st.total),
      t('成功 {0}', st.success),
      t('失败 {0}', st.failed)
    ];
    if (st.partial) bits.push(t('部分 {0}', st.partial));
    if (st.aborted) bits.push(t('中断 {0}', st.aborted));
    bits.push(t('平均 {0} 步', st.avg_steps));
    if (st.avg_prompt_tokens) bits.push('↑' + aiFmtTok(st.avg_prompt_tokens) + ' ↓' + aiFmtTok(st.avg_completion_tokens));
    if (st.cache_hit_pct) bits.push(t('缓存 {0}%', Math.round(st.cache_hit_pct)));
    if (st.humans) bits.push(t('人工介入 {0} 次', st.humans));
    sum.textContent = bits.join(' · ');
    box.appendChild(sum);
  }

  if (!rows.length) {
    var p = document.createElement('div'); p.className = 'muted';
    p.textContent = aiTrHistFilter.q || aiTrHistFilter.outcome || aiTrHistFilter.onlybad
      ? t('没有符合条件的轨迹')
      : t('还没有历史轨迹（发一句话就会有了）');
    box.appendChild(p);
    if (aiTrHist.retention) {
      var rt = document.createElement('div'); rt.className = 'muted';
      rt.style.cssText = 'margin-top:6px;font-size:10px';
      rt.textContent = t('仅覆盖最近 {0} 天（轨迹保留期）', aiTrHist.retention);
      box.appendChild(rt);
    }
    return;
  }

  // ---- 列表 ----
  rows.forEach(function (r) {
    var root = document.createElement('div');
    root.className = 'tr-root ' + (r.outcome === 'failed' || r.outcome === 'aborted' ? 'bad'
      : (r.outcome === 'partial' ? 'warn' : 'ok'));
    // 行上带 id 与步数：E2E 测试要能挑"有步骤的那条"再点进去。
    // 只靠"点第一行"，本机第一条恰好是空步骤的回答型轨迹时就会误判成渲染坏了。
    root.dataset.traceId = r.trace_id || '';
    root.dataset.steps = String(r.steps || 0);
    var head = document.createElement('div');
    head.className = 'tr-head';
    head.style.cursor = 'pointer';
    var when = document.createElement('span');
    when.className = 'tr-id';
    when.textContent = aiTrFmtWhen(r.started);
    var goal = document.createElement('span');
    goal.className = 'tr-sum';
    goal.textContent = r.goal || r.trace_id;
    head.appendChild(when); head.appendChild(goal); head.appendChild(aiTrOutcomeTag(r.outcome));
    root.appendChild(head);

    var meta = document.createElement('div');
    meta.className = 'muted';
    meta.style.cssText = 'font-size:11px;margin:2px 0 0 2px;display:flex;flex-wrap:wrap;gap:6px';
    var mbits = [t('{0} 步', r.steps), aiTrFmtDur(r.dur_ms)];
    if (r.usage) mbits.push('↑' + aiFmtTok(r.usage.prompt_tokens) + ' ↓' + aiFmtTok(r.usage.completion_tokens));
    if (r.ai_fails) mbits.push(t('失败 {0} 次', r.ai_fails));
    if (r.verified) mbits.push(t('已复验'));
    if (r.humans) mbits.push(t('人工 {0}', r.humans));
    meta.textContent = mbits.join(' · ');
    root.appendChild(meta);

    head.onclick = function () { aiTrOpen(r.trace_id); };
    box.appendChild(root);
  });

  if (aiTrHist.retention) {
    var rt2 = document.createElement('div'); rt2.className = 'muted';
    rt2.style.cssText = 'margin-top:10px;font-size:10px';
    rt2.textContent = t('仅覆盖最近 {0} 天（轨迹保留期）', aiTrHist.retention);
    box.appendChild(rt2);
  }
}

function aiTrOpen(id) {
  fetch('/api/trace?id=' + encodeURIComponent(id)).then(function (r) { return r.json(); }).then(function (d) {
    if (!d || !d.ok) { toast((d && d.error) || t('读取失败')); return; }
    aiTrDetail = d;
    aiTrHistRender($ai('ai-pane-trace'));
  }).catch(function () { toast(t('读取失败')); });
}

/* 一步的成色：**只用后端字段**推导，不重判成败。
   failure 步就是失败；verify 步看证据里有没有"未通过"；人工步单独一色。 */
function aiTrStepCls(s) {
  if (s.phase === 'failure') return 'bad';
  if (s.phase === 'human') return 'human';
  if (s.phase === 'verify') return /未通过|未复验/.test(s.evidence || '') ? 'warn' : 'ok';
  if (s.phase === 'success') return 'ok';
  if (s.result === 'fail') return 'bad';
  if (s.phase === 'policy') return 'warn';
  return '';
}

function aiTrRenderDetail(box, d) {
  var tr = d.trace || {};
  var row = d.row || {};

  var back = document.createElement('button');
  back.textContent = '← ' + t('返回列表');
  back.style.cssText = 'background:none;border:1px solid var(--line);border-radius:6px;color:var(--muted);font-size:11px;padding:2px 8px;cursor:pointer;margin-bottom:8px';
  back.onclick = function () { aiTrDetail = null; aiTrHistRender($ai('ai-pane-trace')); };
  box.appendChild(back);

  var title = document.createElement('div');
  title.style.cssText = 'font-weight:600;margin-bottom:4px;word-break:break-word';
  title.textContent = tr.goal || t('（没有记录目标）');
  box.appendChild(title);

  var meta = document.createElement('div');
  meta.className = 'muted';
  meta.style.cssText = 'font-size:11px;margin-bottom:6px;display:flex;flex-wrap:wrap;gap:6px;align-items:center';
  meta.appendChild(aiTrOutcomeTag(tr.outcome));
  var mb = [tr.trace_id, aiTrFmtWhen(tr.started), aiTrFmtDur(row.dur_ms)];
  if (row.usage) mb.push('↑' + aiFmtTok(row.usage.prompt_tokens) + ' ↓' + aiFmtTok(row.usage.completion_tokens));
  if (row.verified) mb.push(t('已复验'));
  var mt = document.createElement('span');
  mt.textContent = mb.join(' · ');
  meta.appendChild(mt);
  box.appendChild(meta);

  // 一句话摘要：与日志/报告里那句同源（后端 traceSummary 生成），避免两处口径分叉
  if (row.summary) {
    var sm = document.createElement('div');
    sm.className = 'muted';
    sm.style.cssText = 'font-size:11px;background:var(--off);border-radius:6px;padding:6px 8px;margin-bottom:8px;line-height:1.6';
    sm.textContent = row.summary;
    box.appendChild(sm);
  }

  var steps = tr.steps || [];
  if (!steps.length) {
    // 记步在执行层（aiExecTool），所以"没有步骤"只有一种含义：
    // **这一回合没调用任何工具**（纯问答）。不是记录缺失，别让人以为是坏的。
    var p = document.createElement('div'); p.className = 'muted';
    p.textContent = t('这一回合没有调用任何工具（纯问答，没有动手）');
    box.appendChild(p);
  } else {
    var ul = document.createElement('ul');
    ul.className = 'tl';
    steps.forEach(function (s) {
      var li = document.createElement('li');
      li.className = 'tl-row ' + aiTrStepCls(s);
      var dot = document.createElement('i'); dot.className = 'tl-dot';
      var sq = document.createElement('span'); sq.className = 'tl-k';
      sq.textContent = String(s.seq || '') + '.';
      var ph = document.createElement('span'); ph.className = 'tl-k';
      ph.textContent = t(AI_TR_PHASE[s.phase] || s.phase);
      var tt = document.createElement('span'); tt.className = 'tl-t';
      tt.textContent = s.action + (s.target ? ' @ ' + s.target : '');
      li.appendChild(dot); li.appendChild(sq); li.appendChild(ph); li.appendChild(tt);
      if (s.dur_ms) {
        var du = document.createElement('span'); du.className = 'tl-dur';
        du.textContent = (s.dur_ms / 1000).toFixed(1) + 's';
        li.appendChild(du);
      }
      if (s.err_kind) {
        var ek = document.createElement('span'); ek.className = 'tr-tag bad';
        ek.style.marginLeft = '6px'; ek.textContent = s.err_kind;
        li.appendChild(ek);
      }
      if (s.evidence) {
        var lines = String(s.evidence).split('\n').length;
        if (lines > 6 || String(s.evidence).length > 160) {
          var det = document.createElement('details'); det.className = 'tl-det';
          var sg = document.createElement('summary');
          sg.textContent = t('展开明细（{0} 行）', lines);
          var pre = document.createElement('div'); pre.className = 'tl-xbody';
          pre.textContent = s.evidence;
          det.appendChild(sg); det.appendChild(pre); li.appendChild(det);
        } else {
          var x = document.createElement('div'); x.className = 'tl-x';
          x.textContent = s.evidence;
          li.appendChild(x);
        }
      }
      ul.appendChild(li);
    });
    box.appendChild(ul);
  }

  // 与审计联动：用这条轨迹的起止时间窗去筛审计（aiAuditFocus 只吃 {at} 毫秒）
  var toMs = function (s) {
    var v = Date.parse(String(s || '').replace(' ', 'T'));
    return isFinite(v) ? v : null;
  };
  var a = toMs(tr.started), b = toMs(tr.ended);
  if (a && b) {
    var bt = document.createElement('button');
    bt.textContent = t('在审计里看这段时间');
    bt.style.cssText = 'margin-top:10px;background:none;border:1px solid var(--line);border-radius:6px;color:var(--brand);font-size:11px;padding:2px 8px;cursor:pointer';
    bt.onclick = function () {
      aiAuditFocus([{ at: a }, { at: b }]);
      aiSide('audit');
    };
    box.appendChild(bt);
  }
}
