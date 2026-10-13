# draw-io-gomcp

一个用 Go 编写的本地、零依赖 [Draw.io](https://www.draw.io/)（diagrams.net）MCP 服务器。

`draw-io-gomcp` 是一个 [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) 服务器，它将
draw.io 的绘图能力开放给任何兼容 MCP 的 AI 客户端。它完全使用纯 Go 实现，是一个**本地 CLI 应用**，
通过 **stdio** 传输层进行通信，因此只需单个二进制文件即可运行在 **Windows、macOS 和 Linux** 上，
无任何额外的运行时依赖。

本项目是 [next-ai-draw-io](https://github.com/next-ai/next-ai-draw-io) 的 **Go 语言移植版**，
基于其 **v0.5** 版本转换而来。它保留了同样简洁的工具接口，但完全使用 Go 从零重构，
今后将**独立演进**。

## 为什么选择 draw-io-gomcp？

| 特性 | 优势 |
| --- | --- |
| 🖥️ **本地运行** | 无云端、无服务器、无需账号。你的图表永远不会离开你的机器。 |
| 🔌 **兼容任何 MCP 客户端** | 可连接 WorkBuddy、Claude、Cursor 或任何其他支持 MCP 的客户端。 |
| 📦 **无需额外环境** | 仅有一个小巧的 CLI 二进制文件。无需 Node.js、Python 或浏览器自动化。 |
| ⚡ **小巧高效** | 单个静态二进制文件，占用极小，启动迅速。 |
| 👁️ **实时预览与重新编辑** | 内置预览功能，可实时查看渲染后的图表，并可再次编辑。 |

## 功能特性

- **MCP stdio 服务器** — 通过标准输入/输出使用 MCP 协议，可接入任何客户端。
- **图表生成** — 让你的 AI 助手根据自然语言描述创建 draw.io 图表。
- **实时预览** — 结果可立即预览，并可通过后续对话不断优化图表。
- **跨平台** — 提供 Windows、macOS（Intel 和 Apple Silicon）以及 Linux 的预编译二进制文件。
- **配置极简** — 只需极少的配置即可开始使用，无需复杂的安装设置。

## 安装

从 [Releases](../../releases) 页面下载适合你平台的预编译二进制文件，或者自行编译：

```bash
# 从源码编译（需要 Go 1.22+）
git clone https://github.com/aosfather/draw-io-gomcp.git
cd draw-io-gomcp
go build -o draw-io-gomcp ./cmd/server

# 运行
./draw-io-gomcp
```

## 配置

`draw-io-gomcp` 只需要少量配置即可工作。推荐的方式是将其注册为客户端配置文件中的
MCP 服务器，并将 `command` 指向该二进制文件。

### 示例：WorkBuddy

将以下条目添加到你的 WorkBuddy MCP 配置（`~/.workbuddy/mcp.json`）中：

```json
{
  "mcpServers": {
    "draw-io": {
      "command": "/absolute/path/to/draw-io-gomcp",
      "args": [],
      "env": {}
    }
  }
}
```

> 请将 `/absolute/path/to/draw-io-gomcp` 替换为你机器上该二进制文件的实际路径。

### 示例：其他 MCP 客户端（Claude Desktop、Cursor 等）

任何支持 `stdio` 传输层的客户端都可以使用相同的配置格式：

```json
{
  "mcpServers": {
    "draw-io": {
      "command": "/absolute/path/to/draw-io-gomcp"
    }
  }
}
```

添加配置后，在客户端中**信任 / 启用**该服务器，然后开启一个新会话。
draw.io 工具就会自动出现。

## 使用方法

连接成功后，只需让你的 AI 助手为你绘制图表即可，例如：

- *"绘制一个登录流程的时序图。"*
- *"为博客系统创建一个类图。"*
- *"制作一个展示前端、API 和数据库的架构图。"*

AI 助手会生成图表，你可以**实时预览**，并提出修改意见
（例如"把数据库的框改成红色"、"添加一条错误处理路径"等）。

## 工作原理

```
┌──────────────┐   stdio (MCP)   ┌────────────────────┐   draw.io   ┌──────────────┐
│ AI 客户端     │ ───────────────▶│ draw-io-gomcp     │ ───────────▶│ 图表 /       │
│ (WorkBuddy)  │ ◀───────────────│ (Go, 本地 CLI)    │ ◀───────────│ 预览         │
└──────────────┘   JSON-RPC      └────────────────────┘   .drawio   └──────────────┘
```

1. AI 客户端通过 MCP stdio 传输层发送工具调用。
2. `draw-io-gomcp` 在本地处理请求，并生成 draw.io 图表源文件。
3. 结果返回给客户端，并在实时预览中渲染，供查看和再次编辑。

## 移植说明与路线图

本项目是 [`next-ai-draw-io`](https://github.com/next-ai/next-ai-draw-io) 的 Go 语言移植版，
基于其 **v0.5** 版本转换而来。Go 版本具有相同的工具接口，但由本仓库独立维护，并将
**独立发展演进**，后续改进包括：

- 更丰富的图表类型和模板
- 更好的预览 / 编辑体验
- 更多 MCP 能力与工具
