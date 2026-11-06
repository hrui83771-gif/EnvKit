/* ---------- AI 助手 ---------- */
// 服务商官方品牌图标（内联 SVG，源文件 tools/brand-icons/*.svg，
// 由 tools/brand_icons_build.py 注入——改图标改源文件后重跑脚本，勿手改这里）
// 服务商官方品牌图标（内联 SVG，源文件 tools/brand-icons/*.svg，
// 由 tools/brand_icons_build.py 注入——改图标改源文件后重跑脚本，勿手改这里）
/*BRAND-ICONS:BEGIN*/
const AI_PROV_ICONS = {
  deepseek:{vb:'0 0 24 24',fill:'#5786FE',svg:'<title>DeepSeek</title><path d="M23.748 4.651c-.254-.124-.364.113-.512.233-.051.04-.094.09-.137.137-.372.397-.806.657-1.373.626-.829-.046-1.537.214-2.163.848-.133-.782-.575-1.248-1.247-1.548-.352-.155-.708-.311-.955-.65-.172-.24-.219-.509-.305-.774-.055-.16-.11-.323-.293-.35-.2-.031-.278.136-.356.276-.313.572-.434 1.202-.422 1.84.027 1.436.633 2.58 1.838 3.393.137.094.172.187.129.323-.082.28-.18.553-.266.833-.055.179-.137.218-.328.14a5.5 5.5 0 0 1-1.737-1.179c-.857-.828-1.631-1.743-2.597-2.46a12 12 0 0 0-.689-.47c-.985-.957.13-1.743.387-1.836.27-.098.094-.433-.778-.428-.872.003-1.67.295-2.687.685a3 3 0 0 1-.465.136 9.6 9.6 0 0 0-2.883-.101c-1.885.21-3.39 1.1-4.497 2.622C.082 8.776-.231 10.854.152 13.02c.403 2.284 1.568 4.175 3.36 5.653 1.857 1.533 3.997 2.284 6.438 2.14 1.482-.085 3.132-.284 4.994-1.86.47.234.962.328 1.78.398.629.058 1.235-.031 1.705-.129.735-.155.684-.836.418-.961-2.155-1.004-1.682-.595-2.112-.926 1.095-1.295 2.768-3.598 3.284-6.733.05-.346.115-.834.108-1.114-.004-.171.035-.238.23-.257a4.2 4.2 0 0 0 1.545-.475c1.397-.763 1.96-2.016 2.093-3.517.02-.23-.004-.467-.247-.588M11.58 18.168c-2.088-1.642-3.101-2.183-3.52-2.16-.39.024-.32.472-.234.763.09.288.207.487.371.74.114.167.192.416-.113.603-.673.416-1.842-.14-1.897-.168-1.361-.801-2.5-1.86-3.301-3.306-.775-1.393-1.225-2.888-1.299-4.482-.02-.385.094-.522.477-.592a4.7 4.7 0 0 1 1.53-.038c2.131.311 3.946 1.264 5.467 2.774.868.86 1.525 1.887 2.202 2.89.72 1.066 1.494 2.082 2.48 2.915.348.291.626.513.892.677-.802.09-2.14.109-3.055-.615zm1.001-6.44a.306.306 0 0 1 .415-.287.3.3 0 0 1 .113.074.3.3 0 0 1 .086.214c0 .17-.136.307-.308.307a.303.303 0 0 1-.306-.307m3.11 1.596c-.2.081-.4.151-.591.16a1.25 1.25 0 0 1-.798-.254c-.274-.23-.47-.358-.551-.758a1.7 1.7 0 0 1 .015-.588c.07-.327-.007-.537-.238-.727-.188-.156-.426-.199-.689-.199a.6.6 0 0 1-.254-.078.253.253 0 0 1-.114-.358 1 1 0 0 1 .192-.21c.356-.202.767-.136 1.146.016.352.144.618.408 1.001.782.392.451.462.576.685.915.176.264.336.536.446.848.066.194-.02.353-.25.45"/>'},
  openai:{vb:'1.68 1.75 16.65 16.5',fill:'currentColor',svg:'<path d="M11.248 18.25q-.825 0-1.568-.314a4.3 4.3 0 0 1-1.32-.874 4 4 0 0 1-1.304.214 4 4 0 0 1-2.046-.544 4.27 4.27 0 0 1-1.518-1.485 4 4 0 0 1-.56-2.095q0-.48.131-1.04A4.4 4.4 0 0 1 2.04 10.71a4.07 4.07 0 0 1 .017-3.4 4.2 4.2 0 0 1 1.056-1.418 3.8 3.8 0 0 1 1.6-.842 3.9 3.9 0 0 1 .76-1.683q.593-.759 1.451-1.188a4.04 4.04 0 0 1 1.832-.429q.825 0 1.567.313.742.314 1.32.875a4 4 0 0 1 1.304-.215q1.106 0 2.046.545a4.14 4.14 0 0 1 1.501 1.485q.578.941.578 2.095 0 .48-.132 1.04.66.61 1.023 1.419.363.792.363 1.666 0 .892-.38 1.717a4.3 4.3 0 0 1-1.072 1.435 3.8 3.8 0 0 1-1.584.825 3.8 3.8 0 0 1-.775 1.683 4.06 4.06 0 0 1-1.436 1.188 4.04 4.04 0 0 1-1.832.429m-4.076-2.062q.825 0 1.435-.347l3.103-1.782a.36.36 0 0 0 .164-.313v-1.42L7.881 14.62a.67.67 0 0 1-.726 0l-3.118-1.798a.5.5 0 0 1-.017.115v.198q0 .841.396 1.551.413.693 1.139 1.089a3.2 3.2 0 0 0 1.617.412m.165-2.69a.4.4 0 0 0 .181.05q.083 0 .165-.05l1.238-.71-3.977-2.31a.7.7 0 0 1-.363-.643v-3.58q-.825.362-1.32 1.122a2.9 2.9 0 0 0-.495 1.65q0 .809.413 1.55.412.743 1.072 1.123zm3.91 3.663q.875 0 1.585-.396a2.96 2.96 0 0 0 1.534-2.64v-3.564a.32.32 0 0 0-.165-.297l-1.254-.726v4.604a.7.7 0 0 1-.363.643l-3.119 1.799a3 3 0 0 0 1.783.577m.627-6.039V8.878L10.01 7.822 8.129 8.878v2.244l1.881 1.056zM7.057 5.859a.7.7 0 0 1 .363-.644l3.119-1.798a3 3 0 0 0-1.782-.578q-.874 0-1.584.396A2.96 2.96 0 0 0 6.05 4.324a3.07 3.07 0 0 0-.396 1.551v3.547q0 .199.165.314l1.237.726zm8.383 7.887q.825-.364 1.303-1.123.495-.758.495-1.65a3.15 3.15 0 0 0-.412-1.55q-.413-.743-1.073-1.123l-3.086-1.782q-.099-.065-.181-.049a.3.3 0 0 0-.165.05l-1.238.692 3.993 2.327a.6.6 0 0 1 .264.264.64.64 0 0 1 .1.363zm-3.317-8.382a.63.63 0 0 1 .726 0l3.135 1.831v-.297q0-.792-.396-1.501a2.86 2.86 0 0 0-1.105-1.155q-.71-.43-1.65-.43-.825 0-1.436.347L8.294 5.941a.36.36 0 0 0-.165.314v1.418z"/>'},
  kimi:{vb:'0 0 24 24',fill:'currentColor',svg:'<title>Kimi</title><path d="M21.765.351C22.998.351 24 1.353 24 2.586S22.998 4.82 21.765 4.82h-1.974c-.15 0-.26-.12-.26-.26V2.586A2.237 2.237 0 0 1 21.765.35M9.41 13.388l8.447-8.377c.16-.16.07-.471-.14-.471h-4.55s-.1.02-.14.06l-9.099 9.029c-.14.14-.35.02-.35-.21V4.81c0-.15-.1-.27-.221-.27H.22c-.12 0-.22.12-.22.27v18.57c0 .15.1.27.22.27h3.137c.12 0 .22-.12.22-.27v-3.79c0-.08.03-.16.08-.21l2.826-2.796c.07-.07.16-.08.241-.03l7.546 5.551a8.9 8.9 0 0 0 4.018 1.493c.12.01.23-.11.23-.27V19.76c0-.14-.08-.25-.19-.26a5.8 5.8 0 0 1-2.355-.942l-6.533-4.73c-.14-.09-.15-.32-.03-.441"/>'},
  grok:{vb:'201.61 56.91 438.67 481.45',fill:'currentColor',svg:'<g> <polygon points="557.09,211.99 565.4,538.36 631.96,538.36 640.28,93.18 "/> <polygon points="640.28,56.91 538.72,56.91 379.35,284.53 430.13,357.05 "/> <polygon points="201.61,538.36 303.17,538.36 353.96,465.84 303.17,393.31 "/> <polygon points="201.61,211.99 430.13,538.36 531.69,538.36 303.17,211.99 "/> </g>'},
  gemini:{vb:'0 0 24 24',fill:'#8E75B2',svg:'<title>Google Gemini</title><path d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81"/>'},
  custom:{vb:'0 0 24 24',fill:'currentColor',svg:'<path d="M3 17.25V21h3.75L17.81 9.94l-3.75-3.75L3 17.25zM20.71 7.04a1 1 0 0 0 0-1.41l-2.34-2.34a1 1 0 0 0-1.41 0l-1.83 1.83 3.75 3.75 1.83-1.83z"/>'}
};
/*BRAND-ICONS:END*/
let aiMessages = [];   // OpenAI 格式消息历史（内存 + localStorage 持久化，刷新页面不丢）
let aiBusy = false;
const $ai = id => document.getElementById(id);

