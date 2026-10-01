async () => {
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const out = new Set();
  const errs = [];
  const cjk = /[\u4e00-\u9fff]/;
  const skip = {SCRIPT:1, STYLE:1, TEXTAREA:1, PRE:1, CODE:1};

  const grab = () => {
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let n;
    while ((n = w.nextNode())) {
      const p = n.parentNode;
      if (!p || skip[p.nodeName]) continue;
      if (p.closest && (p.closest('.log') || p.closest('[data-no-i18n]'))) continue;
      if (p.closest && p.closest('.ai-tool')) continue;
      const t = n.data.trim();
      if (t && cjk.test(t) && !/^[A-Za-z]:\\/.test(t) && t.length < 60) out.add(t);
    }
  };
  const onErr = e => errs.push(String((e && e.message) || e));
  window.addEventListener('error', onErr);

  ekSetLang('en', true);
  await sleep(2600);                       // 等 pollHealth / pollState 按英文重渲染
  for (const p of ['install', 'config', 'chain', 'start']) {
    try { goto(p); } catch (e) { errs.push('goto ' + p + ': ' + e); }
    await sleep(1900);                     // 每个路由都要等异步状态回来后才有文本
    grab();
  }
  try { document.getElementById('btn-about').click(); await sleep(600); grab(); document.getElementById('about-close').click(); } catch (e) {}
  try { aiOpen(); await sleep(500); grab(); } catch (e) {}

  window.removeEventListener('error', onErr);
  ekSetLang('zh', true);
  await sleep(1200);
  return JSON.stringify({residual: [...out].sort(), errors: errs});
}
