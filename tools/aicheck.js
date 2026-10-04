// aicheck.js —— 校验原型里 AI 交互要素是否齐全（对照 DESIGN-v3.0-ui.md §D.2/D.3 清单）
const fs = require('fs');
const path = require('path');
const repo = 'D:\\BCGD\\FarmTrace\\envkit';
const h = fs.readFileSync(path.join(repo, 'docs', 'preview-v3ui.html'), 'utf8');
const out = [];

// §D.2 对话框能力：每项给一个可检的标记
const need = [
  ['sys 消息',        'class="bub sys"'],
  ['用户消息',        'class="bub me"'],
  ['助手消息',        'class="bub ai"'],
  ['时间戳',          'class="tm"'],
  ['思考折叠',        'class="th"'],
  ['执行计划',        'class="pl"'],
  ['计划步骤行',      'class="plr"'],
  ['工具结果折叠',    'class="tr"'],
  ['单次 token 行',   'class="tk"'],
  ['确认卡',          'class="cf"'],
  ['确认卡危险标题',  'cfh'],
  ['停止生成',        '停止生成'],
  ['清空',            '清空'],
  ['缩成小圆点',      '缩成小圆点'],
];
for (const [name, mark] of need) {
  const n = (h.split(mark).length - 1);
  out.push((n > 0 ? 'OK   ' : 'MISS ') + name + ' (x' + n + ')');
}

// §D.3 计费三处
for (const [name, mark] of [
  ['今日统计',   '今日统计'],
  ['对话次数',   '对话'],
  ['输入 token', '↑'],
  ['输出 token', '↓'],
  ['缓存命中率', '缓存命中率'],
  ['余额',       '余额'],
  ['余额刷新',   'bal-ref'],
  ['按天清零',   '今日用量'],
]) {
  const n = (h.split(mark).length - 1);
  out.push((n > 0 ? 'OK   ' : 'MISS ') + name + ' (x' + n + ')');
}

// §D.1 桌宠
for (const [name, mark] of [
  ['悬停卡',   'pettip'],
  ['任务徽标', 'pet-badge'],
  ['上次任务', '上次'],
  ['点击提示', '点我开对话'],
]) {
  const n = (h.split(mark).length - 1);
  out.push((n > 0 ? 'OK   ' : 'MISS ') + name + ' (x' + n + ')');
}

fs.writeFileSync(path.join(repo, 'tools', '_ai.txt'), out.join('\n') + '\n', 'utf8');
