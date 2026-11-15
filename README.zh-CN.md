# ModList Dashboard（模组看板）

一个本地运行的 Minecraft 个人模组管理看板：模组元数据、Logo、维护难度与版本支持矩阵保存在 SQLite 中；首页展示 CurseForge/GitHub 统计数据；内置可选的 AI Agent 面板（聊天 + MCP 工具）。

完整文档（功能、技术栈、数据存储结构）：<https://docs.iafenvoy.com/docs/tool/dashboard>

## 功能

- 模组库管理：编辑元数据、上传 Logo、标签、架构、许可证、各版本加载器支持矩阵
- 首页卡片：状态/加载器标签、CurseForge 下载量、GitHub Issues/PRs
- 依据 Codacy 分数与版本/架构规则自动计算维护难度
- 登录后的管理菜单：编辑模组、重载本地配置、清理各数据源缓存、账户设置
- 可选 AI 助手面板：聊天、文件读写、外部 MCP 服务器
- 可选自动预处理 Issue：分析真实性、代码依据和解决方案，仅生成报告，不修改代码
- 可选 Bot 通知：推送新增 Issue 和预处理报告，首个适配器为 QQ 官方 BotGo
- 云盘区域：存放每个 Agent 会话的临时文件
- 中英文界面

## 目录结构

```text
config/     运行时 YAML/JSON 配置（.yml 为本地文件，不入库，见下文）
static/     前端（HTML/CSS/JS/图片/语言包）
prompts/    Agent 提示词模板（已入库）
agent/      可选 AI/MCP Agent 子系统（Go 包）
bot/        通用通知事件、持久化队列与重试（不依赖平台 SDK）
bot/qqbot/  QQ BotGo 鉴权、WebSocket 接收和消息发送适配器
data/       运行时数据，自动创建（mods.db、users.db、sessions.db、todos.json、drive/）
cache/      HTTP 缓存，自动创建
drive-mcp/  独立的 stdio MCP 服务器，向 AI 工具暴露云盘（可选）
```

## 环境要求

- Go 1.24.4+（SQLite 为纯 Go 实现，无需 CGO）
- Node.js（可选，仅当你使用 `npx` 启动的 MCP 服务器时需要）
- 拉取 GitHub/CurseForge 统计数据时需要网络（可选）

## 编译

```bash
# 在仓库根目录执行
go build -o modlist-dashboard .      # Linux / macOS
go build -o modlist-dashboard.exe .  # Windows
```

可选辅助程序（独立的云盘 MCP 服务器）：

```bash
go build -o drive-mcp/drive-mcp.exe ./drive-mcp
```

程序读取 `config/`、`static/`、`prompts/`、`data/`、`cache/` 时使用相对路径，
所以**必须在仓库根目录启动**二进制。

## 配置

所有 `config/*.yml` 都是**本地文件**（已被 .gitignore 忽略），因为它们可能包含
API 密钥。**首次启动**时程序会自动把缺失的文件从同名 `config/*.yml.example`
模板复制出来，因此全新克隆也能直接跑起来。

| 文件 | 用途 |
| --- | --- |
| `config/config.yml` | 端口、数据源开关、缓存目录、本地项目目录、Codacy 设置 |
| `config/auth.yml` | CurseForge Key、GitHub Token、Codacy Token、默认管理员 |
| `config/ai.yml` | AI 面板的模型提供商（Key 可用 `${环境变量}`） |
| `config/mcps.yml` | AI 面板的外部 MCP 服务器（默认为空） |
| `config/bots.yml` | 可选 Bot 通知开关、事件类型、平台凭据和明确的接收目标 |
| `config/rate.yml` | 各版本状态/架构的难度加权 |
| `config/editor.yml` | 编辑器里许可证/架构/标签的预填选项 |
| `config/agents.yml` | Agent 定义（角色、温度、可用工具） |
| `config/status_config.json` | 状态排序与哪些状态视为已归档 |
| `config/configuration.json` | 界面使用的颜色与 emoji |

> `config/configuration.json` 与 `config/status_config.json` 不含密钥，会随仓库
> 提交；含真实 Token 的 `*.yml` 请勿提交。

## 第一次使用

1. **先启动一次**，让程序自动生成配置：

   ```bash
   ./modlist-dashboard        # Linux/macOS
   modlist-dashboard.exe      # Windows
   ```

   日志会提示 `首次启动：已从模板生成 config/auth.yml` 之类。

2. 停掉服务，编辑生成的配置：

   - `config/auth.yml`：填入 CurseForge Key、GitHub Token、Codacy Token，并设置
     一个强管理员密码。
   - `config/config.yml`：如需要可修改端口（默认 `3456`）。
   - `config/ai.yml`：想用 AI 面板就配置一个可用的模型提供商。
   - `config/mcps.yml`：想用外部 MCP 服务器就在这里添加。

   > 默认管理员只在 `data/users.db` 为空时按 `auth.yml` 创建。如果服务已经跑过，
   > 请直接在网页里改密码（管理 → 账户设置），而不是改 `auth.yml`。

