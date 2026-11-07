# EnvKit 首次引导 / 产品导览 设计方案（v3）

> 日期：2026-11-06　状态：**设计方案，未实现**（本文只定设计，不动代码）
> 一句话：第一次打开先看 **1.2 秒品牌片头**，再自动播 **5 屏沉浸式产品导览**；「关于」里随时重看完整 8 屏；**全站统一换成柔和低饱和配色（不用绿）**；收尾衔接 P1-4 操作教学；**采用成品依赖库**；零外链、零后端改动、零真实副作用。

**v3 相对 v2 的三处决策变更（用户拍板，本轮全部收敛）**

| # | v2 | v3（已定） |
| --- | --- | --- |
| 1 | 柔和色板只挂 `#onboard`，**主界面绿系不动** | **主界面也换**：色板提升为全站 `:root` 变量，含手机页；不再有两套色、不需要切换过渡 |
| 2 | 1.2 秒品牌开场是「可选增强，默认不做」 | **要做**，作为首次播放的独立片头层（§4.2） |
| 3 | 收尾「预留按钮但先隐藏」，P1-4 单独排期 | **要衔接**：S6 一起做 P1-4 最小版（driver.js 4 步高亮），收尾 CTA 落地即可用（§5、§8.2） |

> v2 里以下条款在 v3 **作废**：「色板只作用于 `#onboard`」「不改 `:root` 的 `--brand*`」「关闭时做中性淡出避免硬切」「片头属于可选增强」「P1-4 未实现前按钮先隐藏」。

---

## 0. 决策记录（7 项，全部已定）

| 项 | 结论 |
| --- | --- |
| 屏数 | 完整 **8 屏**；首次自动播 **5 屏** = `[0,1,2,5,7]`；「关于」重看完整 8 屏 |
| 配色 | **柔和低饱和、不用绿**；雾蓝 / 藕杏 / 淡紫 / 暖米白；**全站生效**（主控制台 + 引导 + 手机页） |
| 依赖库 | **用**，选可直接 `<script>` 引的 UMD/IIFE：Swiper 12 + Anime.js v4 + driver.js |
| 品牌开场 | **做**，1.2 秒，仅首次自动播时出现，可跳过 |
| loading | **不做首屏 loading**；保留「产品头像状态体」（进度环 + 真实等待场景） |
| 收尾衔接 | **衔接 P1-4**，S6 落地最小版；未落地时按钮按 feature flag 自动隐藏，不留死按钮 |
| 状态存储 | 只存前端 `localStorage['ek-guide-v1']`，后端 Config 完全不动 |

**三条底层结论**

1. **7~8 屏不砍，拆两层**：首次 5 屏约 30 秒（+1.2 秒片头）随时可跳，完整 8 屏放「关于」主动点开。既保住「开机动画感」，又不一上来把人劝退。
2. **库只选能直接 `<script>` 引的**：项目无构建工具、无 npm import、CSP `default-src 'self'`，所以必须是 UMD / IIFE 全局构建落 `web/vendor/`。
3. **配色改造走「改值不改名」**：`app.css` 里 261 处 `var(--brand)` 之类引用一个都不改，只替换 `:root` / `body[data-theme="dark"]` 的变量值——这是全站换色成本最低、回归面最小的做法。

---

## 1. 现状盘点（代码实证，决定设计边界）

| 约束 | 事实 | 对设计的影响 |
| --- | --- | --- |
| 构建 | `tools/web_build.py:59-80` 把 `web/src/js/00..80-*.js` 按数字前缀 `sorted()` 空串拼成单个内联 `<script>`，CSS 填 `<!--@css-->` | 新增 `90-onboard.js` 自动纳入，**构建脚本不用改**；vendor 文件不进这条链 |
| 前端形态 | 非 SPA：7 个 `<section class="panel">` + `.nav[data-panel]`，`goto(panel)`（`20-shell.js:147`）切 `.show` | 引导做成 body 顶层全屏 overlay，不新增路由、不碰面板切换 |
| 现成弹窗 | `.modal.show` + `.mbox/.mhead/.mfoot`（`app.css:382`），ESC/遮罩关闭（`20-shell.js:369/411`） | 复用关闭手感，独立 z-index（§4.4） |
| 「关于」 | `index.tpl.html:1172-1204`，`#about` 的 `.mfoot` 目前**只有** `#about-close`(:1202) | 左边加一个 `#about-replay` 即可 |
| **主界面配色** | `app.css:4-17`：`:root` + 暗色两套变量，`--brand:#2f7d4f` 绿系；**261 处 `var()` 引用** | **v3 要改**，但只改变量值（§2.1） |
| **硬编码色** | `app.css` 约 **100 行**写死 hex/rgb（不含 10 行变量定义）；`index.tpl.html:1218` 一处；js 里 14 处；`mobile.html` **91 处** | 换色的真实工作量在这里，必须分三类（§2.4） |
| **手机页独立** | `mobile.html:8-106` 自带一整套内联 CSS，注释明说「不复用 app.css」，底色 `#faf8f5`、主蓝 `#4a6fa5` 水彩风 | 本来就跟新色板同族，**是资产不是负担**：微调状态点即可，两端天然收敛 |
| **CDP 颜色断言** | `tools/cdp_ai_pick_test.js:190/195` 断言 docx 卡片色 `rgb(43, 87, 154)`（=`#2b579a`，`app.css:886/946`）；`cdp_stats_test.js:172` 的 `getComputedStyle` 只量盒子 | 换色时**格式识别色不许动**，否则回归假红；全量 CDP 必须重跑 |
| 形象资源 | `APP_ICON` = `cici.png` base64（`00-core.js:17-26`）、桌宠三视图内联 SVG（`index.tpl.html:1311-1322`）、`tools/brand-icons/*.svg` | 片头 / 头像 / 向导 / 插画全现成，**不需要 Lottie/AE** |
| 持久化 | 全 `localStorage`，key 惯例 `ek-*`；**无任何首次/引导标记** | 用 `ek-guide-v1`（带版本号，将来加页可定向重播） |
| i18n | key = 中文原文、缺项回退（`10-i18n.js:1-5`）；`i18n_inventory.py` 查漏 → 补 `tools/i18n_en.json` → `i18n_build.py` 自动重建 | 全部文案走 `t()`，S2 就按此写 |
| **vendor 落地链路** | `main.go:54-63` 三个 `//go:embed web/vendor/*.min.js` 变量；`main.go:166-167` 两条 `http.HandleFunc("/vendor/…", staticBytes(…))`；`index.tpl.html:12-14` 同域 `<script src="/vendor/…">`；vConsole 另在 `lanserve.go:165` | **每个文件要加两处**：embed 变量 + 主路由；CSS 同理。`THIRD-PARTY-NOTICE.md:54-62` 是「说明 + 版本/许可/位置表 + 许可全文」格式 |
| 零 CDN | `main.go:33/43-62` 全 embed；`securityMW` CSP `default-src 'self'`（`main.go:394-403`）；`web_source_test.go:25-42` 卡 src↔产物一致 | 库必须落本地 + 同域路径，不能 npm import、不能 CDN |
| 后端配置 | `state.go:76-137` Config **无 first_run / guide 字段**；`configBody()` 整包 POST 有覆盖风险（`configfile.go:172-215`） | 状态放 localStorage，语义就是「这台浏览器看过没」 |
| 首屏 | 无 loading / splash；Go 一次吐出完整内联页；`/api/config` 回填不挡 DOM（`40-ops.js:1563` init） | 片头挂 init 末尾，天然不抢首屏、不等待任何资源 |
| 已有提案 | `docs/UX-2026-11-06.md:98-100` P1-4「借 driver.js 做 3-4 步高亮引导」；`index.tpl.html:101-105` 已有 4 步静态 `.guide` 条 | **分工**：本次 = 产品导览（为什么用）；P1-4 = 操作教学（怎么用）。v3 把 P1-4 最小版纳入 S6，收尾真正衔接 |

---

## 2. 全站柔和配色迁移（v3 重写，重点）

**原则：低饱和（20~35%）、高明度、暖底、无纯黑纯白；三色以内 —— 雾蓝主色、藕杏辅色、淡紫只做点缀。界面语义色不出现绿。**

### 2.1 做法：改变量值，不改变量名

直接替换 `app.css:4-17` 的 `:root` / `body[data-theme="dark"]` 值，**变量名原样保留**。这样 261 处 `var()` 引用、`index.tpl.html` 15 处内联变量、js 里 16 处引用全部一行不动。引导不再另起一套 `--ob-*`，直接吃全站变量，只留少量派生（§2.5）。

浅色 `:root`（旧 → 新）：