/* 待发送的图片（拖入 / 粘贴进来、还没随消息发出去的）。
   同样是**状态必须放顶部**：aiHistRestore 在页面加载时就会跑，它要清理上次残留的
   `aiImgs`（附件已落盘但消息没发出，重开后不该浮在输入区）。 */
let aiImgs = [];

/* 待发送的**文档**（拖进来的 docx/xlsx/pptx/pdf/文本，正文已由后端落盘）。
   与 aiImgs 并列：这里只放引用（id/name/kind/chars），正文从不进浏览器 ——
   否则又变成"几千字贴进输入框"，而用户要的是一个文件附件。 */
let aiDocs = [];

/* ---------- 放大态 / 检查器 / 轨迹 / 文件引用的**状态** ----------
   声明必须早于 aiAdd 与 aiHistRestore：aiAdd 会读 aiTurnAnchor，
   而 let 变量在声明前处于 TDZ，读到就抛 ReferenceError（恢复历史时会炸）。
   所以状态放这里，函数留在后面的改造区块。 */
let aiBigOn = false;
let aiSideTab = 'trace';
let aiTurnNodes = [];      // 本回合轨迹节点（确认续接轮继续累加，不重置）
let aiStepAt = 0;          // 上一个"执行"节点的时间戳：用来算每一步的墙钟耗时
let aiTurnUsage = null;    // 本回合 token
let aiTurnStartAt = 0;
let aiTurnAnchor = null;   // 本回合第一条助手消息（轨迹挂回它，避免缺前半段）
let aiTurnLog = [];        // 会话内每回合用量摘要
let aiTraceView = null;    // 当前查看的历史回合元素（null = 本回合）
let aiAuditRows = null;
let aiAuditWin = null;     // 审计时间窗：从「轨迹」跳过来时只看本轮
let aiFiles = [];          // 待引用的文件路径
let aiPickPath = '';
let aiCurMission = null;   // 当前绑定的行动 ID（Phase 1）：随 /api/ai/chat 带 mission_id；
                           // 由 aiSendText 第二参设置，普通对话会清掉它
let aiMissionRows = null;  // 行动列表缓存（SSE 推送直接更新它）
let aiMissionES = null;    // 行动 SSE 句柄（只订阅一次，EventSource 自动重连）
// 行动列表默认只铺最近若干条（面板很窄，200 条旧账会把「预案库」顶到屏幕外）。
// 折叠只是**显示**上的，数据全在内存里，不动 missions.json。
// 状态必须放在这里：SSE 每 2 秒可能触发重渲染，放渲染函数里会被重置成"收起"。
let aiMsShowAll = false;
const AI_MS_SHOW = 8;
let aiPbkRows = null;      // 预案库缓存（Phase 2）：行动卡片的预案标题也读它。
                           // 必须声明在文件顶部这一批里 —— 放到使用处（aiPbkLoad 附近）
                           // 会踩 TDZ：渲染函数在声明语句执行前被调用就会 ReferenceError，
                           // 而 node --check 查不出来（本项目已经吃过一次）。
let aiGrowthData = null;   // 成长报表缓存（Phase 3）：同理，声明必须在顶部。
const AI_KIND = {plan:'计划', step:'执行', result:'结果', ver:'复验', human:'等确认', grant:'授权', body:'传输', done:'结论'};
/* 允许"本任务内不再逐次询问"的工具（与服务端 aiGrantFamily 一一对应）。
   放在前端是为了**只在能批量的卡上显示那个勾选**——数据类/系统类动作不给这个选项，
   免得上去了又被服务端拒（那才是真的会误导人）。 */
const AI_BATCH_TOOLS = {start_service:1, stop_service:1, restart_service:1};

function aiOpen(){ $ai('ai-drawer').classList.add('open'); $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight; petShift(true);
  // 放大态：打开时把当前 tab 重画一遍（轨迹是流式追加的，重开抽屉要能接上）
  if(aiBigOn){ if(aiSideTab === 'trace') aiTraceRender(); if(aiSideTab === 'usage') aiUsageRender(); }
  // 焦点跟进输入框：抽屉是类 dialog 的浮层，打开后键盘焦点若留在下面的页面，
  // Tab 会"穿"到看不见的元素上（键盘用户直接迷路）。
  try{ $ai('ai-in').focus(); }catch(_){}
}
$ai('ai-close').onclick = ()=>{ $ai('ai-drawer').classList.remove('open'); aiPickClose(); petShift(false); };
/* ---------- 清空对话：10 秒内可撤销 ----------
   以前点一下就抹掉整段排障过程、附件引用和行动绑定，零确认也捞不回来。
   快照存的是 **DOM 节点**（整段搬进 DocumentFragment），不是重放 aiMessages：
   工具卡、确认卡、时间戳都不在 aiMessages 里，重放会把它们弄丢。
   清空本身仍立即生效、立即删 localStorage —— 既有 CDP 断言不受影响，
   也不弹 confirm（那会把现有 4 个直接 .click() 清空的回归脚本挂住）。 */
function aiClearSnapshot(){
  const box = $ai('ai-msgs');
  const frag = document.createDocumentFragment();
  while(box.firstChild) frag.appendChild(box.firstChild);
  let hist = null;
  try{ hist = localStorage.getItem('ek-ai-hist'); }catch(_){}
  return {frag:frag, hist:hist, msgs:aiMessages.slice(), mission:aiCurMission,
          imgs:aiImgs.slice(), docs:aiDocs.slice(), files:aiFiles.slice()};
}
function aiClearUndo(snap){
  if(!snap) return;
  // 撤销窗口里用户已经发了新消息：绝不能把新对话覆盖掉
  if(aiMessages.length){ toast(t('已经开始了新对话，无法撤销清空')); return; }
  const box = $ai('ai-msgs');
  box.innerHTML = ''; box.appendChild(snap.frag);
  box.scrollTop = box.scrollHeight;
  aiMessages = snap.msgs.slice();
  try{
    if(snap.hist === null) localStorage.removeItem('ek-ai-hist');
    else localStorage.setItem('ek-ai-hist', snap.hist);
  }catch(_){}
  aiCurMission = snap.mission;
  aiImgs = snap.imgs.slice(); aiDocs = snap.docs.slice(); aiFiles = snap.files.slice();
  aiAttachRender(); aiChipsRender(); aiTurnReset();
  toast(t('对话已恢复'));
}
$ai('ai-clear').onclick = ()=>{
  // 连点两次清空：沿用第一次的快照，否则第二次存进去的只剩「对话已清空」那句，
  // 撤销回来就是空的。快照挂在 toast 元素上，随 toast 过期一起回收，不用全局状态。
  const old = document.querySelector('#toasts .toast-undo');
  const snap = (old && old._ekUndoSnap) ? old._ekUndoSnap : aiClearSnapshot();
  document.querySelectorAll('#toasts .toast-undo').forEach(e=>e.remove());
  // 正在流式输出时点清空：必须先掐断流。不清的话，后续 SSE 事件会继续往
  // 已脱离文档的孤儿节点上贴卡片（表现为"清空后凭空冒出确认卡"），
  // aiBusy 也仍然为 true 直到流自然结束。
  if(aiCtrl){ try{ aiCtrl.abort(); }catch(_){} aiCtrl = null; }
  if(aiPending){ try{ aiPending.card.remove(); }catch(_){} aiPending = null; }
  petConfirmDone('idle');   // 抽屉卡清了，桌宠迷你卡与 confirm 态必须一起清
  aiMessages = []; try{ localStorage.removeItem('ek-ai-hist'); }catch(_){} $ai('ai-msgs').innerHTML = '<div class="ai-msg sys">' + t('对话已清空。') + '</div>';
  aiCurMission = null;   // 清空对话 = 解除行动绑定（行动本身仍在列表里，可从「行动」tab 继续）
  // 待发附件一并清掉：对话都清了，还挂着一张图/一份文件在输入区上方，下次发送会莫名其妙带上它
  aiImgs = []; aiDocs = []; aiAttachRender();
  aiFiles = []; aiChipsRender(); aiPickClose(); aiTurnReset(); aiHstat('');
  const el = toastUndo(t('对话已清空'), t('撤销'), ()=>aiClearUndo(snap), 10000);
  if(el) el._ekUndoSnap = snap;
};

