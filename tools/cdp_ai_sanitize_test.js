// CDP 回归：AI 回答的 HTML 净化（自研白名单 + DOMPurify 兜底）。
//
// 守的事实：
//   1) DOMPurify 已随 exe 内嵌并经 /vendor/purify.min.js 同域加载（不依赖 CDN）；
//   2) 危险载荷进 aiMd() 之后，DOM 里既不剩 script，也不剩任何 on* 处理器，
//      更留不下 javascript: 协议的 href —— 这是「同源里有 API 令牌」那条真风险；
//   3) mXSS 变体（svg/style 里藏标签、注释里藏标签、大写混淆）同样被剥；
//   4) 正常 markdown（标题/表格/代码/外链）不被净化搞坏：表格还在、代码语言类还在、
//      外链必须带 target=_blank 与 rel=noopener（否则会把控制台整页顶掉）。
//
// 为什么不用「只测 <script>」当作证据：marked 对 <script> 的处理本来就是文本化，
// 那一条不写净化也会绿——是恒真检查。载荷里必须有能真正执行的路径才有判据价值。
//
// 用法：node tools/cdp_ai_sanitize_test.js
// 隔离：独立端口 18847 + DBG 9387 + ENVKIT_INSTANCE_TAG + 独立 exe 副本。
const { spawn } = require('child_process');
const fs = require('fs');
const http = require('http');
const path = require('path');

const CHROME = 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe';
const REPO = path.resolve(__dirname, '..');
const PORT = 18847;
const URL_ = 'http://127.0.0.1:' + PORT + '/';
const DBG = 9387;
const EXE = path.join(REPO, 'tools', '_aisan', 'EnvKit_aisan.exe');
const DIR = path.dirname(EXE);

function getJSON(url) {
  return new Promise((res, rej) => {
    http.get(url, r => { let d = ''; r.on('data', c => d += c); r.on('end', () => { try { res(JSON.parse(d)); } catch (e) { res(d); } }); }).on('error', rej);
  });
}
function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
function seed() {
  fs.mkdirSync(DIR, { recursive: true });
  // 每轮强制覆盖：只 if(!exists) 复制会让上一轮的旧副本一直被测试，
  // 重建了根 exe 也不生效（这条坑踩过三次）。
  fs.copyFileSync(path.join(REPO, 'EnvKit.exe'), EXE);
  fs.writeFileSync(path.join(DIR, 'config.json'), JSON.stringify({
    dev: { server_port: PORT },
    ai: { enabled: false },
    missions: { enabled: false, inbound_hook: false, briefing_at: 'off' },
  }, null, 2), 'utf8');
}
async function attach(target) {
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
  let mid = 0; const pending = new Map();
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
  };
  const send = (method, params) => new Promise(res => {
    const id = ++mid; pending.set(id, res); ws.send(JSON.stringify({ id, method, params }));
  });
  const evalJS = async expr => {
    const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
    if (r.result && r.result.exceptionDetails) throw new Error('页面异常：' + JSON.stringify(r.result.exceptionDetails));
    return r.result && r.result.result ? r.result.result.value : undefined;
  };
  return { ws, send, evalJS };
}

