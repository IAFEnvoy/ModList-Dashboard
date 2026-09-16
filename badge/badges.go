package badge

import (
	"net/http"
	"strings"
)

// ========== 徽章路由 ==========
//
// 徽章全部公开（方便嵌进 README / 外部页面），因此不套 withAuth。
//
// 数据类（内容随模组变化，读数走缓存）：
//
//	/api/badge/curseforge/downloads/:slug     CurseForge 下载量
//	/api/badge/github/downloads/:slug         GitHub Release 资源下载量合计
//	/api/badge/github/issues/:slug            开放 issue 数
//	/api/badge/github/prs/:slug               开放 PR 数
//	/api/badge/license/:slug                  模组许可证
//
// 模组身份（内嵌该模组自己的 logo + 名称，供把它当前置的模组引用）：
//
//	/api/badge/powered-by/:slug               Powered by <模组名>
//
// 静态类（固定文案，直接输出内嵌的 devins-badges 资源）：
//
//	/api/badge/loader/:loader                 Fabric / Forge / NeoForge（无需 slug）
//	/api/badge/discord                        加入 Discord（需配置 config.yml badges.discord）
//	/api/badge/docs/:slug                     文档（使用该模组的 wiki 链接）
//
// 点击落地页（图片徽章本身不能携带链接，把下面这些地址用作 <a href>）：
//
//	/api/badge/link/discord
//	/api/badge/link/docs/:slug
//	/api/badge/link/powered-by/:slug

const badgePathPrefix = "/api/badge/"

// Handler 是 /api/badge/ 的 HTTP 入口，注册到 http.ServeMux 即可：
//
//	mux.HandleFunc("/api/badge/", corsMiddleware(badge.Handler))
func Handler(w http.ResponseWriter, r *http.Request) {
	handleBadge(w, r)
}

// handleBadge 按路径段数分发。
func handleBadge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	segments := badgePathSegments(r.URL.Path)
	if len(segments) == 0 {
		http.NotFound(w, r)
		return
	}

	// 点击落地页。
	if segments[0] == "link" {
		handleBadgeLink(w, r, segments[1:])
		return
	}

	switch len(segments) {
	case 1:
		handleNamedBadge(w, r, segments[0])
	case 2:
		switch segments[0] {
		case "loader":
			handleLoaderBadge(w, r, segments[1])
		case "docs":
			handleDocsBadge(w, r, segments[1])
		case "license":
			// /api/badge/license/<slug>：许可证只依赖模组元数据，两个段即可。
			handleModBadge(w, r, "license", "license", segments[1])
		case "powered-by":
			// /api/badge/powered-by/<slug>：模组身份徽章，只读内存。
			handlePoweredByBadge(w, r, segments[1])
		default:
			http.NotFound(w, r)
		}
	case 3:
		handleModBadge(w, r, segments[0], segments[1], segments[2])
	default:
		http.NotFound(w, r)
	}
}

