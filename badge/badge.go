package badge

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// ========== 徽章 SVG 模板 ==========
//
// 视觉样式与 Anvil-Dev/icon.anvilcraft.dev 的模板保持一致（56px 高、8px 圆角、
// Inter 字体、白色 15% 描边、右下投影）。宽度按实际内容计算，因此模板里所有
// 与宽度相关的坐标都由 renderBadgeTemplate 直接算出，不做二次缩放。

const badgeHeight = 56

// 每个徽章的几何常量。
const (
	badgeTextLeft      = 60  // 文字块左边界（含图标时）
	badgeTextLeftPlain = 16  // 文字块左边界（无图标时）
	badgePaddingRight  = 16  // 右内边距
	badgeWidthWithIcon = 251 // 有图标时的基准宽度（与上游模板一致）
	badgeWidthNoIcon   = 195 // 无图标时的基准宽度
	badgeFontSizeTitle = 16
	badgeFontSizeValue = 17
)

// badgeTemplateBody 是各类徽章共用的骨架：背景渐变、描边、顶部内容与两行文字。
// __TOP__ 是顶部图标区（图标或模组 logo），__EXTRA__ 放在文字之后，用于在
// 数值行右侧追加图标等自定义内容（如 issue/PR 徽章的 <icon><count> 组合）。
const badgeTemplateBody = `<svg width="__W__" height="56" viewBox="0 0 __W__ 56" fill="none" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
<rect width="__W__" height="56" rx="8" fill="url(#badge_bg)"/>
<rect x="1.05" y="1.05" width="__BORDER__" height="53.9" rx="6.95" stroke="white" stroke-opacity="0.15" stroke-width="2.1"/>
__ICON__
<g filter="url(#badge_shadow)">
<text transform="translate(__TEXT_LEFT__ 9.5)" fill="#E8E8E8" style="white-space: pre" xml:space="preserve" font-family="Inter" font-size="16" font-weight="500" letter-spacing="0em"><tspan x="0" y="15.3182">__TITLE__</tspan></text>
<text transform="translate(__TEXT_LEFT__ 28.5)" fill="__VALUE_COLOR__" style="white-space: pre" xml:space="preserve" font-family="Inter" font-size="17" font-weight="800" letter-spacing="0em">__VALUE_TS__</text>
__EXTRA__
</g>
<defs>
<filter id="badge_shadow" x="__SHADOW_X__" y="3.9" width="__SHADOW_W__" height="48.2" filterUnits="userSpaceOnUse" color-interpolation-filters="sRGB">
<feFlood flood-opacity="0" result="BackgroundImageFix"/>
<feColorMatrix in="SourceAlpha" type="matrix" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 127 0" result="hardAlpha"/>
<feOffset/>
<feGaussianBlur stdDeviation="2.8"/>
<feComposite in2="hardAlpha" operator="out"/>
<feColorMatrix type="matrix" values="0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0.25 0"/>
<feBlend mode="normal" in2="BackgroundImageFix" result="effect1_dropShadow"/>
<feBlend mode="normal" in="SourceGraphic" in2="effect1_dropShadow" result="shape"/>
</filter>
<linearGradient id="badge_bg" x1="__GRADIENT_X__" y1="0" x2="__GRADIENT_X__" y2="56" gradientUnits="userSpaceOnUse">
<stop stop-color="__BG_START__"/>
<stop offset="1" stop-color="__BG_END__"/>
</linearGradient>
__CLIP__
</defs>
</svg>
`

