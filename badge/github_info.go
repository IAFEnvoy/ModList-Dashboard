package badge

// ========== GitHub 仓库元数据（徽章用） ==========

// GitHubItem 单条 issue / PR。
type GitHubItem struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	State       string   `json:"state"`
	StateReason string   `json:"state_reason,omitempty"` // completed / not_planned / reopened
	Draft       bool     `json:"draft"`
	Merged      bool     `json:"merged"`
	URL         string   `json:"url"`
	CreatedAt   string   `json:"created_at"`
	User        string   `json:"user"`
	Labels      []string `json:"labels"`
	Archived    bool     `json:"archived"`
}

// GitHubInfo 一个仓库的 issue / PR 汇总，也是 gh2: 缓存的载荷类型。
type GitHubInfo struct {
	OpenIssues   int          `json:"open_issues"`
	OpenPRs      int          `json:"open_prs"`
	Issues       []GitHubItem `json:"issues"`
	PullRequests []GitHubItem `json:"pull_requests"`

	// 下面的计数由全部条目派生，供徽章按状态细分展示。
	NotPlannedIssues int `json:"not_planned_issues,omitempty"` // 已关闭且 reason=not_planned
	ClosedIssues     int `json:"closed_issues,omitempty"`      // 已关闭但未标注 reason 的兜底
	DraftPRs         int `json:"draft_prs,omitempty"`          // 开放且为 draft
	ClosedPRs        int `json:"closed_prs,omitempty"`         // 已关闭且未合并
}

// CountIssueStates 按状态统计 issue：open / completed / not_planned / closed。
// 与前端 core.js 的 getGhStateIcon 保持同一套判定，避免两端口径不一致。
func (g *GitHubInfo) CountIssueStates() (open, completed, notPlanned, closed int) {
	for _, item := range g.Issues {
		switch {
		case item.State != "closed":
			open++
		case item.StateReason == "completed":
			completed++
		case item.StateReason == "not_planned":
			notPlanned++
		default:
			closed++
		}
	}
	return
}

// CountPRStates 按状态统计 PR：open / merged / draft / closed。
// merged 优先于 draft/open（已合并的 PR 不再算 draft），与前端一致。
func (g *GitHubInfo) CountPRStates() (open, merged, draft, closed int) {
	for _, item := range g.PullRequests {
		switch {
		case item.Merged:
			merged++
		case item.Draft && item.State == "open":
			draft++
		case item.State == "open":
			open++
		default:
			closed++
		}
	}
	return
}

// CountClosedIssueStates 把「已关闭」按结束原因分成两类：
// notPlanned 是 GitHub 上标为 not planned 的，closed 是其余（completed 之外的兜底）。
//
// 这是 CountIssueStates 第二、四位的口径。两者分开存是因为缓存载荷里要保留
// 原始细分；但徽章上这两个状态共用同一个灰色叉号图标，展示时会合并成一个数字。
func (g *GitHubInfo) CountClosedIssueStates() (notPlanned, closed int) {
	for _, item := range g.Issues {
		if item.State != "closed" {
			continue
		}
		switch item.StateReason {
		case "completed":
		case "not_planned":
			notPlanned++
		default:
			closed++
		}
	}
	return
}

// RefreshDerivedCounts 重新计算派生计数（数据拉取完成后调用一次）。
func (g *GitHubInfo) RefreshDerivedCounts() {
	if g == nil {
		return
	}
	g.NotPlannedIssues, g.ClosedIssues = g.CountClosedIssueStates()
	_, _, g.DraftPRs, g.ClosedPRs = g.CountPRStates()
}