3. 再次启动，浏览器打开 <http://localhost:3456>（或你配置的端口）。

4. 用管理员账号登录后，先到 **管理 → 账户设置** 修改默认密码。

5. 添加第一个模组：**管理 → 编辑模组 → 新建**，填写必填项（ID、名称、简介等）后
   保存；还可以上传 Logo、设置状态/架构/标签，并维护各版本的加载器支持矩阵。
   首页列表会从 `data/mods.db` 自动更新。

## 自动预处理 Issue

登录后，在 **Agent → 设置** 中打开 **自动预处理 Issue**。开关默认关闭，读取、修改设置和查看处理状态均需要有效的登录凭据。开启后按 `server.cache_ttl_minutes` 和各仓库当前缓存的到期时间刷新 GitHub 列表；成功拉取后的差量事件才会将新增且仍为 Open、项目未归档的 Issue 加入派发队列，PR 不参与。登录后手动清除 **GitHub 缓存** 或 **全部缓存** 会立即触发重新拉取，使用相同的增量规则；不会补扫历史 Issue，也不会重跑已有报告。派发与手动派发一样创建 AI 会话、附带 Issue 正文并启动助手；后台顺序处理任务，模型分析不会阻塞列表的 TTL 刷新。关闭后不再启动新任务，当前任务会完成。

**每次重启后，各仓库首次成功从 GitHub 拉取的列表只校准基线，不触发预处理；缓存命中和失败请求不会消耗这个首次保护。** 即使磁盘已有缓存或持久化基线也遵循此规则，因此停机期间新增、已出现在首次列表中的 Issue 也只记入基线。从下一次成功拉取开始识别新增项。清除缓存不会重置基线、首次保护或已有处理记录。

预处理使用 `config/ai.yml` 的默认模型、`auth.yml` 中对应仓库所有者的 GitHub Token，以及 `project_paths` 下已有的仓库目录（`<项目根目录>/<仓库名>`）。必须启用 GitHub 数据源；缺少本地源码会显示失败原因，不会自动克隆。内置 `issue-preprocessor` 助手只允许读取、搜索和抓取参考资料，服务端禁止其修改文件、执行 Shell、调用外部 MCP 或子 Agent，包括开启审批绕过时。

报告包含问题概述、真实性结论、证据与根因、解决方案、验证计划；证据不足时会明确说明。点击 Issue 旁的 **查看报告** 或进入 AI 对话历史查看。各仓库上次同步的 Issue 编号和待派发任务都会持久保存；正文修改、重新打开、重复刷新和重启不会重复派发旧 Issue。刷新失败不推进基线，关闭期间观察到的 Issue 只更新基线。已有待派发任务在重启后继续保留，处理器之后由新增、显式重试或配置变更事件唤醒，不由 TTL 定时器反复扫描。失败任务可在开关开启时手动重试；服务重启中断的任务会标记为失败。

开关和任务索引保存在 `data/issue-preprocess.json`，完整报告保存在 `data/sessions.db`。

## Bot 通知（可选）

已接入 `github.com/tencent-connect/botgo v0.2.1`。首次启动从 `config/bots.yml.example` 生成默认关闭的 `config/bots.yml`；不启用时不会请求 QQ API 或创建通知队列。修改配置后需重启服务，管理菜单的配置重载不重启 Bot 模块。

要启用 QQ 通知，在本地 `config/bots.yml` 中配置：

```yaml
enabled: true
notify_new_issues: true
notify_reports: true
dashboard_url: "https://dashboard.example.com" # 可选，报告会话仍需登录
providers:
  qq:
    enabled: true
    app_id: "${QQBOT_APP_ID}"
    app_secret: "${QQBOT_APP_SECRET}"
    sandbox: false
targets: [] # 群聊可在网站生成绑定码，无需手动填写 OpenID
```

凭据可直接填写，也可由环境变量提供。仍可在 `targets` 手动指定固定目标，`type` 支持 `channel`（频道子频道）、`group`（群聊）和 `c2c`（单聊）；群聊/单聊使用开放平台的 OpenID，不能填写 QQ 群号或 QQ 号。需在 QQ 开放平台配置服务器出口 IP 白名单、相应接口权限与沙箱成员。

### 网站生成绑定码，在群里绑定

