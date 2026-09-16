package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modlist-dashboard/badge"
)

// ========== 徽章端到端测试（走真实的缓存 / DB / 注入） ==========
//
// 徽章包内部的单元测试在 badge/badge_test.go（用内存假环境）。
// 这里只测真正跨越边界的部分：handler 注册、404 与"不碰缓存"的约束、
// 以及按条刷新时的去重——后者依赖 main 包真实的 refreshCacheAsync 实现。

// TestMain 补齐 main() 才会初始化的并发限流通道。
// 这些通道在 main.go 里由 config 构建，测试二进制不会执行 main()，
// 若保持 nil，fetchCFDownloads 等抓取函数会在发送时永久阻塞。
func TestMain(m *testing.M) {
	prevCF, prevGH, prevCD := cfLimiter, ghLimiter, cdLimiter
	if cfLimiter == nil {
		cfLimiter = make(chan struct{}, 2)
	}
	if ghLimiter == nil {
		ghLimiter = make(chan struct{}, 2)
	}
	if cdLimiter == nil {
		cdLimiter = make(chan struct{}, 2)
	}
	code := m.Run()
	cfLimiter, ghLimiter, cdLimiter = prevCF, prevGH, prevCD
	os.Exit(code)
}

// withTestCacheDir 把缓存目录指向临时目录，避免测试写入真实 cache/。
func withTestCacheDir(t *testing.T) {
	t.Helper()
	prev := config.Server.CacheDir
	config.Server.CacheDir = filepath.Join(t.TempDir(), "cache")
	t.Cleanup(func() { config.Server.CacheDir = prev })
}

// withTestMods 用指定的模组集合替换全局元数据缓存，并在测试结束后还原。
func withTestMods(t *testing.T, metas ...ModMeta) {
	t.Helper()
	prev := modMetas
	modMetaMu.Lock()
	modMetas = make(map[string]ModMeta, len(metas))
	for _, meta := range metas {
		modMetas[meta.ID] = meta
	}
	modMetaMu.Unlock()
	t.Cleanup(func() {
		modMetaMu.Lock()
		modMetas = prev
		modMetaMu.Unlock()
	})
}

// withBadgeDeps 注入徽章依赖（模组数据取自测试用的内存表）。
func withBadgeDeps(t *testing.T) {
	t.Helper()
	initBadge()
	t.Cleanup(func() { badge.Init(badge.Deps{}) })
}

func testMod(id, cfID, ghRepo string) ModMeta {
	meta := ModMeta{ID: id, Name: id}
	meta.IDs.CF = cfID
	meta.IDs.GH = ghRepo
	return meta
}

// requestBadge 直接调用徽章处理器（不过 ServeMux，也不需要 CORS）。
func requestBadge(path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	badgeHandlerForTest(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// TestBadgeUnknownSlugReturns404WithoutTouchingCache 守住最关键的一条约束：
// slug 不匹配任何模组时直接 404，不读也不写任何缓存、不发起任何上游请求。
func TestBadgeUnknownSlugReturns404WithoutTouchingCache(t *testing.T) {
	withTestCacheDir(t)
	withTestMods(t, testMod("known-mod", "12345", "owner/known"))
	withBadgeDeps(t)

	cacheMu.RLock()
	before := len(cache)
	cacheMu.RUnlock()

	for _, path := range []string{
		"/api/badge/curseforge/downloads/other-mod",
		"/api/badge/github/downloads/other-mod",
		"/api/badge/github/issues/other-mod",
		"/api/badge/github/prs/other-mod",
		"/api/badge/powered-by/other-mod",
		"/api/badge/license/other-mod",
	} {
		if rr := requestBadge(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rr.Code)
		}
	}

	cacheMu.RLock()
	after := len(cache)
	cacheMu.RUnlock()
	if after != before {
		t.Fatalf("unknown slug changed the cache: %d -> %d entries", before, after)
	}
}

// TestBadgeRejectsMalformedPathOrKind 校验路由段的健壮性。
func TestBadgeRejectsMalformedPathOrKind(t *testing.T) {
	withTestCacheDir(t)
	withTestMods(t, testMod("known-mod", "12345", "owner/known"))
	withBadgeDeps(t)

	for _, path := range []string{
		"/api/badge",
		"/api/badge/",
		"/api/badge/curseforge",
		"/api/badge/curseforge/downloads",
		"/api/badge/curseforge/downloads/known-mod/extra",
		"/api/badge/curseforge/unknown/known-mod",
		"/api/badge/unknown/downloads/known-mod",
		"/api/badge/loader/quilt",
		"/api/badge/nope",
	} {
		if rr := requestBadge(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rr.Code)
		}
	}
}

// TestRefreshCacheAsyncDeduplicatesInFlightKey 同一条缓存的并发刷新只执行一次。
func TestRefreshCacheAsyncDeduplicatesInFlightKey(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	fetch := func() {
		atomic.AddInt32(&calls, 1)
		once.Do(func() { close(started) })
		<-release
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshCacheAsync("dedupe:test", fetch)
		}()
	}
	wg.Wait()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fetch never started")
	}
	close(release)
	drainRefreshes(t)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("fetch ran %d times for concurrent callers of one key, want 1", got)
	}
}

// TestRefreshCacheAsyncRunsDifferentKeysIndependently 不同键各自刷新，互不阻塞。
func TestRefreshCacheAsyncRunsDifferentKeysIndependently(t *testing.T) {
	var calls int32
	var wg sync.WaitGroup
	for _, key := range []string{"k:1", "k:2", "k:3"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			refreshCacheAsync(k, func() { atomic.AddInt32(&calls, 1) })
		}(key)
	}
	wg.Wait()
	drainRefreshes(t)

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("fetch ran %d times for 3 distinct keys, want 3", got)
	}
}

// TestRefreshCacheAsyncAllowsRetryAfterCompletion 一轮结束后同键可以再来一次。
func TestRefreshCacheAsyncAllowsRetryAfterCompletion(t *testing.T) {
	var calls int32
	done := make(chan struct{})
	refreshCacheAsync("retry:key", func() {
		atomic.AddInt32(&calls, 1)
		close(done)
	})
	<-done
	drainRefreshes(t)

	again := make(chan struct{})
	refreshCacheAsync("retry:key", func() {
		atomic.AddInt32(&calls, 1)
		close(again)
	})
	select {
	case <-again:
	case <-time.After(3 * time.Second):
		t.Fatal("second refresh of the same key never ran")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("fetch ran %d times, want 2", got)
	}
}

// TestBadgeServesCachedDownloadsFromRealCache 走一遍真实缓存读写。
func TestBadgeServesCachedDownloadsFromRealCache(t *testing.T) {
	withTestCacheDir(t)
	withTestMods(t, testMod("known-mod", "12345", ""))
	withBadgeDeps(t)

	writeCacheDirect("cf:12345", int64(1_234_567), time.Hour)

	rr := requestBadge("/api/badge/curseforge/downloads/known-mod")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">1,234,567</tspan>") {
		t.Fatalf("cached value not rendered: %q", body[:min(200, len(body))])
	}
	if !strings.Contains(body, ">CurseForge Downloads</tspan>") {
		t.Fatal("missing title")
	}
}

// drainRefreshes 等待后台刷新结束，避免测试之间互相干扰。
func drainRefreshes(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		refreshingMu.Lock()
		n := len(refreshingKeys)
		refreshingMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	refreshingMu.Lock()
	pending := len(refreshingKeys)
	refreshingMu.Unlock()
	t.Fatalf("%d background refresh(es) never finished", pending)
}
