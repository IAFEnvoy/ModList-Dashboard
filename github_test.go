package main

import (
	"testing"

	"modlist-dashboard/badge"
)

func TestMarkRepoArchivedUsesCurrentModStatus(t *testing.T) {
	prevArchived := statusConfig.Archived
	prevStatus := statusConfig.Order
	prevMods := modMetas
	statusConfig.Archived = []string{"End Support"}
	live := ModMeta{ID: "live", Status: "Maintaining"}
	live.IDs.GH = "owner/live"
	dead := ModMeta{ID: "dead", Status: "End Support"}
	dead.IDs.GH = "owner/dead"
	modMetaMu.Lock()
	modMetas = map[string]ModMeta{
		"live": live,
		"dead": dead,
	}
	modMetaMu.Unlock()
	t.Cleanup(func() {
		statusConfig.Archived = prevArchived
		statusConfig.Order = prevStatus
		modMetaMu.Lock()
		modMetas = prevMods
		modMetaMu.Unlock()
	})

	info := &badge.GitHubInfo{
		OpenIssues:   99, // 模拟缓存里过期的统计值
		OpenPRs:      99,
		Issues:       []badge.GitHubItem{{Number: 1, State: "open"}, {Number: 2, State: "closed"}, {Number: 3, State: "open"}},
		PullRequests: []badge.GitHubItem{{Number: 3, State: "open"}, {Number: 4, State: "closed"}},
	}

	markRepoArchived(info, "owner/dead")
	if info.OpenIssues != 2 || info.OpenPRs != 1 {
		t.Fatalf("archived repo open counts should reflect real open items: issues=%d prs=%d", info.OpenIssues, info.OpenPRs)
	}
	for i, item := range info.Issues {
		if !item.Archived {
			t.Fatalf("issue %d of archived repo is not marked archived", i)
		}
	}
	for _, item := range info.PullRequests {
		if !item.Archived {
			t.Fatal("PR of archived repo is not marked archived")
		}
	}

	markRepoArchived(info, "owner/live")
	if info.OpenIssues != 2 || info.OpenPRs != 1 {
		t.Fatalf("maintained repo open counts changed unexpectedly: issues=%d prs=%d", info.OpenIssues, info.OpenPRs)
	}
	for i, item := range info.Issues {
		if item.Archived {
			t.Fatalf("issue %d of maintained repo is marked archived", i)
		}
	}

	// 未匹配到任何模组的仓库不应 panic，视为未归档
	markRepoArchived(info, "unknown/repo")
	if info.Issues[0].Archived {
		t.Fatal("unmatched repo should not be archived")
	}
	markRepoArchived(nil, "owner/dead") // 应安全跳过
}
