package badge

import (
	"fmt"
	"math"
	"strings"
)

// ========== GitHub Issues / Pull Requests 徽章 ==========
//
// 两行结构：
//
//	GitHub Issues
//	[●] 24  [✔] 254  [✖] 140
//
// 第一行是标题，第二行并排显示各状态的「图标 + 数量」——只出图标不出文字，
// 状态含义靠图标本身表达。图标与配色取自 static/img/*.svg
// （前端 core.js 的 getGhStateIcon 用的同一套），因此与看板列表语义一致。

const (
	// ghStateIconSize 状态图标的绘制边长（16x16 的图标按原尺寸绘制）。
	ghStateIconSize = 16
	// ghStateItemGap 同一项内图标与数字之间的间距。
	ghStateItemGap = 6
	// ghStateGroupGap 相邻两项之间的最小间距。
	ghStateGroupGap = 16
	// ghStateMaxGroupGap 相邻两项之间的最大间距。
	//
	// 把剩余空间平均分掉能让整行看起来是排版过的，但项数少时（issue 只有三项）
	// 会把中间的空白拉得很大。因此给间距设上限，剩下的空间宁可留白在右侧。
	ghStateMaxGroupGap = 26
	// ghStateCountSize 数字字号，比正文略小以便和 16px 图标视觉匹配。
	ghStateCountSize = 15
	// ghStateIconTopOffset 把图标的几何中心对齐到数字基线所需的半字高修正。
	//
	// 16px 图标放在 y = 基线 - 8 时中心正好落在基线上；而人眼读数字时看的是字形
	// 中心（约在基线之上半个字高），所以先退这 1px，再叠加下面的抬升量。
	ghStateIconTopOffset = 1
	// ghStateIconLift 在 ghStateIconTopOffset 之外，图标再往上的像素数。
	//
	// 图标是实心方块，数字只有笔画，几何中心对齐会显得图标偏低，因此额外抬高。
	// 总抬升量 = ghStateIconTopOffset + ghStateIconLift。
	ghStateIconLift = 5
)

// ghStateSegment 计数行里的一项。
type ghStateSegment struct {
	Icon  ghStateIcon
	Count int
	Label string // 仅用于测试与日志；徽章上不显示
}

// githubIssueSegments 返回 issue 徽章的各状态计数，顺序与前端列表一致。
//
// 「Not planned」与「Closed」共用同一个灰色叉号，放在一起分两个数字并排会显示成
// 两个一模一样的图标，因此合并成一项。原始细分仍保留在缓存载荷里（见 GitHubInfo）。
func githubIssueSegments(info *GitHubInfo) []ghStateSegment {
	open, completed, notPlanned, closed := info.CountIssueStates()
	return []ghStateSegment{
		{Icon: ghStateIssueOpen, Count: open, Label: "Open"},
		{Icon: ghStateIssueCompleted, Count: completed, Label: "Done"},
		{Icon: ghStateIssueClosed, Count: notPlanned + closed, Label: "Closed"},
	}
}

// githubPRSegments 返回 PR 徽章的各状态计数。
func githubPRSegments(info *GitHubInfo) []ghStateSegment {
	open, merged, draft, closed := info.CountPRStates()
	return []ghStateSegment{
		{Icon: ghStatePROpen, Count: open, Label: "Open"},
		{Icon: ghStatePRMerged, Count: merged, Label: "Merged"},
		{Icon: ghStatePRDraft, Count: draft, Label: "Draft"},
		{Icon: ghStatePRClosed, Count: closed, Label: "Closed"},
	}
}

// ghStateCountWidth 估算计数字符串的宽度，仅用于排布与画布宽度计算。
// 系数按 Inter ExtraBold 在 15px 下的数字宽度取，三位数会略微低估，
// 宁可估宽一点，否则末项的间距会被多算出来的余量顶开。
func ghStateCountWidth(count int) float64 {
	return float64(len(fmt.Sprint(count))) * ghStateCountSize * 0.65
}

