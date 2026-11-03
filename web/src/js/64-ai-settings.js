/* ---------- 表单自动保存 + 自动拉取模型（2026-10-23） ----------
 *
 * 用户裁定的新主链路：**改完即存、存完即拉**。
 * 模型名的真值永远来自服务商的 /models（内置预置只是拉取失败时的兜底），
 * 所以把「保存配置 → 拉取模型列表」从两个手动按钮改成一条自动链：
 * 选服务商（URL 自动回填）→ 填 Key → 防抖自动保存 → 自动拉取回填。
 * 拉取成功 = Key 和网址都对（当场验证，不用再点「测试连接」）；
 * 拉取失败 = 错误原样亮出来，用户立刻知道是 Key 错还是地址错。
 */

function aiCollectPayload(){
  const model = aiCurrentModel();
  if(!model) return null;    // 模型还没选（比如刚切服务商正在拉取），这轮不存
  return {enabled:$ai('ai_enabled').checked, memory_enabled:$ai('ai_memory_on').checked,
    provider:$ai('ai_provider').value, base_url:$ai('ai_base').value.trim(),
    api_key:$ai('ai_key').value.trim(), model,
    // 内部调用模型：**总是带上**（含空串）。它是可清空的选项，
    // 不传等于"保留旧值"，用户就再也删不掉了。后端按指针处理：
    // 传空串 = 清空（回到跟主模型一样），不传 = 不动。
    utility_model:$ai('ai_utility_model').value.trim(),
    temperature: ($ai('ai_temp').value.trim() === '' ? null : Number($ai('ai_temp').value))};
}

/* 自动保存状态字：静默保存也要让用户看见「存没存上」——
   2026-10-23 用户把「保存链炸了」误判成「启用开关失灵」，根因就是全程无反馈。 */
function aiAutoSaveSt(txt, bad){
  const el = $ai('ai_autosave_st');
  if(!el) return;
  el.textContent = txt ? t(txt) : '';
  el.classList.toggle('bad', !!bad);
}

/* 拉取失败的人话分类：超时 / 401 / 404 / DNS 各是各的病，
   别让用户对着 Go 的报错原文猜。命中不了就原样透出（不编造）。 */
function aiPullErrHuman(err){
  err = String(err||'');
  // 后端对「地址≠已存服务商」的 401 有专属说明（防密钥发往陌生域名没带 Key），
  // 这个要先于「Key 被拒」判断，否则会把「没带 Key」误说成「Key 错」
  if(err.indexOf('未附带 API Key') >= 0)
    return t('该地址与已保存的服务商不一致，为防密钥外发未附带 Key——先保存配置再拉取');
  const m = err.match(/HTTP (\d+)/);
  if(m){
    const code = m[1];
    if(code === '401' || code === '403') return t('Key 被拒（HTTP {0}）：检查 API Key 是否正确', code);
    if(code === '404') return t('模型地址不存在（HTTP 404）：检查 Base URL 是否少了 /v1 或多了路径');
    return t('服务商返回 HTTP {0}', code) + '：' + err;
  }
  if(/Client\.Timeout|context deadline/i.test(err)) return t('连接超时：服务商无响应或网络不通');
  if(/no such host|lookup/i.test(err)) return t('域名无法解析：检查 Base URL 拼写与网络');
  if(/connection refused/i.test(err)) return t('连接被拒：地址或端口不对');
  if(/x509|certificate|tls:/i.test(err)) return t('HTTPS 证书异常：检查地址与系统时间');
  return t('拉取失败：') + err;
}

async function aiPullModelsCore(){
  // 拉取前必须已存 Key（/models 用后端 DPAPI 里的 Key 做 Bearer）；
  // 没有就跳过——让用户先填 Key，免得每次都吃一个注定 401 的请求。
  if(!aiKeySet && !$ai('ai_key').value.trim()) return;
  try{
    const c = await (await fetch('/api/ai/models', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({base_url:$ai('ai_base').value.trim()})})).json();
    if(!c.ok){ $ai('h-ai').textContent = t('自动保存完成，但拉取模型失败：')+aiPullErrHuman(c.error); return false; }
    const cur = aiCurrentModel();
    aiPulledModels = c.models || [];
    aiPulledFrom = $ai('ai_base').value.trim();   // Base URL 改动后旧列表作废（同手动拉取）
    aiApplyProvider(cur);
    $ai('h-ai').textContent = t('已自动保存，已拉取 {0} 个模型并回填', aiPulledModels.length);
    return true;
  }catch(e){ $ai('h-ai').textContent = t('自动保存完成，但拉取模型失败：')+aiPullErrHuman(e); return false; }
}

async function aiAutoSaveNow(){
  const payload = aiCollectPayload();
  if(!payload) return;
  aiAutoBusy = true;
  aiAutoSaveSt('保存中…');
  try{
    const r = await fetch('/api/ai/config', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(payload)});
    if(!r.ok){ aiAutoSaveSt('保存失败：HTTP '+r.status, true); return; }
    if(payload.api_key){ $ai('ai_key').value=''; aiKeySet = true;
      // Key 已入库，刷新脱敏占位符（只动 placeholder，不整表重载——整表重载会打断用户正在输入的字段）
      try{ const c = await (await fetch('/api/ai/config')).json();
        if(c.key_set) $ai('ai_key').placeholder = t('已保存（{0}），留空保留', c.key_masked||'****');
      }catch(e){}
    }
    memSyncGate();
    await aiPullModelsCore();
    aiAutoSaveSt('已自动保存 ✓');   // 拉取的成败在 h-ai 里说，这里只报保存结果
  }finally{ aiAutoBusy = false; }
}

