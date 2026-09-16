package badge

import (
	"strings"
	"time"
)

// ========== 数据类徽章（内容来自缓存） ==========
//
// 每个徽章对应一个稳定的缓存键；读取时若条目过期，先返回旧值，再只为这一个键
// 排队后台刷新（Deps.RefreshAsync，同键去重）。任何情况下都不会触发全量预热。

// badgeDataSpec 描述一个数据类徽章的取值方式。
type badgeDataSpec struct {
	// cacheKey 该徽章依赖的缓存键；ok=false 表示模组缺少对应平台信息。
	cacheKey func(meta ModMeta) (string, bool)
	// refetch 后台刷新这个缓存键（同键并发会被去重）。
	refetch func(cacheKey string, meta ModMeta, ttl time.Duration)
	// read 从缓存值中取出要展示的数字；找不到条目时 ok=false。
	read func(cacheKey string, meta ModMeta) (int64, bool)
}

// badgeDataSpecs 全部数据类徽章，键为 "<platform>/<kind>"。
var badgeDataSpecs = map[string]badgeDataSpec{
	"curseforge/downloads": {
		cacheKey: func(meta ModMeta) (string, bool) {
			if strings.TrimSpace(meta.CF) == "" {
				return "", false
			}
			return "cf:" + meta.CF, true
		},
		refetch: func(cacheKey string, meta ModMeta, ttl time.Duration) {
			cfID := meta.CF
			refreshCache(cacheKey, func() {
				if value := deps.FetchCFDownloads(cfID); value > 0 {
					writeCache(cacheKey, value, ttl)
				}
			})
		},
		read: readBadgeCacheInt,
	},
	"github/downloads": {
		cacheKey: func(meta ModMeta) (string, bool) {
			owner, repo, ok := splitGitHubRepo(meta.GH)
			if !ok {
				return "", false
			}
			return "ghdl:" + owner + "/" + repo, true
		},
		refetch: func(cacheKey string, meta ModMeta, ttl time.Duration) {
			owner, repo, ok := splitGitHubRepo(meta.GH)
			if !ok {
				return
			}
			refreshCache(cacheKey, func() {
				if value := deps.FetchGHReleaseCount(owner, repo); value > 0 {
					writeCache(cacheKey, value, ttl)
				}
			})
		},
		read: readBadgeCacheInt,
	},
	"github/issues": {
		cacheKey: badgeGitHubRepoKey,
		refetch:  refetchGitHubInfo,
		read: func(cacheKey string, _ ModMeta) (int64, bool) {
			return readBadgeGitHubCount(cacheKey, true)
		},
	},
	"github/prs": {
		cacheKey: badgeGitHubRepoKey,
		refetch:  refetchGitHubInfo,
		read: func(cacheKey string, _ ModMeta) (int64, bool) {
			return readBadgeGitHubCount(cacheKey, false)
		},
	},
}

// badgeGitHubRepoKey 返回仓库元数据缓存键（开放 issue / PR 数量等）。
func badgeGitHubRepoKey(meta ModMeta) (string, bool) {
	owner, repo, ok := splitGitHubRepo(meta.GH)
	if !ok {
		return "", false
	}
	return CacheKeyPrefix + owner + "/" + repo, true
}

// refetchGitHubInfo 触发一次仓库元数据的后台刷新。
// 具体抓取与写缓存由 main 包负责（那里有 token 与限速逻辑），这里只管排队。
func refetchGitHubInfo(cacheKey string, meta ModMeta, ttl time.Duration) {
	owner, repo, ok := splitGitHubRepo(meta.GH)
	if !ok {
		return
	}
	repoPath := owner + "/" + repo
	refreshCache(cacheKey, func() { deps.RefreshGitHub(repoPath, ttl) })
}

// ========== 对外部能力的薄封装 ==========
// 统一做 nil 检查：纯渲染测试可以不注入这些能力。

// cacheTTL 当前缓存有效期；未注入时退化为 30 分钟。
func cacheTTL() time.Duration {
	if deps.CacheTTL == nil {
		return 30 * time.Minute
	}
	return deps.CacheTTL()
}

func readCache(key string) CacheEntry {
	if deps.ReadCache == nil {
		return CacheEntry{}
	}
	return deps.ReadCache(key)
}

