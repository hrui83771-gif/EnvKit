/* ---------- 使用文档：把操作页上的长说明集中到一处 ----------
   内容只取自 README 与原来的「关于」清单，不写不存在的功能；
   目录树 / 正文 / 右侧目录都从同一份数据生成，不会两处对不上。
   语言走 dz(zh,en) 双语文案，并监听 ek:lang 事件整页重渲染 —— 不能指望
   applyI18n 去翻译 JS 拼出来的节点。 */
(function(){
  const body = document.getElementById('docs-body');
  const tree = document.getElementById('docs-tree');
  const toc  = document.getElementById('docs-toc');
  const q    = document.getElementById('docs-q');
  if(!body || !tree || !toc) return;

  const lang = ()=> document.documentElement.getAttribute('lang') === 'en' ? 'en' : 'zh';
  const val  = x => (x && typeof x === 'object' && !Array.isArray(x)) ? (x[lang()] || x.zh || '') : x;
  const z    = (zh,en) => ({zh:zh, en:en});
  const p    = (zh,en) => ({k:'p', zh:zh, en:en});
  const note = (zh,en) => ({k:'note', zh:zh, en:en});
  const h3   = (id,zh,en) => ({k:'h3', id:id, zh:zh, en:en});
  const list = (k, items) => ({k:k, items:items});
  const code = (lines) => ({k:'code', code:lines});

  const D = [
    {id:'quick', group:z('快速开始','Get started'), title:z('四步走完','Four steps'), body:[
      p('双击 EnvKit.exe 后浏览器打开本地控制台，按左侧顺序走一遍即可；顶部状态胶囊会随检测与启动结果更新。',
        'Double-click EnvKit.exe, open the local console, and follow the four steps in the sidebar. Status chips update as checks and starts finish.'),
      list('ol', [
        z('环境安装：检测 Go / Node.js / MySQL，镜像下载带 SHA256 校验与失败重试。',
          'Environment: detect Go / Node.js / MySQL; mirrored downloads with SHA256 checks and retries.'),
        z('程序配置：填项目目录与数据库连接，建库建表、备份还原都在这里。',
          'Configuration: set project paths and database connection; create schema, back up and restore here.'),
        z('链端校验：填 SSH 与链目录，检查节点端口、进程与共识视图。',
          'Chain: set SSH and chain paths; check node ports, processes and consensus view.'),
        z('程序启动：先后端再前端；日志按来源分栏，退出 EnvKit 时子进程自动回收。',
          'Start: backend first, then frontend. Logs are split by source; child processes are reaped when EnvKit exits.'),
      ]),
      note('控制台默认 http://127.0.0.1:18765/，端口冲突会顺延。黑色命令行窗口就是服务本体：关掉它网页就打不开；要从托盘菜单「隐藏窗口到托盘」，不是结束进程。',
           'The console defaults to http://127.0.0.1:18765/ and shifts ports on conflict. The black console window IS the service: closing it kills the page. Use the tray menu to hide it, not to end the process.'),
      h3('quick-src', '从源码构建', 'Build from source'),
      code('git clone https://github.com/hrui83771-gif/EnvKit.git\ncd EnvKit\ngo build -trimpath -ldflags "-s -w" -o EnvKit.exe .'),
      p('需要 Go 1.26 或更高。改前端只改 web/src/，然后运行 python tools/web_build.py 重拼；直接改 web/index.html 会在下次构建时被覆盖。',
        'Go 1.26+ is required. Edit web/src/ only, then run python tools/web_build.py; changes made directly to web/index.html are overwritten on the next build.'),
    ]},
    {id:'install', group:z('快速开始','Get started'), title:z('环境安装','Environment setup'), body:[
      p('检测会同时读版本号与安装位置，不只看命令能不能跑。安装走国内镜像，下载后校验 SHA256，失败自动换源重试。',
        'Detection reads both version and install path, not just whether a command runs. Installs use domestic mirrors, verify SHA256, and retry from another source on failure.'),
      list('ul', [
        z('可演练安装：先看会下载什么、装到哪里，再决定是否真装。',
          'Dry run available: see what would be downloaded and installed before committing.'),
        z('组件版本集中管理，可单独升级，不必重装整套环境。',
          'Component versions are managed centrally and can be upgraded individually.'),
        z('GOPROXY 与 npm registry 按国内可达的镜像自动配置。',
          'GOPROXY and npm registry are pointed at reachable domestic mirrors.'),
      ]),
      note('注册或启动 MySQL 服务需要管理员权限；遇到这一步时右键 EnvKit.exe「以管理员身份运行」，其余功能不受影响。',
        'Registering or starting the MySQL service needs administrator rights. Right-click EnvKit.exe and choose Run as administrator only for that step; everything else works normally.'),
    ]},
    {id:'config', group:z('快速开始','Get started'), title:z('程序配置','Configuration'), body:[
      p('config.json 放在 exe 同目录，覆盖内置默认值；config.dist.json 是干净模板。包含组件版本与下载地址、MySQL 参数、项目目录、链端 SSH 等。',
        'config.json lives next to the exe and overrides built-in defaults; config.dist.json is a clean template. It covers component versions, MySQL, project paths, chain SSH and more.'),
      p('数据库连接检测、建库 + 建表一步到位、一键备份 / 还原（失败自动重试）、前端 npm install / 后端 go mod tidy、配置导入导出（可脱敏分享）、环境诊断报告，都在这一页。',
        'Database checks, schema creation, one-click backup/restore with retries, npm install / go mod tidy, redacted config export/import and diagnostic reports all live on this page.'),
      h3('config-files', '本机文件', 'Files on this machine'),
      code('config.json\nconfig.dist.json\naudit-YYYYMMDD.jsonl\nenvkit-YYYYMMDD.log'),
      note('API Key 与数据库密码经 Windows DPAPI 加密，只在本机可解密。导出的配置默认脱敏，分享前再确认一次没有带上密钥。',
        'API keys and database passwords are encrypted with Windows DPAPI and decrypt only on this machine. Exported configs are redacted by default — check again before sharing.'),
    ]},
    {id:'chain', group:z('功能','Features'), title:z('链端校验','Chain checks'), body:[
      p('支持 FISCO-BCOS v2 / WeBASE-Front 连通检测、SSH 密码或私钥登录、页面内嵌终端与远程目录浏览；节点异常可一键检测或交给后台守护自动恢复，并触发 Windows 通知。',
        'Supports FISCO-BCOS v2 / WeBASE-Front connectivity, SSH password or key login, an embedded terminal and remote directory browsing. Node recovery can be manual or handled by the background guard with Windows notifications.'),
      note('判活看的是共识视图是否推进，不是区块高度。PBFT 空块只共识不落盘，块高静止是正常态；拿块高判活会把健康链永远判成未恢复。',
        'Liveness is judged by consensus view progression, NOT block height. PBFT empty blocks reach consensus without being stored, so a static height is normal; judging by height marks healthy chains as unrecovered.'),
      list('ul', [
        z('SSH 首次连接按 TOFU 记录主机指纹，指纹变化即拒绝。',
          'SSH uses TOFU: the host fingerprint is recorded on first connect and any change is rejected.'),
        z('自愈态势页展示守护轨迹与影子晋升证据，空列表表示尚无记录，不代表守护没配置。',
          'The self-healing page shows guard traces and shadow promotion evidence. An empty list means no records yet, not that guarding is disabled.'),
      ]),
    ]},
    {id:'start', group:z('功能','Features'), title:z('程序启动与日志','Start and logs'), body:[
      p('前后端一键启动（先后端、再前端），访问地址自动解析；日志按来源分栏并清理 ANSI 转义码，支持复制、下载和自动滚动。',
        'Start backend then frontend with one click; access URLs are resolved automatically. Logs are split by source, stripped of ANSI codes, and support copy, download and auto-scroll.'),
      list('ul', [
        z('停止服务时报告端口占用：第三方进程只报告，不会替你结束。',
          'Stopping reports port occupancy: third-party processes are reported, never killed.'),
        z('子进程绑定 Job Object，EnvKit 退出时自动回收，避免留下孤儿进程。',
          'Child processes are bound to a Job Object and reaped when EnvKit exits, so no orphans are left behind.'),
      ]),
      note('操作审计只追加、不修改、不回滚，按天分文件保留 30 天；密码与 API Key 在入库前已脱敏。',
        'Operation audit is append-only, never rewritten or rolled back, kept in daily files for 30 days. Passwords and API keys are redacted before being written.'),
    ]},
    {id:'ai', group:z('功能','Features'), title:z('AI 助手','AI assistant'), body:[
      p('右下角桌宠是状态出口，点它打开 AI 抽屉。26 个受控工具覆盖状态、日志、审计、诊断、数据库、检测、启停、白名单与清理；对话支持 Markdown、表格与代码高亮。',
        'The desktop pet is a status outlet; click it to open the AI drawer. 26 controlled tools cover status, logs, audit, diagnostics, database, checks, start/stop, allowlist and cleanup. Chat supports Markdown, tables and code highlighting.'),
      list('ul', [
        z('写操作一律先确认：服务端返回 428 强制确认，模型跳不过去；不提供任意命令执行。',
          'Every write needs confirmation: the server returns 428 and the model cannot bypass it. Arbitrary command execution is not offered.'),
        z('复验结果三态展示：通过 / 已执行未验证 / 失败；命令返回 0 不等于任务完成。',
          'Verification has three states: passed / executed but unverified / failed. Exit code 0 does not mean the task is done.'),
        z('可拖入 SQL 走「先备份 → 确认 → 执行」；也能读 docx / xlsx / pptx / pdf，由后端抽取并真分页。',
          'Drop in SQL for backup → confirm → execute, or drag docx / xlsx / pptx / pdf for backend extraction with real pagination.'),
        z('记忆与经验默认关闭；即使开启也只影响建议顺序，不改变任何操作的权限档位。',
          'Memory and experience are off by default; even when enabled they only affect suggestion order, never permission levels.'),
      ]),
    ]},
    {id:'mobile', group:z('功能','Features'), title:z('手机访问','Mobile access'), body:[
      p('个人中心打开「手机访问」后，电脑另开一个局域网端口（默认 18766）并扫码配对。桌面控制台只绑 127.0.0.1，手机端口上只有 12 条接口。',
        'Turn on Mobile access in the profile to open a separate LAN port (default 18766) and pair by QR code. The desktop console binds only to 127.0.0.1; the mobile port exposes just 12 endpoints.'),
      list('ul', [
        z('看屏幕默认开（只读），剪贴板与终端默认关，按危险程度分档；可再加 6 位动态验证码。',
          'Screen view is on by default (read-only); clipboard and terminal are off by default, graded by risk. An optional 6-digit one-time code adds another gate.'),
        z('未配对设备连「功能开没开」都探不到，配对码与能力开关先鉴权再判断。',
          'Unpaired devices cannot even detect whether a capability is on; pairing and capability checks are authenticated first.'),
      ]),
    ]},
    {id:'pet', group:z('功能','Features'), title:z('桌宠','Desktop pet'), body:[
      p('右下角的大肥鱼会跟着任务换状态：待机、思考、干活、等待确认、出错；进度环显示当前任务，悬停能看到任务名与耗时。',
        'The fish changes with task state: idle, thinking, working, awaiting confirmation or error. The ring shows progress; hovering shows the task name and elapsed time.'),
      list('ul', [
        z('右键菜单可归位、听它说一句、三档大小、原地 / 溜达 / 跟随鼠标、鼠标穿透、打开 AI 抽屉、立即检测。',
          'The context menu offers home, say, three sizes, still / wander / follow, click-through, AI drawer and immediate check.'),
        z('双击喂食（五种食物），按住 1.5 秒摸头，拖动甩会晕；天气默认关闭，在设置里开启后气泡给实况 / 细节 / 建议三行。',
          'Double-click to feed (five foods), hold 1.5s to pet, drag fast to make it dizzy. Weather is off by default; enable it in Settings for a three-line report.'),
      ]),
      note('开了鼠标穿透后整只鱼都不吃点击，提示气泡约 5 秒后自己收起；要恢复就刷新页面，或从托盘重新打开控制台。',
        'With click-through on, the pet ignores clicks and the hint bubble hides itself after about five seconds. Refresh the page or reopen the console from the tray to restore.'),
    ]},
    {id:'faq', group:z('排障','Troubleshooting'), title:z('常见问题','FAQ'), body:[
      list('ol', [
        z('窗口是黑的、能关吗？—— 那是服务进程本体，关掉网页就打不开；用托盘「隐藏窗口到托盘」让它继续在后台跑。',
          'Can I close the black window? No — it is the service. Closing it kills the page; use the tray action to hide it instead.'),
        z('端口被占用？—— 停止页会报告占用者；第三方进程只报告不结束。EnvKit 端口冲突会自动顺延。',
          'Port in use? The stop page reports the owner but never kills third-party processes. EnvKit shifts to the next free port.'),
        z('MySQL 服务起不来？—— 注册 / 启动服务需要管理员权限，右键 EnvKit.exe 以管理员身份运行。',
          'MySQL service will not start? Registering/starting it needs admin rights; run EnvKit.exe as administrator.'),
        z('桌宠点不到了？—— 你开了鼠标穿透。刷新页面，或从托盘打开控制台即可恢复。',
          'Pet ignores clicks? Click-through is on. Refresh the page or open the console from the tray.'),
        z('显示「已执行，未验证」？—— 命令确实跑了，但端口 / 进程 / HTTP / 备份校验还没给出客观证据，不能算完成。',
          '“Executed, unverified”? The command ran, but port / process / HTTP / backup checks have not produced objective evidence yet, so it is not complete.'),
        z('链上区块高度不动是卡死了吗？—— 不一定。空闲链只共识不落盘，判活要看共识视图推进。',
          'Is a static block height a stuck chain? Not necessarily. Idle chains reach consensus without storing blocks; check consensus view progression.'),
      ]),
    ]},
    {id:'about', group:z('关于','About'), title:z('关于与免责声明','About and disclaimer'), body:[
      {k:'ver'},
      p('作者 / 项目地址：https://github.com/hrui83771-gif',
        'Author / project: https://github.com/hrui83771-gif'),
      h3('about-disclaimer', '免责声明', 'Disclaimer'),
      list('ol', [
        z('本软件按“现状”（AS IS）提供，作者不对准确性、可靠性、适用性及特定用途的适用性作任何明示或暗示的担保，也不保证无缺陷或永久可用。',
          'This software is provided AS IS. The author makes no warranty, express or implied, as to accuracy, reliability, fitness for a particular purpose, or that it is defect-free or permanently available.'),
        z('使用本软件产生的任何后果（含环境配置失败、数据丢失、服务中断、端口 / 进程异常、链端操作失误等）均由使用者自行承担，作者不承担直接或间接责任。',
          'Any consequence of using this software (including failed setup, data loss, service interruption, port/process faults or chain operation errors) is the user’s responsibility; the author accepts no direct or indirect liability.'),
        z('本软件依赖 Go、Node.js、MySQL、FISCO-BCOS、WeBASE 等第三方组件，其版权与许可归各自权利方所有；本软件不因此获得任何授权。',
          'This software depends on third-party components including Go, Node.js, MySQL, FISCO-BCOS and WeBASE. Their copyrights and licenses belong to their respective owners; no license is granted by this software.'),
        z('链上数据不可篡改，但仅供参考，不构成投资、法律、财务或商业建议；据此操作的风险由使用者自行承担。',
          'On-chain data is immutable but provided for reference only. It is not investment, legal, financial or business advice; risks from acting on it are borne by the user.'),
        z('作者保留在不另行通知的情况下修改、暂停或停止提供本软件及相关服务的权利。',
          'The author reserves the right to modify, suspend or discontinue this software and related services without notice.'),
      ]),
      h3('about-license', '第三方声明', 'Third-party notices'),
      p('桌宠形象与台词移植自 dafeiyu-pet（MIT），托盘库 systray 为 Apache-2.0，前端内嵌 marked（MIT）与 highlight.js（BSD-3-Clause）。完整许可见仓库 THIRD-PARTY-NOTICE.md。',
        'Pet art and lines are adapted from dafeiyu-pet (MIT); systray is Apache-2.0; bundled marked (MIT) and highlight.js (BSD-3-Clause). Full notices are in THIRD-PARTY-NOTICE.md.'),
    ]},
  ];

  function flat(x){
    if(typeof x === 'string') return x;
    if(Array.isArray(x)) return x.map(flat).join(' ');
    if(x && typeof x === 'object') return Object.keys(x)
      .filter(k => k !== 'code').map(k => flat(x[k])).join(' ');
    return '';
  }

  function renderBody(){
    let html = '';
    D.forEach(sec => {
      html += '<section class="docs-sec"><h2 id="doc-' + sec.id + '">' + esc(val(sec.title)) + '</h2>';
      sec.body.forEach(b => {
        if(b.k === 'p') html += '<p>' + esc(val(b)) + '</p>';
        else if(b.k === 'note') html += '<div class="docs-note">' + esc(val(b)) + '</div>';
        else if(b.k === 'h3') html += '<h3 id="doc-' + b.id + '">' + esc(val(b)) + '</h3>';
        else if(b.k === 'ul' || b.k === 'ol')
          html += '<' + b.k + '>' + b.items.map(i => '<li>' + esc(val(i)) + '</li>').join('') + '</' + b.k + '>';
        else if(b.k === 'code')
          html += '<div class="docs-code"><button class="docs-copy" type="button">' + esc(t('复制')) +
                  '</button><pre>' + esc(b.code) + '</pre></div>';
        else if(b.k === 'ver'){
          const ve = document.getElementById('about-ver');
          html += '<p>' + esc(val(z('当前版本：', 'Current version: ')) + (ve ? ve.textContent.trim() : t('未知'))) + '</p>';
        }
      });
      html += '</section>';
    });
    body.innerHTML = html;
    renderToc();
  }

  function renderTree(){
    let html = '', last = null, n = 0;
    D.forEach(sec => {
      const g = val(sec.group);
      if(g !== last){
        if(last !== null) html += '</div>';
        html += '<div class="docs-grpbox" data-g="' + (n++) + '"><div class="docs-grp">' + esc(g) + '</div>';
        last = g;
      }
      html += '<button class="docs-item" type="button" data-sec="' + sec.id + '">' + esc(val(sec.title)) + '</button>';
    });
    if(last !== null) html += '</div>';
    tree.innerHTML = html;
    filterTree();
  }

  function renderToc(){
    const hs = Array.prototype.slice.call(body.querySelectorAll('h2[id],h3[id]'));
    toc.innerHTML = '<div class="docs-toc-t">' + esc(t('本页目录')) + '</div>' +
      hs.map(h => '<a href="#" data-anchor="' + h.id + '"' + (h.tagName === 'H3' ? ' class="sub"' : '') +
        '>' + esc(h.textContent) + '</a>').join('');
  }

  function filterTree(){
    const v = (q.value || '').trim().toLowerCase();
    Array.prototype.forEach.call(tree.querySelectorAll('.docs-grpbox'), box => {
      let visible = 0;
      Array.prototype.forEach.call(box.querySelectorAll('.docs-item'), b => {
        const sec = D.filter(s => s.id === b.dataset.sec)[0];
        if(!sec) return;
        const hay = (val(sec.title) + ' ' + val(sec.group) + ' ' + flat(sec.body)).toLowerCase();
        const on = !v || hay.indexOf(v) >= 0;
        b.hidden = !on;
        if(on) visible++;
      });
      box.hidden = visible === 0;
    });
  }

  function scrollTo(id){
    const t = document.getElementById('doc-' + id);
    if(!t) return;
    body.scrollTop += t.getBoundingClientRect().top - body.getBoundingClientRect().top - 6;
    Array.prototype.forEach.call(tree.querySelectorAll('.docs-item'), b =>
      b.classList.toggle('on', b.dataset.sec === id));
    if(window.matchMedia('(max-width:1080px)').matches) return;
    Array.prototype.forEach.call(toc.querySelectorAll('[data-anchor]'), a =>
      a.classList.toggle('on', a.dataset.anchor === 'doc-' + id));
  }

  tree.addEventListener('click', e => {
    const b = e.target.closest && e.target.closest('.docs-item');
    if(b) scrollTo(b.dataset.sec);
  });
  toc.addEventListener('click', e => {
    const a = e.target.closest && e.target.closest('[data-anchor]');
    if(!a) return;
    e.preventDefault();
    const h = document.getElementById(a.dataset.anchor);
    if(h) body.scrollTop += h.getBoundingClientRect().top - body.getBoundingClientRect().top - 6;
    Array.prototype.forEach.call(toc.querySelectorAll('[data-anchor]'), x => x.classList.toggle('on', x === a));
  });
  q.addEventListener('input', filterTree);
  body.addEventListener('scroll', () => {
    let cur = '';
    Array.prototype.forEach.call(body.querySelectorAll('h2[id]'), h => {
      if(h.getBoundingClientRect().top <= body.getBoundingClientRect().top + 90) cur = h.id.slice(4);
    });
    if(cur) Array.prototype.forEach.call(tree.querySelectorAll('.docs-item'), b =>
      b.classList.toggle('on', b.dataset.sec === cur));
  });
  body.addEventListener('click', async e => {
    const b = e.target.closest && e.target.closest('.docs-copy');
    if(!b) return;
    const pre = b.parentNode.querySelector('pre');
    const s = pre ? pre.textContent : '';
    try{
      if(navigator.clipboard && navigator.clipboard.writeText) await navigator.clipboard.writeText(s);
      else{
        const ta = document.createElement('textarea');
        ta.value = s; document.body.appendChild(ta); ta.select();
        document.execCommand('copy'); ta.remove();
      }
    }catch(_){ /* 剪贴板被浏览器拦下时也要给可见反馈，按钮不能像没点中 */ }
    b.textContent = t('已复制');
    setTimeout(() => { b.textContent = t('复制'); }, 1200);
  });

  document.addEventListener('ek:lang', () => { renderBody(); renderTree(); });
  renderBody();
  renderTree();
})();
