package badge

import (
	"embed"
	"net/http"
	"strings"
)

// ========== devins-badges 静态徽章资源 ==========
//
// badge/assets/*.svg 来自 @intergrav/devins-badges（CC0-1.0），用
// scripts/sync-devins-badges.ps1 同步。这些是「固定文案」的成品徽章：文字已被
// 转成矢量路径，内容不可改写，只能整体输出。需要动态文字（下载量、许可证、
// 平台名）的徽章走 badge.go 的模板渲染。
//
// 放在包内而不是仓库根的 assets/：go:embed 不支持 `../`，资源跟着包走最省事。
//
//go:embed assets/*.svg
var badgeAssets embed.FS

// badgeAssetDir 是 FS 内的目录名。FS 的路径分隔符固定为 "/"，
// 且 ReadDir 需要不带尾斜杠的目录路径，因此这里不写尾斜杠。
const badgeAssetDir = "assets"

// badgeAssetPath 返回资源在 FS 内的完整路径。
func badgeAssetPath(file string) string {
	return badgeAssetDir + "/" + file
}

// 固定徽章资源表。键即 URL 中使用的名字。
//
// 加载器与文档徽章不在这里：它们的文案要分两行显示，而 devins-badges 的成品矢量
// 把文字烘焙成了路径、无法改排版，所以那两类改用本项目模板渲染（见 badge_text.go），
// 只复用上游的图标形状与配色（见 badge_icons_gen.go）。
var (
	// badgeDiscordAsset Discord 邀请徽章（"Chat with us on Discord"）。
	badgeDiscordAsset = "discord.svg"
)

// serveBadgeAsset 输出内嵌的静态徽章资源。
func serveBadgeAsset(w http.ResponseWriter, r *http.Request, file string) {
	data, err := badgeAssets.ReadFile(badgeAssetPath(file))
	if err != nil {
		// 内嵌资源缺失属于构建期问题，不应在运行时发生。
		http.Error(w, "badge asset unavailable", http.StatusInternalServerError)
		return
	}
	// 公共头由 writeBadgeBytes 统一设置（含 no-cache 与 ETag）；长 max-age 会把
	// 旧资源钉在浏览器里，换资源后刷新页面也看不到更新。
	writeBadgeBytes(w, r, data)
}

// ========== 链接 ==========

// safeHTTPURL 校验一个用户配置的链接可用作跳转目标。
// 只接受 http/https，且拒绝含空白/控制字符或引号的值，避免生成
// javascript: 之类的伪协议链接或破坏 HTML 属性。
func safeHTTPURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.ContainsAny(raw, " \t\r\n<>\"'`") {
		return "", false
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return raw, true
	}
	// 协议相对写法 //host/path → https://host/path
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw, true
	}
	// 没有协议：必须看起来像域名，避免把 "javascript:alert(1)" 补成合法链接。
	if strings.Contains(raw, ":") {
		return "", false
	}
	host := raw
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if host == "" || !strings.Contains(host, ".") {
		return "", false
	}
	return "https://" + raw, true
}

// badgeDiscordURL 返回配置的 Discord 邀请链接。
func badgeDiscordURL() (string, bool) {
	return safeHTTPURL(deps.DiscordInvite)
}

// badgeWikiURL 返回模组配置的 wiki 链接（documentation 徽章的目标）。
func badgeWikiURL(meta ModMeta) (string, bool) {
	return safeHTTPURL(meta.Wiki)
}