func writeCache(key string, value interface{}, ttl time.Duration) {
	if deps.WriteCache == nil {
		return
	}
	deps.WriteCache(key, value, ttl)
}

func refreshCache(key string, fetch func()) {
	if deps.RefreshAsync == nil {
		return
	}
	deps.RefreshAsync(key, fetch)
}

// readBadgeCacheInt 读取 int64 缓存条目（含已过期条目，便于先返回旧值）。
func readBadgeCacheInt(cacheKey string, _ ModMeta) (int64, bool) {
	e := readCache(cacheKey)
	if !e.Found {
		return 0, false
	}
	n, ok := e.Value.(int64)
	return n, ok
}

// readBadgeGitHubCount 读取开放 issue / PR 数量。
func readBadgeGitHubCount(cacheKey string, issues bool) (int64, bool) {
	e := readCache(cacheKey)
	if !e.Found {
		return 0, false
	}
	// 缓存里可能是刚写入的 nil（拉取失败），断言失败时按未命中处理。
	info, ok := e.Value.(*GitHubInfo)
	if !ok || info == nil {
		return 0, false
	}
	if issues {
		return int64(info.OpenIssues), true
	}
	return int64(info.OpenPRs), true
}

// badgeGitHubInfo 读取仓库元数据缓存（含全部 issue/PR 条目），
// 供需要按状态细分的徽章使用。
//
// 与其它数据徽章同样的策略：缓存新鲜就直接用；过期或缺失时先返回旧值（如果有），
// 再只为这一个键排队后台刷新，不影响任何其它模组。
func badgeGitHubInfo(meta ModMeta, ttl time.Duration) (*GitHubInfo, bool) {
	cacheKey, ok := badgeGitHubRepoKey(meta)
	if !ok {
		return nil, false
	}

	e := readCache(cacheKey)
	if e.Found && !e.Stale {
		if info, ok := e.Value.(*GitHubInfo); ok && info != nil {
			return info, true
		}
		// 缓存里是 nil（上次拉取失败）：当作未命中，重新排队。
	}
	refetchGitHubInfo(cacheKey, meta, ttl)
	if e.Found {
		if info, ok := e.Value.(*GitHubInfo); ok && info != nil {
			return info, true
		}
	}
	return nil, false
}

// badgeDataValue 读取某个数据类徽章的数值。
// handled=false 表示该 platform/kind 不是受支持的数据类徽章。
// hit=false 表示没有可用的缓存值（本轮返回 N/A），此时只会后台填充对应的一条缓存。
func badgeDataValue(platform, kind string, meta ModMeta, ttl time.Duration) (value int64, hit bool, handled bool) {
	spec, ok := badgeDataSpecs[platform+"/"+kind]
	if !ok {
		return 0, false, false
	}
	cacheKey, ok := spec.cacheKey(meta)
	if !ok {
		// 模组没有填对应的平台 ID：没有缓存可读，也不会创建。
		return 0, false, true
	}

	e := readCache(cacheKey)
	if e.Found && !e.Stale {
		// 命中且未过期：直接返回，不触发任何刷新。
		value, hit = spec.read(cacheKey, meta)
		return value, hit, true
	}

	// 未命中或已过期：先尽力返回已有旧值（过期条目仍然可读），再只刷新这一条。
	value, hit = spec.read(cacheKey, meta)
	spec.refetch(cacheKey, meta, ttl)
	return value, hit, true
}

// splitGitHubRepo 拆分 "owner/repo"，缺失或格式错误时返回 false。
func splitGitHubRepo(repoPath string) (string, string, bool) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(repoPath), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", false
	}
	return owner, repo, true
}

// ========== 加载器支持情况 ==========

// badgeLoaderName 把 URL 里的加载器名映射到模组 versions 矩阵中实际出现的名字。
// 模组数据由用户手工填写，大小写与分隔符可能不一致，因此在这里归一化比对。
func badgeLoaderName(meta ModMeta, loader string) (string, bool) {
	want := normalizeLoaderName(loader)
	if want == "" {
		return "", false
	}
	for _, loaders := range meta.Versions {
		for name := range loaders {
			if normalizeLoaderName(name) == want {
				return name, true
			}
		}
	}
	return "", false
}

// normalizeLoaderName 去掉大小写与常见分隔符，便于比较 "NeoForge"/"neo-forge"。
func normalizeLoaderName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch r {
		case ' ', '-', '_', '.', '/':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