function aiScheduleAutoSave(){
  if(aiSuppress) return;
  aiAutoSaveSt('有改动，待保存…');
  clearTimeout(aiAutoTimer);
  aiAutoTimer = setTimeout(()=>{ if(!aiAutoBusy) aiAutoSaveNow(); else aiScheduleAutoSave(); }, 900);
}

// 绑定自动保存：下拉/开关用 change，文本输入用 input（防抖合并击键）
// provider 的 change 在上面既有绑定里处理（清拉取缓存 + 应用预置），这里只挂保存调度
['ai_base','ai_key','ai_temp','ai_utility_model'].forEach(id=>$ai(id).addEventListener('input', aiScheduleAutoSave));
['ai_enabled','ai_memory_on'].forEach(id=>$ai(id).addEventListener('change', aiScheduleAutoSave));

/* 对话消费：读取 SSE 流 */
const AI_TOOL_CN = {get_system_state:'读取环境状态', get_logs:'读取日志', get_diag_report:'生成诊断报告',
  run_detection:'重新检测组件', db_backup:'备份数据库', start_service:'启动服务', stop_service:'停止服务',
  restart_service:'重启服务', apply_whitelist:'加入 Defender 白名单', db_check:'检测数据库连接',
  cleanup_processes:'清理后台进程', get_project_brief:'读取项目画像', list_project:'查看项目目录',
  search_files:'搜索项目文件', read_file:'读取项目文件', verify_environment:'复验环境状态',
  sync_chain_ip:'同步链地址到 config.toml',
  // v3.3 补漏：以下 9 个工具后端注册表里真实存在，此前没进这张表 ——
  // 中文界面上它们会以英文裸名出现在轨迹/确认卡里（"→ db_query"）。
  db_query:'只读查询业务数据库', db_list:'列出数据库与表',
  get_audit_log:'读取审计日志', list_backups:'列出备份',
  get_env_snapshot:'读取环境快照', check_env_req:'检查项目版本要求',
  get_chain_guard:'读取链守护状态', recall_lessons:'检索经验教训',
  manage_memories:'管理长期记忆'};
function aiToolName(name){ return t(AI_TOOL_CN[name] || name) + '（' + name + '）'; }

// v2.9：把 verify_environment 的客观结果渲染成状态徽章。
//
// ## 为什么它不是「又一个tool_result」
//
// tool_result 展示的是**工具返回的文本**，而那段文本是写给模型读的指令
// （「必须如实告诉用户还没确认成功」之类）。用户看到的是**模型对它的转述**——
// 而转述可能说反。这正是「执行即成功」在展示层的重演。
//
// 徽章只认 SSE 里的 ok/verified 两个布尔值，
// 所以 AI 说得再漂亮也改不了它的颜色。
//
//## 三态必须三色
//
//|状态 | 含义 | 为什么不能合并 |
// |---|---|---|
// | ok && verified | 真的恢复了 | — |
// | ok && !verified | 动作执行了，但**没证据说恢复了** | 与「恢复成功」合并 = 白做 |
// | !ok | 复验本身失败 | 与上面两种合并 = 又变成一句「已处理」 |
function aiRenderVerify(ev){
  // **cls 与 badge 由 Go 侧算好后传过来**（aiVerifyBadgeClass/Text）。
  // 这里只做映射，不重新判断三态 —— 两处独立判断会各自演化，
  // 然后徽章就开始显示错误状态，而它唯一的价值就是「它说验了就是真验了」。
  // 兜底仍按 ok/verified 算，只是为了后端旧版本时不至于空白。
  const cls = ev.cls || (function(){ const ok=!!ev.ok, vf=!!ev.verified;
    return !ok ? 'is-bad' : (vf ? 'is-ok' : 'is-pending'); })();
  // 徽章文案**必须走 t()** —— Go 侧传来的 badge 是中文，
  // 而界面可能切到英文。直接显示会让英文界面里混进中文。
  // 所以按三态重新查表，Go 侧的 badge 只作兜底。
  const badge = t(!ev.ok ? '复验未通过' : (ev.verified ? '已复验通过' : '已执行·未确认'))
    || ev.badge || '';
  const d = document.createElement('div');
  d.className = 'ai-verify ' + cls;
  const head = document.createElement('div'); head.className = 'v-head';
  const b = document.createElement('span'); b.className = 'v-badge'; b.textContent = badge;
  head.appendChild(b);
  /* 归因要用后端专门传来的 ev.tool：v3.0 让 start/restart_service 的
     "启动即复验"也发 verify_result 并带上 tool 字段，就是为了让下游分辨
     这条结论是哪次动作产生的。写死 verify_environment 会把归因丢掉
     （用户看到「复验环境：已复验通过」，其实是「重启后端」的复验）。 */
  const tgt = document.createElement('span');
  tgt.textContent = aiToolName(ev.tool || 'verify_environment').replace(/（[^）]*）$/, '');
  head.appendChild(tgt);
  if(ev.msg){
    const m = document.createElement('span'); m.className = 'v-msg'; m.textContent = ev.msg;
    head.appendChild(m);
  }
  d.appendChild(head);
  const evs = Array.isArray(ev.evidence) ? ev.evidence.filter(Boolean) : [];
  if(evs.length){
    const det = document.createElement('details');
    const sm = document.createElement('summary'); sm.textContent = t('查看证据');
    det.appendChild(sm);
    const pre = document.createElement('pre'); pre.className = 'v-ev'; pre.textContent = evs.join('\n');
    det.appendChild(pre);
    d.appendChild(det);
  }
  $ai('ai-msgs').appendChild(d);
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
}