| 变量 | 旧值 | 新值 | 说明 |
| --- | --- | --- | --- |
| `--bg` | `#f5f7fa` | `#F7F4EF` | 暖米白，对齐手机页 `#faf8f5` |
| `--card` | `#ffffff` | `#FFFCF8` | 比背景亮一档，不用纯白 |
| `--soft` / `--soft2` | `#fbfcfe` / `#f2f6f4` | `#FBF9F5` / `#F2EEE8` | 旧 `--soft2` 略带绿，一并去 |
| `--off` / `--hover` | `#eef2f6` / `#e2e8ef` | `#F0EBE3` / `#E9E2D8` | 暖灰阶 |
| `--line` | `#e3e8ef` | `#E7E0D6` | 0.5~1px 描边 |
| `--text` / `--muted` | `#1f2933` / `#6b7785` | `#3E3C3A` / `#8B867E` | 暖灰黑，非纯黑 |
| `--brand` | `#2f7d4f` | `#5E7BA3` | **雾蓝深**，实心按钮配白字；与手机页主蓝 `#4a6fa5` 同族 |
| `--brand2` | `#3a9d63` | `#8CA6C6` | 雾蓝，hover / 描边 / 状态点 |
| `--brand-weak` | `#e9f4ee` | `#E6EEF7` | 淡雾蓝底 |
| `--ok` / `--okbg` | `#1f9d55` / `#e6f6ec` | `#5E7BA3` / `#E6EEF7` | **通过改雾蓝，不用绿** |
| `--warn` / `--warnbg` | `#b07800` / `#fdf3e0` | `#B08A4E` / `#F8EFE0` | 暖杏 |
| `--err` / `--errbg` | `#d64545` / `#fdeaea` | `#C06A66` / `#F9EBE9` | 淡珊瑚，仍要醒目 |
| `--info` / `--infobg` | `#3b6fb0` / `#eaf2fb` | `#7C93AE` / `#EDF1F6` | 灰蓝，与"通过"拉明度差 |
| `--dotoff` / `--focusbg` | `#c7cede` / `#ffffff` | `#D6CEC2` / `#FFFCF8` | — |

> 这张表是 **S1 的起点不是终稿**：S1 用真实取色 + 对比度实测校准，判据写进 §11。

### 2.2 深色：沿用蓝灰骨架，只换绿系

`app.css:11-17` 现有暗底是 `#0f1520` / `#161e2b` 蓝灰系，**本身不带绿，不动**。只把 8 个绿系值换掉：`--brand → #7C9CC4`、`--brand2 → #A3BAD8`、`--brand-weak → #2A3342`、`--ok → #7C9CC4`、`--okbg → #232F3E`、`--warn → #D6AC72`、`--err → #D08A85`、`--info → #9AAEC6`；文字/描边沿用。深色工作量因此远小于浅色，也避免整站暗色重配。

### 2.3 语义色不要靠颜色单打独斗

`--ok`（通过）与 `--info`（信息）改成同族蓝后，**色相区分变弱**。按无障碍要求，正向 / 提示 / 异常必须同时给**图标 + 文案**（✓ / i / !），不能只靠一个小圆点。这条同时覆盖 `mobile.html:44-47` 的 `.dot.on/.wait/.bad`。

### 2.4 约 100 行硬编码色：分三类处理

**A 类 · 必须换（绿系语义 / 装饰）**

| 位置 | 现值 | 处理 |
| --- | --- | --- |
| `app.css:133` `@keyframes ekpulse` | `rgba(48,164,95,…)` 绿脉冲 | → `rgba(94,123,163,…)` |
| `app.css:269` `.log .ok` | `#7ee2a0` 亮绿 | → 柔蓝 `#A8C4E2` |
| `app.css:294` `.log.light .ok` | `#1f7a44` 深绿 | → `var(--ok)` |
| `app.css:366` `.tbar .tdot` | `#37b56a` 绿点 | → `var(--ok)` |
| `app.css:680-682` `.ai-verify.is-ok/is-pending/is-bad` | 绿 / 黄 / 红三套硬编码 | → 雾蓝 / 暖杏 / 淡珊瑚 |
| `app.css:614` `.ai-msg.user.ai-md pre` | `#12331f` 墨绿代码块 | → 深蓝灰 `#1B2431` |
| `app.css:449` `.pet-badge.green` | `#22c55e` 亮绿 | → 淡紫 `#B7A9C4` |
| `app.css:473/474` 桌宠 zzz / 水滴 | `#9db4d8` / `#7cc4f8` 高饱和蓝 | → 低饱和同族 |
| `js/40-ops.js:1207` 端口「空闲」 | `#2fb344` 亮绿（`:1201` 占用 `#e5484d` 同步降饱和） | → `var(--ok)` / `var(--err)` |
| `js/30-profile.js:133` 终端主题 | `cursor:#9ad1a8`、`selectionBackground:#2f7d4f55` | → 雾蓝 |
| `js/50-pet.js:135/139/145/667/879` 彩纸 | `#9fd8cb` 薄荷绿 + 高饱和数组 | → 藕杏 / 淡紫 / 雾蓝，整体降饱和 |
| `mobile.html:45-47` `.dot.on/.wait/.bad` | `#5fa87a` 绿 + `rgba(95,168,122,.16)` | → 雾蓝 + 配套柔光 |

**B 类 · 明确保留（不是界面配色）**

| 位置 | 理由 |
| --- | --- |
| `app.css:943-952` 文件格式四色（PDF 红 / DOCX 蓝 / **XLSX 绿** / PPTX 橙）、`:885-888` 图标色 | **第三方格式识别资产**，换掉会让人认不出文件类型。`cdp_ai_pick_test.js:190/195` 写死断言 docx = `rgb(43,87,154)`，动它 = 回归假红。这是「全站无绿」唯一有意的例外：一个小小的 XLSX 徽标。 |
| `#c0392b` danger 确认框（`app.css:279-281/916/955`）、`index.tpl.html:1218` `#exit-confirm` 内联 | **破坏性操作警示，绑定 428 权限闸**。降饱和会弱化危险语义，保留深红，只收敛使用面积。 |
| 日志 / 终端深色底 `#0f1720` / `#10161f` / `#1a2230`、hljs 代码高亮色（`:639-651`） | 代码阅读区，不随明暗主题走，属于「内容区」不是「界面配色」。 |
| `60-ai.js` 模型图标、`tools/brand-icons/*.svg` | 第三方品牌色（Gemini 紫等）。 |
| `mobile.html:57` `.btn.primary #4a6fa5` | 与新 `--brand` 同族，保留或对齐为 `#5E7BA3` 均可。 |

**C 类 · 柔化（纯白 / 纯黑大面积）**：`.pet-bubble` 的 `#fff`（`:441/442`）、`app.css:86/102` 的 `background:#fff`、桌宠气泡内文字 `#333` 等，换成 `var(--card)` / `var(--text)`，避免暖底上突兀的纯白块。

### 2.5 引导专用派生变量（少量）

全站变量之外，只给 `#onboard` 加 4 个：`--ob-glow`（藕杏柔光斑）、`--ob-lilac:#B7A9C4`（章节点）、`--ob-accent:#D9B2A0`（光带）、`--ob-ring`（进度环）。其余全部吃 `var(--brand*)` / `var(--text)` 等全站变量——**单一色板，不再有两套色**。

### 2.6 对比度与核对清单

- `--text` on `--bg` ≥ 4.5:1；**浅雾蓝底不许配白字**（`#8CA6C6` 不够），CTA 一律用 `--brand` 深蓝或浅底深字；S1 用真实取色核对，不靠观感。
- 全站视觉核对范围：**7 个 panel + 13 张 `set-card` + 6 个 `.modal` + 桌宠 / 气泡 / 右键菜单 + 日志窗（明暗两种）+ AI 抽屉 / 附件卡 / 确认卡 + 手机页**。逐块截图比对，不只看首屏。
- 门禁：`web_build.py --check`、`go test ./...`（`web_source_test.go` 卡 src↔产物）、**全量 CDP 回归**（尤其 `cdp_ai_pick_test.js` 的颜色断言不许被误伤）、`dist_check.py`、明暗两套主题各跑一遍。

---

## 3. 首次 5 屏（不变）

`FIRST_RUN_SLIDES = [0, 1, 2, 5, 7]`（0 基）：

| 顺序 | 屏 | 为什么进首次 |
| --- | --- | --- |
| 1 | **① 开场** | 品牌第一印象，头像入场 + 跳过 |
| 2 | **② 为什么存在** | 先讲价值，回答「这东西跟我有什么关系」 |
| 3 | **③ 四步走** | 讲清主流程，对应现有静态 `.guide`，最实用 |
| 4 | **⑥ AI 与桌宠** | 最差异化、动画感最强的一屏，含写操作确认卡 |
| 5 | **⑧ 收尾** | 给出下一步 + 衔接 P1-4 |

**留给完整版的 3 屏**：④ 状态看得见（检测报告）、⑤ 日志分区、⑦ 安全边界。收尾页用一行小字提一句「权限与外连边界在完整导览第 7 屏」，不展开。

| | 首次自动播 | 「关于」里主动重看 |
| --- | --- | --- |
| 片头 | **有**（1.2 秒，可跳过） | **无**，直接进屏 1 |
| 屏数 | 5 屏（约 30 秒） | 完整 8 屏 + 章节宫格，可任意跳 / 单页重看 |
| 跳过 | 右上角常驻「跳过」、ESC、进度点直达 | 随时关 |
| 进度点 | 只显示 5 个点 | 显示 8 个点 |
| 标记 | 播完或跳过都写 `ek-guide-v1=1` | 不改写首次标记 |

