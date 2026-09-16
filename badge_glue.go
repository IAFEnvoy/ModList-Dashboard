package main

import (
	"net/http"
	"time"

	"modlist-dashboard/badge"
)

// badgePrefixPath 徽章路由前缀。用函数而不是常量，避免与 badge 包里的东西重名。
func badgePrefixPath() string { return "/api/badge/" }

// 内嵌徽章静态资源。go:embed 不允许 `../`，所以内嵌留在 main 包，
// 再把 FS 注入给 badge 包（见 badge.Deps.Assets）。
//

// initBadge 把 main 包的能力注入徽章包。
// 徽章包不反向依赖 main，因此这里显式列出它需要的每一项。
func initBadge() {
	badge.Init(badge.Deps{

		ModMeta: func(id string) (badge.ModMeta, bool) {
			meta, ok := getModMeta(id)
			if !ok {
				return badge.ModMeta{}, false
			}
			return badge.ModMeta{
				ID:       meta.ID,
				Name:     meta.Name,
				License:  meta.ModMeta.License,
				GH:       meta.IDs.GH,
				CF:       meta.IDs.CF,
				Wiki:     meta.IDs.Wiki,
				Versions: meta.Versions,
			}, true
		},
		ValidID:  validModID,
		ReadLogo: readModLogo,

		CacheTTL: ttlCache,
		ReadCache: func(key string) badge.CacheEntry {
			value, found, stale := readCacheStale(key)
			return badge.CacheEntry{Value: value, Found: found, Stale: stale}
		},
		WriteCache: func(key string, value interface{}, ttl time.Duration) {
			writeCache(key, value, ttl)
		},
		RefreshAsync: refreshCacheAsync,
		RefreshGitHub: func(repoPath string, ttl time.Duration) {
			// getGitHubInfoCached 自身按 gh2:<repo> 管理缓存，包含写入与限速。
			_ = getGitHubInfoCached(repoPath, ttl)
		},

		FetchCFDownloads:    fetchCFDownloads,
		FetchGHReleaseCount: fetchGitHubReleaseDownloads,

		DiscordInvite: config.Badges.Discord,
	})
}

// badgeHandlerForTest 让 main 的端到端测试直接调用徽章处理器
// （测试里不走 http.ServeMux，也不需要 CORS 中间件）。
func badgeHandlerForTest(w http.ResponseWriter, r *http.Request) {
	badge.Handler(w, r)
}
