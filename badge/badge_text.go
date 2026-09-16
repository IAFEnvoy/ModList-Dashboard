package badge

import (
	"net/http"
	"sort"
	"strings"
)

// ========== 加载器 / 文档徽章（两行文字） ==========
//
// 这两类徽章的文案要分两行显示：
//
//	Available for       Read the
//	Fabric              Documentation
//
// 上游 devins-badges 的成品矢量把文字烘焙成了路径，无法改排版，因此这里改用
// 本项目模板渲染文字，只复用它的图标形状与品牌配色（见 badge_icons_gen.go）。

// badgeTextBadge 描述一个"两行文字 + 图标"的徽章。
type badgeTextBadge struct {
	Title string // 第一行（中性色）
	Value string // 第二行（强调色）
	Icon  string // 图标标记
	Style string // badgeAssetStyle 的键
}

// 加载器徽章：一个加载器一个固定徽章，不需要 slug。
var badgeLoaderBadges = map[string]badgeTextBadge{
	"fabric": {
		Title: "Available for", Value: "Fabric",
		Icon: badgeIconFabric, Style: "badgeIconFabric",
	},
	"forge": {
		Title: "Available for", Value: "Forge",
		Icon: badgeIconForge, Style: "badgeIconForge",
	},
	"neoforge": {
		Title: "Available for", Value: "NeoForge",
		Icon: badgeIconNeoForge, Style: "badgeIconNeoForge",
	},
}

// badgeDocsBadge 文档徽章（文案固定，是否可用取决于模组有没有 wiki 链接）。
var badgeDocsBadge = badgeTextBadge{
	Title: "Read the", Value: "Documentation",
	Icon: badgeIconDocs, Style: "badgeIconDocs",
}

// badgeLoaderKeys 返回排序后的加载器名，保证输出稳定。
func badgeLoaderKeys() []string {
	keys := make([]string, 0, len(badgeLoaderBadges))
	for k := range badgeLoaderBadges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderTextBadge 用本项目模板渲染两行文字徽章，配色取自上游资源。
func renderTextBadge(badge badgeTextBadge) string {
	style := badgeAssetStyle[badge.Style]
	return renderBadgeTemplate(badgeSpec{
		Title:      badge.Title,
		Value:      badge.Value,
		ValueColor: style.Accent,
		Icon:       badge.Icon,
		BgStart:    style.BgStart,
		BgEnd:      style.BgEnd,
	})
}

// handleLoaderBadge 处理 /api/badge/loader/<loader>。
// 加载器徽章是固定资源，不需要 slug，也不读取任何缓存或上游接口。
func handleLoaderBadge(w http.ResponseWriter, r *http.Request, loader string) {
	badge, ok := badgeLoaderBadges[strings.ToLower(loader)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeBadgeSVG(w, r, renderTextBadge(badge))
}

// handleDocsBadge 处理 /api/badge/docs/<slug>：文档徽章指向该模组的 wiki 链接，
// 因此只有配置了 wiki 的模组才会出图。
func handleDocsBadge(w http.ResponseWriter, r *http.Request, slug string) {
	meta, ok := deps.ModMeta(slug)
	if !ok {
		http.Error(w, "unknown mod", http.StatusNotFound)
		return
	}
	if _, ok := badgeWikiURL(meta); !ok {
		http.Error(w, "this mod has no wiki link", http.StatusNotFound)
		return
	}
	writeBadgeSVG(w, r, renderTextBadge(badgeDocsBadge))
}