> 换哪 5 屏只改 `FIRST_RUN_SLIDES` 一个数组；片头是否重看由 `mode` 决定，不额外存状态。

---

## 4. 触发、层级与品牌开场（v3 新增片头）

### 4.1 触发与状态机

一个组件、两种模式：`open(mode)`，`mode = 'first' | 'replay'`；同一套 DOM、同一套状态机。

```
状态机： idle → open(mode)
         ├─ mode='first'  → playBrand() → 片头 1.2s → goto(0)
         ├─ mode='replay' → goto(0)（跳过片头）
         ├─ goto(i) → next / prev / jump(i)
         ├─ skip()   → 写标记 → close() → 焦点归还触发元素
         ├─ finish() → 写标记 → close()
         └─ close()  → 移除 .show / 解绑键盘 / 恢复 body 滚动与桌宠显隐
```

- **触发点**：`40-ops.js` init 末尾（约 :1563）`await loadConfig()` 之后读 `ek-guide-v1`；无标记 → `open('first')`，不阻塞任何已有初始化。
- **重放入口**：`#about` 的 `.mfoot`(:1202) 加 `<button id="about-replay">重新观看介绍</button>`（放「知道了」左边）。事件用 `document` + `closest()` 委托写在 `90-onboard.js`，**不改 `20-shell.js`**（跨 IIFE TDZ 的坑）。
- **片头期间的「跳过」语义要分清**：点一下 / 任意键 → **只结束片头**、立刻进屏 1；不是关掉整个引导。屏 1 之后的「跳过」才是关闭整个引导。

### 4.2 品牌片头 1.2 秒（Anime.js timeline）

`#ob-brand` 是 `#onboard` 内、**Swiper 容器之外**的绝对定位覆盖层（不占 slide、不影响进度点）。元素：暖米白全屏底 + 藕杏柔光斑 + cici 大头像 + 光环 + EnvKit 字标 + tagline + 版本小字。素材全用现成 `APP_ICON`。

| 时间 | 动作（只动 transform / opacity） |
| --- | --- |
| 0–120ms | 底色淡入、柔光斑从中心扩散 `scale .6→1.15` + `opacity 0→.35` |
| 120–520ms | 头像 `scale .82→1`、`opacity 0→1`；藕杏光环扩散一次后收束 |
| 480–880ms | 字标 `letter-spacing 6→1px` + 上浮；tagline / 版本 stagger 60ms 淡入 |
| 900–1200ms | 整层轻微上移 -12px + 淡出；同时屏 1 的大头像从同一位置淡入 |

**共享元素视觉接力**：片头头像与屏 1 头像是同一张图、同一屏幕坐标和尺寸交接，看起来是「同一个头像带我走进去」；实现上是两个节点做位置/尺寸匹配，**不强制跨 Swiper 移动同一个 DOM**，避免破坏 slide 结构。

- **不是 loading**：片头不等待任何资源、不阻塞 DOM（Go 首屏已渲染完）、时长固定、可跳过——它是叙事层。§7 的「不做 loading」结论不变。
- **减弱动效**：`prefers-reduced-motion` 下片头直接压成 0（不播），Swiper `speed=0`，Anime 只跑 opacity；`50-pet.js:38` 已有同类处理。
- **只播一次**：`mode='first'` 才有；「关于」重看默认不含片头，避免每次重看都等 1.2 秒。想连片头一起看，可在章节宫格加一个「重看片头」小入口（可选，S7 再定）。

### 4.3 交互

底部进度点（可点跳）、← →、ESC、滚轮 / 触摸横向翻页、右上角常驻「跳过」、章节宫格入口。

### 4.4 层级与可访问性

- 层级：header 5 < 菜单 40/60 < `.modal` 80 < `#toasts` 95 < 桌宠右键 999。`#onboard{z-index:100}`；打开时 body 加 `.ob-active` 隐藏桌宠与 toast 容器，关闭精确移除。
- 顺手把 UX 报告 P0-3 的 modal 老问题一次做对（至少引导自己）：`role="dialog" aria-modal="true" aria-labelledby`、打开移焦点进对话框、Tab 循环、ESC 关闭、**任何关闭路径都把焦点还回触发按钮**；`aria-live` 播报「第 N 屏，共 M 屏」。片头层 `aria-hidden="true"`，别让读屏念一遍 logo。

---

## 5. 八屏逐页设计

**框架元素（统一）**：右上角「跳过」、底部进度点、左右翻页、cici 小头像贯穿向导（屏 1 从中央缩小飞到左下角，屏 8 飞回中央）、章节菜单入口、每屏文案 ≤ 2~3 行讲价值不讲功能清单。首次只出现标 ★ 的 5 屏。

| # | 标题 | 一句话要传达的 | 主要元素 | 可点的演示 | 首次 |
| --- | --- | --- | --- | --- | --- |
| 1 ★ | 开场 · 一个文件就够了 | 单文件、零依赖、开箱即用 | 大 cici 头像 + 柔光环、EnvKit 字标、tagline、版本徽标、双按钮 | 「看看它能做什么」/「跳过」 | ✅ |
| 2 ★ | 为什么存在 | 把散落的工具收敛成一个 | 左侧 5 个灰暗「以前」卡片（终端/DB/SSH/浏览器/日志）、中间流动光带、右侧亮起的 EnvKit 卡 | 悬停看被谁接管；点光带重播汇聚 | ✅ |
| 3 ★ | 四步走 | 主流程是一条明确的线 | 横向 4 段轨道（安装→配置→链端→启动，放大现有 `.guide`）、每步说明卡、迷你进度 | 点任一段切说明 + 逐项打勾；←→ 移动 | ✅ |
| 4 | 状态看得见 | 检测不是黑盒，会给下一步 | 仿真检测报告卡（Go/Node/MySQL/FISCO/端口/磁盘）、状态点、总体徽章 | 「跑一次演示检测」逐行扫入；点行展开「异常会提示什么」 | — |
| 5 | 日志分区 | 出问题一眼定位 | 5 个 scope 标签、等宽日志窗、错误高亮行、「最近错误」小卡 | 切分区；「插入一条异常」看联动 | — |
| 6 ★ | AI 助手 & 桌宠 | 说人话就能运维 | 会眨眼/漂浮的桌宠 SVG、对话气泡、示例问句、**写操作确认卡** | 只读问句 → 气泡回复；写类 → 弹**演示**确认卡 | ✅ |
| 7 | 安全边界 | 权限闸门是特性不是限制 | 4 档权限阶梯、每档例子、「默认关闭」开关示意、外连与「不执行任意命令」说明 | 点档位看「能做什么/需什么确认」 | — |
| 8 ★ | 收尾 · 准备好了 | 明确下一步 + 可重看 | 头像飞回中央、3 个 CTA、章节点、一行「关于里随时再看」 | 点章节点跳任意屏；CTA 见下 | ✅ |

**逐屏动效要点**（色彩一律走 §2 全站变量，不再用绿）

- **屏 1**：柔光斑缓慢漂移 → 头像 `scale .6→1` + 藕杏光环扩散 → 字标/副文 stagger 上浮 → 头像缩小飞向左下角成为向导。片头交接见 §4.2。
- **屏 2**：旧卡片依次淡入并轻微抖动 → SVG 连线 `stroke-dashoffset` 绘制 → 被藕杏光带「吸」入 → 右侧卡以雾蓝亮起放大。
- **屏 3**：轨道自左向右逐段点亮流动（雾蓝），选中段放大；说明卡随选中滑入。
- **屏 4**：骨架行 → 逐行扫入结果，状态点灰转**雾蓝 / 暖杏**，最后总体徽章弹入。
- **屏 5**：旧行滑出、新行打字机式滚入；异常行淡珊瑚标记从左滑入并联动「最近错误」。
- **屏 6**：桌宠漂浮 + 眨眼，气泡逐字出现，确认卡从底部滑入并轻微脉冲。
- **屏 7**：阶梯自下而上依次亮起（淡紫→雾蓝），悬停展开例子。
- **屏 8**：向导头像飞回中央、光环收束，CTA 依次上浮，章节点依次点亮。

**屏 8 的三个 CTA**

1. 「去看安装页」→ 关引导 + `goto('install')`，**只切面板，不自动跑检测**。
2. **「跟着做一遍」→ 衔接 P1-4**：关引导 → `document.dispatchEvent(new CustomEvent('ek:tour-start', {detail:{source:'onboard-finish'}}))` → `EKTour.start()` 跑 4 步高亮引导（§8.2）。**按钮显示条件**：`window.EKTour && typeof EKTour.start === 'function'`，S6 未落地时自动隐藏，绝不出现点了没反应的死按钮。
3. 「以后再说」→ 直接关。

**演示数据红线**：所有 mini demo 用内置静态数据，**不 fetch 任何 `/api/*`、不写配置、不触发真实确认卡/权限逻辑、不执行任何命令**，界面标「演示」。P1-4 的高亮引导也只做「指向 + 说明 + 让用户自己点」，不代点、不代跑检测。

---

## 6. 动效与「现代化」的具体手法