let aiCtrl = null;      // AbortController：手动"停止生成"用
// aiGen 代际计数：abort 旧流后立刻开新流（确认投递失败的兜底路径就是这样），
// 旧流的 finally 若晚于新流执行，会把新流刚设的 aiBusy/按钮态冲掉——
// 流式进行中输入框又可再发一条，两条流交错往同一容器贴消息。
// 规则：finally 里先比对代际，不是当前代就整段跳过复位。
let aiGen = 0;
let aiPending = null;   // {asst, card}：待确认的写操作卡片。用户无视卡片直接发新消息时清理悬挂回合

async function aiConsume(path, body){
  const gen = ++aiGen;   // 本次流的代际：finally 里只有当代际匹配才允许复位全局态
  aiOpen(); aiBusy = true; $ai('ai-send').disabled = true; $ai('ai-stop').style.display = '';
  petAIState('thinking');   // 桌宠联动：立即进思考态（确认后续接轮也走这里，状态从 confirm → thinking）
  body = Object.assign({}, body || {}, {lang: EK_LANG});   // 让助手跟随界面语言作答
  // 行动绑定（Phase 1）：aiCurMission 非空时本回合是该行动的执行回合。
  // 放在 aiConsume 统一注入：确认续接轮（body.confirm）也自动带上，断点才接得回去。
  if(aiCurMission) body.mission_id = aiCurMission;
  // 回退头：某些本机代理/预览容器会剥掉 POST body（实测表现为服务端 decode 出 0 条消息，
  // 模型对着空输入自说自话）。令牌走请求头能完好穿过，所以把"最后一条用户消息 + 确认回执"
  // 也放进请求头兜底，body 丢失时服务端据此恢复。
  let fb = {last: '', recent: [], confirm: body.confirm || null, lang: EK_LANG};
  const ms = body.messages || [];
  for(let i = ms.length - 1; i >= 0; i--){ if(ms[i] && ms[i].role === 'user'){
    // 只发了图没写字的回合：回退头至少给出"[图片]"，否则恢复出来的历史里
    // 这一条是空 user 消息，模型会当成"用户什么都没问"。
    fb.last = String(ms[i].content || '').slice(0, 2000) ||
      ((Array.isArray(ms[i].images) && ms[i].images.length) ? '[图片]' : '');
    break; } }
  // 最近 6 条（每条截 200 字）：只带 last 的话，body 一旦被吃，模型等于失忆——
  // 它会重新问一遍（那些"菜单式无效回复"往往就是这么来的），这一轮缓存也全废。
  // 6×200 编码后约 3~4KB，仍在常见代理的头长限制内。
  fb.recent = ms.slice(-6).filter(m=>m && (m.role === 'user' || m.role === 'assistant'))
    .map(m=>({r: m.role, c: String(m.content || '').slice(0, 200)}));
  let cur = null, curText = '', curTs = '';
  // 触发本回合的那条用户原话——回合末尾工具栏的「重新提问」用它
  let reaskText = '';
  for(let i = aiMessages.length - 1; i >= 0; i--){ if(aiMessages[i] && aiMessages[i].role === 'user'){ reaskText = String(aiMessages[i].content || ''); break; } }
  let think = null, rsnText = '', rsnEl = null, renderQueued = false, rsnQueued = false;
  // v2.3 N5：计划面板。同一轮里所有步骤归到一个可折叠块内，
  // 结束后自动收起——用户中途能看，收完不占屏。
  let planEl = null, planList = null, planN = 0;
  const planBox = (maxTurns, reason)=>{
    if(planEl){ planEl.remove(); planEl = null; planList = null; }
    planN = 0;
    planEl = document.createElement('details');
    planEl.className = 'ai-plan';
    const sm = document.createElement('summary');
    sm.dataset.reason = reason || '';
    sm.textContent = t('执行计划（{0}）', reason ? reason + ' · ' + t('上限 {0} 轮', maxTurns) : t('上限 {0} 轮', maxTurns));
    planList = document.createElement('div');
    planList.className = 'ai-plan-list';
    planEl.appendChild(sm);
    planEl.appendChild(planList);
    $ai('ai-msgs').appendChild(planEl);
  };
  const planStep = (seq, text)=>{
    if(!planList){ planBox(0, ''); }
    const row = document.createElement('div');
    row.className = 'ai-plan-row';
    // 服务端数据必须 esc()：工具名/目标里可能有用户可控内容
    row.innerHTML = '<span class="ai-plan-n">' + esc(String(seq|| ++planN)) + '</span><span>' + esc(text||'') + '</span>';
    planList.appendChild(row);
    planN = Number(seq) || planN;
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  };
  const planEnd = (text)=>{
    if(!planEl) return;
    // 收完自动折叠：步骤已经执行完，继续展开只是噪音
    planEl.open = false;
    const sm = planEl.querySelector('summary');
    if(sm && text) sm.title = text;
    planEl = null; planList = null;
  };
  // 提额：标题里的上限要跟着变，否则用户看到的还是那个已经作废的初值。
  // 标题上留一个可点开看原因的 title——不额外插一行，那个位置留给结论。
  const planExtend = (maxTurns, reason)=>{
    if(!planEl){
      planBox(maxTurns, reason);
      return;
    }
    const sm = planEl.querySelector('summary');
    if(!sm) return;
    sm.textContent = t('执行计划（{0}）', (sm.dataset.reason || '') + ' · ' + t('上限 {0} 轮', maxTurns));
    sm.title = reason || '';
    const row = document.createElement('div');
    row.className = 'ai-plan-row';
    row.innerHTML = '<span class="ai-plan-n">+</span><span style="color:var(--ok)">' +
      esc(t('每一轮都有新信息，上限自动放宽到 {0} 轮', maxTurns)) + '</span>';
    planList.appendChild(row);
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  };
  try{
  // 思考气泡的构造函数。**单独抽出来是因为它要能被重建** ——
  // v2.3 起 `plan_start` 是服务端第一个事件，到达时就把气泡删了，
  // 而 `reasoning` 片段在它之后才到，不重建的话那段尾部渲染是死代码。
  // （v1.10.3 时 reasoning 能显示，正是因为没有 plan_start 抢删。）
  const mkThink = ()=>{
    if(think) return think;
    think = document.createElement('div'); think.className = 'ai-think';
    think.innerHTML = '<span>' + t('思考中') + '</span><span class="ai-dot"></span><span class="ai-dot"></span><span class="ai-dot"></span>';
    $ai('ai-msgs').appendChild(think);
    $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
    return think;
  };
  mkThink();
  const dropThink = ()=>{ if(think && think.parentNode){ think.remove(); think = null; } };
    aiCtrl = new AbortController();
    // 声明"我发了多少字节"：服务端据此判断请求体是**没到**还是**被截断**。
    // 长度按 UTF-8 字节算（不是 JS 的字符数，中文一个字 3 字节，用字符数会假报不符）。
    //
    // 发送前裁剪 + 剥冗余字段（v3.3）：aiMessages 只在**落盘**时截 60 条，内存里
    // 一个长会话是无限增长的——每轮都整体 JSON.stringify 两次（长度头 + body），
    // 请求体随会话线性膨胀。后端反正会裁到 40 条，客户端先裁到 60 条再发，
    // 省的是传输与两侧序列化的钱。ts 是前端本地渲染时间戳用的，后端 aiMsg
    // 没有该字段，纯浪费字节。tool_calls / reasoning_content 必须保留——
    // 思考型模型要求原样回传，确认续接路径靠 tool_calls 对齐。
    const _msgs = (Array.isArray(body.messages) ? body.messages : []).slice(-60).map(m => {
      const o = {role: m.role, content: String(m.content == null ? '' : m.content)};
      if(m.reasoning_content) o.reasoning_content = m.reasoning_content;
      if(m.tool_calls) o.tool_calls = m.tool_calls;
      if(m.tool_call_id) o.tool_call_id = m.tool_call_id;
      if(m.name) o.name = m.name;
      // 图片引用必须跟着走：这里是白名单式重建，漏一个字段就是**静默丢图**
      // （2026-10-31 实测：气泡里图好好的，模型答"我没收到图片"）。
      // 只传 id/name/mime —— 本地预览用的 url（data URL）绝不能进请求体。
      if(Array.isArray(m.images) && m.images.length){
        o.images = m.images.filter(x=>x && x.id).map(x=>({id:x.id, name:x.name || '', mime:x.mime || ''}));
        if(!o.images.length) delete o.images;
      }
      return o;
    });
    const _bodyStr = JSON.stringify(Object.assign({}, body, {messages: _msgs}));
    const _bodyLen = new TextEncoder().encode(_bodyStr).length;
    /* 整体超时兜底：后端的 aiTurnTimeout 是**每轮** 6 分钟，多轮任务叠加可以
       让界面转十几分钟，期间唯一逃生是手点「停止」。这里给整条请求设 15 分钟
       硬上限，到点自动 abort，界面至少会说出"连接中断"而不是永远三点动画。
       AbortSignal.any 在老浏览器不存在 → 手动联动两个信号。 */
    const _deadline = AbortSignal.timeout ? AbortSignal.timeout(15*60*1000) : null;
    const _sig = (_deadline && AbortSignal.any) ? AbortSignal.any([aiCtrl.signal, _deadline]) : aiCtrl.signal;
    if(_deadline && !AbortSignal.any){
      _deadline.addEventListener('abort', ()=>{ try{ aiCtrl.abort(); }catch(_){} }, {once:true});
    }
    const resp = await fetch(path, {method:'POST', signal: _sig, headers:{'Content-Type':'application/json', 'X-EnvKit-Body-Len': String(_bodyLen), 'X-EnvKit-Chat-Fallback': encodeURIComponent(JSON.stringify(fb))}, body: _bodyStr});
    if(!resp.ok){
      /* 后端失败时回的是 JSON（如 {"type":"error","text":"AI 助手未配置：…"}），
         直接把 resp.text() 甩到气泡里 = 用户看到一整行 JSON。
         解出 text 字段；结构对不上就退回到状态码，至少是句人话。 */
      let msg = 'HTTP ' + resp.status;
      try{ const j = await resp.json(); if(j && j.text) msg = j.text; }catch(_){}
      aiErr(msg); return;
    }
    const reader = resp.body.getReader(); const dec = new TextDecoder();
    let buf = '';
    // 流式渲染节流：每个 delta 全量 innerHTML 重排是 O(n²)，合并到每帧一次。
    // 流式期间**不做代码高亮**：每帧 innerHTML 重建会让高亮元素全部作废，
    // 等于每帧重跑一次 highlight，长回答直接卡死；收尾时统一高亮一次。
    let streamDone = false;
    const flushRender = ()=>{
      renderQueued = false;
      if(!cur || streamDone) return;
      cur.dataset.raw = curText;
      aiFill(cur, curText, {hl:false});
      $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
    };
    while(true){
      const {done, value} = await reader.read();
      if(done) break;
      buf += dec.decode(value, {stream:true});
      let i;
      while((i = buf.indexOf('\n\n')) >= 0){
        const line = buf.slice(0, i).trim(); buf = buf.slice(i+2);
        if(!line.startsWith('data: ')) continue;
        let ev; try{ ev = JSON.parse(line.slice(6)); }catch(_){ continue; }
        aiTraceEvent(ev);   // 旁路采集：一个事件 → 轨迹里一个节点（不改动下面任何渲染逻辑）
        if(ev.type === 'delta'){
          dropThink();
          if(!cur){ cur = aiAdd('ai', '', aiNow(), reaskText); curTs = cur.dataset.ts || ''; curText = ''; }
          curText += ev.text;
          if(!renderQueued){ renderQueued = true; requestAnimationFrame(flushRender); }
        } else if(ev.type === 'reasoning'){
          // 思考型模型的 reasoning_content 流：在"思考中"气泡里滚动展示尾部
          if(ev.text){
            rsnText += ev.text;
            // 气泡可能已被 plan_start 删掉（它是服务端第一个事件，比
            // reasoning 先到）——重建它，否则尾部展示永远是死代码。
            mkThink();
            // parentNode 检查：气泡删了再建之后，rsnEl 还指向已脱离 DOM 的
            // 旧节点，只判 !rsnEl 会把文字写进一个看不见的元素里。
            if(!rsnEl || rsnEl.parentNode !== think){
              rsnEl = document.createElement('div'); rsnEl.className = 'ai-think-txt'; think.appendChild(rsnEl);
            }
            /* 与正文同一个 rAF 节流：思考流一次回合能来上千条，逐条
               textContent + scrollTop 是强制同步布局，网络 pace 会直接打到
               layout 上（比正文流更糟，正文好歹合并过）。 */
            if(!rsnQueued){ rsnQueued = true; requestAnimationFrame(()=>{
              rsnQueued = false;
              if(!rsnEl || rsnEl.parentNode !== think) return; // 气泡被删：放弃这次写入
              rsnEl.textContent = '…' + rsnText.slice(-140);
              $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
            }); }
          }
        } else if(ev.type === 'plan_start'){
          // v2.3 N5：开跑前告诉用户这一轮的上限与理由。
          // 以前是静默开始、跑到 8 轮静默停下，用户两头都不知道。
          dropThink();
          planBox(ev.max_turns, ev.reason);
        } else if(ev.type === 'plan_step'){
          // 每步都显示"正在做什么"——用户看着 AI 查了什么，
          // 比只看结论更可信（结论要能追到查过的东西）。
          planStep(ev.seq, ev.text);
        } else if(ev.type === 'plan_end'){
          planEnd(ev.text);
        } else if(ev.type === 'plan_extend'){
          planExtend(ev.max_turns, ev.reason);
        } else if(ev.type === 'tool_result'){
          dropThink();
          petAIState('busy'); // 桌宠联动：工具真正在执行了。原来这行挂在死的 tool_call 分支上（后端从不发那个事件），等于从没生效过
          const d = document.createElement('details'); d.className = 'ai-tool';
          /* 图标跟服务端 ok 走：失败还顶着绿勾是最糟的一种误导
             （用户扫一眼觉得"工具都成功了"，实际全是红字）。 */
          const tokOk = (typeof ev.ok === 'boolean') ? ev.ok : true;
          const sm = document.createElement('summary');
          sm.textContent = (tokOk ? '\u2713 ' : '\u2717 ') + aiToolName(ev.tool) + ' \u00b7 ' + t('查看结果');
          const pre = document.createElement('pre'); pre.textContent = ev.result || '';
          d.appendChild(sm); d.appendChild(pre);
          $ai('ai-msgs').appendChild(d);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
        } else if(ev.type === 'verify_result'){
          // v2.9：客观复验结果直接上界面。
          // **不用模型的话描述成败** —— 徽章只认 SSE 里的 ok/verified。
          // 三态必须可辨：ok=false=未通过；ok=true&verified=false=已执行但没复验；
          // 两者合并成一种颜色就等于把这个功能白做。
          dropThink();
          aiRenderVerify(ev);
        } else if(ev.type === 'confirm_request'){
          dropThink();
          // 助手 tool_calls 消息进历史（协议要求 tool 结果必须跟随）
          const asst = {role:'assistant', content: curText, tool_calls:[{id:ev.tool_call_id, type:'function', function:{name:ev.tool, arguments:JSON.stringify(ev.args||{})}}]};
          if(rsnText) asst.reasoning_content = rsnText;
          if(curTs) asst.ts = curTs;
          aiMessages.push(asst);
          // v3.2：服务端在**同一个 SSE 流里**等我们的决定，这一轮还没结束。
          // 把当前气泡收尾：确认之后模型接着说的一段要另起一条，不能拼到旧气泡上
          // （拼上去会出现"结论 + 执行结果"粘在同一段里的错位）。
          cur = null; curText = ''; rsnText = ''; curTs = '';
          // 确认卡片用 DOM + textContent 构建：ev.text 含模型给的 args，直接 innerHTML 是注入面
          const card = document.createElement('div');
          card.className = 'ai-confirm';
          const cb = document.createElement('b'); cb.textContent = t('AI 请求执行：'); card.appendChild(cb);
          card.appendChild(document.createTextNode(ev.text || ev.tool));
          card.appendChild(document.createElement('br'));
          card.appendChild(document.createTextNode(t('确认后将真实执行该操作。')));
          // 批量授权（v3.2）：只有服务启停族给这个勾选。
          // 为什么只给这三个：它们在同一次任务里天然连着（停 → 起 → 复验），
          // 逐次点头换来的是三次上下文重建而不是安全；数据/系统类动作不在此列。
          let batchBox = null;
          if(AI_BATCH_TOOLS[ev.tool]){
            const lab = document.createElement('label'); lab.className = 'ai-batch';
            batchBox = document.createElement('input'); batchBox.type = 'checkbox';
            const sp = document.createElement('span');
            sp.textContent = t('本任务内后续同类操作不再询问（最多 2 次，仅限服务启停）');
            lab.appendChild(batchBox); lab.appendChild(sp); card.appendChild(lab);
          }
          const btns = document.createElement('div'); btns.className = 'btns';
          const by = document.createElement('button'); by.className = 'yes'; by.textContent = t('确认执行');
          const bn = document.createElement('button'); bn.className = 'no'; bn.textContent = t('拒绝');
          btns.appendChild(by); btns.appendChild(bn); card.appendChild(btns);
          $ai('ai-msgs').appendChild(card);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
          // 键盘可达：卡弹出后把焦点放到"确认执行"，Tab 两次就能批/拒。
          // 不移焦点的话，键盘用户要从输入框 Tab 过十几步才能摸到这两个按钮。
          try{ by.focus(); }catch(_){}
          aiPending = {asst, card, id: ev.tool_call_id, tool: ev.tool, args: ev.args || {},
            batchOf: ()=> !!(batchBox && batchBox.checked)};
          aiHstat(t('等你确认'));
          // 桌宠联动：等确认状态 + 蓝色徽标，点徽标/点宠物直接在迷你卡里批，不必开抽屉
          petAskConfirm(t('AI 请求执行'), ev.text || ev.tool, ()=>aiDecide(true), ()=>aiDecide(false));
          by.onclick = ()=>aiDecide(true);
          bn.onclick = ()=>aiDecide(false);
        } else if(ev.type === 'usage'){
          // 本轮消耗：尾部一行摘要（点开是 DSH 式明细卡），并累计到桌宠"今日统计"
          if(ev.usage){
            aiUsageAdd(ev.usage);
            aiTurnUsage = Object.assign({}, ev.usage, {
              model: ev.model || '', provider: ev.provider || '',
              steps: aiTurnNodes.length, ms: aiTurnStartAt ? (Date.now() - aiTurnStartAt) : 0
            });
            const host = cur || aiTurnAnchor;
            if(host) aiUsageLine(host, aiTurnUsage);
            if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
            $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
          }
        } else if(ev.type === 'waiting_confirm'){
          // 服务端已在本请求里等我们点按（v3.2）：这一轮没有结束，别当它已经答完
          aiHstat(t('等你确认'));
        } else if(ev.type === 'body_recovered'){
          // 服务端没收到完整请求体（代理/容器吃掉了），用请求头里的最近几条兜底恢复。
          // 必须让用户看见：他有权知道"刚才那段上下文没送达"，否则只会觉得模型变笨了。
          aiBodyWarn(ev.messages);
          aiTraceAdd('body', t('请求体未到达，已恢复 {0} 条消息', ev.messages), {cls:'warn'});
        } else if(ev.type === 'grant_open'){
          // 用户勾了"后续同类不再问"：把这件事显式说出来。
          // 不显示的话，后面几步不再弹卡就会变成"它怎么自己动了"——授权必须可见。
          aiTraceAdd('grant', t('已开启批量授权：{0} · 本任务内最多 {1} 次', t(ev.label || ''), ev.left), {cls:'warn'});
          aiAdd('sys', t('已授权：本任务内{0}最多再执行 {1} 次，不再逐次询问', t(ev.label || ''), ev.left));
          aiHstat(t('批量授权 · 剩 {0} 次', ev.left));
        } else if(ev.type === 'grant_used'){
          aiTraceAdd('grant', t('批量授权执行：{0}（剩余 {1} 次）', ev.text || aiToolName(ev.tool), ev.left), {cls:''});
          aiAdd('sys', t('已用批量授权执行：{0}（剩余 {1} 次）', ev.text || aiToolName(ev.tool), ev.left));
          aiHstat(t('批量授权 · 剩 {0} 次', ev.left));
        } else if(ev.type === 'info'){
          // 服务端阶段性提示（如"正在压缩较早的对话历史"）：换掉思考气泡的文案。
          // 不发这种提示时，摘要那次前置调用最长 60s 里用户只看到三点动画，
          // 第一反应就是"卡死了"去刷新，白烧一次 token。
          dropThink(); mkThink();
          const tip = document.createElement('div'); tip.className = 'ai-think-txt';
          // 后端发的是中文原文；t() 查不到键时原样返回，不会更糟
          tip.textContent = t(ev.text || '');
          think.appendChild(tip);
          $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
        } else if(ev.type === 'error'){
          dropThink(); aiErr(ev.text);
        } else if(ev.type === 'done'){
          /* 结束原因必须可见。断流 / 模型空回复 / 预算耗尽在界面上的样子
             与"正常答完"完全一样 —— 用户的描述只能是"它自己停了，
             不知道咋回事，得重新对话才能拿到结果"。
             completed 与 awaiting_confirm 是预期路径，不打扰。 */
          if(ev.reason && ev.reason !== 'completed' && ev.reason !== 'awaiting_confirm' && ev.note){
            dropThink();
            aiAdd('sys', t(ev.note));
          }
        }      }
    }
    if(cur){
      streamDone = true;             // 先关掉节流回调，免得它在最终渲染后把高亮冲掉
      cur.dataset.raw = curText;
      aiFill(cur, curText);          // 这一帧带代码高亮
      const fin = {role:'assistant', content: curText}; if(rsnText) fin.reasoning_content = rsnText; if(curTs) fin.ts = curTs; aiMessages.push(fin);
    }
  }catch(e){
    if(gen !== aiGen) return; // 已有新流开跑：这次 abort 是被取代的旧流，别再贴"已停止"消息
    if(e && e.name === 'AbortError'){ aiAdd('sys', t('已停止生成')); }
    else { if(think && think.parentNode) think.remove(); aiErr(t('连接中断：') + e); }
  }
  finally{
    // 只有**当代际**才复位全局态：旧流晚到的 finally 不能把新流的
    // aiBusy/按钮态冲掉（那会让输入框在流式中途重新可用，两条流交错贴消息）。
    if(gen === aiGen){
      if(think && think.parentNode) think.remove();
      aiBusy = false; aiCtrl = null;
    // 轨迹与用量**挂到消息元素上**：工具栏的「轨迹/导出」按元素取，
    // 不依赖"当前指向哪一轮"的全局指针——用户回头看历史回合时才不会串。
    const _dur = aiTurnStartAt ? (Date.now() - aiTurnStartAt) : 0;
    const _nodes = aiTurnNodes.slice();
    [aiTurnAnchor, cur].forEach(el=>{
      if(!el) return;
      el.__trace = _nodes; el.__turnUsage = aiTurnUsage; el.__dur = _dur;
    });
    const _u = aiTurnUsage || {};
    const _pct = aiCachePct(_u);
    aiTurnLog.push({at: aiNow().slice(6), tin: Number(_u.prompt_tokens) || 0, tout: Number(_u.completion_tokens) || 0,
      hit: Number(_u.cache_hit) || 0, miss: Number(_u.cache_miss) || 0, steps: _nodes.length, ms: _dur,
      model: _u.model || ''});
    if(aiTurnUsage){
      aiHstat('↑' + aiFmtTok(_u.prompt_tokens) + ' ↓' + aiFmtTok(_u.completion_tokens) +
        (_pct !== null ? ' · ' + t('缓存 {0}%', Math.round(_pct * 100)) : '') + ' · ' + t('{0} 步', _nodes.length));
    }
    if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
    $ai('ai-send').disabled = false; $ai('ai-stop').style.display = 'none';
    aiHistSave();   // 回合结束落盘（含确认卡决定后的回合）
    // 回合结束：思考/干活状态回待机；等确认或出错的状态保留（由用户决定/看日志后复位）
    petAIIdle();
    } // if(gen === aiGen)
  }
}
/* 手动停止：中断上游流式请求（服务端检测到断开会同步中止） */
$ai('ai-stop').onclick = ()=>{ if(aiCtrl){ try{ aiCtrl.abort(); }catch(e){} } };

