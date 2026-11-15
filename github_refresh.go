package main

import (
	"context"
	"strings"
	"time"

	"modlist-dashboard/badge"
)

var gitHubRefreshWake = make(chan struct{}, 1)

func notifyGitHubRefresh() {
	select {
	case gitHubRefreshWake <- struct{}{}:
	default:
	}
}

// Shared TTL refresh is independent of preprocessing and notification workers.
// Either consumer can request fresh issues without enabling the other module.
func runGitHubTTLRefresh(ctx context.Context) {
	for ctx.Err() == nil {
		timer := time.NewTimer(refreshDueGitHubRepositories())
		select {
		case <-ctx.Done():
		case <-gitHubRefreshWake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// Wake at the next existing cache expiry; newly fetched entries use the current
// configured TTL. The shared cache suppresses duplicate background requests.
func refreshDueGitHubRepositories() time.Duration {
	ttl := ttlCache()
	s := issuePreprocessor
	preprocessing := s != nil && s.enabled()
	if (!preprocessing && !botService.WantsIssues()) || !dsEnabled("github") {
		return ttl
	}
	seedBotIssues()
	next := ttl
	for _, meta := range sortedModMetas() {
		parts := strings.Split(meta.IDs.GH, "/")
		if len(parts) != 2 || isStatusArchived(meta.Status) || authConfig.gitHubToken(parts[0]) == "" {
			continue
		}
		repo := meta.IDs.GH
		key := badge.CacheKeyPrefix + repo
		cacheMu.RLock()
		entry, found := cache[key]
		cacheMu.RUnlock()
		if remaining := time.Until(entry.expireTime); found && entry.data != nil && remaining > 0 {
			if remaining < next {
				next = remaining
			}
		} else {
			refreshCacheAsync(key, func() { refreshGitHubInfo(repo, ttl) })
		}
	}
	return next
}