- **品牌片头**：1.2 秒短叙事，头像 / 字标 / 光环三段接力，与屏 1 共享元素过渡（§4.2）。
- **全屏沉浸**：引导期间隐藏 header/nav/桌宠，只留内容 + 进度。
- **共享元素过渡**：cici 头像贯穿片头 + 8 屏，位置/大小连续变化，把「翻页」变成「一个向导带你走」。
- **stagger 入场**：每屏 3~6 个元素按 60~90ms 间隔上浮淡入（Anime.js `stagger()`）。
- **微交互**：按钮磁吸 / hover 视差 / 光标跟随柔光（低强度，别抢内容）。
- **只动 transform 和 opacity**，不动 width/height/top/left；非当前屏 `visibility:hidden` + 暂停动画。
- **克制**：柔和暖底 + 大留白，不要赛博霓虹、不要高饱和渐变；「现代化」靠节奏和连贯性，不靠特效堆砌。
- 参考方向：Apple 产品页滚动叙事、Linear/Vercel 式渐入、Awwwards scrollytelling；**只借手法不借素材**。

---

## 7. loading / 产品头像状态体

**判断：不做首屏 loading。** Go 一次吐出完整内联页、无阻塞请求；一个 0 等待却转 1 秒的 spinner 是负体验。**品牌片头不是 loading**（§4.2），两者不冲突。

**「产品头像加载状态」留在三个真地方：**

1. **引导进度向导**：cici 小头像周围一圈进度环（首次 5 段 / 完整 8 段），当前屏点亮 —— 这是「头像状态」最自然的现代用法，不假装等待。
2. **真实等待场景**：AI 首响应、环境检测、备份/复验、版本检查（后端 8s goroutine）。用头像**呼吸 / 进度环**替换通用 spinner，配一句人话状态，有开始有结束。
3. **片头入场**：头像缩放 + 光环本身就是品牌启动的仪式感，不额外做 loading 页。

规则：**没有等待就不显示，有等待才显示，且必须能结束**。

---

## 8. 依赖库方案（v3：3 个库 / 5 个文件）

### 8.1 前提：必须能直接 `<script>` 引

项目**无构建、传统 `<script>` 拼接、CSP 只允许 `'self'`**。ESM-only 的入口要么走同域 `<script type="module">` 单独桥接，要么排除。选 UMD / IIFE 全局构建。

### 8.2 最终组合

| 库 | 版本 / 文件 | 许可 | 全局对象 | 负责 |
| --- | --- | --- | --- | --- |
| **Swiper** | 12.x · `swiper-bundle.min.js` + `swiper-bundle.min.css` | **MIT** | `Swiper` | 8 屏容器、翻页、进度点、键盘、滚轮、触摸、`slideChange` |
| **Anime.js** | 4.5.x · `anime.umd.min.js`（`dist/bundles/`） | **MIT** | `anime`（`const {animate, stagger, createTimeline} = anime`） | 品牌片头 timeline、页内 stagger 入场、共享头像形变、SVG 连线、进度环 |
| **driver.js** | 1.x · `driver.js.iife.js` + `driver.css` | **MIT** | `window.driver.js.driver` | **S6 / P1-4**：4 步控件高亮引导（选安装目录 → 配数据库 → 点检测 → 看日志分区） |
| 自写状态机 | `90-onboard.js` 约 180 行 | — | `EKOnboard` / `EKTour` | 首次触发、片头编排、`ek-guide-v1`、关于重放、跳过/焦点/reduced-motion、8 屏静态演示数据、P1-4 步骤表 |

**为什么是这三个**

- 三份 **MIT**、全部有官方 UMD/IIFE 全局构建（driver.js 官方文档明确给 `driver.js.iife.js` + `window.driver.js.driver`）、零运行时依赖、零构建、零 CDN —— 与 `THIRD-PARTY-NOTICE.md` 现有 marked/highlight/vConsole 的落地惯例一致。
- Swiper bundle 自带 `pagination` / `keyboard` / `mousewheel` / `effect-fade`，覆盖 §4.3 全部交互，**不用手写翻页逻辑**。
- Anime.js v4 提供 UMD 全局构建（不只是 ESM），timeline + stagger + SVG + 数值动画足够片头 + 8 屏；比 GSAP 小、许可更干净。
- driver.js 正好兑现 UX 报告 P1-4 原本的建议（当时说「一个依赖都不该引」，现在用户已明确要用库），且只需 2 个文件、约 5KB，就换来收尾 CTA 不是空按钮。
- 体积粗估：Swiper bundle js ~150KB / gz ~45KB + css ~18KB / gz ~4KB；Anime.js UMD min ~30-40KB / gz ~12KB；driver.js iife ~12KB / gz ~5KB + css ~3KB。合计 gz 约 70KB。exe 已 15MB、本地同域加载，可忽略；**具体数字锁版本后实测**。

**关键 API 用法（示意）**

```js
const sw = new Swiper('#ob-swiper', {
  slidesPerView: 1, loop: false, speed: 600, effect: 'fade',
  allowTouchMove: true,
  keyboard: { enabled: true }, mousewheel: { forceToAxis: true },
  pagination: { el: '#ob-dots', clickable: true },
  on: { slideChange(s) { EKOnboard.onSlide(s.activeIndex); } }
});
const { animate, stagger, createTimeline } = anime;
animate('#ob-swiper .swiper-slide-active .ob-in', {
  opacity: [0, 1], translateY: [16, 0],
  delay: stagger(70), duration: 520, ease: 'out(3)'
});
// S6：收尾 CTA 只发事件，P1-4 自己监听，不产生硬依赖
document.addEventListener('ek:tour-start', () => {
  const d = window.driver.js.driver({ steps: EKTour.STEPS });
  d.drive();
});
```

### 8.3 备选与排除

| 候选 | 许可 / 形态 | 结论 |
| --- | --- | --- |
| GSAP 3.13+ `/minified/gsap.min.js` | UMD 全局 `gsap`；2024 起含全部插件商用免费，但**是 GreenSock 自定义标准许可、非 MIT** | ⚠️ 效果更强、时间线更好写；若接受在 NOTICE 里单列一份自定义许可文本，可替换 Anime.js |
| Motion One / Motion | MIT、~4KB gz，但**主要是 ESM** | ⚠️ 需 `<script type="module">` + 本地 esm + 全局桥，与现有拼接链分开 |
| Keen Slider | MIT、~5.5KB gz、vanilla | ⚠️ 更轻，但翻页效果要自己写，不如 Swiper 成品感强 |
| intro.js / shepherd | GPL+商业 / 许可说法冲突 | ❌ 授权风险，且是控件教学不是产品展示 |
| React Joyride / Framer Motion | React-only | ❌ 项目无 React |
| Lottie / dotLottie | Apache-2.0，需 AE 产出资源 | ❌ 项目没有动画素材 |
| fullPage.js | 商业授权 | ❌ 排除 |

> 核实来源：Swiper 官方 Getting Started、Anime.js v4 安装文档（`dist/bundles/anime.umd.min.js` 全局 `anime`、MIT、v4.5.0）、driver.js 官方 Installation（`driver.js.iife.js` / `driver.css` / `window.driver.js.driver`）、GSAP 安装文档。**落地前锁死版本号、核对随包 LICENSE、记录 SHA256、把三份 MIT 全文写进 `THIRD-PARTY-NOTICE.md`**。

### 8.4 vendor 落地清单（精确，别漏）

1. `web/vendor/` 新增 **5 个文件**：`swiper-bundle.min.js`、`swiper-bundle.min.css`、`anime.umd.min.js`、`driver.js.iife.js`、`driver.css`（**官方发布产物原样，不自己打包、不改内容**）。
2. `main.go` 的 vendor 变量块（:54-63 附近）加 5 条 `//go:embed web/vendor/…`；路由区（:166-167 附近）加 5 条 `http.HandleFunc("/vendor/…", staticBytes(…))`，CSS 用 `text/css; charset=utf-8`。
3. `index.tpl.html` head（:12-14 附近）加 2 个 `<link>` + 3 个 `<script src="/vendor/…">`。**顺序**：库在前，拼接的内联 `<script>` 在后。
4. `THIRD-PARTY-NOTICE.md` 新增一节，按现有格式写「说明 + 资源/版本/许可/位置表 + **三份 MIT 许可全文**」。
5. **必须 `go build` 重建 exe**（embed 在 exe 里的老坑），并过 `web_build.py --check` / `web_source_test.go`（改了 tpl）。
6. `lanserve.go:165` 只给手机页注册 vConsole。引导 / P1-4 只在主控制台，**暂不注册**；以后 LAN 口也要看就同步加同名路由。
7. `tools/dist_check.py` 加断言：引导页无外链、5 个 vendor 文件同域请求均 200。
8. 核对 Swiper / driver.js 的 CSS **不含全局 reset / 元素级规则**（都是 `.swiper*` / `.driver*` 前缀，风险低）；若有，把样式收进 `#onboard` 作用域。

---

## 9. 落地涉及的文件与接口