(async () => {
  const fails = [];
  const ok = (cond, msg) => { if (cond) console.log('  PASS ' + msg); else { fails.push(msg); console.log('  FAIL ' + msg); } };

  seed();
  const env = Object.assign({}, process.env, { ENVKIT_INSTANCE_TAG: 'aisan' });
  const envkit = spawn(EXE, [], { cwd: DIR, env: env, stdio: 'ignore', detached: false });
  let ready = false;
  for (let i = 0; i < 60; i++) {
    try { await getJSON(URL_ + 'api/health'); ready = true; break; } catch (e) { await sleep(500); }
  }
  if (!ready) { envkit.kill(); console.log('envkit not ready'); process.exit(1); }

  const chrome = spawn(CHROME, [
    '--headless=new', '--disable-gpu', '--no-proxy-server', '--disable-extensions',
    '--remote-debugging-port=' + DBG, '--user-data-dir=' + path.join(REPO, 'tools', '_cdptmp_aisan'),
    '--no-first-run', 'about:blank',
  ], { stdio: 'ignore' });
  await sleep(2500);

  try {
    let targets = null;
    for (let i = 0; i < 10; i++) {
      try { targets = await getJSON('http://127.0.0.1:' + DBG + '/json/list'); break; } catch (e) { await sleep(800); }
    }
    if (!targets || !Array.isArray(targets)) throw new Error('cdp targets unavailable');
    const page = targets.find(t => t.type === 'page');
    const c = await attach(page);
    const { send, evalJS } = c;
    await send('Runtime.enable', {});
    await send('Page.enable', {});
    await send('Page.navigate', { url: URL_ });
    await sleep(2600);
    // 页面里 /api 的请求要带令牌；这里只读 localStorage 与渲染结果，不发 API。
    // 语言固定中文：i18n 会把标题按浏览器语言替换，断言认结构不认字面。
    await evalJS(`(function(){try{window.ekSetLang && window.ekSetLang('zh',true);}catch(e){}return 'ok';})()`);

    // ---- 1) DOMPurify 本体：同域加载、版本对得上 ----
    // 注意 DOMPurify 3.x 的导出是「可调用的函数对象」，typeof 是 'function' 不是 'object'。
    // 只认 'object' 会假红一次（版本都能读到却判不可用），所以两种都收。
    const lib = JSON.parse(await evalJS(`JSON.stringify({
      has: !!window.DOMPurify && typeof window.DOMPurify.sanitize === 'function' &&
           (typeof window.DOMPurify === 'object' || typeof window.DOMPurify === 'function'),
      kind: typeof window.DOMPurify,
      ver: (window.DOMPurify && (window.DOMPurify.version || '')) || '',
      // 版本取不到时至少证明它能干活：把一个 script 剥成空
      probe: window.DOMPurify ? window.DOMPurify.sanitize('<img src=x onerror="alert(1)">') : 'NO_LIB'
    })`));
    ok(lib.has, 'DOMPurify 同域内嵌可用（/vendor/purify.min.js，typeof=' + lib.kind + '）');
    ok(lib.ver === '3.2.6', 'DOMPurify 版本与声明一致（实测 ' + (lib.ver || '空') + '）');
    ok(/onerror/i.test(lib.probe) === false, 'DOMPurify 自身剥掉 onerror（探针 → ' + String(lib.probe).slice(0, 40) + '）');

    // ---- 2) 危险载荷过 aiMd 之后的真实 DOM ----
    // 载荷分三类，缺一类就会留下「看起来绿、其实没测」的洞：
    //   ① 直白注入（script / img onerror / javascript: href）
    //   ② 命名空间混淆（SVG / MathML 里的 tagName 保留大小写，最容易绕过大小写白名单）
    //   ③ 结构性变形（注释里藏标签、表单嵌套）
    // ② 这一组是真漏洞的原始载荷：修之前 <svg><foreignObject><img onerror> 会整块逃逸，
    // 因为脱壳分支把未检查的子树直接提升到父级。少这一组，断言就永远不会红。
    const payloads = {
      script: '正常文字<script>window.__xss=1<\/script>后面还有字',
      imgOnerror: '看图 <img src=x onerror="window.__xss=2">',
      jsHref: '点我 [链接](javascript:window.__xss=3)',
      svgMxss: '<svg></p><style><img src=x onerror=window.__xss=4></style></svg>文字',
      caseMix: '<ScRiPt>window.__xss=5</ScRiPt><IMG SRC=x OnErRoR=window.__xss=6>',
      iframeForm: '<iframe src="//evil"></iframe><form action="/api/config/import"><input name=x></form>尾',
      dataAttr: '<p onclick="window.__xss=7" data-x="1" style="background:url(javascript:1)">段落</p>',
      // 纯 HTML 命名空间里的「未知标签包危险子元素」——模型最爱写的 <div align=center> 就是这一类。
      // 脱壳分支如果不先递归净化子树，被剥掉的壳就成了「免疫区」，里面的 on* 随子节点上移。
      // 这条与 SVG 无关，所以单独去掉 namespace 检查挡不住它，必须靠递归那条。
      divWrapper: '<div align="center"><img src=x onerror="window.__xss=11"></div>',
      spanNested: '<span><a href="javascript:window.__xss=12">x</a></span>',
      svgForeign: '<svg><foreignObject><img src=x onerror=window.__xss=8></foreignObject></svg>',
      mathMtext: '<math><mtext><table><mglyph><style><img src=x onerror=window.__xss=9></style></mglyph></table></mtext>',
      svgUse: '<svg><use href="data:image/svg+xml,<svg id=x xmlns=http://www.w3.org/2000/svg><image href=1 onerror=window.__xss=10 /></svg>"></use></svg>',
    };
    const probe = JSON.parse(await evalJS(`JSON.stringify((function(){
      var out={}, msgs=${JSON.stringify(payloads)};
      window.__xss=0;
      // 三个入口分别跑，才能定位「是哪一层在挡」：
      //   md     = 端到端（marked → 自研 → DOMPurify），产品事实
      //   self   = 只过自研白名单，证明它自己不依赖兜底
      //   purify = 只过 DOMPurify，证明兜底本身有效（绕过自研）
      function scan(html, host){
        host.innerHTML = html || '';
        document.body.appendChild(host);
        var on=[];
        Array.prototype.forEach.call(host.querySelectorAll('*'), function(e){
          Array.prototype.forEach.call(e.attributes, function(a){
            if(/^on/i.test(a.name)) on.push(e.tagName+'.'+a.name);
          });
        });
        var r={
          tags: Array.prototype.map.call(host.querySelectorAll('*'), function(e){return e.tagName;}),
          on: on,
          foreignNs: Array.prototype.filter.call(host.querySelectorAll('*'), function(e){
            return e.namespaceURI && e.namespaceURI !== 'http://www.w3.org/1999/xhtml';
          }).length,
          jsHref: Array.prototype.some.call(host.querySelectorAll('a'), function(a){
            return /^javascript:/i.test(a.getAttribute('href')||'');
          }),
          text: (host.textContent||'')
        };
        host.remove();
        return r;
      }
      Object.keys(msgs).forEach(function(k){
        var raw=msgs[k];
        out[k]={
          md: scan(aiMd(raw), document.createElement('div')),
          self: scan(aiSanitizeHtml(raw), document.createElement('div')),
          purify: scan(aiPurify(raw), document.createElement('div'))
        };
      });
      out.executed=window.__xss;
      return out;
    })())`));

    const keys = Object.keys(payloads);
    ok(probe.executed === 0, '所有载荷都没有真的执行（window.__xss 应为 0，实测 ' + probe.executed + '）');
    ok(keys.every(k => probe[k].md.tags.indexOf('SCRIPT') < 0), '端到端（aiMd）后 DOM 里不存在 SCRIPT 元素');
    const mdOn = keys.filter(k => probe[k].md.on.length);
    ok(mdOn.length === 0, '端到端（aiMd）没有任何残留 on* 事件属性' +
      (mdOn.length ? '：' + JSON.stringify(mdOn.map(k => k + ':' + probe[k].md.on.join(','))) : ''));
    ok(keys.every(k => probe[k].md.foreignNs === 0), '端到端（aiMd）不残留 SVG / MathML 等非 HTML 命名空间元素');
    ok(keys.every(k => probe[k].md.jsHref === false), '端到端（aiMd）剥掉 javascript: 协议 href');

    // 自研层单独：不靠 DOMPurify 也必须干净。这条挡的是「兜底把自家漏洞盖住」。
    const selfOn = keys.filter(k => probe[k].self.on.length);
    ok(selfOn.length === 0, '自研白名单单独也能挡住全部载荷（不依赖 DOMPurify 兜底）' +
      (selfOn.length ? '：' + JSON.stringify(selfOn.map(k => k + ':' + probe[k].self.on.join(','))) : ''));
    const selfNs = keys.filter(k => probe[k].self.foreignNs > 0);
    ok(selfNs.length === 0, '自研白名单单独不残留非 HTML 命名空间元素' +
      (selfNs.length ? '：' + selfNs.join(',') : ''));

    // DOMPurify 单独：绕过自研，只喂原始 HTML，它自己也得挡住。
    const purOn = keys.filter(k => probe[k].purify.on.length);
    ok(purOn.length === 0, 'DOMPurify 单独也能挡住全部载荷' +
      (purOn.length ? '：' + JSON.stringify(purOn.map(k => k + ':' + probe[k].purify.on.join(','))) : ''));
    ok(keys.every(k => probe[k].purify.tags.indexOf('SCRIPT') < 0), 'DOMPurify 单独不残留 SCRIPT');

    ok(probe.svgMxss.md.tags.indexOf('SVG') < 0 && probe.svgMxss.md.tags.indexOf('STYLE') < 0,
      'mXSS 变体（svg/style 里藏 img）被整体移除，不是只改了属性');
    ok(probe.svgForeign.md.tags.indexOf('IMG') < 0 && probe.svgForeign.md.tags.indexOf('FOREIGNOBJECT') < 0,
      'SVG foreignObject 里的 onerror 不再逃逸（原始漏洞载荷）');
    ok(probe.caseMix.md.tags.indexOf('IMG') < 0, '大小写混淆的 <IMG OnErRoR> 同样被移除');
    ok(probe.iframeForm.md.tags.indexOf('IFRAME') < 0 && probe.iframeForm.md.tags.indexOf('FORM') < 0,
      'iframe / form（可用于打本机 API）被移除，尾部文字保留：' + JSON.stringify(probe.iframeForm.md.text.trim()));
    ok(probe.dataAttr.md.tags.indexOf('P') >= 0 && /段落/.test(probe.dataAttr.md.text),
      '未知属性被剥但元素与文字保留（脱壳而不是整块删掉）');

    // ---- 3) 正常 markdown 不被净化搞坏 ----
    const good = JSON.parse(await evalJS(`JSON.stringify((function(){
      var host=document.createElement('div');
      host.innerHTML=aiMd('# 标题\\n\\n| 列A | 列B |\\n|---|---|\\n| 1 | 2 |\\n\\n\\u0060\\u0060\\u0060python\\nprint(1)\\n\\u0060\\u0060\\u0060\\n\\n[外链](https://example.com/x) 与 [站内](/docs)');
      document.body.appendChild(host);
      var a=host.querySelector('a[href^="https://"]');
      var inner=host.querySelector('a[href^="/"]');
      var out={
        h1: host.querySelectorAll('h1').length,
        table: host.querySelectorAll('table').length,
        th: host.querySelectorAll('th').length,
        pre: host.querySelectorAll('pre').length,
        codeLang: (host.querySelector('pre code')||{}).className||'',
        outTarget: a?a.getAttribute('target'):'',
        outRel: a?a.getAttribute('rel'):'',
        innerHref: inner?inner.getAttribute('href'):''
      };
      host.remove();
      return out;
    })())`));
    ok(good.h1 === 1 && good.table === 1 && good.th === 2 && good.pre === 1,
      '标题 / 表格 / 代码块结构完整（h1=' + good.h1 + ' table=' + good.table + ' th=' + good.th + ' pre=' + good.pre + '）');
    ok(/language-python/.test(good.codeLang), '代码块语言类名保留（' + good.codeLang + '）');
    ok(good.outTarget === '_blank' && /noopener/.test(good.outRel),
      '外链带 target=_blank 与 rel=noopener（' + good.outTarget + ' / ' + good.outRel + '）');
    ok(good.innerHref === '/docs', '站内相对链接没有被误剥（' + good.innerHref + '）');

    c.ws.close();
  } catch (e) {
    console.log('TESTERR:', e.stack || e.message);
    fails.push('EXCEPTION');
  } finally {
    try { chrome.kill(); } catch (_) {}
    try { envkit.kill(); } catch (_) {}
    console.log('RESULT: ' + (fails.length === 0 ? 'PASS' : 'FAIL') + ' (' + fails.length + ' failed)');
    process.exit(fails.length ? 1 : 0);
  }
})();
