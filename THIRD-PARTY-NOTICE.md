# 第三方资源声明（Third-Party Notices）

本文件随 EnvKit 一同分发，用于满足所使用第三方开源资源的许可声明义务。

## 大肥鱼桌宠形象与台词

- **来源**：<https://github.com/1190fasheqi/dafeiyu-pet>
- **作品**：「鲸鱼娘 · 大肥鱼」透明桌面宠物（DeepSeek V4 Pro 社区二创形象）
- **许可**：MIT License（全文见下文）
- **版权**：Copyright (c) 2026 dafeiyu-pet 作者及贡献者

### 使用内容

| 内容 | 来源文件 | 在本项目中的位置 |
|---|---|---|
| 桌宠形象三视图（正面 / 侧面 / 背面） | `sprites/正面_238.png`、`侧面_238.png`、`背面_238.png` | 以 base64 data URI 内嵌于 `web/index.html`，分别为 `#pet-img`、`#pet-img-side`、`#pet-img-back`；像素未作任何修改 |
| 台词池（5 类） | `桌宠.py` 中 `LINES` / `REACT_LINES` / `INNER_LINES` / `DRAG_LINES` / `FOOD_LINES` 精选 | `web/index.html` 中 `FISH_LINES` / `FISH_REACT` / `FISH_INNER` / `FISH_DRAG` / `FISH_FOOD`，按本项目语境译为中英双语（英文见 `tools/i18n_en.json`） |
| 交互行为设计 | `桌宠.py`（`TICK_INTERVAL` 定时器、`SIZE_LEVELS`、三种模式 wander / follow / still、拖拽侧面朝向、双击投喂、呼吸 / 摇摆 / 跳跃 / 拉伸动画） | 以行为规格为参考，在 `web/index.html` 的桌宠模块中用 DOM + CSS 重写实现（非逐行移植） |

**已剔除的内容**：原仓库 `INNER_LINES` 中语气粗俗的条目、以及「彩虹模式」彩蛋，本项目均未采用。喂食共 5 种食物，与原仓库一致；连击 15 次的彩蛋改为「鲸式喷水」，与原仓库的彩虹模式不同。

桌宠的其余交互逻辑（与 AI 抽屉联动、拖入 `.sql` 触发备份流程、进度环、确认徽标、AI 对话、天气、系统监控等）为本项目自研，与上述仓库的实现无关。

该形象为 DeepSeek 社区二创作品，与本项目及 FISCO-BCOS、WeBASE 等组件无任何隶属或背书关系。

### MIT License