$ai('ai-send').onclick = async ()=>{
  if(aiBusy) return;
  const raw = $ai('ai-in').value.trim();
  if(!raw && !aiImgs.length) return;   // 文字和图片都没有：什么都不发
  // 图片先落地（等所有上传完成），再组消息。失败项交给用户处理，不静默丢弃。
  const pics = await aiAttachTake();
  if(pics === null){ toast(t('图片都没载入成功，先别发；点缩略图上的 × 移除后再试'), 'err'); return; }
  // 已选文件只以"路径引用"的形式并入这条消息（读取由 read_file 在沙箱内完成）
  const tx = aiFileRef() + raw;
  $ai('ai-in').value = '';
  // 复用 aiSendText（剥悬挂卡、记时间戳、进历史、起流式回合都在里面）——
  // 这里原先把那套动作又抄了一遍，改一处漏一处的事故已经发生过一次。
  await aiSendText(tx, null, pics || []);
};
$ai('ai-in').addEventListener('keydown', e=>{
  // isComposing：中文输入法里按 Enter 是确认候选词，不是发送（否则拼音还没上屏消息就飞出去了）
  if(e.isComposing || e.keyCode === 229) return;
  if(e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); $ai('ai-send').click(); }
});

/* ---------- 错误一键发 AI 分析（桌宠右键「分析最近错误」） ---------- */
/* ekIsErrLine 纯函数：这一行算不算"错误"。
   抽成纯函数是为了能拿**构造的行**测规则：只用真实日志测的话，本机恰好没有
   某种坏值时断言会空转（过滤条件改坏了测试照样绿）。 */
