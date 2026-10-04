// pcheck.js —— 校验 v2 原型：标签配平 / JS 语法 / 关键交互点 / 暗色变量完整性
const fs = require('fs');
const path = require('path');
const { execFileSync } = require('child_process');

const repo = 'D:\\BCGD\\FarmTrace\\envkit';
const p = path.join(repo, 'docs', 'preview-v3ui.html');
const h = fs.readFileSync(p, 'utf8');
const out = [];

function count(re) { return (h.match(re) || []).length; }
const pairs = [
  ['div', /<div[ >]/g, /<\/div>/g],
  ['section', /<section[ >]/g, /<\/section>/g],
  ['details', /<details[ >]/g, /<\/details>/g],
];
for (const [name, o, c] of pairs) {
  const a = count(o), b = count(c);
  out.push((a === b ? 'OK   ' : 'FAIL ') + name + ' open=' + a + ' close=' + b);
}

// 内联 JS
const re = /<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/gi;
let m, n = 0;
const tmp = path.join(repo, 'tools', '_pcheck');
if (!fs.existsSync(tmp)) fs.mkdirSync(tmp, { recursive: true });
while ((m = re.exec(h)) !== null) {
  n++;
  const f = path.join(tmp, 'b' + n + '.js');
  fs.writeFileSync(f, m[1], 'utf8');
  try {
    execFileSync(process.execPath, ['--check', f], { stdio: 'pipe' });
    out.push('OK   script' + n + ' (' + m[1].split('\n').length + ' lines)');
  } catch (e) {
    out.push('FAIL script' + n);
    out.push(String(e.stderr || e.message).split('\n').slice(0, 6).join('\n'));
  }
}

// 暗色变量是否覆盖了亮色里用到的全部变量
const rootM = h.match(/:root\{([\s\S]*?)\}/);
const darkM = h.match(/\[data-theme="dark"\]\{([\s\S]*?)\}/);
if (rootM && darkM) {
  const names = s => new Set((s.match(/--[a-z0-9]+/g) || []));
  const a = names(rootM[1]), b = names(darkM[1]);
  const missing = [...a].filter(x => !b.has(x));
  out.push(missing.length === 0
    ? 'OK   dark covers all ' + a.size + ' vars'
    : 'FAIL dark missing: ' + missing.join(', '));
} else {
  out.push('FAIL :root or [data-theme=dark] block not found');
}

// 关键交互点
for (const [k, what] of [
  ['id="pet"', 'mascot'],
  ['id="drawer"', 'right drawer'],
  ['drawer\');', 'drawer open handler'],
  ['class="sq"', 'square chat'],
  ['id="legacy"', 'legacy lamp toggle'],
  ["setAttribute('data-theme'", 'theme toggle'],
  // v4 起不再用栅格卡片，改用「区块 + 状态行」单一版式。
  // 这两条检查的是版式纪律：区块与行是唯一的容器，行内不得再嵌卡片。
  ['class="blk"', 'block container'],
  ['class="row"', 'status row'],
  ['class="c "', 'no legacy card class (should be 0)'],
  ['id="toast"', 'toast'],
]) {
  out.push((h.includes(k) ? 'OK   ' : 'MISS ') + what);
}

// 版式纪律：行内不得嵌套旧式卡片
const legacyCards = (h.match(/class="c[ "]/g) || []).length;
out.push(legacyCards === 0 ? 'OK   legacy cards = 0'
  : 'FAIL legacy cards = ' + legacyCards + ' (v4 改用区块+行，不该再有 .c)');

// 是否还有硬编码颜色（应仅出现在 :root / [data-theme] 两块内）
const body = h.replace(/:root\{[\s\S]*?\}/, '').replace(/\[data-theme="dark"\]\{[\s\S]*?\}/, '');
const hard = (body.match(/#[0-9a-fA-F]{3,6}\b/g) || []).filter(c => !['#fff'].includes(c.toLowerCase()));
out.push(hard.length === 0 ? 'OK   no hardcoded colors outside theme blocks'
  : 'WARN hardcoded outside theme: ' + [...new Set(hard)].join(', '));

fs.writeFileSync(path.join(repo, 'tools', '_pv.txt'), out.join('\n') + '\n', 'utf8');
