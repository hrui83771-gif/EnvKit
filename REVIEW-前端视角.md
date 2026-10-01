# EnvKit 前端评审 —— 顶级前端视角

> 评审对象：`web/index.html`（单文件 ~2076 行 / 145KB，内嵌 CSS + JS + i18n 字典）
> 评审基线：v1.9.4。所有问题均基于源码逐行核对，标注了行号。

## 先说做得好的（值得保持）

- **服务端驱动的 428 确认流**（`postConfirm`，L1776）：前端先探一次、风险文案由后端下发，比前端写死提示准确，安全设计正确。
- **fetch 包装自动带令牌 + 403 自动刷新**（L1130-1153）：CSRF 防护无侵入，过期自愈。
- **CSS 变量双主题**（L8-21）：暗色模式零 JS 成本切换。
- **MutationObserver 实现动态内容 i18n**：思路巧妙，解决了"渲染时生成的文案无法静态翻译"问题（但有性能代价，见严重级 3）。
- **SSE 日志按 scope 分流 + EventSource**：实时日志管线干净。
- **xterm + WebSocket 桥接 SSH 终端**：集成完整（resize 联动、全屏、二进制流）。

---

## 致命级（真实用户每天会踩 / 会坏功能）

### F1. 中文输入法按 Enter 会把候选词半成品直接发出去

`ai-in` 的 keydown（L2061-2063）：

```js
if(e.key === 'Enter' && !e.shiftKey){ e.preventDefault(); $ai('ai-send').click(); }
```

没有判断 `e.isComposing`。用拼音输入法打字时，按 Enter 确认候选词会触发 keydown（`isComposing=true`）→ **消息在拼音还没上屏时就被发送**。这是一个中文用户工具，等于每个用中文问 AI 的人都会踩。

**修法（3 行）**：`if(e.isComposing || e.keyCode === 229) return;`

### F2. 确认卡片被无视后，对话历史进入协议非法状态，下一轮必然 400

确认卡片渲染时把 assistant(tool_calls) 压入 `aiMessages`（L2026-2028）。用户**不点确认/拒绝，直接发下一条消息**时，历史变成：

```
[..., assistant(tool_calls), user(新消息)]
```

服务端 `aiTrimLastToolTurn`（ai_loop.go:413）只裁剪**末尾**的悬挂回合——此时末尾是 user，悬挂的 tool_calls 留在历史中间。下一次请求上游 OpenAI 协议 API 会因 "assistant message with tool_calls must be followed by tool messages" 直接 400，用户只能"清空对话"逃生。

**修法**：发送新消息前检查 `aiMessages` 末尾是否有未决 tool_calls（或维护一个 pendingConfirm 标记），有则自动补一条拒绝 tool 结果（"用户未回应，视为取消"）再发。

### F3. 三处 innerHTML 注入面，其中确认卡片在 AI 输出链路上

| 位置 | 代码 | 数据来源 | 风险 |
|---|---|---|---|
| 确认卡片 L2031 | `card.innerHTML = ... + (ev.text \|\| ev.tool) + ...` | 服务端 `aiToolCnName(tool, args)`，**args 来自模型输出** | 模型被日志内容（间接提示注入）污染后，参数里带 HTML 即执行 |
| 版本表 L1524 | `onclick="upgradeComp('${v.name}',...)"` 字符串拼接 | 服务端组件名（当前可信） | 模式错误 + CSP 不友好 |
| 下载源 L1327 | `h.innerHTML = <label>${c.name}...` | config.json（用户可编辑） | 低危但同类 |

日志窗格用 `textContent`（安全）证明团队有意识，但这三处漏了。**修法**：确认卡片改 `textContent` 组装；版本表改事件委托（`data-*` + 一个 click 监听）；下载源用 `createElement`。顺带说：`aiMd` 先转义再转换 markdown 的顺序是对的，保持。

---

## 严重级（体验与性能的真实损耗）

### S1. `post()` 全家桶静默失败

L1773：`const post=(url,body)=>fetch(...)` —— 返回的 Promise 无人检查。备份数据库、SSH 测试、链端启动、停止服务等十几个按钮全部 fire-and-forget，**后端返回 400/428/500 时用户什么提示都没有**（只能靠 2 秒轮询的状态徽标间接感知，而很多操作连徽标都不映射）。

**修法**：`post()` 内统一检查 `res.ok`，失败弹 toast（见 S6）。5 行改动救全场。

### S2. 日志窗格 DOM 无上限增长 + 逐行强制 reflow

`appendLog`（L1173-1194）：每行一个 div 无限追加（npm install 轻松几千行），且**每行都执行 `scrollTop = scrollHeight`**（强制同步布局）。sys 广播时一行还会复制进 5 个窗格（L1175）。

**修法**：行数上限（如 2000 行，超出 `removeChild(firstChild)`）；滚动用 `requestAnimationFrame` 合帧；sys 广播若非当前可见窗格可延迟渲染。

### S3. AI 流式渲染 O(n²)

