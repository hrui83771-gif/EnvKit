/* web/src/js/95-project-setup.js —— 首屏之后的项目接入向导。
 *
 * 只做三件事：选目录 → 只读检查 → 保存 + 可选写入数据库配置。
 * 检查接口是 /api/project/inspect，保存接口是 /api/project/apply；
 * 密码永不出现在 inspect 响应里，保存时由后端按候选 ID 重新解析后落盘。
 */
(function(){
  'use strict';

  var WIZ_KEY = 'ek-project-wizard-v1';
  var STEPS = [
    { id: 'front-dir',     title: '检查前端目录',     desc: '目录存在、可读、有项目特征' },
    { id: 'back-dir',      title: '检查后端目录',     desc: '目录存在、可读、有构建文件' },
    { id: 'package',       title: '读取 package.json', desc: '启动脚本与依赖线索' },
    { id: 'front-kind',    title: '识别前端框架',     desc: 'Vite / Next / Vue CLI / CRA / Node' },
    { id: 'front-port',    title: '解析前端端口',     desc: '配置声明、脚本参数、框架默认' },
    { id: 'back-build',    title: '识别后端构建系统', desc: 'Maven / Go' },
    { id: 'back-launch',   title: '识别后端启动方式', desc: 'mvn spring-boot:run / go run' },
    { id: 'back-port',     title: '解析后端端口',     desc: '配置声明、框架默认、端口候选' },
    { id: 'db-files',      title: '扫描数据库配置',   desc: 'application / .env / config / compose' },
    { id: 'db-parse',      title: '解析数据库候选',   desc: '库名、账号、密码占位符' },
    { id: 'tools',         title: '检查工具链与依赖', desc: 'Node / Go / JDK / Maven' },
    { id: 'summary',       title: '汇总项目画像',     desc: '端口占用与最终结果' }
  ];

  var modal = null, state = { view: 1, data: null, checking: false, seq: 0, auto: false, applied: false };
  var lastFocus = null, built = false;

  function q(sel){ return modal ? modal.querySelector(sel) : null; }
  function qa(sel){ return modal ? Array.prototype.slice.call(modal.querySelectorAll(sel)) : []; }
  function wait(ms){ return new Promise(function(res){ setTimeout(res, ms); }); }
  function reduced(){ return !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches); }
  function pathName(p){ p = String(p || ''); var parts = p.replace(/\\/g, '/').split('/'); return parts[parts.length - 1] || p; }
  function pct(n){ n = Math.max(0, Math.min(100, Math.round(n))); return n + '%'; }

  function kindLabel(kind, target, files){
    if(kind === 'vite') return 'Vite';
    if(kind === 'next') return 'Next.js';
    if(kind === 'nuxt') return 'Nuxt';
    if(kind === 'vue-cli') return 'Vue CLI';
    if(kind === 'cra') return 'React (CRA)';
    if(kind === 'node') return 'Node 脚本';
    if(kind === 'go') return 'Go';
    if(kind === 'maven'){
      var f = (files || []).some(function(x){ return String(x).indexOf('application') >= 0; });
      return f ? 'Spring Boot / Maven' : 'Maven';
    }
    return target === 'web' ? '未识别前端' : '未识别后端';
  }

  function portText(ports){
    if(!ports || !ports.length) return '';
    var p = ports[0];
    var src = String(p.source || '');
    if(src === '框架默认端口') src = t('框架默认');
    return p.port + ' · ' + src + (p.confidence ? ' · ' + p.confidence + '%' : '');
  }

  function mk(tag, cls, html){ var e = document.createElement(tag); if(cls) e.className = cls; if(html != null) e.innerHTML = html; return e; }

  /* ---------- 弹层骨架 ---------- */

  function build(){
    if(built) return;
    built = true;
    modal = document.createElement('div');
    modal.className = 'pws';
    modal.id = 'project-wizard';
    modal.setAttribute('aria-hidden', 'true');
    modal.innerHTML =
      '<div class="pws-mask" data-pws-close></div>' +
      '<div class="pws-panel" role="dialog" aria-modal="true" aria-labelledby="pws-title">' +
        '<div class="pws-top">' +
          '<div class="pws-brand"><span class="pws-logo">EK</span><div><b id="pws-title">' + esc(t('项目接入向导')) + '</b><p>' + esc(t('选目录，剩下的交给检查')) + '</p></div></div>' +
          '<button class="pws-x" type="button" data-pws-close aria-label="' + esc(t('关闭')) + '">×</button>' +
        '</div>' +
        '<div class="pws-progress" aria-hidden="true">' +
          '<span data-pill="1"><b>1</b>' + esc(t('目录')) + '</span><i></i>' +
          '<span data-pill="2"><b>2</b>' + esc(t('检查')) + '</span><i></i>' +
          '<span data-pill="3"><b>3</b>' + esc(t('结果')) + '</span>' +
        '</div>' +
        '<div class="pws-body">' +
          '<section class="pws-view" data-view="1"></section>' +
          '<section class="pws-view" data-view="2"></section>' +
          '<section class="pws-view" data-view="3"></section>' +
        '</div>' +
      '</div>';
    document.body.appendChild(modal);

    q('[data-view="1"]').innerHTML =
      '<div class="pws-hero">' +
        '<div class="pws-kicker">' + esc(t('01 / 接入项目')) + '</div>' +
        '<h2>' + esc(t('把项目交给 EnvKit')) + '</h2>' +
        '<p class="pws-lead">' + esc(t('只需要告诉它前后端在哪。EnvKit 会自己识别项目类型、启动方式、端口和数据库配置，不会启动服务、不会安装依赖、不会杀进程。')) + '</p>' +
      '</div>' +
      '<div class="pws-dirs">' +
        '<label class="pws-field"><span>' + esc(t('前端目录')) + '<em>' + esc(t('可选')) + '</em></span>' +
          '<div class="pws-inputrow"><input id="pws-fe" autocomplete="off" spellcheck="false" placeholder="' + esc(t('例如 D:\\project\\web')) + '"><button type="button" class="pws-pick" data-pws-pick="pws-fe">' + esc(t('选择')) + '</button></div>' +
          '<small>' + esc(t('有 package.json / vite.config 的目录')) + '</small></label>' +
        '<label class="pws-field"><span>' + esc(t('后端目录')) + '<em>' + esc(t('可选')) + '</em></span>' +
          '<div class="pws-inputrow"><input id="pws-be" autocomplete="off" spellcheck="false" placeholder="' + esc(t('例如 D:\\project\\server')) + '"><button type="button" class="pws-pick" data-pws-pick="pws-be">' + esc(t('选择')) + '</button></div>' +
          '<small>' + esc(t('有 pom.xml / go.mod / main.go 的目录')) + '</small></label>' +
      '</div>' +
      '<div class="pws-err" id="pws-err1"></div>' +
      '<div class="pws-foot"><span class="pws-note">' + esc(t('至少填一个目录；只填后端也可以。')) + '</span>' +
        '<button class="pws-later" type="button" data-pws-close>' + esc(t('稍后再说')) + '</button>' +
        '<button class="pws-primary" id="pws-start" type="button">' + esc(t('开始检查')) + '<span>→</span></button></div>';

    q('[data-view="2"]').innerHTML =
      '<div class="pws-scan">' +
        '<div class="pws-orb" id="pws-orb"><div class="pws-orb-ring"></div><div class="pws-orb-core"><span id="pws-pct">0%</span><small id="pws-phase">' + esc(t('准备检查')) + '</small></div><i class="pws-dot d1"></i><i class="pws-dot d2"></i><i class="pws-dot d3"></i></div>' +
        '<div class="pws-steplist" id="pws-steps"></div>' +
      '</div>' +
      '<div class="pws-err" id="pws-err2"></div>' +
      '<div class="pws-foot"><span class="pws-note" id="pws-scan-note">' + esc(t('真实读取项目文件。检查很快完成时，画面会放慢到约 4 秒，方便你看清每一步。')) + '</span>' +
        '<button class="pws-later" type="button" id="pws-back1">' + esc(t('返回修改')) + '</button></div>';

    q('[data-view="3"]').innerHTML =
      '<div class="pws-resulthead"><div><div class="pws-kicker">' + esc(t('03 / 检查结果')) + '</div><h2 id="pws-result-title">' + esc(t('可以接入了')) + '</h2><p class="pws-lead" id="pws-sum"></p></div><div class="pws-seal" id="pws-seal"><span>✓</span><small>' + esc(t('只读检查')) + '</small></div></div>' +
      '<div class="pws-cards" id="pws-cards"></div>' +
      '<div class="pws-warnings" id="pws-warnings"></div>' +
      '<div class="pws-dbwrap" id="pws-dbwrap"></div>' +
      '<div class="pws-contractopt">' +
        '<label class="pws-dbuse"><input type="checkbox" id="pws-contract" checked>' + esc(t('同时生成项目级环境契约')) + '</label>' +
        '<small>' + esc(t('写入项目根 envkit.yaml，描述工具、服务、端口和数据库期望；不写密码。之后可用 git diff 审阅。')) + '</small>' +
      '</div>' +
      '<div class="pws-err" id="pws-err3"></div>' +
      '<div class="pws-foot"><span class="pws-note">' + esc(t('保存只写入前后端目录；勾选数据库时会一起写入，密码只在本机加密保存。')) + '</span>' +
        '<button class="pws-later" type="button" data-pws-close>' + esc(t('稍后再说')) + '</button>' +
        '<button class="pws-primary" id="pws-save" type="button">' + esc(t('保存并进入主界面')) + '</button></div>';

    bind();
    resetSteps();
  }

  function bind(){
    modal.addEventListener('click', function(ev){
      var close = ev.target.closest('[data-pws-close]');
      if(close){ closeWizard(true); }
      var pick = ev.target.closest('[data-pws-pick]');
      if(pick){
        var id = pick.getAttribute('data-pws-pick');
        var input = document.getElementById(id);
        openBrowser('dir', input.value || '', '', 'dir', function(p){
          input.value = p;
          state.applied = false;
          clearErr();
          input.focus();
        });
      }
    });
    q('#pws-start').onclick = startInspect;
    q('#pws-back1').onclick = function(){ state.seq++; state.checking = false; showView(1); };
    q('#pws-save').onclick = save;
    modal.querySelectorAll('input').forEach(function(inp){
      inp.addEventListener('input', function(){ state.applied = false; clearErr(); });
    });
  }

  function clearErr(){
    ['#pws-err1','#pws-err2','#pws-err3'].forEach(function(id){ var e = q(id); if(e){ e.textContent = ''; e.classList.remove('on'); } });
  }

  function showErr(id, msg){
    var e = q(id);
    if(!e) return;
    e.textContent = msg;
    e.classList.add('on');
  }

  function showView(n){
    state.view = n;
    qa('.pws-view').forEach(function(v){ v.classList.toggle('on', Number(v.getAttribute('data-view')) === n); });
    qa('[data-pill]').forEach(function(p){ p.classList.toggle('on', Number(p.getAttribute('data-pill')) <= n); });
    q('#pws-scan-note').style.display = n === 2 ? '' : 'none';
    clearErr();
    if(n === 1){ setTimeout(function(){ var i = q('#pws-fe'); if(i) i.focus(); }, 60); }
  }

  function resetSteps(){
    var host = q('#pws-steps');
    if(!host) return;
    host.innerHTML = '';
    STEPS.forEach(function(s, i){
      var row = mk('div', 'pws-step', '<span class="pws-si">' + (i + 1) + '</span><div><b>' + esc(t(s.title)) + '</b><em>' + esc(t(s.desc)) + '</em></div>');
      row.setAttribute('data-step', i);
      host.appendChild(row);
    });
  }

  function setStep(i, status, detail){
    var row = q('.pws-step[data-step="' + i + '"]');
    if(!row) return;
    row.className = 'pws-step' + (status ? ' ' + status : '');
    var icon = row.querySelector('.pws-si');
    if(icon) icon.textContent = status === 'done' ? '✓' : status === 'err' ? '×' : status === 'warn' ? '!' : (status === 'skip' ? '—' : (i + 1));
    var de = row.querySelector('em');
    if(de && detail) de.textContent = detail;
  }

  function setPct(n, phase){
    var p = q('#pws-pct'); if(p) p.textContent = pct(n);
    var o = q('#pws-orb'); if(o) o.style.setProperty('--p', String(Math.max(0, Math.min(100, n))));
    if(phase){ var ph = q('#pws-phase'); if(ph) ph.textContent = phase; }
  }

  function stepDetail(i, d){
    var f = d.frontend || {}, b = d.backend || {}, db = d.database || {};
    switch(STEPS[i].id){
      case 'front-dir':
        if(!f.provided) return t('未提供，跳过前端');
        return f.valid ? t('已确认目录') : t('目录不可用，请检查路径');
      case 'back-dir':
        if(!b.provided) return t('未提供，跳过后端');
        return b.valid ? t('已确认目录') : t('目录不可用，请检查路径');
      case 'package':
        if(!f.provided) return t('未提供前端，跳过');
        return f.package_json ? t('已读取 package.json') : t('没有 package.json，改用目录特征');
      case 'front-kind':
        if(!f.provided) return t('未提供前端，跳过');
        return f.kind ? t('识别为 {0}', kindLabel(f.kind, 'web', f.config_files)) : t('未识别出前端类型');
      case 'front-port':
        if(!f.provided) return t('未提供前端，跳过');
        return f.ports && f.ports.length ? portText(f.ports) : t('没有端口线索，启动时再探');
      case 'back-build':
        if(!b.provided) return t('未提供后端，跳过');
        return b.kind === 'maven' ? t('识别为 Maven（pom.xml）') : b.kind === 'go' ? t('识别为 Go（go.mod / main.go）') : t('未识别出构建系统');
      case 'back-launch':
        if(!b.provided) return t('未提供后端，跳过');
        return b.launch ? b.launch : t('没有可用的启动方式，请在后端目录补 main.go / pom.xml');
      case 'back-port':
        if(!b.provided) return t('未提供后端，跳过');
        return b.ports && b.ports.length ? portText(b.ports) : t('没有端口线索，启动时再探');
      case 'db-files':
        return db.files && db.files.length ? t('扫描了 {0} 个配置文件', db.files.length) : t('没有找到数据库配置文件');
      case 'db-parse':
        if(!db.found) return t('没有发现数据库候选');
        var c0 = db.candidates[0];
        return (c0.name || t('未命名库')) + ' · ' + (c0.user || t('未识别账号')) + ' · ' + (c0.has_password ? t('密码已找到（脱敏）') : t('密码未解析'));
      case 'tools':
        return (d.tools || []).map(function(x){ return x.name + (x.found ? (x.version ? ' ' + x.version : ' ✓') : ' ' + t('未找到')) + (x.warn ? ' · ⚠ ' + x.warn : ''); }).join(' · ') || t('没有需要检查的工具');
      case 'summary':
        var parts = [];
        if(f.provided) parts.push(t('前端') + ' ' + kindLabel(f.kind, 'web', f.config_files));
        if(b.provided) parts.push(t('后端') + ' ' + kindLabel(b.kind, 'backend', b.config_files));
        if(db.found) parts.push(t('数据库') + ' ' + (db.candidates[0].name || t('候选')));
        return parts.join(' · ') || t('没有可汇总的信息');
    }
    return '';
  }

  function stepStatus(i, d){
    var f = d.frontend || {}, b = d.backend || {}, db = d.database || {};
    switch(STEPS[i].id){
      case 'front-dir': return !f.provided ? 'skip' : f.valid ? 'done' : 'err';
      case 'back-dir': return !b.provided ? 'skip' : b.valid ? 'done' : 'err';
      case 'package': return !f.provided || !f.package_json ? 'warn' : 'done';
      case 'front-kind': return !f.provided ? 'skip' : (f.kind && f.kind !== 'none') ? 'done' : 'warn';
      case 'front-port': return !f.provided ? 'skip' : (f.ports && f.ports.length) ? 'done' : 'warn';
      case 'back-build': return !b.provided ? 'skip' : b.kind ? 'done' : 'warn';
      case 'back-launch': return !b.provided ? 'skip' : b.launch ? 'done' : 'warn';
      case 'back-port': return !b.provided ? 'skip' : (b.ports && b.ports.length) ? 'done' : 'warn';
      case 'db-files': return db.files && db.files.length ? 'done' : 'warn';
      case 'db-parse': return db.found ? 'done' : 'warn';
      case 'tools': return ((d.tools || []).every(function(x){ return x.found && !x.warn; })) ? 'done' : 'warn';
      case 'summary': return 'done';
    }
    return 'done';
  }

  /* ---------- 检查流程 ---------- */

  async function startInspect(){
    var f = (q('#pws-fe').value || '').trim();
    var b = (q('#pws-be').value || '').trim();
    if(!f && !b){ showErr('#pws-err1', t('至少选择前端或后端目录')); return; }
    state.data = null;
    state.checking = true;
    var seq = ++state.seq;
    resetSteps();
    setPct(2, t('准备检查'));
    showView(2);
    q('#pws-orb').classList.add('run');
    var t0 = Date.now();
    try{
      var r = await fetch('/api/project/inspect', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({frontend_dir: f, backend_dir: b})
      });
      if(!r.ok) throw new Error(await r.text());
      state.data = await r.json();
    }catch(e){
      if(seq !== state.seq) return;
      state.checking = false;
      q('#pws-orb').classList.remove('run');
      setPct(0, t('检查失败'));
      showErr('#pws-err2', t('检查失败：{0}', e.message || e));
      return;
    }
    if(seq !== state.seq) return;
    var elapsed = Date.now() - t0;
    var delay = reduced() ? 18 : (elapsed < 1800 ? 300 : (elapsed < 4500 ? 220 : 120));
    for(var i = 0; i < STEPS.length; i++){
      if(seq !== state.seq) return;
      setStep(i, 'active', t('读取中…'));
      setPct((i / STEPS.length) * 92 + 4, t('检查中…'));
      await wait(delay);
      if(seq !== state.seq) return;
      setStep(i, stepStatus(i, state.data), stepDetail(i, state.data));
      setPct(((i + 1) / STEPS.length) * 92 + 4, t('检查中…'));
    }
    state.checking = false;
    q('#pws-orb').classList.remove('run');
    setPct(100, t('检查完成'));
    await wait(reduced() ? 50 : 520);
    if(seq !== state.seq) return;
    renderResult(state.data);
    showView(3);
  }

  /* ---------- 结果 ---------- */

  function serviceCard(title, svc, target, i){
    var f = svc || {};
    var ok = f.provided && f.valid;
    var chip = !f.provided ? ['muted', t('未提供')] : (ok ? ['ok', t('已识别')] : ['warn', t('未识别')]);
    var rows = '';
    rows += '<div class="pws-row"><span>' + esc(t('项目类型')) + '</span><b>' + esc(kindLabel(f.kind, target, f.config_files)) + '</b></div>';
    if(f.label) rows += '<div class="pws-row"><span>' + esc(t('项目名')) + '</span><b>' + esc(f.label) + '</b></div>';
    rows += '<div class="pws-row"><span>' + esc(t('目录')) + '</span><b class="pws-path" title="' + esc(f.dir || '') + '">' + esc(f.dir || '—') + '</b></div>';
    rows += '<div class="pws-row"><span>' + esc(t('启动方式')) + '</span><b class="pws-mono">' + esc(f.launch || '—') + '</b></div>';
    rows += '<div class="pws-row"><span>' + esc(t('端口')) + '</span><b>' + (f.ports && f.ports.length ? esc(String(f.ports[0].port)) : '—') + '</b></div>';
    if(f.ports && f.ports.length){
      rows += '<div class="pws-portmeta">' + esc(String(f.ports[0].source || '')) + ' · ' + esc(t('置信度 {0}%', f.ports[0].confidence)) + '</div>';
    }
    if(target === 'web'){
      rows += '<div class="pws-row"><span>' + esc(t('依赖')) + '</span><b>' + esc(f.node_modules ? t('node_modules 已存在') : (f.provided ? t('未安装 node_modules') : '—')) + '</b></div>';
    }
    return '<article class="pws-card" style="--i:' + i + '"><div class="pws-cardtop"><span>' + esc(title) + '</span><em class="pws-chip ' + chip[0] + '">' + esc(chip[1]) + '</em></div>' + rows + '</article>';
  }

  function dbHTML(db){
    if(!db || !db.found || !db.candidates || !db.candidates.length){
      return '<div class="pws-empty">' + esc(t('没有在项目里找到数据库配置。可以稍后在「程序配置」里填写，或现在先只接入前后端。')) + '</div>';
    }
    var rows = db.candidates.map(function(c, i){
      var checked = db.candidates.length === 1;
      var title = c.name || t('未命名库');
      var pwd = c.has_password ? t('已找到（脱敏）') : (c.unresolved && c.unresolved.indexOf('password') >= 0 ? t('引用变量，未解析') : t('未找到'));
      var extra = '';
      if(c.has_password && c.password_source && c.password_source !== c.source){ extra = ' · ' + t('密码来自 {0}', c.password_source); }
      if(c.unresolved && c.unresolved.length){ extra += ' · ' + t('未解析：{0}', c.unresolved.join('、')); }
      var note = c.notes && c.notes.length ? '<div class="pws-dbnote">' + esc(c.notes[0]) + '</div>' : '';
      return '<label class="pws-dbrow"><input type="radio" name="pws-db-choice" value="' + esc(c.id) + '"' + (checked ? ' checked' : '') + '>' +
        '<div class="pws-dbmain"><div class="pws-dbtitle"><b>' + esc(title) + '</b><span>' + esc(c.host || '—') + ':' + esc(String(c.port || 3306)) + '</span></div>' +
        '<div class="pws-dbmeta">' + esc(t('用户 {0}', c.user || '—')) + ' · ' + esc(t('密码 {0}', pwd)) + extra + '</div>' +
        '<div class="pws-dbsrc">' + esc(c.source) + ' · ' + esc(t('置信度 {0}%', c.confidence)) + '</div>' + note + '</div></label>';
    }).join('');
    var files = db.sql_files || [];
    var opts = files.map(function(f){
      return '<option value="' + esc(f.path) + '">' + esc(f.rel || f.name) + (f.size ? ' · ' + fmtSize(f.size) : '') + '</option>';
    }).join('');
    var sqlBlock = files.length
      ? '<label class="pws-dbimport"><input type="checkbox" id="pws-db-import">' + esc(t('建库后导入 SQL')) + '</label>' +
        '<select id="pws-sql-file" class="pws-sqlfile" disabled>' + opts + '</select>'
      : '<span class="pws-dbnosql">' + esc(t('项目里没发现 .sql 文件；仍然可以只建空库。')) + '</span>';
    var hint = db.candidates.length > 1 ? '<span class="pws-dbhint">' + esc(t('有多组候选，请挑一组再保存。')) + '</span>' : '';
    return '<div class="pws-dbhead"><div><span class="pws-dbicon">DB</span><b>' + esc(t('找到 {0} 组数据库配置', db.candidates.length)) + '</b>' + hint + '</div>' +
      '<div class="pws-dbheadact"><label class="pws-dbuse"><input type="checkbox" id="pws-db-use" checked>' + esc(t('同时写入数据库配置')) + '</label>' +
      '<button type="button" class="pws-dbtest" id="pws-db-test">' + esc(t('测试连接')) + '</button></div></div>' +
      '<div class="pws-dblist" id="pws-dblist">' + rows + '</div>' +
      '<div class="pws-dbops">' +
        '<label class="pws-dbcreate"><input type="checkbox" id="pws-db-create" checked>' + esc(t('保存后建库（库不存在时）')) + '</label>' +
        sqlBlock +
        '<div class="pws-dbresult" id="pws-db-result"></div>' +
      '</div>' +
      '<div class="pws-dbprivacy">' + esc(t('密码只在本机加密保存；不会写日志、不会发给 AI。')) + '</div>';
  }

  function fmtSize(n){
    if(n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB';
    if(n >= 1024) return Math.round(n / 1024) + ' KB';
    return n + ' B';
  }

  function selectedDBID(){
    var c = q('input[name="pws-db-choice"]:checked');
    return c ? c.value : '';
  }

  function showDBResult(msg, cls){
    var e = q('#pws-db-result');
    if(!e) return;
    e.textContent = msg || '';
    e.className = 'pws-dbresult' + (cls ? ' ' + cls : '');
  }

  async function testDB(){
    var id = selectedDBID();
    if(!id){ showDBResult(t('请先选择一组数据库配置'), 'err'); return; }
    var btn = q('#pws-db-test');
    if(btn){ btn.disabled = true; btn.textContent = t('连接中…'); }
    showDBResult(t('正在连接 MySQL…'), '');
    try{
      var r = await fetch('/api/project/db-test', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          frontend_dir: (q('#pws-fe').value || '').trim(),
          backend_dir: (q('#pws-be').value || '').trim(),
          db_id: id
        })
      });
      var j = {}; try{ j = await r.json(); }catch(e){}
      if(!r.ok) throw new Error(j.error || (j.message || ('HTTP ' + r.status)));
      if(!j.connected) throw new Error(j.error || t('连接失败'));
      var msg = j.exists ? t('连接成功，库已存在') : t('连接成功，库不存在，可以创建');
      if(j.version) msg += ' · MySQL ' + j.version;
      showDBResult(msg, j.exists ? 'ok' : 'warn');
      var create = q('#pws-db-create');
      if(create && !j.exists) create.checked = true;
    }catch(e){
      showDBResult(t('连接失败：{0}', e.message || e), 'err');
    }finally{
      if(btn){ btn.disabled = false; btn.textContent = t('测试连接'); }
    }
  }

  function renderResult(d){
    var f = d.frontend || {}, b = d.backend || {}, db = d.database || {};
    var cards = serviceCard(t('前端'), f, 'web', 0) + serviceCard(t('后端'), b, 'backend', 1);
    q('#pws-cards').innerHTML = cards;
    var dbwrap = q('#pws-dbwrap');
    dbwrap.innerHTML = dbHTML(db);
    dbwrap.onchange = function(){ state.applied = false; };
    if(dbwrap.querySelector('#pws-db-use')){
      dbwrap.querySelector('#pws-db-use').addEventListener('change', function(e){
        dbwrap.querySelectorAll('input[type="radio"]').forEach(function(x){ x.disabled = !e.target.checked; });
        ['#pws-db-create','#pws-db-import','#pws-sql-file'].forEach(function(sel){
          var x = dbwrap.querySelector(sel);
          if(x) x.disabled = !e.target.checked || (sel === '#pws-sql-file' && !(dbwrap.querySelector('#pws-db-import') || {}).checked);
        });
        if(!e.target.checked) showDBResult('', '');
        state.applied = false;
      });
    }
    var testBtn = dbwrap.querySelector('#pws-db-test');
    if(testBtn) testBtn.onclick = testDB;
    var importBox = dbwrap.querySelector('#pws-db-import');
    var sqlFile = dbwrap.querySelector('#pws-sql-file');
    if(importBox && sqlFile){
      importBox.addEventListener('change', function(){
        sqlFile.disabled = !importBox.checked;
        if(importBox.checked){
          var create = q('#pws-db-create');
          if(create) create.checked = true;
        }
      });
    }
    var parts = [];
    if(f.provided && f.valid) parts.push(t('前端 {0}', kindLabel(f.kind, 'web', f.config_files)));
    if(b.provided && b.valid) parts.push(t('后端 {0}', kindLabel(b.kind, 'backend', b.config_files)));
    if(db.found && db.candidates[0] && db.candidates[0].name) parts.push(t('数据库 {0}', db.candidates[0].name));
    var warn = (d.warnings || []).length;
    var warns = (d.warnings || []).map(function(w){
      return '<div class="pws-warnitem"><span>!</span><div>' + esc(w) + '</div></div>';
    }).join('');
    q('#pws-warnings').innerHTML = warns;
    q('#pws-result-title').textContent = warn ? t('可以接入，但有 {0} 项要先处理', warn) : t('可以接入了');
    q('#pws-sum').textContent = parts.join(' · ') + (warn ? '　·　' + t('{0} 项需要留意', warn) : '');
    q('#pws-seal').classList.toggle('warn', warn > 0);
    q('#pws-seal').querySelector('small').textContent = warn ? t('{0} 项留意', warn) : t('只读检查');
  }

  function candidateByID(id){
    var d = state.data || {};
    var db = d.database || {};
    var list = db.candidates || [];
    for(var i = 0; i < list.length; i++){
      if(list[i].id === id) return list[i];
    }
    return null;
  }

  async function progState(){
    var r = await fetch('/api/program/state');
    if(!r.ok) throw new Error('无法读取进度');
    return await r.json();
  }

  async function waitInitDB(beforeAt, timeoutMs){
    var started = false;
    var deadline = Date.now() + (timeoutMs || 180000);
    while(Date.now() < deadline){
      var st = await progState();
      var s = st['init-db'] || {};
      if(s.running) started = true;
      if((started || (s.at && s.at !== beforeAt)) && !s.running){
        if(s.ok) return s;
        throw new Error(s.msg || t('建库或导入失败'));
      }
      await wait(700);
    }
    throw new Error(t('数据库操作仍在后台执行，请到「程序配置」查看进度'));
  }

  async function save(){
    var f = (q('#pws-fe').value || '').trim();
    var b = (q('#pws-be').value || '').trim();
    if(!f && !b){ showErr('#pws-err3', t('至少选择前端或后端目录')); return; }
    var use = false, id = '', chosen = null;
    var useBox = q('#pws-db-use');
    if(useBox && useBox.checked){
      use = true;
      id = selectedDBID();
      if(!id){ showErr('#pws-err3', t('请选择一组数据库配置，或取消勾选写入')); return; }
      chosen = candidateByID(id);
      if(!chosen){ showErr('#pws-err3', t('数据库候选已变化，请返回重新检查')); return; }
    }
    var doCreate = !!(use && q('#pws-db-create') && q('#pws-db-create').checked);
    var doImport = !!(use && q('#pws-db-import') && q('#pws-db-import').checked);
    var sqlPath = '';
    if(doImport){
      var sqlFile = q('#pws-sql-file');
      sqlPath = sqlFile ? sqlFile.value : '';
      if(!sqlPath){ showErr('#pws-err3', t('请选择要导入的 SQL 文件')); return; }
    }
    if(doCreate && !chosen.name){ showErr('#pws-err3', t('候选里没有数据库名，无法建库')); return; }

    var btn = q('#pws-save');
    btn.disabled = true;
    var old = btn.textContent;
    var beforeAt = '';
    var contractNote = '';
    var contractWarn = '';
    try{
      if(doCreate || doImport){
        try{ beforeAt = ((await progState())['init-db'] || {}).at || ''; }catch(e){}
      }
      if(!state.applied){
        btn.textContent = t('保存中…');
        var r = await fetch('/api/project/apply', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({frontend_dir: f, backend_dir: b, use_database: use, db_id: id})
        });
        if(!r.ok){
          var msg = await r.text();
          try{
            var j = JSON.parse(msg);
            if(j.problems && j.problems.length) msg = j.problems.map(function(p){ return (p.field || '') + ' → ' + (p.msg || ''); }).join('\n');
          }catch(e){}
          throw new Error(msg);
        }
        state.applied = true;
      }
      if(q('#pws-contract') && q('#pws-contract').checked){
        try{
          var cr = await fetch('/api/contract/generate', {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({frontend_dir: f, backend_dir: b, save: true})
          });
          var cj = await cr.json();
          if(!cr.ok || !cj.ok){
            var cp = (cj.problems || []).join('；');
            contractWarn = cp || (cj.error || t('契约未生成'));
          }else if(cj.written){
            contractNote = t('环境契约已生成，可 git diff 审阅');
          }else if(cj.existing){
            contractNote = t('已有环境契约，未覆盖');
          }
        }catch(e){
          contractWarn = e.message || String(e);
        }
      }
      var dbDone = '';
      if(doCreate || doImport){
        btn.textContent = doImport ? t('建库并导入中…') : t('建库中…');
        showDBResult(doImport ? t('正在创建数据库并导入 SQL…') : t('正在创建数据库…'), '');
        var ir = await fetch('/api/program/init-db', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({db: chosen.name, sql: doImport ? sqlPath : ''})
        });
        if(!ir.ok){
          var im = await ir.text();
          throw new Error(im || t('建库请求失败'));
        }
        var done = await waitInitDB(beforeAt, 300000);
        dbDone = doImport ? t('数据库已创建并导入 SQL') : t('数据库已创建');
        showDBResult(dbDone + (done.msg ? ' · ' + done.msg : ''), 'ok');
      }
      markSeen();
      closeWizard(false);
      var doneMsg = dbDone ? t('项目已接入，{0}', dbDone) : t('项目已接入，配置已保存');
      if(contractNote) doneMsg += ' · ' + contractNote;
      toast(doneMsg, 'ok');
      if(contractWarn) toast(t('环境契约未生成：{0}', contractWarn), 'warn');
      try{ await loadConfig(); refreshGuards(); pollHealth(); pollProg(); }catch(e){}
    }catch(e){
      var prefix = state.applied ? t('配置已保存，但数据库操作失败：') : t('保存失败：');
      showErr('#pws-err3', prefix + (e.message || e));
      if(state.applied) btn.textContent = t('重试数据库操作');
    }finally{
      btn.disabled = false;
      if(btn.textContent !== t('重试数据库操作')) btn.textContent = old;
    }
  }

  /* ---------- 打开/关闭 ---------- */

  function markSeen(){ try{ localStorage.setItem(WIZ_KEY, '1'); }catch(e){} }
  function seen(){ try{ return localStorage.getItem(WIZ_KEY) === '1'; }catch(e){ return false; } }

  function openWizard(opts){
    build();
    state.auto = !!(opts && opts.auto);
    state.seq++;
    state.checking = false;
    state.data = null;
    state.applied = false;
    lastFocus = document.activeElement;
    var f = '', b = '';
    try{ if(typeof cfg !== 'undefined' && cfg && cfg.projects){ f = cfg.projects.frontend_dir || ''; b = cfg.projects.backend_dir || ''; } }catch(e){}
    q('#pws-fe').value = f;
    q('#pws-be').value = b;
    clearErr();
    resetSteps();
    setPct(0, t('准备检查'));
    showView(1);
    modal.classList.add('show');
    modal.setAttribute('aria-hidden', 'false');
    document.body.classList.add('pws-open');
  }

  function closeWizard(mark){
    if(!modal) return;
    if(state.checking) state.seq++;
    state.checking = false;
    modal.classList.remove('show');
    modal.setAttribute('aria-hidden', 'true');
    document.body.classList.remove('pws-open');
    if(mark) markSeen();
    if(lastFocus && typeof lastFocus.focus === 'function'){ try{ lastFocus.focus(); }catch(e){} }
    lastFocus = null;
  }

  function shouldAuto(){
    if(seen()) return false;
    var empty = true;
    try{
      if(typeof cfg !== 'undefined' && cfg && cfg.projects){
        empty = !(cfg.projects.frontend_dir || cfg.projects.backend_dir);
      }
    }catch(e){}
    return empty;
  }

  function autoOpen(){
    if(!shouldAuto()) return;
    if(modal && modal.classList.contains('show')) return;
    setTimeout(function(){ if(shouldAuto()) openWizard({auto: true}); }, 650);
  }

  /* 首屏演示结束（90-onboard.js 的 finish）后自动弹一次；
     老用户已经看过演示、但目录仍为空时，加载后也补一次。
     只自动弹一次，跳过/关闭后靠顶栏「接入项目」常驻入口。 */
  document.addEventListener('ek:onboard-finish', autoOpen);
  setTimeout(function(){
    try{
      if(window.EKOnboard && EKOnboard.hasSeen && EKOnboard.hasSeen()) autoOpen();
    }catch(e){}
  }, 1800);

  var btn = document.getElementById('btn-project-setup');
  if(btn) btn.onclick = function(){ openWizard({}); };

  document.addEventListener('keydown', function(e){
    if(e.key === 'Escape' && modal && modal.classList.contains('show')){
      e.preventDefault();
      closeWizard(true);
    }
  });

  window.EKProjectSetup = { open: openWizard, close: closeWizard, seen: seen };
})();
