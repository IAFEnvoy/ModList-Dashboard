package badge

import (
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
)

// ========== 模组身份徽章（Powered by <模组>） ==========
//
// 显示某个模组自己的 logo 与名称，供把它当前置 / 依赖的模组在 README 里引用。
// 形状与其它徽章一致：左侧 40x40 图标区放模组 logo，右侧两行文字。
//
// 数据全部来自内存：logo 是 mods.db 里的 BLOB，名称来自已加载的模块元数据，
// 因此这条路径不读缓存、不访问网络。

// 模组名过长时的截断上限（字符数）。超出后画布会继续变宽，这里只是兜底，
// 避免出现极端宽度的徽章。
const badgeModNameMaxRunes = 32

// badgeLogoSample 是一条已编码的内嵌 logo。
type badgeLogoSample struct {
	Mime   string // 例如 image/webp
	Base64 string // 原始字节的 base64（不含 data: 前缀）
	Width  int
	Height int
}

// badgeLogoCache 缓存 data URI 编码结果，避免每次请求都重新 base64 编码
// 几十 KB 的图片。键为 mod id，条目很小，容量上限足够覆盖整个模组列表。
const badgeLogoCacheLimit = 256

var (
	badgeLogoMu    sync.Mutex
	badgeLogoCache = make(map[string]badgeLogoSample)
	badgeLogoOrder []string
)

// badgeLogo 取（或生成）某个模组 logo 的内嵌编码。
func badgeLogo(modID string) (badgeLogoSample, bool) {
	badgeLogoMu.Lock()
	if sample, ok := badgeLogoCache[modID]; ok {
		badgeLogoMu.Unlock()
		return sample, true
	}
	badgeLogoMu.Unlock()

	data, mime, found, err := deps.ReadLogo(modID)
	if err != nil || !found || len(data) == 0 {
		return badgeLogoSample{}, false
	}
	// 只内嵌浏览器能直接渲染的位图；svg 走同样的 image/*，但需要单独确认类型。
	if !strings.HasPrefix(mime, "image/") {
		return badgeLogoSample{}, false
	}
	width, height, ok := imageDimensions(data)
	if !ok {
		// 探测不到尺寸时按正方形处理，靠 preserveAspectRatio 居中，不会拉伸。
		width, height = 1, 1
	}
	sample := badgeLogoSample{
		Mime:   mime,
		Base64: base64.StdEncoding.EncodeToString(data),
		Width:  width,
		Height: height,
	}

	badgeLogoMu.Lock()
	if _, exists := badgeLogoCache[modID]; !exists {
		if len(badgeLogoOrder) >= badgeLogoCacheLimit {
			oldest := badgeLogoOrder[0]
			badgeLogoOrder = badgeLogoOrder[1:]
			delete(badgeLogoCache, oldest)
		}
		badgeLogoOrder = append(badgeLogoOrder, modID)
	}
	badgeLogoCache[modID] = sample
	badgeLogoMu.Unlock()

	return sample, true
}

// badgeLogoSVG 生成把 logo 放进 40x40 图标区的 <image>。
// 用 preserveAspectRatio="xMidYMid meet" 保证不拉伸：非正方形时按长边缩放并居中。
func badgeLogoSVG(sample badgeLogoSample) string {
	viewBoxW, viewBoxH := sample.Width, sample.Height
	if viewBoxW <= 0 || viewBoxH <= 0 {
		viewBoxW, viewBoxH = 1, 1
	}
	// 同时给出 href 与 xlink:href：不同渲染器（浏览器 / librsvg / 部分 Markdown 预览）
	// 对两者的支持不一致，写两份最稳妥。
	dataURI := "data:" + sample.Mime + ";base64," + sample.Base64
	return `<g clip-path="url(#badge_icon_clip)"><image x="12" y="8" width="40" height="40" ` +
		`preserveAspectRatio="xMidYMid meet" viewBox="0 0 ` +
		formatBadgeCoord(float64(viewBoxW)) + ` ` + formatBadgeCoord(float64(viewBoxH)) +
		`" href="` + dataURI + `" xlink:href="` + dataURI + `"/></g>`
}

// badgeModName 截断过长的模组名。
func badgeModName(name string) string {
	name = strings.TrimSpace(name)
	runes := []rune(name)
	if len(runes) <= badgeModNameMaxRunes {
		return name
	}
	return string(runes[:badgeModNameMaxRunes-1]) + "…"
}

// poweredByModSpec 生成模组身份徽章：第一行 Powered by，第二行模组名，左侧是模组 logo。
func poweredByModSpec(meta ModMeta) badgeSpec {
	name := badgeModName(meta.Name)
	if name == "" {
		name = meta.ID
	}
	spec := badgeSpec{
		Title: "Powered by",
		Value: name,
		// 模组 logo 本身已经是彩色的，这里用中性色，避免与 logo 撞色。
		ValueColor: "#FFFFFF",
		BgStart:    "#202020",
		BgEnd:      "#000000",
	}
	if sample, ok := badgeLogo(meta.ID); ok {
		spec.Icon = badgeLogoSVG(sample)
		// 文字块要紧随 40x40 的 logo；有 logo 时才需要右移。
		spec.TextLeft = badgeTextLeft
	} else {
		// 没有 logo 时退化为纯文字徽章：此时基准宽度是 195（无图标），
		// 文字必须从 16 开始，否则会超出画布。
		spec.TextLeft = badgeTextLeftPlain
	}
	// 宽度按第二行（字号更大）估算，避免名称被右边缘挤掉。
	spec.TextWidth = int(float64(len([]rune(name)))*float64(badgeFontSizeValue)*0.56) + 1
	return spec
}

// badgePrimaryProjectURL 返回模组项目主页：优先 CurseForge，其次 GitHub，最后 wiki。
func badgePrimaryProjectURL(meta ModMeta) (string, bool) {
	if cfID := strings.TrimSpace(meta.CF); cfID != "" {
		return "https://www.curseforge.com/minecraft/mc-mods/" + cfID, true
	}
	if owner, repo, ok := splitGitHubRepo(meta.GH); ok {
		return "https://github.com/" + owner + "/" + repo, true
	}
	return badgeWikiURL(meta)
}

// handlePoweredByBadge 处理 /api/badge/powered-by/<slug>。
func handlePoweredByBadge(w http.ResponseWriter, r *http.Request, slug string) {
	meta, ok := deps.ModMeta(slug)
	if !ok {
		http.Error(w, "unknown mod", http.StatusNotFound)
		return
	}
	writeBadgeSVG(w, r, renderBadgeTemplate(poweredByModSpec(meta)))
}
