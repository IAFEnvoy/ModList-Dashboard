# ModList Dashboard（模组看板）

一个本地运行的 Minecraft 个人模组管理看板：模组元数据、Logo、维护难度与版本支持矩阵保存在 SQLite 中；首页展示 CurseForge/GitHub 统计数据；内置可选的 AI Agent 面板（聊天 + MCP 工具）。

完整文档（功能、技术栈、数据存储结构）：<https://docs.iafenvoy.com/docs/tool/dashboard>

## 功能

- 模组库管理：编辑元数据、上传 Logo、标签、架构、许可证、各版本加载器支持矩阵
- 首页卡片：状态/加载器标签、CurseForge 下载量、GitHub Issues/PRs
- 依据 Codacy 分数与版本/架构规则自动计算维护难度
- 登录后的管理菜单：编辑模组、重载本地配置、清理各数据源缓存、账户设置
- 可选 AI 助手面板：聊天、文件读写、外部 MCP 服务器
- 云盘区域：存放每个 Agent 会话的临时文件
- 中英文界面

## 目录结构

```text
config/     运行时 YAML/JSON 配置（.yml 为本地文件，不入库，见下文）
static/     前端（HTML/CSS/JS/图片/语言包）
prompts/    Agent 提示词模板（已入库）
agent/      可选 AI/MCP Agent 子系统（Go 包）
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
