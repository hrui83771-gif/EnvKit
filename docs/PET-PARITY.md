# 大肥鱼桌宠：原版 vs EnvKit 功能对照

> 依据：原仓库 `1190fasheqi/dafeiyu-pet` @ main（`桌宠.py` 38.5 KB、README）逐项读源码；
> EnvKit 侧对照 `web/src/app.js` 桌宠模块与 `web/src/index.tpl.html` 的 `#pet-ctx` 菜单。
> 结论均可源码复核，不含推测。

## 一、已还原（做得一致或更好）

| 原版功能 | 原版实现 | EnvKit 现状 |
|---|---|---|
| 三视图（正/侧/背） | `sprites/` 三种视图 × 三档尺寸 | ✅ 全部内嵌 base64，交叉淡化 0.16s |
| 左右走自动镜像 | `_set_dir(left/right)` | ✅ `setFacing()` 镜像 transform |
| 三种模式 | wander / follow / still | ✅ 右键菜单三项，`localStorage` 记忆 |
| 行走速度与惯性 | `SPEED=380`，`v += (target-v)*0.3` | ✅ `PET_SPEED=380`，`v += (target-v)*min(1,dt*6)` |
| 散步自动休息 | 到点休息 6-14s | ✅ 休息 8-18s |
| 呼吸 / 摇摆 / 蹦跳 / 拉伸 | `sway` / `stretch` / `jump_t` | ✅ `petSwayAct` / `petStretch` / `petHop` |
| 空闲动作概率 | 1%/tick，35% 跳 / 25% 摇 / 20% 伸 | ✅ 同构实现 |
| 拖拽侧身 + 松手说话 | 松手 50% 说 DRAG_LINES | ✅ 松手 50% |
| 双击喂食 5 种食物 | 🐟🍰🍭🍡💎，各有台词 | ✅ 一致 |
| 单击蹦跳 + 回嘴 | 70% 跳、60% 回嘴 | ✅ 一致 |
| 台词池 5 类 | LINES/REACT/INNER/DRAG/FOOD | ✅ 五池齐全，剔除 3 句粗口 |
| 思维链心声 | 灰色斜体 + 全角括号 | ✅ `.pet-bubble.inner` |
| 说话冷却与去重 | 75 tick 冷却；同句不重复 | ✅ 有 |

## 二、原版有、EnvKit 没有（本次可补）

### 1. 鼠标穿透 ✅ 已补
原版：`set_passthrough()` 设 `WA_TransparentForMouseEvents`，让鱼"点不到"，
穿过它点到底下的窗口；托盘可解除。中途退出程序 `passthrough` 状态会恢复。

EnvKit 实现：`#pet.passthrough{pointer-events:none}` + 右键菜单切换。
**不持久化**（不写 localStorage）——EnvKit 的鱼活在 DOM 里，Go 侧够不到它，
一旦"穿透 + 状态被记住"，用户下次打开页面就再也点不到它、也无处解除。
刷新页面即恢复，这是刻意的取舍。

### 2. 大小三档 ✅ 已补
原版：`SIZE_LEVELS = {"小":0.55, "中":0.7, "大":0.9}`，以 340px 为基准缩放。
EnvKit 实现：`--pet-scale`（0.78 / 1 / 1.28）作用在内层 `#petSvg`，
不碰 `#pet` 自己的 transform（`.min` 还要用）。`localStorage` 记忆。

### 3. 开机自启
原版：`set_autostart()` 写注册表 `HKCU\...\Run`，菜单可勾选。
EnvKit 缺：没有。（可按 `internal/platform/shortcut_windows.go` 同样的纯 syscall 方式写 Run 键。）

### 4. 窗口置顶
原版：`set_topmost()` 切换 `StayOnTop`。
EnvKit：**不适用也无需补**。EnvKit 的桌宠活在浏览器页面里，不是独立置顶窗口；
页面本身就在最前，没有"被别的窗口盖住"的问题。

### 5. 查看天气 ✅ 已补（可选，默认关闭）
原版：`wttr.in` 取当前城市天气，气泡播报。

EnvKit 实现要点（与"不外连"取向的折中）：
- **默认关闭**。`config.json` 的 `pet.weather_enabled` 为 `false`，
  开关在「个人中心 → 桌宠」，需要用户显式打开；关闭时 `/api/pet/weather`
  直接返回 `{"enabled":false}`，**一个字节都不外发**（有单测钉住）。
- 前端不直连 wttr.in，统一走 `GET /api/pet/weather`，由 Go 侧控制超时与出口。
- 开关/城市走独立接口 `GET|POST /api/pet/config`，不复用 `POST /api/config`
  （那个是"整表单替换 + 硬校验"，拿它存一个开关会把其它字段一起提交）。
- `weather_enabled` 用 `*bool`：零值 `false` 无法区分"要关"和"没传"，
  用 `bool` 会让"只改城市"的保存把天气静默关掉（AI 段踩过同样的坑）。

**实测踩到的两个坑**（都已修，见 `weather_test.go`）：
1. wttr.in 的 `lang_zh` / `weatherDesc` 形状是 `[{"value":"…"}]` 而**不是字符串**。
   第一版声明成 `string`，`go vet` 与全量单测全绿（没有用例碰它），
   上线后每次查天气都报"天气数据格式异常"。
2. wttr.in 虽然接受 `lang=zh`，但 `lang_zh` 返回的仍是英文（与 `weatherDesc`
   逐字相同），**它并没有中文数据**。所以界面上的中文是靠本机词表
   `translateWeather()` 翻的（只读、不外连）。

### 6. 设置 Key / AI 对话面板
原版：独立聊天面板 + 自己的 DeepSeek Key。
EnvKit：已有更完整的 AI 抽屉（多轮记忆、工具调用、确认卡），**折算为已覆盖**。

### 7. 回到屏幕内（snap_into_screen）
原版：被拖到屏幕外时一键拉回。
EnvKit：`petClampX` 每帧夹取，**拖不出屏幕，天然不需要**。但"归位"已有。

### 8. 「缩成小圆点」已移除
EnvKit 早期有过一个 `.dot` 态（缩成一个小点，点一下恢复）。
按用户要求**彻底删除**：菜单项、JS 逻辑、两条废译文案，以及 `app.css`
里残留的三条 `#pet.dot` 规则（最后一处死 CSS 在 2026-10-26 复审时清掉）。
保留的是「收起一会儿」——它只是隐藏一段时间，悬停右下角即可找回，
不会出现"找不到它"的情况。原版也没有 dot 态。

## 三、EnvKit 独有（原版没有）

进度环、确认徽标（悬停显示任务名/耗时）、拖入 `.sql` 触发备份、
与 9 种状态联动的表情、AI 分析最近错误、连击彩蛋（5 回嘴 / 10 撒花 / 15 喷水）。

## 四、建议的落地顺序

1. ~~鼠标穿透~~ ✅ v3.2 已补
2. ~~大小三档~~ ✅ v3.2 已补
3. **开机自启** —— 纯注册表写入，与已实现的桌面快捷方式同源（`HKCU\...\Run`）。
4. ~~天气~~ ✅ v3.2 已补（可选、默认关闭）