| 文件 | 改动 | 备注 |
| --- | --- | --- |
| `web/vendor/`（新增 5 文件） | Swiper js/css、Anime.js UMD、driver.js iife/css | 官方产物原样，锁版本 |
| `main.go` | 5 个 `//go:embed` 变量 + 5 条 `/vendor/…` 路由 | v1 写「方案 A 不动」，v2 起必动 |
| `web/src/app.css` | **`:root` / 暗色变量全站换值**；A 类硬编码色替换；新增 `.ob-*` 样式 / `@keyframes` / `prefers-reduced-motion`；少量 `--ob-*` 派生变量 | **变量名不改，261 处 `var()` 不动**；B 类格式色 / danger 红保留 |
| `web/src/mobile.html` | 内联 CSS 状态点去绿、主色对齐 `--brand` | 独立样式表，容易漏 |
| `web/src/js/40-ops.js` / `30-profile.js` / `50-pet.js` | 端口状态色、终端主题色、桌宠彩纸色改柔和 | 共 9 处硬编码 |
| `web/src/index.tpl.html` | head 加 2 link + 3 script；body 顶层加 `#onboard`（`#ob-brand` + Swiper 8 个 slide）；`#about` 的 `.mfoot`(:1202) 加 `#about-replay` | Swiper 要求 `.swiper > .swiper-wrapper > .swiper-slide` 结构 |
| `web/src/js/90-onboard.js`（新增） | 状态机、片头 timeline、`FIRST_RUN_SLIDES`、Swiper 初始化、Anime.js 时间线、`ek-guide-v1`、8 屏静态演示数据、`EKTour` 步骤表、事件委托 | 数字前缀自动拼入；**不 fetch `/api/*`** |
| `web/src/js/40-ops.js` init 末尾（约 :1563） | 首次触发判定 | 只加几行 |
| `web/src/js/10-i18n.js` + `tools/i18n_en.json` | 全部文案英文映射 | `i18n_inventory.py` → `i18n_build.py` 自动重建 |
| `THIRD-PARTY-NOTICE.md` | 新增 Swiper / Anime.js / driver.js 一节 + 三份 MIT 全文 | 必动 |
| `tools/web_build.py` / `web_source_test.go` | **不用改**，但要跑 `--check` 与 `go test` | 已有门禁 |
| `tools/dist_check.py` | 加「无外链 / vendor 可用」断言 | 建议 |
| `tools/cdp_ai_pick_test.js:190/195` | **不改**（docx 格式色断言） | 换色时别误伤，见 §2.4 B 类 |
| 后端 `state.go` / `configfile.go` / `lanserve.go` | **完全不动** | 状态只存 localStorage |
| `tools/cdp_onboard_test.js`（新增） | 片头可跳过、首次播 5 屏、跳过写标记、刷新不重播、关于重放 8 屏（无片头）、章节跳转、ESC、焦点归还、reduced-motion、`ek:tour-start` 事件、明暗两套色板 | `ENVKIT_INSTANCE_TAG` 隔离；造干净状态走应用自己的清空，**别删 Chrome 配置目录**；`clip.scale=2` 量盒子 |

**前端内部接口（不新增 HTTP 端点）**

```
EKOnboard.open(mode)         // 'first'（含片头） | 'replay'（跳过片头）
EKOnboard.playBrand()        // 1.2s；任意键/点击可提前结束，仅进屏 1
EKOnboard.goto(i)            // 0..7（首次模式映射到 FIRST_RUN_SLIDES）
EKOnboard.onSlide(i)         // Swiper slideChange → Anime.js 入场 + 更新头像进度环
EKOnboard.close(returnFocusTo)
localStorage['ek-guide-v1'] = '1'

// 收尾 → P1-4（松耦合，靠事件不靠直接调用）
document.dispatchEvent(new CustomEvent('ek:tour-start', {detail:{source:'onboard-finish'}}))
EKTour.start()               // S6 实现；未实现时收尾按钮自动隐藏
EKTour.STEPS                 // 4 步，对齐 UX 报告 P1-4
```

---

## 10. 分期实施建议（v3：8 阶段）

| 阶段 | 内容 | 完成判据 |
| --- | --- | --- |
| **S0 依赖落地** | 下载 5 个 vendor 文件、embed + 路由 + head 引入 + NOTICE、**重跑 `go build`** | 5 个同域 URL 均 200；`web_build --check` / `go test` 绿；exe 里能取到 `Swiper` / `anime` / `window.driver.js.driver` |
| **S1 全站柔和配色**（v3 新增，独立于引导） | `:root` / 暗色变量换值；A 类硬编码色；`mobile.html`；js 里 9 处；对比度实测；7 panel + 13 卡 + 6 modal + 桌宠 + 日志 + AI 抽屉 + 手机页逐块截图 | **全站无绿色残留**（B 类 XLSX 徽标例外有记录）；明暗主题文字对比度达标；全量 CDP 回归绿（`cdp_ai_pick_test.js` 颜色断言不许假红） |
| **S2 引导骨架** | 8 屏静态 DOM + 状态机 + 首次 5 屏 / 关于重放 8 屏 / 跳过 / 章节跳转 / 进度 | CDP 跑通首次→跳过→刷新不重播→关于能重开；**此时无动画、无片头** |
| **S3 品牌片头 + 页内动效** | 1.2s timeline + 共享元素接力 + stagger 入场 + 页间过渡 + reduced-motion | 片头可跳过、重放不含片头、只播一次；逐屏核对节奏 |
| **S4 交互演示** | 8 屏 mini demo（纯静态）+「演示」标识 | 断言引导期间**零 `/api/*` 请求**、零配置写入 |
| **S5 头像状态体** | 进度环 + 接入真实等待场景 | 无等待不显示，有等待能结束 |
| **S6 P1-4 衔接** | driver.js 4 步高亮、`EKTour.start()`、`ek:tour-start` 事件、收尾 CTA 显示条件 | 收尾按钮点了真能进教学；未落地时自动隐藏；不代点、不代跑检测 |
| **S7 收口** | i18n 全量、`dist_check` 断言、CI 门禁、变异验证（改坏首次触发 / 片头跳过 / 重放 / 全站色板 / P1-4 事件必红） | `go vet` / `go test ./...` / `incident_gate` / `web_build --check` / `dist_check` / `version_check` / `ci_workflow_check` 全绿 |

> **两个看图点**：**S1 做完先看一眼**（全站换色最主观、影响面最大，别等 8 屏做完才发现调性不对）；**S3 做完再看一眼**（片头节奏 1.2 秒是否合适，改起来比 8 屏便宜）。
> 若想把 P1-4 单独排期，只需把 S6 摘掉：收尾按钮会按 feature flag 自动隐藏，其余阶段一行不改。

---

## 11. 红线与风险

- **演示零副作用**：不调 `/api/*`、不写 Config、不触发真实确认/权限逻辑、不执行任意命令。权限闸门一行不动；P1-4 只高亮，不代点、不代跑检测。
- **引导不能挡入口**：参考托盘修复的教训（刻意不调 `systrayReady()` 避免藏控制台），必须一键跳过、ESC 可关、不阻塞任何已有初始化，绝不出现「关不掉的全屏层」。片头也要能跳过。
- **不造死按钮**：收尾「跟着做一遍」以 `EKTour.start` 是否存在为显示条件，不存在就隐藏。
- **全站换色的真实风险**（v3 最大一块）：
  - ① **改值不改变量名**，别顺手重命名变量，否则 261 处引用全炸；
  - ② B 类格式识别色 / danger 红 / 代码高亮色**不许动**，尤其 `cdp_ai_pick_test.js:190/195` 的 `rgb(43,87,154)`；
  - ③ `--ok` 与 `--info` 同族后必须补图标 + 文案，不能只靠颜色；
  - ④ `mobile.html` 是独立内联 CSS，最容易漏；
  - ⑤ 暗色只换绿系、不动蓝灰骨架，别整站暗色重配；
  - ⑥ 换色是纯样式改动但覆盖所有面板，**必须跑全量 CDP + 明暗两套截图**，别只看首屏。
- **语义色别走极端**：「不要绿色」不等于「所有语义色都变粉」；危险 / 破坏性仍需足够醒目，428 权限闸的视觉不变。
- **库相关**：① 锁版本 + 记录 SHA256，升级要重过门禁；② 绝不引 CDN（CSP 会挡，`dist_check` 加断言）；③ 核对 Swiper / driver.js CSS 不污染主界面；④ `prefers-reduced-motion` 下 `speed=0`、片头不播；⑤ **改了 tpl 必须 `go build` 重建 exe**，别只跑 `web_build.py` 就以为完事（embed 老坑）。
- **文案讲价值不讲清单**：每屏 ≤ 2~3 行；i18n 从 S2 就按 `t()` 写，别等最后补英文。
- **首次只一次**：清缓存 / 换浏览器会重播，是 localStorage 语义的可接受代价；跨浏览器才值得加后端字段（需 `*bool` + 独立端点 + `mergeFormConfig` 无条件保留 + `dist_check` 默认值断言）。
- **层级冲突**：`#onboard` 与 `#toasts`(95)、桌宠右键(999)、`.modal`(80) 要实测命中测试，`element.click()` 绕过命中测试的老坑别忘了用 `elementFromPoint` 查。

---

## 12. 决策已全部收敛

7 项决策见 §0，**本版不再有待确认项**。实施中若出现分歧，按以下默认做法，说一声即可改：

