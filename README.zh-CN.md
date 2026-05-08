# CodexScope

[English](README.md) | 简体中文

[![LINUX DO](https://img.shields.io/badge/LINUX-DO-FFB003?style=flat-square)](https://linux.do)

CodexScope 是一个本地优先的 Codex 用量面板，用来查看你本机 Codex 会话日志里的 Token 消耗、额度状态、会话排行、模型排行、调用分布、缓存命中率和费用估算。

![CodexScope dashboard](assets/codexscope-dashboard-24h.png)

它是一个静态 HTML 页面：没有后端、没有账号接入、没有托管遥测。真实用量数据只会导出到本地的 `data.js`，并且这个文件默认不会提交到 git。

## 为什么做

Codex 的用量如果只看零散日志，很难判断 Token 花到哪里、哪个项目最耗、哪个模型占比最高、什么时候接近额度风险。CodexScope 的目标很窄：把本地 Codex 元数据整理成一个可以直接打开的桌面面板。

## 功能

- 累计 Token 趋势，支持绝对值和对数视图
- 日期筛选：近 24 小时、今天、7 天内、30 天内、自定义区间
- 调用分布和 Token 消耗分布，用来观察峰值
- 从本地 `rate_limits` 事件读取 Codex 额度与风险状态
- 会话排行和模型排行，显示 Token 总量与调用次数
- 按官方公开美元价格表做费用估算，默认显示 USD，可切换 CNY
- 从 `~/.codex/sessions` 本地生成数据
- 桌面端优先的轻量静态前端

## 快速开始

下载项目后，直接用浏览器打开 `index.html`，会先看到内置示例数据。

如果要看自己的真实 Codex 用量，双击对应系统的启动脚本：

- **macOS**：双击 `macos/open-dashboard.command`
- **Windows**：双击 `windows/open-dashboard.cmd`

启动脚本会用 Go 版生成器先在本地生成 `data.js`，然后打开 `index.html` 显示真实数据。
后续运行会复用本地 `.codexscope-cache.json` 缓存，只重扫发生变化的会话日志，所以重复启动会快很多。

也可以手动运行：

macOS 或 Linux：

```bash
go run generate_codex_data.go
open index.html
```

Windows PowerShell：

```powershell
go run .\generate_codex_data.go
start .\index.html
```

默认读取的 Codex 日志目录：

- macOS/Linux：`~/.codex/sessions`
- Windows：`%USERPROFILE%\.codex\sessions`

如果你的 Codex 会话目录不在默认位置，可以手动指定：

```powershell
go run .\generate_codex_data.go --root "$env:USERPROFILE\.codex\sessions"
```

生成器会把真实数据写到 `index.html` 同目录下的 `data.js`。`data.js` 和 `.codexscope-cache.json` 都可能包含项目名、会话 id、时间戳、用量模式和额度状态，所以已经被 `.gitignore` 排除。

## 页面里显示什么

- **Token 消耗趋势**：所选时间范围内输入、缓存、输出、推理 Token 的累计变化。
- **额度与风险**：如果本地日志里有 rate-limit 元数据，会显示短窗口额度、周额度、缓存命中和失败风险。
- **时间分布**：按时间桶统计调用次数或 Token 消耗。
- **排行**：当前区间内最活跃的会话和模型。
- **费用统计**：基于本地 Token 数量和内置模型价格表做估算。

## 费用估算说明

费用统计只是估算，不是官方账单。它使用本地 Token 数量和内置的 OpenAI 公开美元价格表计算。实际 ChatGPT/Codex 账单、余额、订阅额度和限制状态，请以官方账号或账单页面为准。

USD 是原始计算币种。CNY 只是展示换算。能联网时，CodexScope 会通过 Frankfurter API 获取 USD/CNY 汇率，并使用 ECB 数据源；如果请求失败，会回退到内置参考汇率，并在页面标记为离线回退。

## 验证布局

响应式视觉检查使用 Playwright：

```bash
npm install
npm run verify
```

## 隐私

CodexScope 不会把数据发送到服务器。`generate_codex_data.go` 只读取本地 Codex 会话日志，并导出以下用量元数据：

- 会话 id 和工作目录 basename
- 模型名
- Token 数量
- rate limit 元数据
- 任务耗时、首 Token 延迟、失败状态

它不会导出提示词、助手回复、工具输出或文件内容。

分享截图或导出的产物前，建议先检查自己的 `data.js`。

## License

MIT
