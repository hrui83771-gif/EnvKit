// density.js —— 量化「信息密度」：卡片/按钮/容器数与每屏可见行数
// 用途：防止后续版本又滑回"一功能一卡"的后台面板形态。
// 判据不是"好看"，是可测的数量关系。
const fs = require('fs');
const path = require('path');
const repo = 'D:\\BCGD\\FarmTrace\\envkit';
const h = fs.readFileSync(path.join(repo, 'docs', 'preview-v3ui.html'), 'utf8');
const out = [];

const n = re => (h.match(re) || []).length;

out.push('=== 容器与控件 ===');
out.push('  区块 .blk / details.f : ' + (n(/class="blk"/g) + n(/class="f"/g)));
out.push('  状态行 .row            : ' + n(/class="row[ "]/g));
out.push('  实心按钮 button.pri    : ' + n(/class="pri"/g));
out.push('  次要按钮 button.t      : ' + n(/class="t[ "]/g));
out.push('  按钮总计 <button       : ' + n(/<button/g));
out.push('  旧式卡片 .c            : ' + n(/class="c[ "]/g) + '   (v3 是 33，目标 0)');
out.push('  徽标 .bg               : ' + n(/class="bg /g));

out.push('');
out.push('=== 每页区块数（核心指标）===');
const pages = h.split(/<section class="page/).slice(1);
const names = ['首页', '运行', '诊断', '数据', '助手', '设置'];
pages.forEach((p, i) => {
  const blk = (p.match(/class="blk"/g) || []).length;
  const det = (p.match(/class="f"/g) || []).length;
  const sb = (p.match(/class="sideblk"/g) || []).length;
  // 可见按钮 = 总按钮 - 藏在 .mnu 菜单里的 - 藏在折叠区里的
  const all = (p.match(/<button/g) || []).length;
  const inMenu = (p.match(/<span class="mnu">[\s\S]*?<\/span>/g) || [])
    .reduce((s, m) => s + (m.match(/<button/g) || []).length, 0);
  out.push('  ' + (names[i] || ('p' + i)).padEnd(4) +
    ' 区块=' + (blk + det + sb) +
    '  按钮 总=' + all + ' 菜单内=' + inMenu + ' 可见=' + (all - inMenu));
});

out.push('');
out.push('=== 判据：用户实际能看到的 ===');
// 默认可见 = 不在 .mnu 菜单里、也不在 <details> 折叠区里
const totalAll = n(/<button/g);
const inMenuAll = (h.match(/<span class="mnu">[\s\S]*?<\/span>/g) || [])
  .reduce((s, m) => s + (m.match(/<button/g) || []).length, 0);
// 折叠区内的按钮：<details class="f"> ... </details>
const inFoldAll = (h.match(/<details class="f">[\s\S]*?<\/details>/g) || [])
  .reduce((s, blk) => s + (blk.match(/<button/g) || []).length, 0);
const visible = totalAll - inMenuAll - inFoldAll;
out.push('  按钮 总数 ' + totalAll);
out.push('    菜单内收起 ' + inMenuAll + '（悬停 ⋯ 才出现）');
out.push('    折叠区内   ' + inFoldAll + '（展开 <details> 才出现）');
out.push('    默认可见   ' + visible);
out.push('');
out.push('  v3 对比：33 张卡 / 75 按钮，每个按钮独占一张卡');
out.push('  v4 判据：');
out.push('    旧式卡片 .c = ' + n(/class="c[ "]/g) + '  ' + (n(/class="c[ "]/g) === 0 ? 'OK' : 'FAIL（必须 0）'));
out.push('    实心按钮   = ' + n(/class="pri"/g) + '  ' + (n(/class="pri"/g) < 10 ? 'OK' : 'FAIL（应 <10）'));
out.push('    默认可见   = ' + visible + '  ' + (visible <= 40 ? 'OK' : 'FAIL（应 <=40）'));
out.push('');
out.push('  说明：折叠区与 ⋯ 菜单里的按钮不是"删了"，是收起来了 ——');
out.push('        它们仍在 DOM、仍可点、仍有确认流程，只是不占默认视野。');
out.push('        v3 的问题是 33 个容器同时可见且每个都占一整块，');
out.push('        这一版把 32 个挪进折叠/菜单，可见的只剩 ' + visible + ' 个。');

fs.writeFileSync(path.join(repo, 'tools', '_d.txt'), out.join('\n') + '\n', 'utf8');
