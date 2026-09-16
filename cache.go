package main

import (
	"encoding/json"
	"fmt"
	"log"
	"modlist-dashboard/badge"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// attemptLabel 重试标记
func attemptLabel(attempt int) string {
	if attempt <= 1 {
		return ""
	}
	return fmt.Sprintf(" 第%d次", attempt)
}

// ========== 内存缓存 ==========

type cacheEntry struct {
	data       interface{}
	expireTime time.Time
}

var (
	cache   = make(map[string]cacheEntry)
	cacheMu sync.RWMutex
)

func readCache(key string) (interface{}, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	entry, ok := cache[key]
	if !ok || time.Now().After(entry.expireTime) {
		return nil, false
	}
	return entry.data, true
}

func readCacheStale(key string) (interface{}, bool, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	entry, ok := cache[key]
	if !ok {
		return nil, false, false
	}
	stale := time.Now().After(entry.expireTime)
	return entry.data, true, stale
}

func writeCache(key string, val interface{}, ttl time.Duration) {
	cacheMu.Lock()
	cache[key] = cacheEntry{data: val, expireTime: time.Now().Add(ttl)}
	cacheMu.Unlock()
	go saveCacheToDisk(key, val, ttl)
}

func writeCacheDirect(key string, val interface{}, ttl time.Duration) {
	cacheMu.Lock()
	cache[key] = cacheEntry{data: val, expireTime: time.Now().Add(ttl)}
	cacheMu.Unlock()
}

// writeCacheBool 缓存 bool 值（用于 archived / commit 等）
func writeCacheBool(key string, val bool, ttl time.Duration) {
	writeCache(key, val, ttl)
}

// writeCacheString 缓存 string 值（用于 commit hash）
func writeCacheString(key string, val string, ttl time.Duration) {
	writeCache(key, val, ttl)
}

// ========== 按键去重的后台刷新 ==========

// refreshingKeys 记录正在后台刷新的缓存键，保证同一条缓存不会被并发重复拉取。
var (
	refreshingMu   sync.Mutex
	refreshingKeys = make(map[string]bool)
)

// refreshCacheAsync 在后台刷新单条缓存，同一个键的并发调用只会真正执行一次。
// 与 prewarmCache 的全量预热不同，这里只处理调用方指定的那一个键。
func refreshCacheAsync(key string, fetch func()) {
	refreshingMu.Lock()
	if refreshingKeys[key] {
		refreshingMu.Unlock()
		return
	}
	refreshingKeys[key] = true
	refreshingMu.Unlock()

	go func() {
		defer func() {
			refreshingMu.Lock()
			delete(refreshingKeys, key)
			refreshingMu.Unlock()
		}()
		fetch()
	}()
}

// ========== TTL / 路径 ==========

func ttlCache() time.Duration {
	ttl := time.Duration(config.Server.CacheTTLMin) * time.Minute
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	return ttl
}

// cacheFilePath 生成分层缓存文件路径。
// cf:12345            → cache/curseforge/12345.json
// gh2:owner/repo      → cache/github/owner/repo/meta2.json
// ghdl:owner/repo     → cache/github/owner/repo/downloads.json
// codacy:owner/repo   → cache/codacy/owner/repo/score.json
// codacy_added:o/r    → cache/codacy/owner/repo/added.json
//
// gh 缓存键带版本号（当前 gh2）：条目里除了 open 计数还派生了
// not_planned / closed / draft 计数，旧版本的条目没有这些字段，
// 靠换键让旧数据自然失效，而不是读到一堆 0。
func cacheFilePath(key string) string {
	base := config.Server.cacheDir()

	// CurseForge — flat under platform dir
	switch {
	case strings.HasPrefix(key, "cf:"):
		return filepath.Join(base, "curseforge", key[3:]+".json")
	case strings.HasPrefix(key, badge.CacheKeyPrefix):
		repo := key[len(badge.CacheKeyPrefix):] // "owner/repo"
		return filepath.Join(base, "github", filepath.FromSlash(repo), "meta2.json")
	case strings.HasPrefix(key, "ghdl:"):
		repo := key[5:] // "owner/repo"
		return filepath.Join(base, "github", filepath.FromSlash(repo), "downloads.json")
	case strings.HasPrefix(key, "codacy:"):
		repo := key[7:]
		return filepath.Join(base, "codacy", filepath.FromSlash(repo), "score.json")
	case strings.HasPrefix(key, "codacy_added:"):
		repo := key[13:]
		return filepath.Join(base, "codacy", filepath.FromSlash(repo), "added.json")
	}

	// fallback
	name := strings.NewReplacer("/", "_", ":", "_", "\\", "_", " ", "_").Replace(key)
	return filepath.Join(base, "misc", name+".json")
}

// ========== 缓存策略 ==========

// staleWhileRevalidate 异步刷新：命中直接返回，过期返回旧值+后台刷新，未命中返回 nil+后台加载。
// 注意：fetchFn 返回 nil 时不会缓存，下次调用会重试（防止失败结果被永久缓存）。
func staleWhileRevalidate(key string, ttl time.Duration, fetchFn func() interface{}) interface{} {
	val, found, stale := readCacheStale(key)
	if found {
		// 缓存中的 nil 视为未命中（可能是之前 fetch 失败写入的）
		if val == nil {
			found = false
		}
	}
	if found {
		if stale {
			log.Printf("[~] [%s] 缓存已过期，先返回旧数据并后台刷新", key)
			go func() {
				newVal := fetchFn()
				if newVal != nil {
					writeCache(key, newVal, ttl)
				}
			}()
		}
		return val
	}
	go func() {
		newVal := fetchFn()
		if newVal != nil {
			writeCache(key, newVal, ttl)
		}
	}()
	return nil
}

// staleWhileRevalidateInt int64 版本
func staleWhileRevalidateInt(key string, ttl time.Duration, fetchFn func() int64) interface{} {
	val, found, stale := readCacheStale(key)
	if found {
		if stale {
			log.Printf("[~] [%s] 缓存已过期，先返回旧数据并后台刷新", key)
			go func() {
				newVal := fetchFn()
				writeCache(key, newVal, ttl)
			}()
		}
		return val
	}
	go func() {
		newVal := fetchFn()
		writeCache(key, newVal, ttl)
	}()
	return int64(0)
}

// ========== 磁盘持久化 ==========

func loadCacheFromDisk() int {
	dir := config.Server.cacheDir()
	os.MkdirAll(dir, 0755)
	var walk func(path string) int
	walk = func(path string) int {
		entries, err := os.ReadDir(path)
		if err != nil {
			return 0
		}
		count := 0
		for _, e := range entries {
			fp := filepath.Join(path, e.Name())
			if e.IsDir() {
				count += walk(fp)
				continue
			}
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			var d diskCacheEntry
			if err := json.Unmarshal(data, &d); err != nil {
				continue
			}
			expireTime := time.Unix(d.ExpireAt, 0)
			if time.Now().After(expireTime) {
				os.Remove(fp)
				continue
			}
			key := d.Key
			if key == "" {
				continue
			}
			// 只恢复当前仍受支持的数据源缓存，避免历史平台数据继续进入内存。
			if !strings.HasPrefix(key, "cf:") &&
				!strings.HasPrefix(key, badge.CacheKeyPrefix) &&
				!strings.HasPrefix(key, "ghdl:") &&
				!strings.HasPrefix(key, "codacy:") &&
				!strings.HasPrefix(key, "codacy_added:") {
				_ = os.Remove(fp)
				continue
			}
			ttl := time.Until(expireTime)
			switch d.Type {
			case "int64":
				writeCacheDirect(key, d.IntVal, ttl)
			case "string":
				writeCacheDirect(key, d.StrVal, ttl)
			case "bool":
				writeCacheDirect(key, d.BoolVal, ttl)
			case "github":
				writeCacheDirect(key, d.GhVal, ttl)
			}
			count++
		}
		return count
	}
	return walk(dir)
}

func saveCacheToDisk(key string, val interface{}, ttl time.Duration) {
	p := cacheFilePath(key)
	os.MkdirAll(filepath.Dir(p), 0755)
	var d diskCacheEntry
	d.Key = key
	d.ExpireAt = time.Now().Add(ttl).Unix()
	switch v := val.(type) {
	case int64:
		d.Type = "int64"
		d.IntVal = v
	case string:
		d.Type = "string"
		d.StrVal = v
	case bool:
		d.Type = "bool"
		d.BoolVal = v
	case *badge.GitHubInfo:
		d.Type = "github"
		d.GhVal = v
	default:
		return
	}
	data, err := json.Marshal(d)
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0644)
}