| 情形 | 默认做法 |
| --- | --- |
| 「全站无绿」与 XLSX 格式绿冲突 | 保留这一个格式识别徽标（B 类），其余绿全换 |
| 片头节奏觉得长 / 短 | 只改 `BRAND_MS` 一个常量（默认 1200），不影响后续 5 屏 |
| 想把 P1-4 单独排期 | 摘掉 S6，收尾按钮按 feature flag 自动隐藏 |
| 「关于」重看也想看片头 | 章节宫格加一个「重看片头」小入口（默认不加） |
| 色板想再调 | S1 完成后统一改 `:root` / 暗色变量值，改值不改变量名 |

---

## 13. 实施记录（v3 落地后补）

以下都是实测出来、方案阶段没预料到的坑，写在这里避免下次重踩。

### 13.1 全屏 overlay 会挡住既有 CDP 回归

首次自动播是全屏 `.ob`（z-index 100），33 个既有 `cdp_*_test.js` 用 `--headless=new`
打开页面时若被判为「首次」，overlay 会吃掉所有对主界面控件的真实鼠标点击 →
整批回归假红。因此 `40-ops.js` 的首次触发加自动化环境保护：
`navigator.webdriver || /HeadlessChrome|PhantomJS/i.test(UA)` 为真时不自动播；
`EKOnboard.open('first')` 与「关于 > 重新观看介绍」两种环境都仍可手动打开。

**关键实测**：本项目 Chrome 只开 `--remote-debugging-port`（没加 `--enable-automation`）
时 `navigator.webdriver` **是 false**。第一版单靠它判断等于没修；第二版改用 UA 里的
`HeadlessChrome`（headless 稳定有、真实浏览器绝无）才真正生效。真实用户的 Chrome
两者皆非，引导照常播。

### 13.2 CDP profile 残留让「首次」变假绿

复用同一个 `--user-data-dir` 时，localStorage 的 `ek-guide-v1` 跨运行持久化，
**从第二次起轮 A 就不再是首次**，但 `skip-marks-seen` 之类断言照样全绿（因为 key
本来就在）。修法：每次运行用唯一 profile 目录，并在首次断言前
`localStorage.clear()` + reload 双保险。

### 13.3 模拟真实用户要用协议层覆写 UA

CDP 里想验证「自动播」必须先伪装成真实浏览器。JS 里
`Object.defineProperty(navigator,'userAgent',...)` 盖不住判据；改用
`Emulation.setUserAgentOverride` 才可靠。环境诊断断言 `envA-real-user-ua`
必须先绿，否则后面所有「首次自动播」全绿都是空转。

### 13.4 新增门禁装置

| 装置 | 覆盖内容 | 规模 |
| --- | --- | --- |
| `web_onboard_test.go` | `go test ./...`（CI 必跑）：演示零副作用、vendor 落地链路、首次 5 屏选集、自动化保护、头像状态体非空转 | 4 个 Test |
| `tools/cdp_onboard_test.js` | 真实 Chrome：三库全局名、片头出现/结束、5/8 屏、真实鼠标翻页与跳过、头像呼吸环、8 屏演示交互、零写操作 | 52 断言 |
| `tools/onboard_mutate.py` | 改坏必红 / 复原字节级一致（M1 选集、M2 片头、M3 自动化保护、M4 演示发写请求） | 4 组变异 |
| `tools/dist_check.py` | 5 个 vendor 同域可达 + 零 CDN 外链 | 新增一段 |

演示零副作用是**双层**判据：运行时给 `fetch` 打桩断言演示期间无任何
`POST/PUT/DELETE/PATCH`、无 `/api/ai/`；源码级断言 `90-onboard.js` 不含
`fetch(`、`/api/`、`eval(`，且 `localStorage.setItem` 恰好一次（只有 `mark()`）。

## 14. v4 视觉重构（v3 交付后的返工）

### 14.1 为什么返工

v3 功能全绿，但用户看完给出三条判断，全部成立：

1. **8 屏同构** —— 每屏都是「kicker + 标题 + lead + 一张卡 + 一行按钮」，等于同一份
   PPT 复制 8 次。功能齐全，但没有一屏有"自己的构图"。
2. **头像滥用** —— 片头、顶栏进度环、开场、桌宠、收尾共 5 处，产品像在反复自我介绍。
3. **AI 与文字演示太弱** —— AI 屏只有「一个气泡 + 两个按钮」，桌宠是头像图 + 两个圆点
   假眼睛；检测/日志只是几行小字，信息密度低于真实界面，没有说服力。

结论：**v3 解决的是"有没有"，v4 要解决"像不像一个现代产品"**。

### 14.2 八屏换成六种构图语言

| # | v3 长相 | v4 构图 | 关键元素 |
| --- | --- | --- | --- |
| 1 开场 | 头像 + 两行字 | **字标 hero + 叠层剖面** | 86px 字标、三层工具卡叠在主卡后面、光带扫过 |
| 2 为什么 | 五张小卡 + 箭头 | **散落 → 汇聚** | 旧工具卡真实散落（不同旋转/位移）、粒子沿光束流动、主卡落定放大 |
| 3 四步 | 四个按钮横排 | **横向时间轴** | 大号细体数字（未选中 0.34 透明度）、轨道 + 进度填充、输入/产出两栏 |
| 4 检测 | 六行小字 | **终端 + 报告双栏** | 深色终端逐行打印 `$ envkit check --all`、报告行带安装路径、逐行落定 |
| 5 日志 | 五个标签 + 几行 | **全幅深色控制台** | 窗口红黄绿灯、分区像编辑器 tab、CSS counter 行号、错误行红竖条 + 插入高亮 |
| 6 AI | 气泡 + 两按钮 | **真实桌宠 + 对话打印** | 克隆主界面 `#petSvg`（三视角）、用户气泡 → trace 行 → 逐字打字 → 确认卡 |
| 7 安全 | 四张按钮卡 | **阶梯** | 逐层递减右缩进形成台阶、每档带确认标签、428 用红色标签 |
| 8 收尾 | 八个数字方块 | **章节地图** | 八张缩略卡（编号 + 名称 + 三条迷你进度条） |

**头像从 5 处收敛到 1 处**（只在片头作为"引子"出现）。顶栏换成
`章节序号 + 细进度条 + 演示标识`；开场和收尾彻底不用头像。

### 14.3 三个真坑（都不是"调参"能解决的）

1. **全局 `h2{display:flex}`（app.css:146）让所有 `.ob-h` 左对齐。**
   flex 容器上 `text-align` 对匿名 item 无效，所以 `.ob-head{text-align:center}` 的继承
   完全失效 —— 表现为"标题莫名偏左、和下面的居中段落对不上"。修复是 `.ob-h{display:block}`。
   这个坑靠读 CSS 找不出来（局部样式看着没问题），是量了 `getBoundingClientRect` +
   `getComputedStyle(textAlign/display)` 才定位的。
2. **`.swiper-slide` 是 flex 行但没 `justify-content:center`**，所有 `max-width` 的场景块
   （`.ob-split`/`.ob-hero`/`.ob-ai`/`.ob-console-scene`）全部靠左，1440 视口下右边缘空 400px。
   视觉上像"内容没居中"，实际是父级 flex 对齐缺失。
3. **检测屏首次进入是空终端 + 全灰报告行**。`renderDetect(true)` 只画空壳，要等用户点按钮
   才有内容 —— 第一眼像坏了。改为 `initDemo(3)` 直接 `runDetect()`。

### 14.4 打字机与断言时序的约束

AI 屏的逐字输出会改变 `#ob-bubble` 在 250ms 内的 `textContent`。为了让断言仍然测真东西
（而不是等一个固定毫秒数），写类问句的脚本被设计成**关键词前置**：

- 气泡文本以「确认卡」开头 → 250ms 时已输出 6~8 字，`/确认卡/` 恒真；
- 确认卡是**同步**弹出的（真实交互也是先拦后解释），不依赖打字进度；
- 点击「知道了」后，先 `endType()` 把当前文本补全，再输出以「权限闸门」开头的结束语。

### 14.5 门禁变化

| 装置 | v3 | v4 |
| --- | --- | --- |
| `web_onboard_test.go` | 4 Test | 4 Test（未改，仍覆盖零副作用/5 屏/vendor/自动化保护） |
| `tools/cdp_onboard_test.js` | 52 断言 | **57 断言**，新增：`layout-crumb`、`layout-progress`、`demo-detect-console`、`demo-pet-cloned`（3 张视角图 + 正面 opacity=1）、`demo-typing-complete`（打字结束后全文） |
| `tools/onboard_mutate.py` | M1~M4 | M1~M4（锚点未变，全部仍能精准打红） |

CDP 仍然用真实鼠标（`Input.dispatchMouseEvent` + `elementFromPoint` 命中测试），
新增的 5 条断言全部落在"用户真的能看到/点到"的层面，不是读内部变量。

---

## 15. v5：首次也播 8 屏 + 细节层（本轮）

### 15.1 唯一的决策变更

| 项 | v3 / v4 | v5 |
| --- | --- | --- |
| 首次播放 | 精选 5 屏 `[0,1,2,5,7]`，完整 8 屏只在「关于」里 | **完整 8 屏 `[0..7]`**，首次和重放走同一条路径 |

