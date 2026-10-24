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
| 桌宠形象（正面图） | `sprites/正面_238.png` | 以 base64 data URI 内嵌于 `web/index.html` 的 `#petSvg`（`<image id="pet-img">`），未作任何修改 |
| 互动 / 待机台词（各 5 句） | `桌宠.py` 中 `REACT_LINES` / `LINES` 精选 | `web/index.html` 中 `FISH_REACT` / `FISH_IDLE` 台词池，为适配本项目语境译为中英双语（原文见 i18n 词典 `tools/i18n_en.json`） |

未使用该仓库中任何含不当内容的台词条目。桌宠的其余交互逻辑（拖拽、喂食面板、AI 对话、天气、系统监控等）为本项目自研，与上述仓库的实现无关。

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