// ghStateSegmentWidth 一项占用的总宽度：图标 + 间距 + 数字。
func ghStateSegmentWidth(seg ghStateSegment) float64 {
	return ghStateIconSize + ghStateItemGap + ghStateCountWidth(seg.Count)
}

// ghStatusBadge 渲染 issue / PR 徽章。
// segments 为 nil 表示缓存里还没有数据，此时只显示标题与 N/A。
func ghStatusBadge(title string, segments []ghStateSegment, hit bool) badgeSpec {
	spec := badgeSpec{
		Title:      title,
		Value:      "N/A",
		ValueColor: "#FFFFFF",
		Icon:       badgeIconGitHub,
		BgStart:    "#202020",
		BgEnd:      "#000000",
	}
	if !hit {
		return spec
	}

	// 各项宽度之和：每项后面都跟一个组间距，末项的间距正好充作右内边距。
	contentWidth := 0.0
	for _, seg := range segments {
		contentWidth += ghStateSegmentWidth(seg) + ghStateGroupGap
	}
	usable := float64(badgeWidthWithIcon - badgeTextLeft)
	if usable < contentWidth {
		usable = contentWidth
	}

	// 间距按剩余空间均分，但夹在 [ghStateGroupGap, ghStateMaxGroupGap] 之间：
	// 项数少时（issue 只有三项）不加限制会把中间空白拉得很大，因此宁可让右侧留白。
	gap := float64(ghStateGroupGap)
	if len(segments) > 1 {
		gap = (usable-contentWidth)/float64(len(segments)-1) + ghStateGroupGap
		if gap > ghStateMaxGroupGap {
			gap = ghStateMaxGroupGap
		}
	}
	rowWidth := contentWidth + (gap-float64(ghStateGroupGap))*float64(len(segments)-1)

	// 数值行的基线：与其它徽章一致（文字块 translate y=28.5 + tspan y=15.1818）。
	baseY := 28.5 + 15.1818

	var b strings.Builder
	x := float64(badgeTextLeft)
	for _, seg := range segments {
		// 图标：16x16 坐标系，中心对齐数字基线后再按 ghStateIconLift 上抬
		fmt.Fprintf(&b, `<g transform="translate(%s %s)">%s</g>`,
			formatBadgeCoord(x),
			formatBadgeCoord(baseY-ghStateIconSize/2-ghStateIconTopOffset-ghStateIconLift),
			ghStateIconPath(seg.Icon))
		// 数字：紧跟在图标右侧，用图标自身的颜色
		fmt.Fprintf(&b, `<text x="%s" y="%s" font-size="%d" font-weight="800" fill="%s">%d</text>`,
			formatBadgeCoord(x+ghStateIconSize+ghStateItemGap), formatBadgeCoord(baseY),
			ghStateCountSize, seg.Icon.Fill, seg.Count)

		x += ghStateSegmentWidth(seg) + gap
	}

	// 数值行不再走默认的文字内容：调用方用 Extra 自行排布。
	spec.Value = ""
	spec.ValueTS = `<tspan x="0" y="15.1818"></tspan>`
	// 内容比基准宽度还宽时画布自动加宽（renderBadgeTemplate 只看这个值），
	// 此时 TextWidth 为正、恰好补上多出来的那部分。
	spec.TextWidth = int(math.Ceil(float64(badgeTextLeft) + rowWidth - float64(badgeWidthWithIcon)))
	spec.Extra = b.String()
	return spec
}

// ghStateIconPath 把一个 16x16 图标渲染成当前颜色的 path。
//
// 必须带 fill-rule="evenodd"：这些 Octicon 的内圆/内孔是靠反向绕线的子路径挖出来的，
// 用默认的 nonzero 规则会把孔填实，图标就变成一块实心色块。
func ghStateIconPath(icon ghStateIcon) string {
	return `<path fill-rule="evenodd" clip-rule="evenodd" fill="` + icon.Fill +
		`" d="` + icon.Path + `"/>`
}
