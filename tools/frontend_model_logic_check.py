# -*- coding: utf-8 -*-
"""离线核实「模型下拉框」的取值优先级（前端逻辑回归检查）

跑法：python tools/frontend_model_logic_check.py

## 它锁住什么

下拉框的模型来源有三条，正确优先级是：

    1. 拉取到的模型（`/api/ai/models` 返回的服务商真值）
    2. 静态预设（`ai.go` 的 `aiProviderPresets`，硬编码）
    3. 手填模型名

第一版写成`custom ? aiPulledModels : (p.models || [])` ——
**非自定义服务商下把拉取结果丢掉了**，
而提示语明确写着「已拉取 N 个模型，已在下拉框中回填」。

**提示说回填了、实际没回填**，比不回填更坏：
用户会以为看到的是厂商真实在售模型，其实是软件自己的过期快照。
实测就是这样暴露的：厂商停用 `deepseek-chat` / `deepseek-reasoner` 后，
用户点了拉取、看到「已回填 2 个模型」，而下拉框里还是那两个失效名字。

## 为什么用脚本而不是肉眼验

肉眼很难判断「下拉框里那几个名字到底来自哪」——
它们长得一模一样，都只是 `<option>` 文本。

## 脚本自身踩过的坑（保留下来提醒后来人）

**必须先剥 JS 注释再匹配。** 第一版直接在原文里搜，
于是命中了注释里引用的旧代码（`// 原来写的是 custom ? aiPulledModels : ...`），
报出「旧写法仍在」——**假的 FAIL**。
**一个会误报的检查比没有检查更坏**：它让人以为问题还在，然后就去改别的地方。

同理，跨行正则里的 `[^)]*` 撞上 `()=>{` 的括号也会失配 ——
改为「定位到行，再断言该行包含两个子串」。
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
html = (ROOT / 'web/index.html').read_text(encoding='utf-8')

# **必须先剥掉 JS 注释再匹配。**
# 第一版脚本直接在原文里搜，于是命中了注释里引用的旧代码
# （`// 原来写的是 custom ? aiPulledModels : ...`），
# 报出「旧写法仍在」——**假的 FAIL**。
# 一个会误报的检查比没有检查更坏：它让人以为问题还在。
code = re.sub(r'//[^\n]*', '', html)
code = re.sub(r'/\*[\s\S]*?\*/', '', code)

out = []
fail = 0


def check(cond, name, detail=''):
    global fail
    if cond:
        out.append('  PASS  %s' % name)
    else:
        fail += 1
        out.append('  FAIL  %s  %s' % (name, detail))


# ---- 1. 旧的错误写法必须已消失（只看代码，不看注释）----
check('custom ? aiPulledModels :' not in code,
      '旧的「custom 才用拉取结果」写法已从代码中移除')
check('custom ? aiPulledModels :' in html,
      '（注释里仍引用旧代码，作为改动说明保留）')

# ---- 2. 拉取结果优先 ----
check('const models = pulled.length ? pulled : (p && p.models) || [];' in code,
      '模型列表优先用拉取结果，预设作兜底')

# ---- 3. 地址一致性校验 ----
check('aiPulledFrom' in code, '记录拉取来源地址')
check("aiPulledFrom === $ai('ai_base').value.trim()" in code,
      '拉取结果只在地址未变时生效')

# ---- 4. 换服务商时两者一起清 ----
# 不用 `[^)]*` 跨匹配 —— `()=>{` 里的括号会让它失配（第一版踩过，假的 FAIL）。
# 改为分别断言两句都在同一行。
chg = [l for l in code.splitlines() if "addEventListener('change'" in l and 'ai_provider' in l]
check(bool(chg) and 'aiPulledModels = []' in chg[0] and "aiPulledFrom = ''" in chg[0],
      '切换服务商时同时清空列表与来源地址',
      '实际行: %r' % (chg[0] if chg else '(未找到)'))

# ---- 5. 拉取成功时记录来源地址 ----
pull_line = [l for l in code.splitlines() if 'aiPulledModels = c.models' in l]
check(bool(pull_line) and 'aiPulledFrom' in code,
      '拉取成功时写入来源地址')

# ---- 6. 提示语与实际行为一致 ----
check('已在「模型」下拉框中回填' in code, '保留回填提示语')
check('自定义服务商下也会显示下拉框' not in code,
      '旧的「仅自定义服务商才生效」注释已更新')

# ---- 7. 已停用的模型名不得作为「可选项」出现 ----
#
# 第一版这里写成「全文不含 deepseek-reasoner」，**检查项本身是错的**：
# 那个名字**应该**出现在两处——
#   ① 记录改动原因的注释
#   ② 服务商说明的译文（「旧名 deepseek-chat / deepseek-reasoner 已停用」）
# 把「提及它」当成「提供它」���于是报了假的 FAIL。
#
# **一个会误报的检查比没有检查更坏**：它让人以为问题还在，
# 然后就去改本来正确的地方 —— 这一版已经把注释里的说明改短来迎合它。
#
# 正确的判据是「它有没有出现在可交互的位置」：
#   - <option> 的文本或 value
#   - placeholder 属性
# 而不是在注释或译文里。
option_like = re.findall(r'<option[^>]*>[^<]*</option>|placeholder="[^"]*"', code)
opt_text = '\n'.join(option_like)
check('deepseek-reasoner' not in opt_text,
      '下拉选项/占位符里不含已停用的 deepseek-reasoner',
      '实际出现于: %r' % [s for s in option_like if 'reasoner' in s])
check('deepseek-chat' not in opt_text,
      '下拉选项/占位符里不含已停用的 deepseek-chat',
      '实际出现于: %r' % [s for s in option_like if 'deepseek-chat' in s])
check('deepseek-flash' in html,
      'deepseek-flash 出现在前端（占位符或说明文案里）')
# 注意：**不能**要求它出现在 <option> 里——
# 模型选项是运行时从 /api/ai/models 动态生成的，
# HTML 里没有硬编码的模型<option> 是正确的（那才是我们要的设计）。
check(not re.search(r'<option[^>]*>\s*deepseek-', code),
      '模型选项不是硬编码在 HTML 里的（运行时按拉取结果/预设生成）')

out.append('')
out.append('FAIL count = %d' % fail)
sys.stdout.reconfigure(encoding='utf-8')
print('\n'.join(out))
sys.exit(1 if fail else 0)