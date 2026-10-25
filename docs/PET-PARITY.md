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

### 1. 鼠标穿透（最高优先级）
原版：`set_passthrough()` 设 `WA_TransparentForMouseEvents`，让鱼"点不到"，
穿过它点到底下的窗口；托盘可解除。中途退出程序 `passthrough` 状态会恢复。

EnvKit 缺：**完全没有**。桌宠锚在右下角，走位时确实会挡住控制台里的按钮。
实现思路：CSS `pointer-events:none` + 右键菜单切换 + 托盘兜底解除
（一旦穿透，网页里就点不到它了，必须有网页外的解除入口，否则死锁）。

### 2. 大小三档
原版：`SIZE_LEVELS = {"小":0.55, "中":0.7, "大":0.9}`，以 340px 为基准缩放。
EnvKit 缺：固定 `PET_W=140, PET_H=150`，没有缩放。
注意：原仓库 `sprites/` 给了 `_187 / _238 / _306` 三套预渲染尺寸
（238 正好对应 0.7 档）。用 CSS `transform:scale()` 即可，无需再嵌 12 张图。

### 3. 开机自启
原版：`set_autostart()` 写注册表 `HKCU\...\Run`，菜单可勾选。
EnvKit 缺：没有。（可按 `shortcut_windows.go` 同样的纯 syscall 方式写 Run 键。）

### 4. 窗口置顶
原版：`set_topmost()` 切换 `StayOnTop`。
EnvKit：**不适用也无需补**。EnvKit 的桌宠活在浏览器页面里，不是独立置顶窗口；
页面本身就在最前，没有"被别的窗口盖住"的问题。

### 5. 查看天气
原版：`wttr.in` 取当前城市天气，气泡播报。
EnvKit：**不建议补**。会让一个本地工具每次点击都外连第三方站点，
与"不外连、无 CDN"的现有取向冲突。若真要，应改为可选且默认关闭。

### 6. 设置 Key / AI 对话面板
原版：独立聊天面板 + 自己的 DeepSeek Key。
EnvKit：已有更完整的 AI 抽屉（多轮记忆、工具调用、确认卡），**折算为已覆盖**。

### 7. 回到屏幕内（snap_into_screen）
原版：被拖到屏幕外时一键拉回。
EnvKit：`petClampX` 每帧夹取，**拖不出屏幕，天然不需要**。但"归位"已有。

## 三、EnvKit 独有（原版没有）

进度环、确认徽标（悬停显示任务名/耗时）、拖入 `.sql` 触发备份、
与 9 种状态联动的表情、AI 分析最近错误、连击彩蛋（5 回嘴 / 10 撒花 / 15 喷水）。

## 四、建议的落地顺序

1. **鼠标穿透** —— 唯一真正影响使用的（挡住按钮），且需要托盘兜底出口。
2. **大小三档** —— 纯 CSS scale，成本最低。
3. **开机自启** —— 纯注册表写入，与已有的快捷方式实现同源。
4. 天气 —— 建议不做，或做成默认关闭的可选项。
