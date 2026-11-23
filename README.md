# ModList Dashboard

A local web dashboard for managing a personal Minecraft mod collection. It keeps mod metadata, logos, maintenance difficulty and support-version records in SQLite, shows CurseForge/GitHub statistics on the homepage, and ships with an optional AI agent panel (chat + MCP tools).

Full documentation (features, tech stack, storage layout): <https://docs.iafenvoy.com/docs/tool/dashboard>

## Features

- Mod library management: edit metadata, upload logos, tags, architecture, licenses, per-version loader support matrix
- Dashboard cards with status/tag chips, CurseForge downloads and GitHub Issues/PRs
- Auto maintenance difficulty rating driven by Codacy quality score and version/architecture rules
- Admin-only management area with local config reload and cache flush actions
- Optional built-in AI assistant panel (chat, file editing, MCP servers) powered by configurable LLM providers
- Optional automatic issue preprocessing: assess validity and code evidence, then report proposed solutions without changing code
- Optional bot notifications for new issues and preprocessing reports, with an official QQ BotGo adapter
- Cloud drive area for storing per-agent session files
- SVG badges for embedding in mod READMEs (downloads, issues/PRs, license, loaders, Discord, docs)
- English and Chinese UI

## Repository layout

```text
config/      Runtime YAML/JSON configs (.yml is local-only; see below)
static/      Web UI (HTML/CSS/JS/images/locales)
prompts/     Agent prompt templates (committed)
agent/       Optional AI/MCP agent subsystem (Go package)
bot/         Platform-independent notification events, durable queue and retries
bot/qqbot/   QQ BotGo authentication, WebSocket receiver and message sender adapter
badge/       Badge subsystem (own package: SVG templates, data sources, embedded assets)
scripts/     Helper scripts (badge asset sync)
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
| `config/config.yml` | Server port, data source toggles, cache dir, local project paths, Codacy settings, badge Discord invite |
| `config/auth.yml` | CurseForge key, GitHub tokens, Codacy token, default admin account |
| `config/ai.yml` | LLM providers for the AI panel (key may use `${ENV_NAME}`) |
| `config/mcps.yml` | External MCP servers for the AI panel (empty by default) |
| `config/bots.yml` | Optional bot toggles, notification types, provider credentials and explicit recipients |
| `config/rate.yml` | Difficulty scoring weights per version status / architecture |
| `config/editor.yml` | Preset options for architecture / tags in the mod editor |
| `data/license/` | License texts: **file name = license name** (e.g. `MIT`, `Apache-2.0`). Drives the editor's license picker and the top-bar "License Download" menu |
| `config/agents.yml` | Agent definitions (role, temperature, enabled tools) |
| `config/status_config.json` | Mod status order and which statuses count as archived |
| `config/configuration.json` | Colors and emoji used by the UI |

> `config/configuration.json` and `config/status_config.json` contain no secrets
> and are committed. Never commit the `*.yml` files with real tokens.

## Badges

Public SVG badges (no login required) for embedding in mod READMEs. `:slug` is a
mod id from the library; an unknown slug returns `404`. Stats come from the
existing cache — a stale entry is served immediately and only that single entry
is refreshed in the background.

| URL | Shows |
| --- | --- |
| `/api/badge/curseforge/downloads/:slug` | CurseForge downloads |
| `/api/badge/github/downloads/:slug` | GitHub release asset downloads |
| `/api/badge/github/issues/:slug` | Issues by state: Open / Done / Closed (not planned is folded into Closed) |
| `/api/badge/github/prs/:slug` | PRs by state: Open / Merged / Draft / Closed |
| `/api/badge/license/:slug` | The mod's license |
| `/api/badge/powered-by/:slug` | The mod's own logo + name (for dependents to reference) |
| `/api/badge/loader/fabric` \| `/forge` \| `/neoforge` | Loader support: "Available for" / loader name (two lines) |
| `/api/badge/discord` | "Join our Discord" (needs `badges.discord` in `config.yml`) |
| `/api/badge/docs/:slug` | "Read the Docs" (needs a wiki link on the mod) |

Badges are images and cannot carry a link, so clickable ones pair with a
redirect landing page to wrap in `<a href>`:

```html
<a href="/api/badge/link/docs/my-mod"><img src="/api/badge/docs/my-mod" alt="docs"></a>
<a href="/api/badge/link/discord"><img src="/api/badge/discord" alt="discord"></a>
<a href="/api/badge/link/powered-by/my-mod"><img src="/api/badge/powered-by/my-mod" alt="Powered by"></a>
```

`/api/badge/powered-by/:slug` embeds the mod's own logo as a base64 data URI, so
the badge stays self-contained (no external image host required).

Counts are rendered as full numbers with thousands separators (`8,755,344`)
rather than `8.8M`-style abbreviations, so the exact value stays readable; the
badge widens as needed instead of truncating.

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

## Automatic issue preprocessing

After signing in, enable **Auto preprocess issues** under **Agent → Settings**. The switch defaults to off. Reading or changing settings and accessing processing status require authentication. GitHub refreshes follow `server.cache_ttl_minutes` and each repository's current cache expiry. Successful refresh events queue only newly added open issues in maintained projects; PRs are excluded. Manually clearing **GitHub caches** or **all caches** while signed in immediately triggers a refresh with the same delta rules, preserving the baseline and existing reports without scanning historical issues. Like manual dispatch, each task creates an AI conversation, attaches the issue body and starts the agent. Tasks run sequentially without blocking TTL refreshes. Turning the switch off stops new tasks; the current task finishes.

**After every restart, each repository's first successful upstream fetch only calibrates its baseline and does not dispatch issues. Cache hits and failed requests do not consume this guard.** This applies even when disk caches and persisted baselines exist, so issues created during downtime and already present in that first snapshot are also baseline-only. Additions are detected from the next successful fetch. Clearing caches does not reset the guard, comparison baseline or processing records.

Preprocessing uses the default model in `config/ai.yml` and the repository owner's GitHub token in `auth.yml`, and prepares source code before analysis. It searches for `<project_paths entry>/<repository name>`; missing repositories are cloned into the first writable root using the remote default branch. Existing checkouts fetch their current branch's `origin` upstream, or the matching remote branch when no upstream is configured. Detached checkouts use the remote default branch. Enable the GitHub data source and install Git.

**Source synchronization currently discards local commits, staged and unstaged changes, and ordinary untracked and Git-ignored files.** The server fetches and verifies the target branch before `reset --hard` and `clean -fdx`; network or authentication fetch failures leave local contents intact. A same-named non-Git directory, mismatched origin or path outside the configured root produces an error rather than being overwritten. Synchronization has a five-minute timeout. Clone, update and cleanup failures show a status beside the issue with details in a hover tooltip and in `[Issue]` logs, plus a retry action; analysis does not continue using stale sources.

After synchronization, the built-in `issue-preprocessor` role can only read/search sources and fetch reference material. The server rejects file changes, shell commands, external MCP calls and subagents, even with approval bypass enabled.

Preprocessing allows up to 30 model calls. Its prompt limits search scope, batches reads and targets completion within 20 rounds. Runtime reminders provide the remaining budget on every call. The final round, or a call started with less than one minute before the analysis deadline, disables tools and requests a report based on collected evidence, including limitations and a validation plan when evidence is insufficient. A model that still fails to produce a report remains failed; intermediate progress is not treated as a successful report.

Reports include a validity assessment, code/log evidence, root cause, proposed solutions and a verification plan, explicitly identifying missing evidence. Completed tasks provide **View report** beside the issue. Running or failed tasks with a session provide **View conversation** for messages and tool history; error details remain in the status tooltip. AI chat history also provides access. Repository issue baselines and queued tasks persist across restarts. Edits, reopened issues, repeated refreshes and restarts do not dispatch old issues again. Failed refreshes leave the baseline intact; issues observed while disabled only advance the baseline. Existing queued tasks survive restart; subsequent additions, explicit retries and configuration changes wake dispatch, without periodic queue scans from the TTL timer. Failed or interrupted tasks can be retried manually while enabled.

Settings and task links are stored in `data/issue-preprocess.json`; full reports are stored in `data/sessions.db`.

## Bot notifications (optional)

The QQ adapter uses `github.com/tencent-connect/botgo v0.2.1`. On first start, `config/bots.yml.example` creates a disabled `config/bots.yml`. Disabled bots make no QQ API requests and create no notification queue. Restart after configuration changes; the admin configuration reload does not restart bots.

Enable QQ notifications in the local `config/bots.yml`:

```yaml
enabled: true
notify_new_issues: true
notify_reports: true
dashboard_url: "https://dashboard.example.com" # Optional; report share links require no login
providers:
  qq:
    enabled: true
    app_id: "${QQBOT_APP_ID}"
    app_secret: "${QQBOT_APP_SECRET}"
    sandbox: false