`FIRST_RUN_SLIDES` 仍然保留成显式数组（而不是把 `mode` 判断删掉）：它是一个**可断言的点**，
将来真要做"精简版"只改这一行，Go 契约测试和 CDP 会立刻报出屏数/顺序变了。
`mode` 之间的差别收敛成两件事——是否播片头、关闭时是否写 `ek-guide-v1`。

### 15.2 每屏补的细节（都带客观信息，不是装饰）

| 屏 | v4 | v5 新增 |
| --- | --- | --- |
| 01 开场 | 叠层剖面静态 | 剖面**每 2.6s 轮转高亮**一块，右下一句说明跟着换；右下加 HUD 四个硬数字（4 步 / 6 项检测 / 5 个分区 / 1 个入口） |
| 02 为什么存在 | 5 张悬浮 chip + 汇聚动画 | 每张旧卡片补一句"代价"（记 5 条命令 / 另开 GUI / 手动 SSH…），改成整齐一列；汇聚后出现 `5 个窗口 · 3 套工具链 → 收拢成 1 条状态线` |
| 03 四步走 | 手动点步骤 | **自动每 2.5s 前进一格**（手动点任意步骤即停），走过的段自动打勾；详情从 2 格扩成 2×2：输入 / 产出 / **前置** / **预计耗时** |
| 04 检测 | 终端 4 行 + 报告逐行 | 终端顶部 2px 进度条跑满 + `1.8s · 6 项`；报告底部补**结论行**（"下一步是「程序配置」"） |
| 05 日志 | 5 分区 × 5 行 + 最近错误 | 每分区补到 6–7 行（控制台不再下半截空着）；侧栏加**五个分区的条数**（当前分区高亮）+ 轮转信息 |
| 06 AI | 两句问答、回答原地覆盖 | **保留多轮**：新回答打字前把上一轮"冻结"成历史气泡，最多留最近两轮 + 逻辑裁剪；trace 带耗时（0.31s / 0.42s / 0.66s）；第三问"这段报错是什么意思"；**桌宠状态联动**（待机→思考中→待你确认→已就绪） |
| 07 安全 | 四档 + 一段描述 | 选中档位后列出 **直接做 / 要确认 / 不做** 三栏具体动作（含"任意 shell"这种反面项） |
| 08 收尾 | 8 个章节点 | 每章**一句摘要**；`8 屏 · 约 90 秒 · 全部是演示数据` 总览；删掉无含义的三段小进度条 |
| 顶栏 | `01` + 章节名 | `01 / 08`，一眼能算还剩几屏 |

### 15.3 顺带修掉的两个排版问题（截图量出来的）

1. `.ob-lead{max-width:52ch}` 对中文太窄（`ch` 按 `0` 的字宽算），居中版式里 30 字的一句话会被压成
   「…都写在 / 界面上。」。`.ob-head .ob-lead` 放开到 100%，交给已经限宽的 `.ob-head`（760px）。
2. 旧卡片加了第二行后 `flex-wrap` 塌成竖排且宽度参差 —— 直接改成 `flex-direction:column` +
   名称/代价两端对齐，宽度一致、和右侧 beam 对齐。

### 15.4 门禁变化

| 装置 | v4 | v5 |
| --- | --- | --- |
| `web_onboard_test.go` | 首次 5 屏 | **首次 8 屏**（顺序也要对） |
| `tools/cdp_onboard_test.js` | 57 断言 | **70 断言**：`first-8-slides` / `first-8-dots` / `layout-crumb-total` / `detail-hud` / `detail-slab-rotate` / `detail-merge-foot` / `detail-step-io4` / `detail-detect-conclusion` / `detail-scope-stats` / `detail-ai-history` / `detail-ai-pet-state` / `detail-ai-second-answer` / `detail-perm-acts` / `detail-chapter-summary` |
| `tools/onboard_mutate.py` | M1 锚点 `[0,1,2,5,7]` | M1 反向：把 8 屏改成 5 屏，断言 `first-8-slides` 必红 |

`detail-*` 全系列只断"用户看得见的东西"：DOM 里有几条、文字对不对、class 有没有真的亮，
不读内部变量；`detail-detect-conclusion` 还要 `#ob-term-prog` 的内联 `width:100%` 真跑到底。


---

## 16. v6：重做第 02 / 04 屏

v5 把每屏都补了信息，但这两屏的**构图**没动过：02 是「五张小纸条 + 一个写着收拢的箭头 + 一张卡」，
04 是「终端 + 表格两个矩形叠着」。信息够了，形状没说服力。这一轮只改形状。

### 16.1 02：五个窗口 → 一条状态线

标题写着「五个窗口」，v5 画的是五个文字 chip。现在画成五个**真的窗口**：

| 元素 | 做法 |
| --- | --- |
| 五个窗口 | 240×58 的窗口剪影，各有标题栏（三点 + 名称 + 状态）和两行等宽内容。内容是真实的错误（`panic: dial tcp :3306`、`ERROR 2003`、`Connection timed out`），不是占位符 |
| 深色终端 | 五个里只有第一个是深色的 —— 「五个窗口里有一个就是终端」比五张同款纸片好认 |
| 错位 | 左边缘每张错开 10px、宽度递减 10px、**右边缘统一对齐**、各带 ±1.2° 旋转 |
| 连线 | 5 条 SVG 贝塞尔，各自从窗口右边缘出发，终点落在状态线竖线上对应节点的高度 |
| 状态线 | 一条竖线 + 5 行「名称 · 点线 · 值」，`align-self:stretch` + `space-between` 撑满 314px |
| 交互 | 悬停任一窗口 → 窗口抬起、对应节点亮、那条线加粗变色 |

**踩到的坑（都是量出来的）**

1. **连线起点悬空**：先用 `left:calc(--i * 13px)` 做阶梯、宽度固定，于是每张卡的右边缘位置不同
   （240/253/266…），而 5 条线统一从「窗口区右边界」出发 → 第一条线离卡片边缘有 52px 空气，看着像没接上。
   改成**左边缘错位 + 宽度递减**，右边缘全部对齐到 290，线才真的是从卡片边上出来的。
2. **点线完全看不见**：`.ob-line-node::after` 用 `border-bottom:1px dashed` 配 `color-mix(--line 82%, transparent)`，
   在米白背景上是像素级的灰，等于没画。换成 `repeating-linear-gradient`（2px 实 5px 空）+ `--muted 50%`。
3. **值被推到最右**：`justify-content:space-between` 让名称和值各占一头，中间 170px 空白把一行读成两列。
   点线就是补这段的 —— 少了它，两端对齐在中文长行里是个反模式。
4. tooltip 换成联动：原来 hover 弹「被「链端校验」接管」，现在直接让对应节点和连线亮。
   同一份信息，联动比 tooltip 少一次「看文字再对上号」的翻译。

### 16.2 04：扫描 → 结论 → 分组

标题写「检测给结论，不给猜测」，v5 的结论是表格底下的一行小灰字。现在把顺序倒过来：

```
扫描条（4 行命令 + 2px 进度 + 1.8s·6 项）
结论条（5/6 大数字 | 六段条 + 判断句 | 下一步）
分组卡（运行时 / 数据与磁盘 / 网络与链端）
```

- **结论条**是整屏的锚点：46px 记分 + 6 段条（5 蓝 1 琥珀，一格就是一项）+ 一句判断 + 右侧独立建议块。
  三列而不是两列 —— 「下一步」是一块可执行建议，不该混在判断句里。
- **分组**：6 项按「属于哪一类」分三组，组头带条数。分组说明每一项归谁管，平铺六行只说明有六项。
- **正常项不写「正常」**：六个都写是噪音。只有异常项才占一个词的宽度说话（`待校验`）。
- 记分用 `countUp` 从 0 滚到 5，比直接贴一个 5 更像「刚算出来的」。

### 16.3 布局：这两屏单独放宽

新增 `.ob-split-grow`（`minmax(248px,.56fr) minmax(0,1.44fr)`，max-width 1160）。

原来的 `.ob-split` 给演示区 546px：02 要放「290 窗口 + 140 连线 + 状态线」，
04 要放「三列组卡」，546 塞不下就全变成小方块。放宽后演示区 710px，文案 276px。
文案列变窄后原来手写的 `<br>` 会和自动折行打架（「…浏览器标 / 签、日志窗口」），顺手去掉。

### 16.4 暗色：--brand 不能当前景字

暗色主题里 `--brand` 仍是 `#526E96`（保 `button{color:#fff}` 的对比度），
所以任何「拿 brand 当文字色」的地方在暗色下都是深蓝压深底：结论条的大数字、下一步标签、
连线悬停时的节点名，全都几乎不可读。

补了一组 `body[data-theme="dark"]` 覆盖，统一换 `--brand2`（`#A3BAD8`）。
另外结论条背景原本 `color-mix(--brand-weak 92%, #fff)`，在暗色下会把横幅照成灰白，
改成混 `var(--card)` —— 亮色是白、暗色是深，自动跟着走。

### 16.5 门禁变化

