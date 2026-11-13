# 发布 draw-io-gomcp 到 Agensi.io 的操作指南

本目录是一个 **Agensi Skill 包**(不是 MCP server 本身)。Agensi 的 Skill 是"给 AI 智能体的指令文件",
通过 Agensi MCP 分发给买家的 agent,在 agent 内运行、不下载原始文件。
`draw-io-gomcp` 是一个需要下载二进制并配置的独立 MCP server,因此本 Skill 的形态是
**"安装 + 配置 + 使用 draw-io-gomcp 的指南"**——这正是它能作为 Agensi Skill 交付的方式。

## 包结构(已就绪)

```
skills/draw-io-gomcp/
├── SKILL.md                       # 必需:frontmatter(name/description) + 指令体
├── scripts/install.sh              # 自动检测平台、下载/编译、写入 mcp.json
├── references/drawio-tools.md      # 14 个工具说明 + 提示词示例
├── references/routing-index.md     # 由 capgen 从 capabilities.json 生成的路由表
└── PUBLISH.md                      # 本文件
```

## 发布步骤

1. **准备账号**
   - 访问 https://www.agensi.io/sell 注册为创作者(Agensi 要求创作者经过 vetting / 验证)。
   - 用 GitHub / 身份完成验证,成为 "Verified creator"。

2. **打包**
   - 先回到 server 仓库根目录刷新路由表:`go run ./cmd/capgen -skill skills/draw-io-gomcp/references/routing-index.md`
     (`go test ./...` 会校验它与 `internal/capabilities/capabilities.json` 是否同步)。
   - 把整个 `skills/draw-io-gomcp/` 目录压缩成 ZIP(确保 `SKILL.md` 在根目录,`name` 字段与目录名一致)。
   - 在 macOS/Linux 上对脚本加可执行位:`chmod +x scripts/install.sh`(上传前)。

3. **上传与填写**
   - 在创作者后台上传 ZIP。
   - 填写 listing:标题、简介、分类(建议选 Developer Tools / Diagramming / Productivity)。
   - 设置价格:一次性买断(如 $9 / $12)或订阅制;平台抽成 30%,创作者得净价 70%,
     结算走 Stripe 或 Solana USDC(通过 Zoneless)。

4. **审核**
   - 提交后自动走 **8 点安全扫描 + AI 审查 + 人工复核**,通常 24–48 小时。
   - 重点会被检查:危险命令、prompt injection、数据外泄。`install.sh` 只做下载/编译/写配置,
     无外联敏感数据,应可顺利通过。

5. **上线与分发**
   - 审核通过后, Skill 出现在商店,并经由 Agensi MCP 直达已连接的 agent。
   - 买家 agent 发现 Skill → 请求批准购买 → 加载 `SKILL.md` 指令 → 运行 `scripts/install.sh` 安装 server。
   - 创作者后台(Creator Studio)可看漏斗数据:浏览、加购、转化、收入、使用次数、评分。

## 注意事项 / 风险

- **形态差异**:Agensi Skill 不能直接"包含" Go 二进制并运行它。本方案让 Skill 在 agent 内
  引导安装独立 MCP server,这是合规且最自然的封装方式。
- **版本演进**:`install.sh` 从 `aosfather/draw-io-gomcp` 的 **latest release** 拉取,Skill 本身无需随 server 版本频繁更新;
  若 server 工具接口大改,再同步更新 `references/drawio-tools.md`。
- **中心化抽成**:Agensi 抽 30%。若你长期做的是去中心化分发(easymcp-registry 路线),可把 Agensi 当作
  "别人如何定价/包装 Skill"的参考样本,而非唯一分发渠道。
- **税务/退款**:Agensi 作为卖方处理买家侧的税、发票与退款,创作者无需在买家所在国注册税务。

## 参考

- 仓库:https://github.com/aosfather/draw-io-gomcp
- Agensi 创作者页:https://www.agensi.io/sell
- SKILL.md 规范:https://www.agensi.io/learn/skill-md-format-reference
