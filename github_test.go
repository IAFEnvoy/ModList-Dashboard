package main

import "testing"

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

	info := &GitHubInfo{
		Issues:       []GitHubItem{{Number: 1}, {Number: 2}},
		PullRequests: []GitHubItem{{Number: 3}},
	}

	markRepoArchived(info, "owner/dead")
	for i, item := range info.Issues {
		if !item.Archived {
			t.Fatalf("issue %d of archived repo is not marked archived", i)
		}
	}
	if !info.PullRequests[0].Archived {
		t.Fatal("PR of archived repo is not marked archived")
	}

	markRepoArchived(info, "owner/live")
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