/* ---------- 彻底隐藏桌面助手（AI 设置页勾选项，localStorage 记忆） ---------- */
function applyPetGone(){
  let gone = false;
  try{ gone = localStorage.getItem('ek-pet-gone') === '1'; }catch(_){}
  const cb = document.getElementById('ai_pet_hide');
  if(cb) cb.checked = gone;
  document.body.classList.toggle('pet-gone', gone);
}
applyPetGone();
$ai('ai_pet_hide').addEventListener('change', function(){
  const gone = this.checked;
  try{ localStorage.setItem('ek-pet-gone', gone ? '1' : '0'); }catch(_){}
  document.body.classList.toggle('pet-gone', gone);
  toast(gone ? t('助手已彻底隐藏，可随时在这里恢复') : t('助手已回来啦'));
});

/* ---------- AI 用量统计：单次消耗行（聊天框）+ 今日累计与余额（桌宠悬停卡） ---------- */
function aiFmtTok(n){
  n = Math.round(Number(n)||0);
  // 百万级用 M：长对话的上下文动辄 5,000k，写成 "5463.1k" 没人读得出来量级
  if(n >= 1000000) return (n/1000000).toFixed(1) + 'M';
  return n >= 10000 ? (n/1000).toFixed(1) + 'k' : String(n);
}
function aiUsageAcc(){
  let u = {date:'', calls:0, tin:0, tout:0, hit:0, miss:0};
  try{ u = Object.assign(u, JSON.parse(localStorage.getItem('ek-ai-usage')||'{}')); }catch(_){}
  const today = new Date().toDateString();
  if(u.date !== today){ u = {date:today, calls:0, tin:0, tout:0, hit:0, miss:0}; }   // 按天清零
  return u;
}
function aiUsageAdd(u){
  if(!u) return;
  const a = aiUsageAcc();
  a.calls++;
  a.tin += Math.round(Number(u.prompt_tokens)||0);
  a.tout += Math.round(Number(u.completion_tokens)||0);
  a.hit += Math.round(Number(u.cache_hit)||0);
  a.miss += Math.round(Number(u.cache_miss)||0);
  try{ localStorage.setItem('ek-ai-usage', JSON.stringify(a)); }catch(_){}
}
let aiBal = {state:'idle', total:'', currency:'', available:null};   // idle|ok|no|err
async function aiBalanceRefresh(){
  try{
    const j = await (await fetch('/api/ai/balance')).json();
    if(!j.supported){ aiBal = {state:'no', total:'', currency:'', available:null}; }
    else if(j.ok){ aiBal = {state:'ok', total:String(j.total||''), currency:String(j.currency||''), available:j.available}; }
    else { aiBal = {state:'err', total:'', currency:'', available:null}; }
  }catch(e){ aiBal = {state:'err', total:'', currency:'', available:null}; }
}
/* j.available 是 DeepSeek 的 is_available：false = 账户被停用/欠费冻结，
   此时余额数字再好看也调不动模型。显示上必须把它单独说出来，
   否则用户看到"余额 ¥12.5"以为一切正常，实际每次请求都被上游拒。 */
function aiBalText(){
  const base = aiBal.currency === 'CNY' ? t('余额 ¥{0}', aiBal.total) : t('余额 {0} {1}', aiBal.total, aiBal.currency);
  return (aiBal.available === false) ? base + ' · ' + t('账户已停用') : base;
}
function petUsageRender(){
  const box = document.getElementById('pet-tipUsage'); if(!box) return;
  const u = aiUsageAcc();
  box.textContent = '';
  const sep = document.createElement('div'); sep.className = 'u-sep'; box.appendChild(sep);
  const hd = document.createElement('b'); hd.textContent = t('今日统计'); box.appendChild(hd);
  box.appendChild(document.createElement('br'));
  const l1 = document.createElement('span');
  l1.textContent = t('对话 {0} 次 · ↑{1} ↓{2}', u.calls, aiFmtTok(u.tin), aiFmtTok(u.tout));
  box.appendChild(l1); box.appendChild(document.createElement('br'));
  if(u.hit + u.miss > 0){
    const l2 = document.createElement('span');
    l2.textContent = t('缓存命中率 {0}%', Math.round(100*u.hit/(u.hit+u.miss)));
    box.appendChild(l2); box.appendChild(document.createElement('br'));
  }
  const l3 = document.createElement('span');
  if(aiBal.state === 'ok'){
    l3.textContent = aiBalText();
    if(aiBal.available === false) l3.style.color = 'var(--muted)';
  } else if(aiBal.state === 'no'){
    l3.textContent = t('仅 DeepSeek 支持');
    l3.style.color = 'var(--muted)';
  } else if(aiBal.state === 'err'){
    l3.textContent = t('余额查询失败');
  } else {
    l3.textContent = '…';
  }
  box.appendChild(l3);
  const ref = document.createElement('span');
  ref.className = 'bal-ref'; ref.textContent = '↻'; ref.title = t('刷新余额');
  ref.onclick = async ()=>{ ref.textContent = '…'; await aiBalanceRefresh(); petUsageRender(); };
  box.appendChild(ref);
}
aiBalanceRefresh();                                    // 进页面查一次余额（非 DeepSeek 静默降级）
setInterval(aiBalanceRefresh, 10*60*1000);             // 每 10 分钟静默刷新

/* 消息时间戳：MM-DD HH:mm，挂在每轮回合末尾的小工具栏里。
   原来那行气泡内左侧小灰字只有 HH:MM，用户反馈"看不清"——没有日期，
   跨天和多日前的对话根本定位不到是哪个回合。时间配月日后放进工具栏，
   "什么时候问的"和"复制/重新提问"放在一起，一眼找得到。
   ts 只认合法格式（本机生成/历史存档回读都过这关）；旧档是 HH:MM 无日期，
   也放行并按原样显示（不编日期）。 */
function aiTsOk(s){ return typeof s === 'string' && /^(([01]\d|2[0-3]):[0-5]\d|(0[1-9]|1[0-2])-(0[1-9]|[12]\d|3[01]) ([01]\d|2[0-3]):[0-5]\d)$/.test(s); }
function aiNow(){ const d = new Date(); return ('0'+(d.getMonth()+1)).slice(-2)+'-'+('0'+d.getDate()).slice(-2)+' '+('0'+d.getHours()).slice(-2)+':'+('0'+d.getMinutes()).slice(-2); }

/* [envkit-verdict] 结论块是**给评测判据看的机器格式**（判据优先读它、词表降级兜底），
   不是给用户看的——用户原话"用户不需要看到"。
   **只在展示层剥**：SSE 原文、aiMessages 历史、localStorage 存档全部保持原样。
   在 Go 侧或入历史前剥，都会让判据的 verdict 覆盖率归零——沙箱采集的就是 SSE 原文。
   从第一次出现处切到文尾：块指令要求"块的三行之后不要再写别的内容"，
   所以切到文尾永不误伤正文；流式中途记号没打完（'[envkit-ver'）也一并切掉，
   免得半截记号在屏幕上闪一下。 */
