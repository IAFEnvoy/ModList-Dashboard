# ModList Dashboard

A local web dashboard for managing a personal Minecraft mod collection. It keeps mod metadata, logos, maintenance difficulty and support-version records in SQLite, shows CurseForge/GitHub statistics on the homepage, and ships with an optional AI agent panel (chat + MCP tools).

## Features

- Mod library management: edit metadata, upload logos, tags, architecture, licenses, per-version loader support matrix
- Dashboard cards with status/tag chips, CurseForge downloads and GitHub Issues/PRs
- Auto maintenance difficulty rating driven by Codacy quality score and version/architecture rules
- Admin-only management area with local config reload and cache flush actions
- Optional built-in AI assistant panel (chat, file editing, MCP servers) powered by configurable LLM providers
- Cloud drive area for storing per-agent session files
- English and Chinese UI

## Repository layout

```text
config/      Runtime YAML/JSON configs (.yml is local-only; see below)
static/      Web UI (HTML/CSS/JS/images/locales)
prompts/     Agent prompt templates (committed)
agent/       Optional AI/MCP agent subsystem (Go package)
data/        Runtime data, created automatically (mods.db, users.db, sessions.db, todos.json, drive/)
cache/       HTTP cache, created automatically
drive-mcp/   Standalone stdio MCP server exposing the cloud drive to AI tools (optional)
```

## Requirements

- Go 1.24.4+ (pure-Go SQLite, no CGO needed)
- Node.js (optional, only if you run `npx`-based MCP servers)
- Network access to the GitHub/CurseForge APIs for live statistics (optional)

## Build

```bash
# from the repository root
go build -o modlist-dashboard .      # Linux / macOS
go build -o modlist-dashboard.exe .  # Windows
```

Optional helper (standalone drive MCP server):

```bash
go build -o drive-mcp/drive-mcp.exe ./drive-mcp
```

The binary must be started from the repository root, because the program reads
`config/`, `static/`, `prompts/`, `data/` and `cache/` relative to the working
directory.

## Configuration

All `config/*.yml` files are **local-only** (git-ignored) because they may hold
API keys. On the **first start** the program auto-generates any missing file by
copying the matching `config/*.yml.example` template, so an empty checkout still
boots.

| File | Purpose |
| --- | --- |
| `config/config.yml` | Server port, data source toggles, cache dir, local project paths, Codacy settings |
| `config/auth.yml` | CurseForge key, GitHub tokens, Codacy token, default admin account |
| `config/ai.yml` | LLM providers for the AI panel (key may use `${ENV_NAME}`) |
| `config/mcps.yml` | External MCP servers for the AI panel (empty by default) |
| `config/rate.yml` | Difficulty scoring weights per version status / architecture |
| `config/editor.yml` | Preset options for license / architecture / tags in the mod editor |
| `config/agents.yml` | Agent definitions (role, temperature, enabled tools) |
| `config/status_config.json` | Mod status order and which statuses count as archived |
| `config/configuration.json` | Colors and emoji used by the UI |

> `config/configuration.json` and `config/status_config.json` contain no secrets
> and are committed. Never commit the `*.yml` files with real tokens.

## First run

1. **Start once** to generate the config files:

   ```bash
   ./modlist-dashboard        # Linux/macOS
   modlist-dashboard.exe      # Windows
   ```

   You will see logs such as `首次启动：已从模板生成 config/auth.yml`.

2. Stop the server and edit the generated files:

   - `config/auth.yml` – fill in your CurseForge API key, GitHub tokens and
     Codacy token, and set a strong admin password.
   - `config/config.yml` – change the port (`3456` by default) if needed.
   - `config/ai.yml` – configure an LLM provider if you want to use the AI panel.
   - `config/mcps.yml` – add external MCP servers if you want them.

   > The default admin is created from `auth.yml` only when `data/users.db` is
   > empty. If the server has already run, change the password from the web UI
   > (Admin → Account settings) instead of editing `auth.yml`.

3. Start the server again and open <http://localhost:3456> (or your configured port).

4. Log in with the admin account, then immediately change the default password
   via **Admin → Account settings**.

5. Add your first mod: **Admin → Edit Mods → New** and fill in the required
   fields (ID, name, description, …). Save; you can also upload a logo image,
   set status/architecture/tags, and maintain the per-version loader support
   matrix. The homepage list updates from `data/mods.db`.

## Data and reset

- Everything you create is stored under `data/`:
  `mods.db` (mods + logos), `users.db` (accounts), `sessions.db` (agent history),
  `todos.json`, `drive/`.
- `cache/` stores fetched API data; use **Admin → cache flush** or delete the
  folder to refetch.
- To reset accounts, delete `data/users.db` and restart (a new admin is then
  seeded from `auth.yml`).
- To start with an empty mod library, delete `data/mods.db` and restart.

## Notes

- The homepage and mod list are public read-only; editing requires login.
- The AI/MCP panel needs at least one valid provider in `config/ai.yml`.
- External `stdio` MCP servers require Node.js (`npx`) to be installed.
- The server listens on all interfaces at the configured port.