// 图标组：与上游模板同尺寸，绘制在 x12 y8 的 40x40 区域内。
const (
	badgeIconCurseForge = `<g clip-path="url(#badge_icon_clip)"><path d="M42.5432 23.3372C42.5432 23.3372 50.7101 22.0441 52 18.2718H39.489V15.2785H12L15.3863 19.2249V23.2682C15.3863 23.2682 23.9308 22.822 27.236 25.3391C31.7601 29.5506 22.1475 35.2434 22.1475 35.2434L20.4992 40.7215C23.0768 38.2566 27.9894 35.0679 36.9965 35.2215C33.5689 36.3094 30.1224 38.0088 27.4392 40.7215H45.6471L43.9325 35.2434C43.9325 35.2434 30.7359 27.4283 42.5432 23.3379V23.3372Z" fill="#F16436"/></g>`
	badgeIconGitHub     = `<g clip-path="url(#badge_icon_clip)"><path fill-rule="evenodd" clip-rule="evenodd" d="M32 8C43.05 8 52 16.7933 52 27.65C51.9983 36.095 46.5167 43.595 38.375 46.295C37.375 46.4917 37 45.8783 37 45.3617C37 44.6983 37.025 42.5867 37.025 39.9583C37.025 38.1167 36.4 36.9367 35.675 36.3233C40.125 35.8317 44.8 34.1617 44.8 26.62C44.8 24.4583 44.025 22.7133 42.75 21.3383C42.95 20.8467 43.65 18.8333 42.55 16.13C42.55 16.13 40.875 15.59 37.05 18.145C35.4205 17.7011 33.7388 17.478 32.05 17.4817C30.35 17.4817 28.65 17.7033 27.05 18.145C23.225 15.615 21.55 16.13 21.55 16.13C20.45 18.8333 21.15 20.8467 21.35 21.3383C20.075 22.7133 19.3 24.4833 19.3 26.62C19.3 34.1367 23.95 35.8317 28.4 36.3233C27.825 36.8133 27.3 37.6733 27.125 38.9517C25.975 39.4683 23.1 40.3017 21.3 37.33C20.925 36.74 19.8 35.2917 18.225 35.315C16.55 35.34 17.55 36.2483 18.25 36.6167C19.1 37.0833 20.075 38.8283 20.3 39.3933C20.7 40.4983 22 42.61 27.025 41.7017C27.025 43.3483 27.05 44.895 27.05 45.3617C27.05 45.8783 26.675 46.4683 25.675 46.295C17.505 43.6233 11.995 36.1117 12 27.6517C12 16.7933 20.95 8 32 8Z" fill="white"/></g>`
	// issue 图标取自 Octicons issue-opened，PR 图标取自 Octicons git-pull-request，
	// 都是 24x24 路径，用 translate(12 8) scale(1.6666667) 铺满 40x40 图标区。
	//
	// 注意：clip-path 与 transform 必须分写在两层 <g> 上。若写在同一个元素上，
	// clipPath 会在该元素自身的 transform 之前解析（浏览器实测行为），
	// 裁剪区不会跟着图形一起平移，图标会被裁掉大半。
	badgeIconIssue = `<g clip-path="url(#badge_icon_clip)"><g transform="translate(12 8) scale(1.6666667)"><path fill="#3FB950" d="M12 1c6.075 0 11 4.925 11 11s-4.925 11-11 11S1 18.075 1 12 5.925 1 12 1ZM2.5 12a9.5 9.5 0 0 0 9.5 9.5 9.5 9.5 0 0 0 9.5-9.5A9.5 9.5 0 0 0 12 2.5 9.5 9.5 0 0 0 2.5 12Zm9.5 2a2 2 0 1 1-.001-3.999A2 2 0 0 1 12 14Z"/></g></g>`
	badgeIconPR    = `<g clip-path="url(#badge_icon_clip)"><g transform="translate(12 8) scale(1.6666667)"><path fill="#A371F7" d="M16 19.25a3.25 3.25 0 1 1 6.5 0 3.25 3.25 0 0 1-6.5 0Zm-14.5 0a3.25 3.25 0 1 1 6.5 0 3.25 3.25 0 0 1-6.5 0Zm0-14.5a3.25 3.25 0 1 1 6.5 0 3.25 3.25 0 0 1-6.5 0ZM4.75 3a1.75 1.75 0 1 0 .001 3.501A1.75 1.75 0 0 0 4.75 3Zm0 14.5a1.75 1.75 0 1 0 .001 3.501A1.75 1.75 0 0 0 4.75 17.5Zm14.5 0a1.75 1.75 0 1 0 .001 3.501 1.75 1.75 0 0 0-.001-3.501Z"/><path fill="#A371F7" d="M13.405 1.72a.75.75 0 0 1 0 1.06L12.185 4h4.065A3.75 3.75 0 0 1 20 7.75v8.75a.75.75 0 0 1-1.5 0V7.75a2.25 2.25 0 0 0-2.25-2.25h-4.064l1.22 1.22a.75.75 0 0 1-1.061 1.06l-2.5-2.5a.75.75 0 0 1 0-1.06l2.5-2.5a.75.75 0 0 1 1.06 0ZM4.75 7.25A.75.75 0 0 1 5.5 8v8A.75.75 0 0 1 4 16V8a.75.75 0 0 1 .75-.75Z"/></g></g>`

	// license 没有对应的 devins-badges 图标（上游没有 license 分类），
	// 这里自绘一个「天平」表示许可证，几何按 40x40 图标区直接给出。
	badgeIconScale = `<g clip-path="url(#badge_icon_clip)" fill="none" stroke="#D8C7A8" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">` +
		`<path d="M32 15v19"/><path d="M26 34.6h12"/><path d="M32 15l-11 3.2M32 15l11 3.2"/>` +
		`<path d="M21 18.2L15.5 30M21 18.2L26.5 30"/><path d="M14.6 30h12.8a6.4 6.4 0 0 1-12.8 0Z"/>` +
		`<path d="M43 18.2L37.5 30M43 18.2L48.5 30"/><path d="M36.6 30h12.8a6.4 6.4 0 0 1-12.8 0Z"/></g>`
)