function aiStripVerdict(s){
  // 守卫查公共前缀 '[envkit-ver'（完整记号与流式中途的半截都含它）——
  // 查完整记号的话，半截记号在早退这里就被放行了，下面两行剥离永远不执行
  if(typeof s !== 'string' || s.indexOf('[envkit-ver') < 0) return s;
  return s.replace(/\[envkit-verdict\][\s\S]*$/, '').replace(/\[envkit-ver$/, '').replace(/\s+$/, '');
}
function aiFill(d, text, opts){
  d.innerHTML = aiMd(aiStripVerdict(text));
  d.classList.add('ai-md');
  if(!opts || opts.hl !== false) aiHighlight(d);
}
function aiAdd(cls, text, ts, reask){
  const d = document.createElement('div');
  d.className = 'ai-msg ' + cls;
  /* data-no-i18n：模型/用户的原文不进翻译管线。
     两个收益：① 全局 i18n Observer 不再对每次流式 innerHTML 重建后的新子树
     整棵重扫（长回答下这是 O(n) 每帧，体感卡顿）；② 模型输出恰好和字典键
     撞车时不会被"顺手"翻成英文。静态界面文案照常翻译（它们不在这个容器里）。 */
  d.setAttribute('data-no-i18n', '');
  if(aiTsOk(ts)) d.dataset.ts = ts;
  // 本回合第一条助手消息：确认续接轮会再建一条，轨迹要挂回第一条才不会缺前半段
  if(cls === 'ai' && !aiTurnAnchor) aiTurnAnchor = d;
  aiFill(d, text);
  if(cls === 'ai'){
    // 复制给用户的也必须是剥干净的文本——结论块是调试信息，不进剪贴板
    d.dataset.raw = aiStripVerdict(text || '');
    const w = document.createElement('div'); w.className = 'ai-wrap';
    w.setAttribute('data-no-i18n', '');
    w.appendChild(d);
    w.appendChild(aiToolbar(d, reask));
    $ai('ai-msgs').appendChild(w);
  } else {
    $ai('ai-msgs').appendChild(d);
  }
  $ai('ai-msgs').scrollTop = $ai('ai-msgs').scrollHeight;
  return d;
}
/* 回合末尾小工具栏：时间 + 复制 + 重新提问。
   reask 是触发这一回合的那条用户原话；为空（如「解读诊断报告」这种无用户提问的回合）
   就不放「重新提问」——没有可重问的问题，放一个按钮只会误导。 */
function aiToolbar(d, reask){
  const tb = document.createElement('div'); tb.className = 'ai-tb';
  if(d.dataset.ts){
    const tm = document.createElement('span'); tm.className = 'ai-tb-time'; tm.textContent = d.dataset.ts;
    tb.appendChild(tm);
  }
  const c = document.createElement('button'); c.className = 'ai-tb-btn'; c.textContent = t('复制');
  c.onclick = ()=>{
    const raw = d.dataset.raw || d.innerText;
    navigator.clipboard.writeText(raw).then(()=>{ c.textContent = t('已复制'); c.classList.add('done');
      setTimeout(()=>{ c.textContent = t('复制'); c.classList.remove('done'); }, 1600); })
      .catch(()=>{ c.textContent = t('失败'); setTimeout(()=>{ c.textContent = t('复制'); }, 1600); });
  };
  tb.appendChild(c);
  if(reask){
    const r = document.createElement('button'); r.className = 'ai-tb-btn'; r.textContent = t('重新提问');
    r.onclick = ()=>{
      if(aiBusy){ toast(t('助手正在忙，稍后再试')); return; }
      // 重新提问 = 拿**同一份材料**再问一次。只带问题文本会把图片/文档引用丢掉，
      // 模型会回「我没收到附件」——和 aiConsume 白名单漏字段是同一类静默丢附件。
      const src = aiMessages.slice().reverse().find(m=>m && m.role === 'user') || {};
      aiSendText(reask, null, src.images || [], src.docs || []);
    };
    tb.appendChild(r);
  }
  /* 轨迹 / 审计 / 导出：三个按钮都对应真实数据，不做装饰。
     「审计」按本轮轨迹的时间窗过滤——没有 trace_id 时这是最诚实的做法，
     比"猜哪几条属于这轮"可靠，也不会把别的回合算进来。 */
  const tr = document.createElement('button'); tr.className = 'ai-tb-btn'; tr.textContent = t('轨迹');
  // 点这条消息的「轨迹」= 看本回合，所以要显式切回来：
  // 否则用户上次停在历史列表，点这里会打开历史列表（看起来像没反应）。
  tr.onclick = ()=>{ if(typeof aiTrMode !== 'undefined'){ aiTrMode = 'turn'; aiTrDetail = null; } aiTraceView = d; aiSide('trace'); };
  tb.appendChild(tr);
  const au = document.createElement('button'); au.className = 'ai-tb-btn'; au.textContent = t('审计');
  au.onclick = ()=>{ aiAuditFocus(d.__trace); aiSide('audit'); };
  tb.appendChild(au);
  const ex = document.createElement('button'); ex.className = 'ai-tb-btn'; ex.textContent = t('导出');
  ex.onclick = ()=>aiExportTurn(d);
  tb.appendChild(ex);
  return tb;
}
/* 发送一段用户文本：发送按钮与「重新提问」共用。
   抽成函数是因为重新提问要复刻发送的全套动作——剥悬挂确认卡、记时间戳、进历史、
   起流式回合；两处各写一遍，迟早只改一处（确认卡悬挂那次事故就是这么来的）。 */
async function aiSendText(tx, missionId, imgs, docs){
  if(aiBusy) return;
  tx = String(tx || '').trim();
  // imgs：已经落盘的图片引用（{id,name,mime}）。只有图片、没写字也要能发 ——
  // "你看看这个报错"在截图场景里本来就只是一张图。
  const pics = (imgs || []).filter(x=>x && x.id);
  // docs：已经抽取并落盘的文档引用（{id,name,kind,chars}）。同理，"就这个文件，你看下"也要能发。
  const dlist = (docs || []).filter(x=>x && x.id);
  if(!tx && !pics.length && !dlist.length) return;
  // 行动绑定（Phase 1）：带 missionId = 本回合是该行动的执行回合；
  // 不带 = 普通对话，清掉旧绑定（一次对话只属于一个行动，红线 5 同源）。
  aiCurMission = missionId || null;
  aiTurnReset();   // 新的一轮用户发言 = 新的一条轨迹（确认续接轮不重置）
  // 用户无视确认卡片直接发新消息：把悬挂的 assistant(tool_calls) 从历史里剥掉，
  // 否则下一轮请求历史变成 [..., assistant(tool_calls), user(新消息)]，上游直接 400
  if(aiPending){ aiPending.card.remove(); const ix = aiMessages.indexOf(aiPending.asst); if(ix >= 0) aiMessages.splice(ix, 1); aiPending = null; }
  petConfirmDone('idle');   // 悬挂回合被清掉时，桌宠那边也必须收口，别留一张点了没反应的空卡
  const sendTs = aiNow();
  const el = aiAdd('user', tx, sendTs);
  const um = {role:'user', content:tx, ts:sendTs};
  if(pics.length){
    um.images = pics;
    aiMsgImgsRender(el, pics);   // 气泡里回显：用户得能确认"我发的确实是这张"
  }
  if(dlist.length){
    // 气泡里只回显**文件名卡片**，正文不进 DOM —— 这正是"附件"与"粘贴一大段"的区别
    um.docs = dlist;
    aiMsgDocsRender(el, dlist);
  }
  aiMessages.push(um);
  await aiConsume('/api/ai/chat', {messages: aiMessages});
}
/* ---------- Markdown 渲染 ----------
   以前这里是 4 行正则（围栏码 / 行内码 / 粗体 / 列点），模型输出的标题、有序列表、
   表格、链接全都退化成纯文本。现在交给 marked 解析，代码块交给 highlight.js。

   安全：模型输出是**不可信输入**（日志、网页、用户文件都可能进上下文），
   所以解析后过一遍白名单净化——脚本/事件属性/危险协议一律去掉，外链图片不加载。
   复制仍用 dataset.raw（原文），渲染方式的变化不影响剪贴板内容。 */
function aiEscHtml(s){
  return String(s == null ? '' : s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}
const AI_MD_OK = {P:1,BR:1,HR:1,STRONG:1,B:1,EM:1,I:1,DEL:1,S:1,CODE:1,PRE:1,
  BLOCKQUOTE:1,UL:1,OL:1,LI:1,H1:1,H2:1,H3:1,H4:1,H5:1,H6:1,A:1,
  TABLE:1,THEAD:1,TBODY:1,TR:1,TH:1,TD:1,INPUT:1};
function aiSanitizeHtml(html){
  const tpl = document.createElement('template');
  tpl.innerHTML = html;
  const bad = {SCRIPT:1,STYLE:1,IFRAME:1,OBJECT:1,EMBED:1,FORM:1,SVG:1,MATH:1,LINK:1,META:1};
  const walk = (root)=>{
    for(const el of Array.from(root.children)){
      const tag = el.tagName;
      if(bad[tag]){ el.remove(); continue; }
      if(!AI_MD_OK[tag]){
        // 认不出的标签脱壳保留文字（模型爱写 <div align=center> 这类）
        const frag = document.createDocumentFragment();
        while(el.firstChild) frag.appendChild(el.firstChild);
        el.replaceWith(frag);
        continue;
      }
      for(const at of Array.from(el.attributes)){
        if(at.name.toLowerCase().startsWith('on')) el.removeAttribute(at.name);
      }
      if(tag === 'A'){
        const href = el.getAttribute('href') || '';
        if(!/^(https?:|mailto:|#|\/)/i.test(href)) el.removeAttribute('href');
        else { el.setAttribute('target','_blank'); el.setAttribute('rel','noopener noreferrer'); }
      } else if(tag === 'INPUT'){
        if(el.getAttribute('type') !== 'checkbox'){ el.remove(); continue; }
        el.setAttribute('disabled','');
        for(const at of Array.from(el.attributes)){
          if(!['type','checked','disabled'].includes(at.name.toLowerCase())) el.removeAttribute(at.name);
        }
      } else if(tag === 'CODE'){
        const cls = (el.getAttribute('class') || '').split(/\s+/).filter(c=>/^language-[\w+-]+$/.test(c));
        if(cls.length) el.setAttribute('class', cls.join(' ')); else el.removeAttribute('class');
      } else {
        for(const at of Array.from(el.attributes)){
          const n = at.name.toLowerCase();
          if(n !== 'title' && !(n === 'align' && (tag === 'TH' || tag === 'TD' || tag === 'P'))) el.removeAttribute(at.name);
        }
      }
      walk(el);
    }
  };
  walk(tpl.content);
  return tpl.innerHTML;
}
function aiMd(t){
  const src = String(t == null ? '' : t);
  if(!window.marked || typeof marked.parse !== 'function') return aiEscHtml(src);
  let html;
  try{ html = marked.parse(src, {gfm:true, breaks:true, async:false}); }
  catch(e){ return aiEscHtml(src); }
  return aiSanitizeHtml(html);
}
function aiHighlight(root){
  if(!root || !window.hljs) return;
  try{
    root.querySelectorAll('pre code').forEach(el=>{
      if(el.dataset.hl === '1') return;
      try{ hljs.highlightElement(el); }catch(e){}
      el.dataset.hl = '1';
    });
  }catch(e){}
}
/* aiErr(msg)：参数名不能叫 t —— 那会遮蔽 i18n 的全局 t()，同作用域里
   一旦调用 t('…') 就变成对字符串取下标，静默返回 undefined。
   t(msg)：后端推下来的错误文案是中文原文，英文界面下按词表翻译；
   查不到的键 t() 原样返回，不会更糟。 */
function aiErr(msg){ aiAdd('err', t(msg)); }

/* ---------- 对话历史持久化：localStorage 存取，刷新/重开页面不丢 ---------- */
function aiHistSave(){
  try{
    const h = aiMessages.slice();
    // 尾部悬挂的 assistant(tool_calls)（确认卡还没批）不入档：恢复后缺 tool 结果会打坏下一轮请求
    while(h.length && h[h.length-1].tool_calls) h.pop();
    const slim = h.slice(-60).map(m => {
      const o = {role: m.role, content: String(m.content == null ? '' : m.content)};
      if(aiTsOk(m.ts)) o.ts = m.ts;
      // 图片只存引用（id/name/mime），几十字节：base64 存进来会写爆 5MB 配额，
      // 而 QuotaExceededError 被这里的 try/catch 吞掉后，表现是"历史莫名丢了"。
      if(Array.isArray(m.images) && m.images.length){
        o.images = m.images.filter(x=>x && x.id).map(x=>({id:x.id, name:x.name || '', mime:x.mime || ''}));
      }
      // 文档同理只存引用（几十字节）。正文永远只在服务端 aidoc/ 里，
      // 存进 localStorage 一次就够写爆 5MB 配额。
      if(Array.isArray(m.docs) && m.docs.length){
        o.docs = m.docs.filter(x=>x && x.id).map(x=>({id:x.id, name:x.name || '', kind:x.kind || '', chars:x.chars || 0}));
      }
      return o;
    });
    if(slim.length){ localStorage.setItem('ek-ai-hist', JSON.stringify(slim)); }
    else { localStorage.removeItem('ek-ai-hist'); }
  }catch(_){}
}
function aiHistRestore(){
  let h = null;
  try{ h = JSON.parse(localStorage.getItem('ek-ai-hist') || 'null'); }catch(_){}
  if(!Array.isArray(h)) return;
  aiMessages = h.filter(m => m && (m.role === 'user' || m.role === 'assistant') &&
    typeof m.content === 'string' &&
    // 只有图片（或只有文档）没有文字的消息必须留下：否则"发个文件问一句"的那一轮，
    // 附件回来了、问题没了
    (m.content.trim() || (Array.isArray(m.images) && m.images.length) ||
      (Array.isArray(m.docs) && m.docs.length)));
  aiMessages.forEach(m=>{
    if(Array.isArray(m.images)){
      m.images = m.images.filter(x=>x && x.id);
      if(!m.images.length) delete m.images;
    }
    if(Array.isArray(m.docs)){
      m.docs = m.docs.filter(x=>x && x.id);
      if(!m.docs.length) delete m.docs;
    }
  });
  if(!aiMessages.length) return;
  aiMessages.forEach(m => {
    const el = aiAdd(m.role === 'user' ? 'user' : 'ai', m.content, aiTsOk(m.ts) ? m.ts : undefined);
    if(m.role === 'user' && m.images) aiMsgImgsRender(el, m.images);
    if(m.role === 'user' && m.docs) aiMsgDocsRender(el, m.docs);
  });
  aiAdd('sys', t('已恢复上次的对话（{0} 条）', aiMessages.length));
}
aiHistRestore();

/* ================= 放大态 / 检查器（轨迹·审计·用量） / 文件引用 =================
   四条设计约束（沿用项目既有口径，不是新发明）：
   1) 只加 DOM 与样式，不动主区任何面板；紧凑态仍是原来那条 430px 抽屉。
   2) 轨迹的颜色只表达三态（中性/通过/失败）；人工介入单独一种，因为它决定
      "这次算不算自主" —— 与 trace 的口径一致。
   3) 审计树的数据全部来自 /api/audit 的 trace_id 分组，前端不重新判定结果，
      只做"把已有事实排成树"这件事。
   4) 文件选择只产出"路径引用"，不读文件内容——读文件是 AI 的 read_file 在沙箱内
      的职责；在浏览器里另开一条读取路径，等于绕过他们自己设的边界。 */

/* 状态变量在上面（aiMessages 旁）声明：aiAdd 在页面加载恢复历史时就会读 aiTurnAnchor。 */

function aiHstat(s){ const el = $ai('ai-hstat'); if(el) el.textContent = s || ''; }
function aiClock(ms){
  const d = new Date(ms || Date.now());
  return ('0'+d.getHours()).slice(-2)+':'+('0'+d.getMinutes()).slice(-2)+':'+('0'+d.getSeconds()).slice(-2);
}

function aiSide(tab){
  const dr = $ai('ai-drawer'); if(!dr) return;
  if(!dr.classList.contains('open')) aiOpen();
  if(!dr.classList.contains('big')){
    aiBigOn = true; dr.classList.add('big');
    const b = $ai('ai-expand'); if(b){ b.textContent = '⤡'; b.title = t('还原'); }
  }
  aiSideTab = tab || 'trace';
  document.querySelectorAll('#ai-tabs button').forEach(x=>x.classList.toggle('on', x.dataset.tab === aiSideTab));
  ['trace','audit','usage','missions'].forEach(k=>{ const p = $ai('ai-pane-'+k); if(p) p.hidden = (k !== aiSideTab); });
  if(aiSideTab === 'trace') aiTraceRender();
  if(aiSideTab === 'audit') aiAuditLoad();
  if(aiSideTab === 'usage') aiUsageRender();
  if(aiSideTab === 'missions') aiMissionsLoad();
}
function aiBig(on){
  const dr = $ai('ai-drawer'); if(!dr) return;
  aiBigOn = !!on;
  dr.classList.toggle('big', aiBigOn);
  const b = $ai('ai-expand');
  if(b){ b.textContent = aiBigOn ? '⤡' : '⤢'; b.title = t(aiBigOn ? '还原' : '放大'); }
  if(aiBigOn) aiSide(aiSideTab); else aiPickClose();
}

/* ---------- 轨迹：本回合的 step 时间轴 ---------- */
function aiTurnReset(){
  aiTurnNodes = []; aiTurnUsage = null; aiTurnStartAt = Date.now(); aiTurnAnchor = null; aiTraceView = null; aiStepAt = 0;
  aiTraceRender(); aiUsageRender();
}
function aiTraceAdd(kind, text, opt){
  const n = {kind: kind, text: String(text || ''), cls: '', detail: '', at: Date.now()};
  if(opt) Object.assign(n, opt);
  // 每步耗时：从"执行"到"结果"的墙钟差。不向服务端多要字段——
  // 事件到达顺序本身就是时序证据，而且这个数才是用户感知的"这一步花了多久"。
  if(kind === 'step') aiStepAt = n.at;
  else if((kind === 'result' || kind === 'ver') && aiStepAt && !n.dur) n.dur = n.at - aiStepAt;
  aiTurnNodes.push(n);
  if(aiBigOn && aiSideTab === 'trace') aiTraceRender();
  return n;
}
/* 一个事件 → 一个节点。**只在展示层做归类**：判据（ok/verified）始终来自服务端字段，
   前端不重新判断成败——那正是"徽章说验了就是真验了"的前提。 */
function aiTraceEvent(ev){
  if(!ev || !ev.type) return;
  if(ev.type === 'plan_start'){
    aiTraceAdd('plan', t('预算 {0} 轮', ev.max_turns) + (ev.reason ? ' · ' + ev.reason : ''));
  } else if(ev.type === 'plan_extend'){
    aiTraceAdd('plan', t('上限放宽到 {0} 轮', ev.max_turns), {cls:'warn', detail: ev.reason || ''});
  } else if(ev.type === 'plan_step'){
    aiTraceAdd('step', (ev.seq ? ev.seq + '. ' : '') + (ev.text || aiToolName(ev.tool)),
      {detail: ev.target ? ('target: ' + ev.target) : ''});
  } else if(ev.type === 'tool_result'){
    const rs = String(ev.result || '');
    /* 成败判据优先用服务端的 ok 字段（v3.3 起 tool_result 带上）。
       旧的中文正则只作旧版本后端的兜底 —— 它连 "connection refused"
       这种英文报错都标不红，而且和"前端不重新判定结果"的原则相悖。 */
    const bad = (typeof ev.ok === 'boolean') ? !ev.ok : /^(错误|执行出错)|失败|拒绝|不在白名单/.test(rs);
    aiTraceAdd('result', aiToolName(ev.tool), {cls: bad ? 'bad' : '', detail: rs});
  } else if(ev.type === 'verify_result'){
    aiTraceAdd('ver', ev.ok ? (ev.verified ? t('已复验通过') : t('已执行·未确认')) : t('复验未通过'), {
      cls: ev.ok ? (ev.verified ? 'ok' : 'warn') : 'bad',
      detail: [ev.msg].concat(Array.isArray(ev.evidence) ? ev.evidence : []).filter(Boolean).join('\n')
    });
  } else if(ev.type === 'confirm_request'){
    aiTraceAdd('human', t('等待你确认：{0}', aiToolName(ev.tool)), {cls:'human', detail: ev.text || ''});
    aiHstat(t('等你确认'));
  } else if(ev.type === 'plan_end'){
    aiTraceAdd('done', ev.text || t('已得出结论'), {cls:'ok'});
  } else if(ev.type === 'usage'){
    aiTurnUsage = ev.usage || null;
    if(aiTurnUsage) aiHstat(t('↑{0} ↓{1}', aiFmtTok(aiTurnUsage.prompt_tokens), aiFmtTok(aiTurnUsage.completion_tokens)));
    if(aiBigOn && aiSideTab === 'usage') aiUsageRender();
  } else if(ev.type === 'error'){
    aiTraceAdd('done', String(ev.text || ''), {cls:'bad'});
  }
}
function aiTraceRender(){
  const box = $ai('ai-pane-trace'); if(!box) return;
  box.textContent = '';
  /* 历史视图整块由 61-ai-trace.js 负责（它自己会画切换条与筛选栏），
     所以这里直接交出去，不重复画头部。
     aiTrMode 用 var 声明：本文件在拼接后先执行，用 let 会落进 TDZ。 */
  if(typeof aiTrMode !== 'undefined' && aiTrMode === 'hist'){ aiTrHistRender(box); return; }
  box.appendChild(aiTrSwitchRow());
  const nodes = (aiTraceView && Array.isArray(aiTraceView.__trace)) ? aiTraceView.__trace : aiTurnNodes;
  // 头部先给结论：几步、多久、花多少、命中多少。明细在下面，别让人先读一堆时间戳。
  const head = document.createElement('div'); head.className = 'muted';
  head.style.cssText = 'margin-bottom:8px;display:flex;flex-wrap:wrap;gap:6px;align-items:center;line-height:1.5';
  if(aiTraceView){
    head.appendChild(document.createTextNode(t('历史回合的轨迹（只读）')));
    const back = document.createElement('button'); back.textContent = t('回到本回合');
    back.style.cssText = 'background:none;border:1px solid var(--line);border-radius:6px;color:var(--muted);font-size:11px;padding:1px 7px;cursor:pointer';
    back.onclick = ()=>{ aiTraceView = null; aiTraceRender(); };
    head.appendChild(back);
  } else {
    const bits = [t('{0} 个节点', (nodes || []).length)];
    if(aiTurnStartAt) bits.push(((Date.now() - aiTurnStartAt) / 1000).toFixed(1) + 's');
    if(aiTurnUsage){
      bits.push('↑' + aiFmtTok(aiTurnUsage.prompt_tokens) + ' ↓' + aiFmtTok(aiTurnUsage.completion_tokens));
      const p = aiCachePct(aiTurnUsage);
      if(p !== null) bits.push(t('缓存 {0}%', Math.round(p * 100)));
    }
    head.appendChild(document.createTextNode(t('本回合') + ' · ' + bits.join(' · ')));
  }
  box.appendChild(head);
  if(!nodes || !nodes.length){
    const p = document.createElement('div'); p.className = 'muted'; p.textContent = t('还没有轨迹。发一句话就会有了。');
    box.appendChild(p); return;
  }
  const ul = document.createElement('ul'); ul.className = 'tl';
  nodes.forEach(n=>{
    const li = document.createElement('li'); li.className = 'tl-row ' + (n.cls || '');
    const dot = document.createElement('i'); dot.className = 'tl-dot';
    const k = document.createElement('span'); k.className = 'tl-k'; k.textContent = aiClock(n.at);
    const kd = document.createElement('span'); kd.className = 'tl-k'; kd.textContent = t(AI_KIND[n.kind] || n.kind);
    const tt = document.createElement('span'); tt.className = 'tl-t'; tt.textContent = n.text;
    li.appendChild(dot); li.appendChild(k); li.appendChild(kd); li.appendChild(tt);
    if(n.dur){ const d = document.createElement('span'); d.className = 'tl-dur'; d.textContent = (n.dur / 1000).toFixed(1) + 's'; li.appendChild(d); }
    if(n.detail){
      const lines = n.detail.split('\n').length;
      if(lines > 8){
        // 长证据默认折叠：轨迹是看时序的，不是看日志的；要看再展开
        const det = document.createElement('details'); det.className = 'tl-det';
        const sm = document.createElement('summary'); sm.textContent = t('展开明细（{0} 行）', lines);
        const pre = document.createElement('div'); pre.className = 'tl-xbody'; pre.textContent = n.detail;
        det.appendChild(sm); det.appendChild(pre); li.appendChild(det);
      } else {
        const x = document.createElement('div'); x.className = 'tl-x'; x.textContent = n.detail; li.appendChild(x);
      }
    }
    ul.appendChild(li);
  });
  box.appendChild(ul);
  // 图例：三种状态色 + 人工点。颜色含义只有一份口径，写在页面上而不是只在注释里。
  const lg = document.createElement('div'); lg.className = 'tl-legend';
  [['ok', t('已验证')], ['warn', t('已执行·未确认')], ['bad', t('失败')], ['human', t('人工确认')]].forEach(x=>{
    const s = document.createElement('span'); s.className = x[0];
    const i = document.createElement('i');
    const e = document.createElement('em'); e.textContent = x[1];
    s.appendChild(i); s.appendChild(e); lg.appendChild(s);
  });
  box.appendChild(lg);
}

/* ---------- 审计分支树：trace_id 做根，actor 做泳道 ---------- */
function aiAuditFocus(nodes){
  aiAuditWin = null;
  if(nodes && nodes.length){
    const from = nodes[0].at - 5000, to = nodes[nodes.length-1].at + 5000;
    aiAuditWin = {from: from, to: to};
  }
}
function aiAuditLoad(force){
  const box = $ai('ai-pane-audit'); if(!box) return;
  if(!box.dataset.shell){
    box.dataset.shell = '1';
    box.innerHTML =
      '<div class="pbar">' +
        '<input id="ai-audit-q" placeholder="' + esc(t('筛选动作 / 目标')) + '">' +
        '<label class="muted" style="display:flex;gap:4px;align-items:center;white-space:nowrap"><input type="checkbox" id="ai-audit-onlybad">' + esc(t('只看异常')) + '</label>' +
        '<button id="ai-audit-re">' + esc(t('刷新')) + '</button>' +
      '</div>' +
      '<div id="ai-audit-win"></div>' +
      '<div id="ai-audit-list"></div>';
    $ai('ai-audit-re').onclick = ()=>aiAuditLoad(true);
    $ai('ai-audit-onlybad').onchange = ()=>aiAuditRender();
    $ai('ai-audit-q').oninput = ()=>aiAuditRender();
  }
  const list = $ai('ai-audit-list');
  if(list && !aiAuditRows) list.textContent = t('读取审计…');
  if(aiAuditRows && !force){ aiAuditRender(); return; }
  fetch('/api/audit?n=400&day=all').then(r=>r.json()).then(d=>{
    aiAuditRows = (d && d.entries) || [];
    aiAuditRender();
  }).catch(()=>{ if(list) list.textContent = t('读取失败'); });
}
function aiAuditRender(){
  const list = $ai('ai-audit-list'); if(!list) return;
  const q = (($ai('ai-audit-q') && $ai('ai-audit-q').value) || '').trim().toLowerCase();
  const onlyBad = !!($ai('ai-audit-onlybad') && $ai('ai-audit-onlybad').checked);
  const winBox = $ai('ai-audit-win');
  if(winBox){
    winBox.textContent = '';
    if(aiAuditWin){
      const tip = document.createElement('div'); tip.className = 'muted';
      tip.style.cssText = 'margin-bottom:6px;display:flex;gap:6px;align-items:center';
      tip.appendChild(document.createTextNode(t('已按本轮时间窗过滤')));
      const clr = document.createElement('button');
      clr.textContent = t('清除'); clr.style.cssText = 'background:none;border:1px solid var(--line);border-radius:6px;color:var(--muted);font-size:11px;padding:1px 7px;cursor:pointer';
      clr.onclick = ()=>{ aiAuditWin = null; aiAuditRender(); };
      tip.appendChild(clr); winBox.appendChild(tip);
    }
  }
  list.textContent = '';
  const rowsAll = aiAuditRows || [];
  if(!rowsAll.length){ const p = document.createElement('div'); p.className='muted'; p.textContent = t('暂无审计记录'); list.appendChild(p); return; }
  const inWin = e=>{
    if(!aiAuditWin) return true;
    const ts = Date.parse(String(e.ts || '').replace(' ', 'T'));
    return !isFinite(ts) || (ts >= aiAuditWin.from && ts <= aiAuditWin.to);
  };
  const isBad = e=>e.result === 'fail' || e.result === 'denied' || e.result === 'unverified';
  const match = e=>{
    if(onlyBad && !isBad(e)) return false;
    if(!q) return true;
    return (String(e.action||'') + ' ' + String(e.target||'') + ' ' + String(e.detail||'')).toLowerCase().indexOf(q) >= 0;
  };
  // 分组：一条 trace = 一次任务；无 trace_id 的归到"未归属"
  const groups = [], ix = new Map();
  for(const e of rowsAll){
    if(!inWin(e)) continue;
    const k = e.trace_id || '_';
    let g = ix.get(k);
    if(!g){ g = {k: k, rows: []}; ix.set(k, g); groups.push(g); }
    g.rows.push(e);
  }
  // 有 trace_id 的组 = 一次真实任务，排前面；「未归属」（按钮触发的零散操作）
  // 永远是最大的一坨（实测一天 294 条），放最后并且默认折叠，否则它会把树撑爆。
  groups.sort((a, b)=>(a.k === '_' ? 1 : 0) - (b.k === '_' ? 1 : 0));
  let shown = 0;
  for(const g of groups){
    const rows = g.rows.filter(match);
    if(!rows.length) continue;
    shown++;
    const worst = rows.some(e=>e.result === 'fail' || e.result === 'denied') ? 'bad'
      : (rows.some(e=>e.result === 'unverified' || e.result === 'started') ? 'warn' : 'ok');
    const det = document.createElement('details');
    det.className = 'tr-root ' + worst;
    // 默认只展开"真的失败/被拒"的那几条：正常 trace 与「未归属」都收成一行。
    // 全展开的话 40 条 trace × 3 行 ≈ 120 行，等于把表格又搬回来了；
    // 而"未复验"不该自动抢注意力——它是常态，不是故障。
    det.open = (g.k !== '_') && rows.some(e=>e.result === 'fail' || e.result === 'denied');
    const sm = document.createElement('summary'); sm.className = 'tr-head';
    const id = document.createElement('span'); id.className = 'tr-id'; id.textContent = g.k === '_' ? t('未归属') : g.k;
    const sum = document.createElement('span'); sum.className = 'tr-sum';
    const cnt = {user:0, ai:0, guard:0, system:0, shadow:0};
    rows.forEach(e=>{ if(cnt[e.actor] != null) cnt[e.actor]++; });
    sum.textContent = t('{0} 条', rows.length) + '  ' +
      Object.keys(cnt).filter(k=>cnt[k]).map(k=>k + ' ' + cnt[k]).join(' · ');
    const tg = document.createElement('span'); tg.className = 'tr-tag ' + worst;
    tg.textContent = worst === 'bad' ? t('有异常') : (worst === 'warn' ? t('未复验') : t('正常'));
    sm.appendChild(id); sm.appendChild(sum); sm.appendChild(tg);
    det.appendChild(sm);
    // 同 (actor, action, target, result) 合成一行：一天 294 条 env_check 压成「×286」一行，
    // 想看哪一次再点开。树是给人看"这次任务发生了什么"的，不是给日志做镜像。
    const agg = [], ax = new Map();
    for(const e of rows){
      const key = [e.actor, e.action, e.target, e.result].join('|');
      let a = ax.get(key);
      if(!a){ a = {e: e, n: 0, dur: 0, rows: []}; ax.set(key, a); agg.push(a); }
      a.n++; a.dur += Number(e.dur_ms) || 0; a.rows.push(e);
    }
    for(const a of agg){
      const e = a.e;
      const r = document.createElement('div'); r.className = 'tr-row';
      const ac = document.createElement('span'); ac.className = 'tr-act'; ac.textContent = e.actor || '';
      const n = document.createElement('span'); n.className = 'tr-name';
      n.textContent = (e.action || '') + (e.target ? ' · ' + e.target : '') + (a.n > 1 ? '  ×' + a.n : '');
      const tag = document.createElement('span'); tag.className = 'tr-tag ' + (isBad(e) ? 'bad' : (e.result === 'started' ? 'warn' : 'ok'));
      tag.textContent = e.result || '';
      r.appendChild(ac); r.appendChild(n); r.appendChild(tag);
      if(a.dur){ const d = document.createElement('span'); d.className = 'tr-id'; d.textContent = a.dur + 'ms'; r.appendChild(d); }
      r.onclick = ()=>{
        const exist = r.nextElementSibling;
        if(exist && exist.classList.contains('tr-detail')){ exist.remove(); return; }
        const box = document.createElement('div'); box.className = 'tr-detail';
        box.textContent = a.rows.map(x=>[('ts: ' + (x.ts || '')), ('verify: ' + (x.verify || '-')),
          ('params: ' + (x.params || '-')), ('detail: ' + (x.detail || '-'))].join('  ')).join('\n');
        r.after(box);
      };
      det.appendChild(r);
    }
    list.appendChild(det);
  }
  if(!shown){ const p = document.createElement('div'); p.className='muted'; p.textContent = t('没有命中'); list.appendChild(p); }
}

/* ---------- 用量：DSH 式明细卡 + 会话/今日汇总 ---------- */
function aiNum(n){ n = Math.round(Number(n) || 0); return n.toLocaleString('en-US'); }
function aiCachePct(u){
  const hit = Number(u && u.cache_hit) || 0, miss = Number(u && u.cache_miss) || 0;
  if(hit + miss <= 0) return null;
  return hit / (hit + miss);
}
function aiUsageBrief(u){
  if(!u) return '';
  const bits = ['↑' + aiFmtTok(u.prompt_tokens), '↓' + aiFmtTok(u.completion_tokens)];
  const p = aiCachePct(u);
  if(p !== null) bits.push(t('缓存 {0}%', Math.round(p * 100)));
  return bits.join(' · ');
}
/* 一张卡答四件事：花了多少、命中多少、哪个模型、合计多少。
   行序照 DSH：提供方/模型 → 命中率 → 未缓存输入 → 缓存读取 → 缓存写入 → 输出。 */
function aiUsageCard(u, title, note){
  u = u || {};
  const card = document.createElement('div'); card.className = 'ai-ucard';
  const hit = Number(u.cache_hit) || 0, missRaw = Number(u.cache_miss) || 0;
  const prompt = Number(u.prompt_tokens) || 0, out = Number(u.completion_tokens) || 0;
  // 有的上游只报 prompt + hit，不单独报 miss：缺口按 prompt-hit 兜底，而不是显示 0
  const uncached = missRaw > 0 ? missRaw : Math.max(0, prompt - hit);
  const pct = (hit + uncached) > 0 ? (100 * hit / (hit + uncached)) : null;
  const head = document.createElement('div'); head.className = 'uc-head';
  const hb = document.createElement('b'); hb.textContent = title;
  const hv = document.createElement('span'); hv.textContent = aiNum(prompt + out) + ' tok';
  head.appendChild(hb); head.appendChild(hv); card.appendChild(head);
  const row = (k, v, cls)=>{
    const r = document.createElement('div'); r.className = 'uc-row' + (cls ? ' ' + cls : '');
    const a = document.createElement('span'); a.textContent = k;
    const b = document.createElement('b'); b.textContent = v;
    r.appendChild(a); r.appendChild(b); card.appendChild(r);
  };
  row(t('提供方 / 模型'), ([u.provider, u.model].filter(Boolean).join('/')) || '—');
  row(t('缓存命中'), pct === null ? '—' : pct.toFixed(1) + '%', pct !== null && pct >= 50 ? 'hi' : '');
  row(t('未缓存输入'), aiNum(uncached) + ' tok');
  row(t('缓存读取'), aiNum(hit) + ' tok');
  // 「缓存写入」行已删除（v3.3）：后端 usage 里根本没有这个字段，
  // 原来那行 '0 tok' 是画出来的假数据 —— 展示一个永远为 0 的编造值，
  // 不如不展示（DeepSeek 的计费说明已在卡片底部的 note 里）。
  row(t('输出'), aiNum(out) + ' tok');
  if(note){ const n = document.createElement('div'); n.className = 'uc-note'; n.textContent = note; card.appendChild(n); }
  return card;
}
/* 消息尾部的用量行：默认只显示一行摘要，点开才是那张卡——
   聊天区保持干净，但"这一轮到底花了多少"随时可查。 */
function aiUsageLine(host, u){
  if(!host) return;
  host.__usage = u;
  const oldLine = host.querySelector('.ai-usage'); if(oldLine) oldLine.remove();
  const oldCard = host.querySelector('.ai-ucard'); if(oldCard) oldCard.remove();
  const ln = document.createElement('div'); ln.className = 'ai-usage';
  ln.textContent = aiUsageBrief(u) + '  ▾';
  ln.title = t('点开看本轮用量明细');
  const card = aiUsageCard(u, t('本轮用量'), t('缓存写入一栏对 DeepSeek 恒为 0：未命中的输入按普通输入计费，不单列写入。'));
  card.hidden = true;
  ln.onclick = ()=>{ card.hidden = !card.hidden; ln.textContent = aiUsageBrief(u) + (card.hidden ? '  ▾' : '  ▴'); };
  host.appendChild(ln);
  host.appendChild(card);
}
function aiUsageRender(){
  const box = $ai('ai-pane-usage'); if(!box) return;
  box.textContent = '';
  if(!aiTurnUsage && !aiTurnLog.length){
    const tip = document.createElement('div'); tip.className = 'muted'; tip.style.marginBottom = '8px';
    tip.textContent = t('还没有数据。发一条消息后这里会显示 token、步数与耗时。');
    box.appendChild(tip);
  }
  if(aiTurnUsage){
    box.appendChild(aiUsageCard(aiTurnUsage, t('本轮用量'),
      t('缓存写入一栏对 DeepSeek 恒为 0：未命中的输入按普通输入计费，不单列写入。')));
  }
  const grid = (pairs)=>{
    const g = document.createElement('div'); g.className = 'us-grid';
    pairs.forEach(p=>{ const b = document.createElement('b'); b.textContent = p[0]; const s = document.createElement('span'); s.textContent = p[1]; g.appendChild(b); g.appendChild(s); });
    return g;
  };
  const sum = aiTurnLog.reduce((a,x)=>({tin:a.tin+x.tin, tout:a.tout+x.tout, hit:a.hit+x.hit, miss:a.miss+x.miss, n:a.n+1}),
    {tin:0, tout:0, hit:0, miss:0, n:0});
  const h2 = document.createElement('div'); h2.className = 'muted'; h2.style.margin = '10px 0 4px';
  h2.textContent = t('本会话 · {0} 轮', sum.n); box.appendChild(h2);
  box.appendChild(grid([
    [t('输入'), aiFmtTok(sum.tin)], [t('输出'), aiFmtTok(sum.tout)],
    [t('平均每轮'), aiFmtTok(sum.n ? (sum.tin + sum.tout) / sum.n : 0)],
    [t('缓存命中'), (sum.hit + sum.miss) > 0 ? Math.round(100 * sum.hit / (sum.hit + sum.miss)) + '%' : '—']
  ]));
  if(aiTurnLog.length){
    const bars = document.createElement('div'); bars.className = 'us-bars';
    const max = Math.max.apply(null, aiTurnLog.map(x=>x.tin + x.tout)) || 1;
    aiTurnLog.slice(-16).forEach((x,i)=>{
      const b = document.createElement('div'); b.className = 'us-bar';
      b.style.height = Math.max(2, Math.round(44 * (x.tin + x.tout) / max)) + 'px';
      const p = (x.hit + x.miss) > 0 ? Math.round(100 * x.hit / (x.hit + x.miss)) + '%' : '—';
      b.title = '#' + (aiTurnLog.length - Math.min(16, aiTurnLog.length) + i + 1) +
        ' ↑' + x.tin + ' ↓' + x.tout + ' · 缓存 ' + p + ' · ' + x.steps + ' 步';
      bars.appendChild(b);
    });
    box.appendChild(bars);
    // 逐轮明细：定位"哪一轮掉点"靠它（合计永远看不出冷启动/重建）
    const hh = document.createElement('div'); hh.className = 'muted'; hh.style.margin = '10px 0 4px';
    hh.textContent = t('最近回合'); box.appendChild(hh);
    aiTurnLog.slice(-8).reverse().forEach(x=>{
      const r = document.createElement('div'); r.className = 'uc-row';
      const a = document.createElement('span');
      const p = (x.hit + x.miss) > 0 ? Math.round(100 * x.hit / (x.hit + x.miss)) + '%' : '—';
      a.textContent = (x.at || '') + ' · ↑' + aiFmtTok(x.tin) + ' ↓' + aiFmtTok(x.tout) +
        ' · 缓存 ' + p + ' · ' + x.steps + ' 步';
      const b = document.createElement('b'); b.textContent = (x.ms ? (x.ms / 1000).toFixed(1) + 's' : '');
      r.appendChild(a); r.appendChild(b); box.appendChild(r);
    });
  }
  const a = aiUsageAcc();
  const h3 = document.createElement('div'); h3.className = 'muted'; h3.style.margin = '10px 0 4px';
  h3.textContent = t('今日（本机累计）'); box.appendChild(h3);
  box.appendChild(grid([[t('对话'), String(a.calls)], [t('输入'), aiFmtTok(a.tin)], [t('输出'), aiFmtTok(a.tout)],
    [t('缓存命中'), (a.hit + a.miss) > 0 ? Math.round(100 * a.hit / (a.hit + a.miss)) + '%' : '—']]));
  const note = document.createElement('div'); note.className = 'muted'; note.style.marginTop = '8px'; note.style.lineHeight = '1.5';
  note.textContent = t('会话统计只在本次页面打开期间累积；今日统计写在本机 localStorage。');
  box.appendChild(note);
}