| 装置 | v5 | v6 |
| --- | --- | --- |
| `tools/cdp_onboard_test.js` | 70 断言 | **77**：`detail-merge-shape`（5 窗口/5 线/5 节点/1 深色）、`detail-merge-hover`（真鼠标移到第 3 个窗口，断言第 3 节点和第 3 条线同时亮）、`detail-merge-hover-reset`、`detail-detect-groups`、`detail-detect-score`、`detail-detect-segs`、`detail-detect-msg` |
| `tools/onboard_mutate.py` | M1–M4 | **M1–M7**：M5「悬停只亮节点不亮线」→ `detail-merge-hover` 必红；M6「检测少一组」→ `detail-detect-groups` 必红；M7「记分算错」→ `detail-detect-score` 必红 |

`detail-merge-hover` 用 `Input.dispatchMouseEvent` 发真实 `mouseMoved`，
断言的是 DOM 上真的多了 `.own` / `.on` —— 不是读内部变量，也不是 DOM `.click()`。


---

## 17. v7：删掉假按钮，桌宠把内置对话接过来说

这一轮全部来自实际点用后的反馈。四件事，同一类问题：**演示里出现了"看起来在做事、
其实什么也没做"的东西**。

### 17.1 两个按钮为什么删，而不是改文案

| 按钮 | 它声称的 | 实际发生的 |
| --- | --- | --- |
| 02「重播汇聚」 | 重新演示"五个窗口收拢" | 进屏时已经自动播过一次；再播一遍只是同一段动画，不提供任何新信息 |
| 03「依次打勾」 | 四步依次完成 | 一次也没跑过，勾号是我自己画上去的 |

两个都删掉，并且**连带删掉它们的产物**：`showStep()` 里那句
`b.classList.toggle('done', k < i)` 和整块 `.ob-step.done` 样式一起下掉。
只删按钮不删勾号，等于把"假进度"留在屏幕上——这一屏真正能表达的只有
"现在看到第几段"（`.on` + 进度线），不表达"完成了几段"。

03 的自动前进也一起去掉了：它和打勾是同一个假进度，而且会在你读说明的时候
把焦点抢到下一步。现在这一屏完全由点击驱动，底部一句
「点任一段切换说明 · 上一步的产出就是这一步的输入」代替了原来的按钮 + 副标题。

删掉按钮后 02 少了一个唯一的交互，于是补了**反向联动**：点右侧状态节点，
左侧对应的窗口被挑出来（`.picked`）并抬起，那条连线加粗；再点一次取消。
原来只能"悬停窗口 → 节点亮"，现在是双向的——用户既可以问"这个窗口被谁接管"，
也可以问"这个节点替代了谁"。

### 17.2 卡片为什么看起来歪

v6 给五张窗口各配了 ±1.2° 的旋转，想让它们"像真的开了五个窗口"。实际效果是
五条右边缘一起看时高高低低，读成排版事故，而不是随性。

修法不是把旋转调小，而是**删掉旋转**，让错位只由位移产生：

- 左边缘按 `--i * 20px` 逐张缩进（原来是 10px，太小，读不出"阶梯"）
- 宽度 `calc(290px - var(--i) * 20px)`，保证**右边缘全部落在 290**
- 右边缘必须齐：五条连线是从这个边出发的，边不齐线就悬空

20px 的台阶配 64px 的行高，读起来是"五个窗口先后打开"；旋转只会读成"没对齐"。

### 17.3 06 把桌宠的内置对话接过来

原来的 06 只有"AI 对话 + 一个跟着变的文字芯片"。桌宠在那儿站着，但不会说话。
这一轮把主界面的**内置台词**接进来（照抄 `50-pet.js`，不另编）：

| 位置 | 内容 | 来源 |
| --- | --- | --- |
| 头顶气泡 | 思考中 ··· / 点我确认！/ ✓ 搞定 | `setSt()` 里的状态气泡 |
| 菜单「说句话」 | 我不思考了 / 我先去吃饭啦！这个你测一下~ / 大肥鱼坐的住 | `FISH_LINES` / `FISH_REACT` |
| 菜单「投喂 🐟」 | 小鱼干！我的最爱！ | `FISH_FOOD` |
| 菜单「查天气」 | 我瞅瞅天… → 合肥今天 19° · 湿度 42° · 体感 17° | `petWeather()` / `wxDetail()` |
| 菜单「看错误」 | 出错了…点我看日志 → 帮你在日志里标出来了 | `setSt('error')` / `deepLink()` |

三个细节值得单独说：

1. **气泡跟着状态机说话**。`petState()` 现在同时改两处：芯片写"在干什么"，
   气泡说"它此刻会说什么"。问写类问题时先「思考中」，回答落地才「点我确认！」——
   顺序反了就是"先撒花再干活"。点确认后才是 ✓。
2. **天气和看错误是两步**。先出声（「我瞅瞅天…」），900ms 后再给结果。
   一步到位反而不像真的在查。
3. **气泡居中是量出来的**。`#petSvg` 里三张视角图并排、只显示正面那张，
   可见的那只不在 SVG 盒子正中（实测偏右约 10%）。所以气泡用 `left:59%`
   而不是 `50%`，窄屏缩小后也仍然对得上头。

顺带修了一个真 bug：`.ob-pet-glow` 那圈 260×260 的光斑正好盖在菜单按钮上，
**真实鼠标点不到**（`elementFromPoint` 命中 `DIV.ob-pet-glow`），而 DOM
`.click()` 照样能触发——正是这类"DOM 全绿但点不动"的坑。加 `pointer-events:none`。

### 17.4 从「关于」进来点"跟着做一遍"，导航为什么会歪

复现路径：关于 → 重新观看介绍 → 翻到第 8 屏 → 跟着做一遍。

`#about` 是个 `.modal`（z-index 80），点「重新观看介绍」时**没有被关掉**，
一直垫在引导层下面。引导收尾时 `finish()` 只关引导层，于是 driver.js 开始教学时：

- 遮罩画在最上层（driver 的 z-index 比 modal 高），把关于弹窗一起调暗
- 浮层却按 `#guide` / `#pg-detect` 这些**底下控件**的坐标定位

结果就是浮层指着一个被弹窗盖住的位置，看起来"导航歪了"。

修法：开始教学前先收掉所有还开着的弹窗。这里踩了个二次坑——第一版写的是
`qa('.modal.show')`，而 `qa()` 的定义是 `OB.querySelectorAll(...)`，
**作用域在引导层内部**，永远查不到主页面上的 `.modal`。改成 `closeModals()`
用 `document` 查，并且在 `EKTour.start()` 和按钮处理里各调一次
（前者兜住"别处调用教学"的情况）。

### 17.5 04 的确认卡从浮层改成占位

原来确认卡是 `position:absolute; right:12px; bottom:62px` 浮在对话上方，
弹出来正好盖住刚打完的那句回答——而"回答 + 卡片"本来就是一条消息的两半。
改成排在输入框上面的普通块级元素，出现时把面板撑高一点，谁也不盖谁。

### 17.6 03 补了一行"会写什么"

删掉假进度之后这一屏信息偏薄，于是给每步补一行**副作用**——
用户真正担心的是"这一步会不会动我的机器"：

| 步骤 | 会写 |
| --- | --- |
| 环境安装 | `C:\EnvKit\tools` 下的解压目录 · 当前用户的 PATH 与 GOPROXY |
| 程序配置 | `config.json`（原子写入，旧配置留快照）· 目标库的表结构 |
| 链端校验 | **不写任何东西**：只读端口、证书与区块高度 |
| 程序启动 | 两个子进程 · 按日轮转的日志文件 · 进程树登记 |

纯只读的那一步标签写「只读」而不是「会写」，用品牌色；其余用中性标签。
它和 07 屏的权限阶梯是同一件事的两个视角：07 说"什么权限能做什么"，
这里说"每一步实际会落下什么"。

### 17.7 门禁变化

| 装置 | v6 | v7 |
| --- | --- | --- |
| `tools/cdp_onboard_test.js` | 77 断言 | **100**：`detail-merge-pick` / `-pick-sticky` / `-pick-off`（点节点→挑窗口，再点取消）、`no-fake-buttons`（两个假按钮必须不存在）、`detail-step-nowrite-check`（`.done` 恒为 0）、`detail-step-writes`（纯只读那步标签必须是"只读"）、`detail-confirm-inline`（卡片在对话流里、不压住回答）、`detail-pet-bubble-think` / `-wait`（两个阶段）、`detail-pet-menu` / `detail-pet-say-line`（必须是内置台词白名单）/ `detail-pet-weather`（两步）、`tour-closes-modal` / `tour-popover-shown` / `tour-popover-inside` / `tour-closed` |
| `tools/onboard_mutate.py` | M1–M7 | **M1–M11**：M8「关弹窗查错作用域」→ `tour-closes-modal` 必红；M9「光斑挡住桌宠菜单」→ `mouse-hit-pet-say` 必红；M10「自编桌宠台词」→ `detail-pet-say-line` 必红；M11「加回打勾」→ `detail-step-nowrite-check` 必红 |

M9 的变异锚点直接改 `pointer-events:none` 本身——在它前面插 `auto` 会被同一条
规则里的 `none` 盖掉，等于没变异。