1. 启用以上配置并重启。在 QQ 开放平台启用机器人的群聊能力，使用 **WebSocket** 接收事件；服务启动后通过 BotGo 获取凭证和网关，建立出站长连接并自动订阅群聊 @机器人消息事件 `GROUP_AT_MESSAGE_CREATE`（`1 << 25`）。服务器需要能访问 QQ 的 HTTPS 和 WSS 接口，无需配置公网回调地址或开放回调端口。
2. 登录网站，打开「Agent → 设置」中的「QQ 群通知绑定」，点击「生成绑定码」。在目标群中 @机器人 并发送完整的 `/bind 绑定码`。绑定码有效期 10 分钟、仅限一次使用，重新生成会作废旧码；绑定码只应发到要接收通知的群。
3. 服务端从已鉴权的 QQ WebSocket 会话消息中取得 `group_openid` 并保存绑定，不需要手动找 OpenID。机器人会回复绑定结果：不带码的 `/bind` 提示用法，失效码或重复绑定说明原因，其他群内 @ 消息回复功能说明。网站每 5 秒更新绑定列表；即使回复失败，也可在网站确认结果。重复或失效码不产生绑定。生成、查看和解绑均需登录，账号只能查看和解绑自己的绑定；已经绑定的群不能被另一账号接管。
4. **群主**使用新版手机 QQ，在群成员列表选择机器人，进入它的**群消息设置**并允许主动推送；这不是通用的「机器人权限设置」。单聊则由用户在机器人资料页开启主动消息。绑定不自动授予该权限，仍受平台配额限制；`40034105` 通常表示未允许主动消息。

新绑定只接收之后产生的通知，不补发历史报告。网站解绑立即停止后续通知并移除该群待发队列，已在发送中的消息可能仍送达。绑定持久保存，重启不需要重新绑定。固定 `targets` 由配置文件管理，网页解绑不移除它们。通用绑定码和绑定存储位于 `bot`，QQ 鉴权、WebSocket 会话和消息处理位于 `bot/qqbot`；新增平台可复用同一套绑定逻辑。

群内指令反馈使用消息 API 被动回复，携带事件中的原始 `msg_id` 和 `msg_seq: 1`，不要求先开启主动推送。发送失败最多尝试 3 次，同一回复重试沿用原序号；短时间内的重复事件不会重复处理绑定。新增 Issue 和报告仍使用主动消息，需要群主授权。

启动即获取登录凭证，记录 `[QQBot] 登录成功` / `登录失败`，收到 `READY` 后记录 WebSocket 接入成功；凭证刷新、连接中断和 `RESUMED` 会话恢复也有日志。连接按 QQ 下发的周期发送心跳，心跳超时或断线后退避重连，优先通过会话 ID 和事件序号续传；会话失效则重新鉴权，权限、分片或封禁等不可重试错误会停止接入并记录错误码。群消息处理与心跳独立，回复拥塞不会阻塞心跳，服务退出时取消连接和回复任务。

日志不输出 AppSecret、Token、绑定指令或消息正文，正常的缓存凭证查找不会反复刷登录成功日志。一次性码仅保存哈希。若群内仍提示服务异常，请检查 `[Bot] QQ 群消息回复失败` 中的 QQ 错误码和 traceID。原 `/api/bot/qq/webhook` 路由已移除，请停用开放平台中旧的 Webhook 配置。

参考：[QQ WebSocket 接入](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/event-emit/websocket.html)、[群聊消息事件](https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_at_message_create.html)、[主动消息常见问题](https://bot.q.qq.com/wiki/bot_FAQ/)。

新增 Issue 通知复用当前 GitHub 缓存 TTL 和手动清除缓存的刷新事件，独立于自动预处理开关；须启用 GitHub 数据源和对应 GitHub Token。每次重启后各仓库首次成功拉取只校准通知基线，读取磁盘缓存不能代替首次拉取。之后只推送新增且仍开放的 Issue，不推送 PR、历史 Issue、编辑或重新打开的旧 Issue。预处理成功后推送最终报告正文，长报告分段发送；附件原文、工具输出与推理过程不会进入消息。报告只发送到明确配置或绑定的目标，不会创建公开分享链接。

`data/bot-notifications.json` 保存绑定、绑定码哈希、通知基线、已入队的报告会话和各目标的发送进度。后台串行发送，失败以 10 秒至 320 秒退避重试，重启后从未发送的分段续发；移除的目标或关闭的事件类型会丢弃其待发消息，不转发给新目标。发送语义为至少一次：平台已接受而本地来不及保存时可能重复，平台配额或网络问题也可能延迟通知。

平台扩展仅需实现 `bot.Sender` 的 `Validate` / `Send`，在 `bot.go` 注册适配器及其配置，然后添加相应 `provider` 的目标。Issue 差量识别、报告事件、绑定码、存储和重试集中在通用模块；`bot/qqbot` 只包含 QQ 鉴权、事件解析和收发逻辑。

## 数据与重置

- 你创建的数据都在 `data/` 下：`mods.db`（模组与 Logo）、`users.db`（账号）、
  `sessions.db`（Agent 历史）、`todos.json`、`drive/`。
- `cache/` 存放拉取的接口数据；可在管理菜单清理对应缓存，或直接删除该目录重拉。
- 重置账号：删除 `data/users.db` 后重启，会按 `auth.yml` 重新生成管理员。
- 清空模组库：删除 `data/mods.db` 后重启。

## 备注

- 首页与模组列表公开只读；编辑功能需要登录。
- AI/MCP 面板需要 `config/ai.yml` 里至少有一个可用的模型提供商。
- 外部 `stdio` MCP 服务器需要安装 Node.js（`npx`）。
- 服务默认监听所有网卡的配置端口。