```
MIT License

Copyright (c) 2026

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

> 说明：MIT 的唯一义务就是**保留上述版权声明与许可文本**——本项目通过本文件及随附分发履行，无需公开修改后的源码，也不限制商用。除本条目列明的资源外，EnvKit 依赖的其余组件（Go、Node.js、MySQL、FISCO-BCOS、WeBASE 等）的版权与许可归各自权利方所有，见 README「免责声明」。

## 前端内置库：marked / highlight.js / vConsole

AI 回复的 Markdown 渲染与代码高亮，以及手机端的调试控制台，由下面三个库完成。三者都是上游发布产物，原样内嵌（`go:embed`），未作修改，也不经由任何外网 CDN 加载——手机端 vConsole 仅在访问地址带 `?debug` 时，由页面动态请求同域文件 `/vendor/vconsole.min.js`。

| 资源 | 版本 | 许可 | 在本项目中的位置 |
|---|---|---|---|
| marked | 12.0.2 | MIT | `web/vendor/marked.min.js` |
| highlight.js | 11.9.0 | BSD-3-Clause | `web/vendor/highlight.min.js` |
| vConsole | 3.15.1 | MIT | `web/vendor/vconsole.min.js` |

### MIT License（marked）

Copyright (c) 2018+, MarkedJS (<https://github.com/markedjs/marked>)
Copyright (c) 2011-2018, Christopher Jeffrey (<https://github.com/chjj/>)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### BSD-3-Clause License（highlight.js）

Copyright (c) 2006, Ivan Sagalaev. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

### MIT License（vConsole）

Copyright (C) 2017 THL A29 Limited, a Tencent company. All rights reserved.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

> `vconsole.min.js` 由上游 bundle 构建，内含 core-js 的 polyfill 代码
> （MIT，Copyright © 2014-2022 Denis Pushkarev）。该部分随 `vconsole.min.js`
> 原样分发，未作修改。

## Go 依赖：golang.org/x/sync

`verify_environment` 的链端探针并发使用 `golang.org/x/sync/errgroup`。

- **许可**：BSD-3-Clause，Copyright 2009 The Go Authors（条款同上）。

## PDF 解析：github.com/ledongthuc/pdf

拖进 AI 对话区的 `.pdf` 由它抽取文字层（`ai_extract.go` 的 `aiExtractPDF`）。

- **许可**：BSD-3-Clause，Copyright (c) 2009 The Go Authors（该库是 `rsc.io/pdf` 的
  持续维护分支，沿用原始版权声明；条款全文见上文「BSD-3-Clause License（highlight.js）」）
- **位置**：编译进 `EnvKit.exe`（Go 模块依赖，纯 Go 实现，无 cgo）
- **无额外传递依赖**：`go.sum` 里只有它自己
- **说明**：仅用于**读取**文字层。扫描件（整页为图片、无文字层）抽不出内容，
  此时 EnvKit 会明确提示改用页面截图；加密 PDF 需先在阅读器里另存为不加密副本。
  本库不执行 PDF 内的任何脚本或动作。

## 托盘库：github.com/getlantern/systray

托盘图标与右键菜单（打开控制台 / 程序配置 / 打开日志目录 / 隐藏窗口到托盘 / 退出）。

- **许可**：Apache License 2.0，全文见仓库内 `LICENSES/Apache-2.0.txt`
- **位置**：编译进 `EnvKit.exe`（Go 模块依赖），经 `internal/platform/tray_windows.go` 调用
- **版权**：Copyright 2014 The go-systray Authors
- **传递依赖**：`getlantern/context`、`getlantern/errors`、`getlantern/golog`、
  `getlantern/hex`、`getlantern/hidden`、`getlantern/ops`、`oxtoacart/bpool` 均为
  Apache-2.0；`go-stack/stack` 为 MIT。

## 首次引导 / 产品导览：Swiper / Anime.js / driver.js

首次运行的品牌片头与 8 屏产品导览、以及收尾衔接的 4 步操作教学，由下面三个库完成。三者都是上游官方 UMD / IIFE 发布产物，**原样内嵌、未作任何修改**，走 `go:embed` + 同域 `/vendor/…` 路由加载，不经由任何外网 CDN；页面 CSP `default-src 'self'` 保持不变。锁版本后记录的 SHA256 见下表，升级时须重跑前端全门禁。

| 资源 | 版本 | 许可 | 在本项目中的位置 | SHA256 |
|---|---|---|---|---|
| Swiper（`swiper-bundle.min.js`） | 12.2.0 | MIT | `web/vendor/swiper-bundle.min.js` | `1e313555b04cb9b5273b7a817158708a05f58e18f91d97a4239dab6f68d23c4a` |
| Swiper（`swiper-bundle.min.css`） | 12.2.0 | MIT | `web/vendor/swiper-bundle.min.css` | `95482d9f6501bcbcb3fb9309e776cf547d3553ed4dbb95c595223cf876277684` |
| Anime.js UMD bundle | 4.5.0 | MIT | `web/vendor/anime.umd.min.js` | `8d5b3a58a1f64023a04a4cedeef135a2263b18da04b35177ac13438a0bad033b` |
| driver.js IIFE | 1.10.2 | MIT | `web/vendor/driver.js.iife.js` | `541520f33f84cc5cf6696b421f705cc9efe2d72656b3b5cd7f72c22e6d7e17c0` |
| driver.js 样式 | 1.10.2 | MIT | `web/vendor/driver.css` | `ff1934a5a7ef3f50deb9266ba8945b3eb407ee10b97da12feda4b7c10055bf12` |

> 三者 CSS 均为 `.swiper*` / `.driver*` 前缀类名，不含全局 reset 或元素级规则；引入前已核对，不会污染主界面样式。

### MIT License（Swiper）

Copyright (c) 2019 Vladimir Kharlampidi

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### MIT License（Anime.js）

Copyright (c) 2025 Julian Garnier

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### MIT License（driver.js）

Copyright (c) Kamran Ahmed

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