function ekIsErrLine(line){
  const lvl = lvlOf(line);
  if(lvl !== '') return lvl === 'err';   // 有级别标签：只信标签
  return BARE_ERR_RE.test(line);         // 无标签：才走字面兜底（与界面标红同一套词）
}

/* ekMergeErrLines 纯函数：把 [{key,text}] 合成 [{text,keys,tag}]。
   同一条文本会同时出现在多个分区里（sys 级日志被广播到 install/config/web/
   backend/chain 五个区，见 appendLog），不合并的话模型会以为五个组件各自报错，
   而实际上只有一条。 */
function ekMergeErrLines(pairs){
  const byText = new Map();
  (pairs || []).forEach(p=>{
    if(!p || !ekIsErrLine(p.text)) return;
    const seen = byText.get(p.text) || new Set();
    seen.add(p.key); byText.set(p.text, seen);
  });
  const out = [];
  byText.forEach((keys, text)=>{
    const list = [...keys];
    out.push({text: text, keys: list, tag: list.length > 1 ? '[sys]' : '[' + list[0] + ']'});
  });
  return out;
}
// CDP 断言入口（tools/cdp_err_collect_test.js）
window.__ekErrProbe = {isErrLine: ekIsErrLine, merge: ekMergeErrLines};

function collectErrRows(){
  const pairs = [];
  for(const k in LOG){
    const L = LOG[k]; if(!L || !L.lines) continue;
    L.lines.forEach(l=>pairs.push({key: k, text: l}));
  }
  return ekMergeErrLines(pairs).slice(-15);   // 返回行对象：级别要按正文判，不能按拼好的前缀判
}
async function aiAnalyzeError(){
  const rows = collectErrRows();
  if(!rows.length){ toast(t('最近的日志里没有发现错误')); return; }
  if(aiBusy){ toast(t('助手正在忙，稍后再试'), 'err'); return; }
  aiOpen();
  const nErr = rows.filter(r=>lvlOf(r.text)==='err').length;
  const lines = rows.map(r=>r.tag + ' ' + r.text);
  // 标题必须和内容一致：只说"错误日志"而实际全是警告时，模型会顺着这个前提
  // 硬找一个不存在的故障 —— 实测被要求"查看错误日志"，而日志里根本没有错误。
  const tx = t('我遇到了环境问题，以下是最近的日志（错误 {0} 条、警告 {1} 条）。请先判断里面是否真的有错误，再给解决步骤；如果只有警告，请直接说明要不要处理：\n',
    nErr, rows.length - nErr) + lines.join('\n');
  await aiSendText(tx);
}