targets: [] # Groups can bind through the website without manually entering an OpenID
```

Credentials accept literal values or environment variables. You can still specify static targets using `channel`, `group` or `c2c`; group/user targets require platform OpenIDs, not QQ group/account numbers. Configure outbound IP allowlists, API permissions and sandbox members in the QQ developer portal.

### Bind a group with a one-time code

1. Enable the configuration above and restart. Enable group messaging in the QQ developer portal and use **WebSocket** event delivery. The service obtains credentials and a gateway through BotGo on startup, opens an outbound connection and subscribes to `GROUP_AT_MESSAGE_CREATE` (`1 << 25`). The server needs access to QQ's HTTPS and WSS endpoints; no public callback URL or inbound callback port is required.
2. Log into the dashboard, open **QQ group notifications** under **Agent → Settings** and generate a code. @Mention the bot in the destination group and send `/bind CODE`. Codes expire after 10 minutes, work once, and are invalidated when regenerated. Only send a code to the group that should receive notifications.
3. The server takes `group_openid` from the authenticated QQ WebSocket session and persists the binding. The bot replies with the binding result, usage for `/bind` without a code, reasons for invalid codes or existing bindings, and help for other group @messages. The website refreshes bindings every 5 seconds and remains the confirmation source if the reply fails. Invalid or reused codes do not bind. Generation, listing and removal require website authentication; users only see and remove their own bindings. A bound group cannot be taken over by another account.
4. The **group owner**, using the latest mobile QQ, must select the bot from the group member list and allow proactive messages in its **group message settings**. For C2C, the user enables proactive messages on the bot profile. Binding does not grant this permission; platform quotas still apply. Error `40034105` generally indicates missing proactive-message permission.

New bindings only receive future notifications. Unbinding drops that group's queued deliveries; an already in-flight message can still arrive. Bindings survive restarts. Static `targets` remain managed by configuration and are not removed by website unbinding. The common binding store/code logic lives in `bot`; QQ authentication, WebSocket sessions and message handling live in `bot/qqbot` and can be extended with other adapters.

Group command feedback uses the message API for passive replies with the original event message ID and `msg_seq: 1`, without requiring proactive-message permission. Failed replies receive up to three attempts, retaining the same sequence; recent duplicate events do not repeat binding. New-issue/report notifications remain proactive and require group-owner authorization.

Startup retrieves credentials and logs `[QQBot]` login success/failure, followed by WebSocket connection success on `READY`. Credential refreshes, disconnections and `RESUMED` session recovery are also logged. Connections follow QQ's heartbeat interval and reconnect with backoff after disconnects or missed acknowledgments, using the session ID and sequence to resume events. Invalid sessions authenticate again; non-retryable permission, shard or ban errors stop the receiver with an error code. Message processing is separate from heartbeats, and cancellation closes connections and reply workers.

Logs exclude AppSecret, tokens, binding commands and message bodies; cached-token lookups do not generate repeated success logs. Only code hashes are persisted. For client service errors, inspect the QQ error code and traceID in the group-reply failure log. The old `/api/bot/qq/webhook` route has been removed; disable any previous Webhook configuration in the developer portal.

References: [QQ WebSocket](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/event-emit/websocket.html), [group message event](https://bot.q.qq.com/wiki/develop/api-v2/autogen/event/group_at_message_create.html), [messaging FAQ](https://bot.q.qq.com/wiki/bot_FAQ/).

New-issue notifications follow the existing GitHub cache TTL and manual cache-clear refresh events independently of the preprocessing switch, requiring the GitHub data source and the owner's token. Each repository's first successful fetch after every restart only calibrates the notification baseline; reading disk caches does not substitute for that upstream fetch. Later, only newly added open issues are sent; PRs, historical issues, edits and reopened issues are excluded.

After successful preprocessing, the notification worker makes one isolated, tool-free call to the default model in `ai.yml` to summarize the final report as plain Chinese text. The summary body is limited to 200 characters, excluding the header and final link. It retains the problem, validity assessment, main cause and recommendation, omitting detailed code paths, reproduction and verification steps. Markdown is removed and length is enforced before sending. Each report produces one message per recipient, with one shared summary for all recipients. The full report stays in the website conversation. The summary call has a 60-second timeout; failure produces a short completion notice and a log rather than sending the full report.

When `dashboard_url` is configured, the message ends with a public report link using the existing `/share/<id>` feature. **No login is required.** Only the final report message is shared, excluding attachments, tool output, intermediate messages and reasoning. Without a dashboard URL, the message asks readers to check the website. Prepared summaries and share links persist for send retries and restarts; old queued long reports are summarized before remaining delivery.

`data/bot-notifications.json` persists bindings, code hashes, repository baselines, reports awaiting summaries, prepared summary messages and per-target send progress. A background worker retries failures with a 10–320 second backoff. Messages for removed targets or disabled event types are discarded and never redirected. Delivery is at least once: a crash between platform acceptance and saving progress can repeat a message; network problems or platform limits can delay delivery.

To add another platform, implement `bot.Sender` (`Validate` / `Send`), register its adapter/configuration in `bot.go`, and configure targets with its provider name. Issue detection, report events, binding codes, persistence and retries belong to the shared module; `bot/qqbot` owns QQ authentication, event decoding and message transport.

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
- Badge endpoints are public by design so they can be embedded elsewhere.

## Third-party assets

Badge artwork is taken from
[DevIn's Badges](https://github.com/intergrav/devins-badges) by intergrav,
released under [CC0 1.0](https://creativecommons.org/publicdomain/zero/1.0/).

- `badge/assets/discord.svg` is the only embedded upstream file, served as-is by
  `/api/badge/discord`.
- Loader / documentation badges need two lines of text, which the upstream files
  cannot express (their text is baked into vector paths), so only their icon
  shapes and brand colours are reused — extracted into `badge_icons_gen.go`.
- `scripts/sync-devins-badges.ps1` refreshes the Embedded Discord asset and
  regenerates that file. Icon sources are downloaded to a temp directory and are
  not kept in the repository.