每个 delta 都 `cur.innerHTML = aiMd(curText)` 全量重排 + 全量 markdown 重解析（L2010），同时 MutationObserver（L1119）对每次替换的新增子树做全量 `querySelectorAll('*')` + TreeWalker 扫描。长回答时每字符成本线性增长。`rsn`/`rsnText`（L1986）从头到尾没被赋值——思考模型的 reasoning 展示是个没接完的线头。

**修法**：AI 消息容器加 `data-no-i18n`（AI 回复本就不该被翻译字典改写）；markdown 增量渲染或至少 throttle 到 50ms/帧；要么接通 reasoning 展示、要么删掉死变量。

### S4. 轮询无节制

L1838：`setInterval(2s)` 固定发 3 个请求（health/state/prog），**页面切走也不停**；SSE 断线（L1199）3 次错误后 `alert+reload` 无退避，服务重启窗口期可能 reload 循环。

**修法**：`visibilitychange` 时暂停/恢复；失败退避（2s→5s→10s 封顶）；SSE 重连用指数退避替代 alert。更进一步：三个轮询可合并为一个 dashboard 接口，或直接复用现有 SSE 通道推送状态（省掉 3/2s 的 HTTP）。

### S5. 脏配置没有离开警告 + 自动保存与立即操作存在竞态

输入后 800ms 防抖自动保存（L1265-1273），期间关页/刷新**静默丢失**（无 `beforeunload`）；且"改完立刻点建库"时，操作用的 DOM 值是新值、config 保存可能还在路上，两边状态短暂不一致。

**修法**：`dirty` 时注册 `beforeunload`；操作类按钮点击前先 `saveNow()` await。

### S6. 无统一反馈组件，`alert()` 到处在飞

令牌过期、导出失败、SQL 未选……全用阻塞式 `alert`（L1146、1554、1571 等）。已有 `busy-banner` 体系，差一个非阻塞 toast 组件（右上角、自动消失、可堆叠）。这也是 S1 修复的前提。

### S7. 死代码与双份维护

- `.ai-stop` 样式（L199）存在但**没有停止按钮**——AI 长回答/长工具执行无法手动中断（服务端支持客户端断开，前端没接 AbortController）。
- `AI_TOOL_CN`（L1972）与后端 `aiToolCnName` 双份维护，已经漏了 `restart_service`（回落显示英文工具名）。
- i18n 字典有弯引号/直引号重复 key（L747-748 两条 greeting），`i18n_inventory.py` 应该加去重检查。

---

## 规范级（有空再做，做了更好）

| 项 | 现状 | 建议 |
|---|---|---|
| 无障碍 | 模态无焦点陷阱/恢复；侧栏导航是 div+click，键盘不可达；状态只靠颜色圆点 | 模态聚焦循环 + Escape 已有（补 AI 抽屉的 Escape 关闭）；nav 换 button 或加 role/tabindex；状态点加文字/图形冗余；日志区加 `aria-live="polite"` |
| 文件拆分 | 145KB 单文件，CSS/JS/字典全内联 | `go:embed` 本就支持 `web/*` 多文件——拆成 style.css / app.js / i18n.js，**零分发成本**（仍是一个 exe），换来编辑器高亮、lint、diff 可读性 |
| 内联样式 | 大量 `style="margin-top:6px"` 散落 | 收进工具类（`.mt6`）或组件类 |
| 设计 token | 只有颜色变量，间距/圆角/字号随手写 | 补 `--sp-1/2/3`、`--radius` 变量 |
| 系统 prefers | 无 `prefers-color-scheme` 跟随、无 `prefers-reduced-motion` | 首次无存储时跟随系统；ekpulse 等动画加 reduced-motion 降级 |
| 令牌卫生 | SSE/导出用 `?t=` 查询串（L1197、1545、1580） | SSE 无法带 header 属浏览器限制，可改一次性短时效令牌；导出类可走 fetch+blob（分发包已是这么做的，统一即可） |
| favicon | JS 动态注入（L683） | 直接在 `<head>` 放 data-uri link，避免首帧闪烁 |
| verCmpJs | 不处理预发布号（`1.25.0-rc1` 解析为 1.25.0） | 加 `-` 截断；预发布视为低于正式版 |
| CSP | 无（内联全开） | 单文件内联是硬约束，短期可不做；若拆文件后可上 nonce 版 CSP |
| AI 历史持久化 | 刷新即丢（服务端有 40 条上限，上下文本身安全） | localStorage 存最近 N 条（pendingConfirm 状态除外） |
| Enter 发送区域 | 仅 AI 输入框有；SSH 命令框 Enter 不发送 | `ssh_cmd` 也加 Enter 执行（同样要 isComposing） |

---

## 优先级路线建议

1. **今天就改**（合计 < 30 行）：F1 isComposing、F2 悬挂确认兜底、S1 post 统一报错。
2. **本周顺手**（各 < 50 行）：F3 三处 innerHTML、S2 日志上限+rAF、S4 可见性暂停、S6 toast 组件。
3. **下个迭代**：S3 流式渲染重构、文件拆分、无障碍一轮。
4. 规范级按表随缘。

预估总量：致命级 + 严重级 1-6 全部落地约 300 行改动，不动任何后端协议。