/* 诊断报告 AI 解读 */
$ai('btn-ai-explain').onclick = ()=>{
  if(aiBusy) return;   // 连点两次会并发两条流（aiConsume 入口原本不设防）
  aiMessages = [];
  aiConsume('/api/ai/explain', {});
};
$ai('btn-ai-open-chat').onclick = aiOpen;


/* v3.3：日志默认折叠与审计趋势折叠已改为标记层初始态（class=collapsed / <details>），
   这里原先那段'加载后替用户点按钮'的脚本已删除——界面初始状态不该依赖脚本时序。 */

/* ==================== 设置页（v3.4） ====================
   三区共用同一套只读行渲染（ekRo）：基线与看板本质都是「键 → 值 / 出处」。
   两条铁律：
   1) 打开就重读（goto 里调 ekSettingsEnter）：本页与「程序配置」「链端校验」
      共用同一份 config.json，任何一侧改完，另一侧不该还显示旧值。
   2) 缺数据不许显示成 0 或「—」，那等于谎报 —— 一律写清缺什么。
*/
/* EK_SET 已声明在文件顶部（goto 的深链调用早于本段求值，见那里的注释）。 */

function ekSettingsEnter(){ ekSetPane(EK_SET.tab || 'base'); }

function ekSetPane(name){
  const tabs = document.getElementById('set-tabs');
  if(tabs) tabs.querySelectorAll('.cfg-tab').forEach(b=>b.classList.toggle('on', b.dataset.setTab===name));
  document.querySelectorAll('#panel-settings .cfg-pane').forEach(p=>p.classList.toggle('show', p.dataset.setPane===name));
  EK_SET.tab = name;
  if(name==='base'){ ekSetLoadBase(); ekSetLoadExtras(); }
  else if(name==='dev') ekSetLoadDev();
  else if(name==='eval'){ ekEvLoad(); ekEvPoll(true); }
  if(name!=='eval') ekEvPoll(false);
}

/* 只读键值列表：rows = [{k, v, cls}]，cls 取 ok/bad/warn */
function ekRo(sel, rows){
  const el = typeof sel==='string' ? $(sel) : sel;
  if(!el) return;
  if(!rows || !rows.length){ el.innerHTML = '<div class="ro-empty">' + t('暂无可显示的内容') + '</div>'; return; }
  el.innerHTML = rows.map(r=>{
    const v = (r.v==null || r.v==='') ? '—' : r.v;
    /* 键也过一遍 t()：这些标签是渲染时拼进 innerHTML 的，applyI18n 扫不到，
       不过 t() 的话英文界面下这里会是一列中文，和旁边已经译好的文字混排。 */
    return '<div class="ro-row"><span class="k">'+esc(t(r.k))+'</span>' +
           '<span class="v'+(r.cls?' '+r.cls:'')+'">'+esc(v)+'</span></div>';
  }).join('');
}