// badgePathSegments 返回去掉前缀与空段后的路径段。
func badgePathSegments(path string) []string {
	rest := strings.Trim(strings.TrimPrefix(path, badgePathPrefix), "/")
	if rest == "" {
		return nil
	}
	segments := make([]string, 0, 3)
	for _, part := range strings.Split(rest, "/") {
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

// handleNamedBadge 处理 /api/badge/<name> 形式的固定徽章。
func handleNamedBadge(w http.ResponseWriter, r *http.Request, name string) {
	switch name {
	case "discord":
		// 未配置邀请链接时不输出徽章，避免生成点不动的死链。
		if _, ok := badgeDiscordURL(); !ok {
			http.Error(w, "discord invite is not configured", http.StatusNotFound)
			return
		}
		serveBadgeAsset(w, r, badgeDiscordAsset)
	default:
		http.NotFound(w, r)
	}
}

// handleModBadge 处理 /api/badge/<platform>/<kind>/<slug>。
func handleModBadge(w http.ResponseWriter, r *http.Request, platform, kind, slug string) {
	if !deps.ValidID(slug) {
		http.NotFound(w, r)
		return
	}
	// 未匹配任何模组的 id 直接 404，此时不读取也不创建任何缓存。
	meta, ok := deps.ModMeta(slug)
	if !ok {
		http.Error(w, "unknown mod", http.StatusNotFound)
		return
	}

	switch {
	case platform == "github" && (kind == "issues" || kind == "prs"):
		// 按状态细分展示：第一行标题，第二行各状态图标 + 数量。
		// 数据仍走同一条缓存（gh2:<repo>），缓存没建立时后台只刷新这一条。
		info, hit := badgeGitHubInfo(meta, cacheTTL())
		var segments []ghStateSegment
		title := "GitHub Issues"
		if hit {
			if kind == "issues" {
				segments = githubIssueSegments(info)
			} else {
				title = "GitHub Pull Requests"
				segments = githubPRSegments(info)
			}
		}
		writeBadgeSVG(w, r, renderBadgeTemplate(ghStatusBadge(title, segments, hit)))

	case kind == "downloads":
		value, hit, handled := badgeDataValue(platform, kind, meta, cacheTTL())
		if !handled {
			http.NotFound(w, r)
			return
		}
		spec, ok := badgeDataSpecFor(platform, kind, value)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if !hit {
			// 缓存尚未建立：本轮先给占位值，后台只刷新这一条缓存。
			spec.Value = "N/A"
		}
		writeBadgeSVG(w, r, renderBadgeTemplate(spec))

	case platform == "license":
		// 路由层把 /api/badge/license/<slug> 归一化为 license/license/<slug>。
		writeBadgeSVG(w, r, renderBadgeTemplate(licenseSpec(meta)))

	default:
		http.NotFound(w, r)
	}
}

// handleBadgeLink 处理点击落地页：/api/badge/link/<name>[/<slug>]。
func handleBadgeLink(w http.ResponseWriter, r *http.Request, segments []string) {
	switch {
	case len(segments) == 1 && segments[0] == "discord":
		target, ok := badgeDiscordURL()
		if !ok {
			http.Error(w, "discord invite is not configured", http.StatusNotFound)
			return
		}
		serveBadgeLink(w, r, target)

	case len(segments) == 2 && segments[0] == "docs":
		meta, ok := deps.ModMeta(segments[1])
		if !ok {
			http.Error(w, "unknown mod", http.StatusNotFound)
			return
		}
		target, ok := badgeWikiURL(meta)
		if !ok {
			http.Error(w, "this mod has no wiki link", http.StatusNotFound)
			return
		}
		serveBadgeLink(w, r, target)

	case len(segments) == 2 && segments[0] == "powered-by":
		meta, ok := deps.ModMeta(segments[1])
		if !ok {
			http.Error(w, "unknown mod", http.StatusNotFound)
			return
		}
		target, ok := badgePrimaryProjectURL(meta)
		if !ok {
			http.Error(w, "this mod has no project link", http.StatusNotFound)
			return
		}
		serveBadgeLink(w, r, target)

	default:
		http.NotFound(w, r)
	}
}

// ========== 模板渲染类徽章的配色与文案 ==========

// badgeDataSpecFor 返回数据类徽章的渲染参数。
func badgeDataSpecFor(platform, kind string, value int64) (badgeSpec, bool) {
	switch platform + "/" + kind {
	case "curseforge/downloads":
		return badgeSpec{
			Title: "CurseForge Downloads", Value: formatBadgeCount(value),
			ValueColor: "#F16436", Icon: badgeIconCurseForge,
			BgStart: "#2C130B", BgEnd: "#210D08",
		}, true
	case "github/downloads":
		return badgeSpec{
			Title: "GitHub Downloads", Value: formatBadgeCount(value),
			ValueColor: "#FFFFFF", Icon: badgeIconGitHub,
			BgStart: "#202020", BgEnd: "#000000",
		}, true
	case "github/issues":
		return badgeSpec{
			Title: "Open Issues", Value: formatBadgeCount(value),
			ValueColor: "#3FB950", Icon: badgeIconIssue,
			BgStart: "#202020", BgEnd: "#000000",
		}, true
	case "github/prs":
		return badgeSpec{
			Title: "Open Pull Requests", Value: formatBadgeCount(value),
			ValueColor: "#A371F7", Icon: badgeIconPR,
			BgStart: "#202020", BgEnd: "#000000",
		}, true
	}
	return badgeSpec{}, false
}

// licenseSpec 生成模组许可证徽章；未填写 license 时显示 Unknown。
func licenseSpec(meta ModMeta) badgeSpec {
	license := strings.TrimSpace(meta.License)
	if license == "" {
		license = "Unknown"
	}
	return badgeSpec{
		Title: "License", Value: license,
		ValueColor: "#D8C7A8", Icon: badgeIconScale,
		BgStart: "#202020", BgEnd: "#000000",
	}
}