// 图标区几何：所有图标都绘制在 x12 y8 起的 40x40 区域内，超出部分由 clipPath 裁掉。
// 测试会拿这些常量校验每个图标的 transform 是否把图形正好映射进该区域。
const (
	badgeIconSize   = 40
	badgeIconLeft   = 12
	badgeIconTop    = 8
	badgeIconRight  = badgeIconLeft + badgeIconSize
	badgeIconBottom = badgeIconTop + badgeIconSize
)

// badgeIconClip 定义图标裁剪区域。
const badgeIconClip = `<clipPath id="badge_icon_clip"><rect width="40" height="40" fill="white" transform="translate(12 8)"/></clipPath>`

// ========== 数值格式化 ==========

// insertThousandsSeparators 只作用于纯数字串，因此可直接按位置插入分隔符。
func insertThousandsSeparators(digits string) string {
	if len(digits) <= 3 {
		return digits
	}
	var b strings.Builder
	b.Grow(len(digits) + len(digits)/3)
	first := len(digits) % 3
	if first == 0 {
		first = 3
	}
	b.WriteString(digits[:first])
	for i := first; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// formatBadgeCount 输出带千位分隔符的完整数字（134724 → "134,724"）。
// 刻意不用 K/M/B 缩写：下载量要能一眼读准确值。负数按 0 处理。
func formatBadgeCount(n int64) string {
	if n < 0 {
		return "0"
	}
	return insertThousandsSeparators(strconv.FormatInt(n, 10))
}

// escapeBadgeXML 转义要嵌入 SVG 文本节点的值。
func escapeBadgeXML(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(value)
}

// formatBadgeCoord 输出坐标，整数不带小数位。
func formatBadgeCoord(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// ========== SVG 组装 ==========

// badgeSpec 描述一枚徽章的渲染参数。
type badgeSpec struct {
	Title      string // 第一行文案
	Value      string // 第二行数值（已格式化）
	ValueColor string // 数值颜色（同时作为强调色）
	Icon       string // 图标组标记，空表示无图标
	BgStart    string // 背景渐变起始色
	BgEnd      string // 背景渐变结束色
	// TextLeft 覆盖文字块左边界（0 = 按是否有图标自动决定）。
	// 左侧放较宽的模组 logo 时需要右移，避免压住图片。
	// TextWidth 覆盖按文本长度自动估算的宽度（0 = 自动），用于宽度已知的情况。
	TextLeft  int
	TextWidth int
	// Extra 追加到文字之后的 SVG 片段（位于同一个 <g> 内）。
	// 用于在数值行右侧排布 <icon><count> 组合；坐标以徽章画布左上角为原点。
	Extra string
	// ValueTS 完全接管数值行的 <tspan> 内容（空 = 用 Value 生成一段普通文字）。
	// 需要在一行里混排文字与图标时使用。
	ValueTS string
}

// renderBadgeTemplate 按内容计算画布宽度并填充模板。
// 有图标时基准 251px（与上游模板一致），无图标时基准 195px；
// 文字超出基准宽度时画布随之增长，描边、投影区域与渐变中点同步跟随。
func renderBadgeTemplate(spec badgeSpec) string {
	textLeft := badgeTextLeftPlain
	baseWidth := badgeWidthNoIcon
	if spec.Icon != "" {
		textLeft = badgeTextLeft
		baseWidth = badgeWidthWithIcon
	}
	if spec.TextLeft > 0 {
		textLeft = spec.TextLeft
	}
	// 粗略估算文本宽度（Inter 在 16/17px 下的平均字宽），仅用于决定画布宽度。
	// 系数比字面平均值略宽松，宁可多留白也不裁字。
	content := float64(spec.TextWidth)
	if content <= 0 {
		titleWidth := float64(len([]rune(spec.Title))) * float64(badgeFontSizeTitle) * 0.52
		valueWidth := float64(len([]rune(spec.Value))) * float64(badgeFontSizeValue) * 0.55
		content = titleWidth
		if valueWidth > content {
			content = valueWidth
		}
	}
	width := baseWidth
	if needed := textLeft + int(content+0.999) + badgePaddingRight; needed > width {
		width = needed
	}

	icon, clip := "", ""
	if spec.Icon != "" {
		icon, clip = spec.Icon, badgeIconClip
	}
	// 投影滤镜区域必须从左边界左侧一点开始，否则文字会落在投影之外。
	shadowX := float64(textLeft) - 5.6
	shadowWidth := float64(width) - shadowX - 6.4

	// 数值行：默认就是一段文字；调用方可用 ValueTS 完全接管（自行排布内容）。
	valueTS := `<tspan x="0" y="15.1818">` + escapeBadgeXML(spec.Value) + `</tspan>`
	if spec.ValueTS != "" {
		valueTS = spec.ValueTS
	}

	return strings.NewReplacer(
		"__W__", strconv.Itoa(width),
		"__BORDER__", strconv.FormatFloat(float64(width)-2.1, 'f', 2, 64),
		"__ICON__", icon,
		"__CLIP__", clip,
		"__EXTRA__", spec.Extra,
		"__TEXT_LEFT__", strconv.Itoa(textLeft),
		"__TITLE__", escapeBadgeXML(spec.Title),
		"__VALUE_TS__", valueTS,
		"__VALUE__", escapeBadgeXML(spec.Value),
		"__VALUE_COLOR__", spec.ValueColor,
		"__BG_START__", spec.BgStart,
		"__BG_END__", spec.BgEnd,
		"__SHADOW_X__", formatBadgeCoord(shadowX),
		"__SHADOW_W__", formatBadgeCoord(shadowWidth),
		"__GRADIENT_X__", formatBadgeCoord(float64(width)/2),
	).Replace(badgeTemplateBody)
}

// ========== HTTP 处理 ==========

// writeBadgeSVG 输出通过模板渲染的 SVG 徽章。
func writeBadgeSVG(w http.ResponseWriter, r *http.Request, svg string) {
	writeBadgeBytes(w, r, []byte(svg))
}

// writeBadgeBytes 输出一枚徽章，并带上强校验器（ETag）与 no-cache。
//
// 徽章是纯函数式的（同一份缓存 + 同一段代码 → 同一张图），但代码本身会变。
// 如果响应不带任何缓存头，浏览器会按启发式规则直接复用旧图，改完代码刷新页面
// 看到的还是旧徽章；带上 no-cache 后浏览器每次都会回来校验，内容没变则 304，
// 变了立刻换新。
func writeBadgeBytes(w http.ResponseWriter, r *http.Request, body []byte) {
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)

	if r != nil {
		// 只在客户端回传的就是这一版时走 304（多个 ETag 用逗号分隔）。
		for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			if candidate == etag || candidate == "*" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	_, _ = w.Write(body)
}

// serveBadgeLink 输出一个指向 target 的极简 HTML 跳转页。
// 徽章本身是图片，无法携带链接，因此「打开 Discord」「打开文档」类徽章
// 需要一个可点击的落地地址：把它作为徽章的 href 即可。
func serveBadgeLink(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	target = htmlEscape(target)
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta http-equiv="refresh" content="0;url=` + target + `">
<title>Redirecting…</title></head>
<body style="margin:0;display:grid;place-items:center;min-height:100vh;background:#14161a;color:#e8e8e8;font:14px/1.6 system-ui,sans-serif">
<p>Redirecting to <a style="color:#7cc4ff" href="` + target + `">` + target + `</a>…</p>
</body></html>
`))
}

// htmlEscape 转义要嵌入 HTML 文本/属性中的值。
func htmlEscape(value string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	).Replace(value)
}
