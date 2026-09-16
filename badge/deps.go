// Package badge 提供 /api/badge/... 下的所有 SVG 徽章：
// 数据类（下载量、issue/PR 计数、许可证）、模组身份类（Powered by）与静态资源类。
//
// 本包不直接依赖 main 包：模组元数据、缓存读写、上游抓取、配置与内嵌资源
// 都通过 Deps 注入，因此可以独立构建与测试。
package badge

import (
	"io/fs"
	"time"
)

// ModMeta 是徽章用得到的模组信息（main 包 ModMeta 的一个子集）。
type ModMeta struct {
	ID       string
	Name     string
	License  string
	GH       string // "owner/repo"
	CF       string // CurseForge 项目 id
	Wiki     string // 文档链接
	Versions map[string]map[string]string
}

// CacheEntry 是一条缓存值。
type CacheEntry struct {
	Value interface{}
	Found bool
	Stale bool
}

// Deps 是徽章包对外部世界的全部依赖，由 main 包在启动时填充。
//
// 用函数字段而不是接口：main 里这些能力本来就是包级函数与全局状态，
// 直接注入最小改动，也便于测试时替换成假数据。
type Deps struct {
	// Assets 内嵌的静态徽章资源（badge/assets/discord.svg）。
	// 用 fs.FS 而非 embed.FS，测试里可以塞任意实现。
	Assets fs.FS

	// ModMeta 按模组 id 取元数据；ok=false 表示没有这个模组。
	ModMeta func(id string) (ModMeta, bool)
	// ValidID 校验 id 是否是合法的模组 id（字符集白名单）。
	ValidID func(id string) bool
	// ReadLogo 读取模组 logo（BLOB + mime）；found=false 表示没有图。
	ReadLogo func(id string) (data []byte, mime string, found bool, err error)

	// CacheTTL 当前缓存有效期（config.yml 的 cache_ttl_minutes）。
	CacheTTL func() time.Duration
	// ReadCache 读一条缓存（含过期标记）。
	ReadCache func(key string) CacheEntry
	// WriteCache 写一条缓存（按 ttl 过期，并异步落盘）。
	WriteCache func(key string, value interface{}, ttl time.Duration)
	// RefreshAsync 后台只刷新指定的一个缓存键（同键去重）。
	RefreshAsync func(key string, fetch func())
	// RefreshGitHub 触发某个仓库元数据的后台刷新（含缓存写入）。
	RefreshGitHub func(repoPath string, ttl time.Duration)

	// FetchCFDownloads 抓取 CurseForge 下载量（仅缓存未命中时调用）。
	FetchCFDownloads func(cfID string) int64
	// FetchGHReleaseCount 抓取 GitHub Release 资源下载量合计。
	FetchGHReleaseCount func(owner, repo string) int64

	// DiscordInvite 全局 Discord 邀请链接（config.yml 的 badges.discord）。
	DiscordInvite string
}

var deps Deps

// Init 注入依赖，必须在处理任何徽章请求前调用一次。
func Init(d Deps) {
	deps = d
}

// CacheKeyPrefix 是仓库元数据缓存（Issues/PR）的键前缀，带版本号。
//
// 导出给 main 包使用：缓存落盘路径、重启恢复白名单、缓存刷新动作都要按它匹配。
// 带版本号的原因：条目里除了 open 计数还派生了 not_planned / closed / draft 计数，
// 旧格式条目没有这些字段，换前缀让它们自然失效，而不是读到一堆 0。
const CacheKeyPrefix = "gh2:"
