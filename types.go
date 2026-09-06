package main

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

// ========== 配置结构 ==========

type Config struct {
	// 注意：IgnoreArchived 已移除，归档状态现在通过 status_config.json 的 archived 数组判断
	Server       ServerConfig                `yaml:"server"`
	DataSources  map[string]DataSourceConfig `yaml:"datasources"`
	Retry        RetryConfig                 `yaml:"retry"`
	Codacy       CodacyConfig                `yaml:"codacy"`
	ProjectPaths []string                    `yaml:"project_paths"`
}

// StatusConfig 状态配置（从 config/status_config.json 加载）
type StatusConfig struct {
	Order    []string `json:"order"`
	Archived []string `json:"archived"`
}

var statusConfig StatusConfig

// isStatusArchived 判断给定状态是否为已归档
func isStatusArchived(status string) bool {
	for _, a := range statusConfig.Archived {
		if a == status {
			return true
		}
	}
	return false
}

type CodacyConfig struct {
	Enabled        bool   `yaml:"enabled"`
	Concurrency    int    `yaml:"concurrency"`
	CacheTTLHour   int    `yaml:"cache_ttl_hours"`
	Provider       string `yaml:"provider"`
	AddWaitMinutes int    `yaml:"add_wait_minutes"`
}

func (c CodacyConfig) ttl() time.Duration {
	if c.CacheTTLHour <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(c.CacheTTLHour) * time.Hour
}

func (c CodacyConfig) limiter() chan struct{} {
	n := c.Concurrency
	if n <= 0 {
		n = 2
	}
	return make(chan struct{}, n)
}

func (c CodacyConfig) addWait() time.Duration {
	if c.AddWaitMinutes <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(c.AddWaitMinutes) * time.Minute
}

type RateConfig struct {
	Version               map[string]float64 `yaml:"version"`
	Architecture          map[string]float64 `yaml:"architecture"`
	MultiLoaderMultiplier float64            `yaml:"multi_loader_multiplier"`
}

func (r RateConfig) mlm() float64 {
	if r.MultiLoaderMultiplier <= 0 {
		return 1.5
	}
	return r.MultiLoaderMultiplier
}

type ServerConfig struct {
	Port        int    `yaml:"port"`
	CacheTTLMin int    `yaml:"cache_ttl_minutes"`
	CacheDir    string `yaml:"cache_dir"`
}

func (s ServerConfig) cacheDir() string {
	if s.CacheDir == "" {
		return "cache"
	}
	return s.CacheDir
}

type DataSourceConfig struct {
	Enabled     bool `yaml:"enabled"`
	Concurrency int  `yaml:"concurrency"`
}

func (d DataSourceConfig) limiter() chan struct{} {
	n := d.Concurrency
	if n <= 0 {
		n = 2
	}
	return make(chan struct{}, n)
}

type RetryConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	BaseDelayMs int `yaml:"base_delay_ms"`
}

func (r RetryConfig) attempts() int {
	if r.MaxAttempts <= 0 {
		return 1
	}
	return r.MaxAttempts
}

func (r RetryConfig) delayMs() int {
	if r.BaseDelayMs <= 0 {
		return 1000
	}
	return r.BaseDelayMs
}

type AuthConfig struct {
	CurseForge struct {
		APIKey string `yaml:"api_key"`
	} `yaml:"curseforge"`
	GitHub struct {
		Tokens map[string]string `yaml:"tokens"`
	} `yaml:"github"`
	Codacy struct {
		APIToken string `yaml:"api_token"`
	} `yaml:"codacy"`
	Admin struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"admin"`
}

func (a AuthConfig) gitHubToken(owner string) string {
	t, ok := a.GitHub.Tokens[owner]
	if !ok || t == "" {
		return ""
	}
	return t
}

// ========== 模组元数据 ==========

// FlexStringArray 兼容 JSON 中的字符串或字符串数组
type FlexStringArray []string

func (f *FlexStringArray) UnmarshalJSON(b []byte) error {
	b = []byte(strings.TrimSpace(string(b)))
	if len(b) > 0 && b[0] == '"' {
		var s string
		json.Unmarshal(b, &s)
		*f = []string{s}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(f))
}

type ModMeta struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Logo        string `json:"logo"`
	ModMeta     struct {
		ModID        string          `json:"id"`
		Package      string          `json:"package"`
		License      string          `json:"license"`
		Architecture FlexStringArray `json:"architecture"`
	} `json:"mod_meta"`
	IDs struct {
		GH   string `json:"gh"`
		CF   string `json:"cf"`
		Wiki string `json:"wiki"`
	} `json:"ids"`
	Tags     []string                     `json:"tags"`
	Versions map[string]map[string]string `json:"versions"`
	Status   string                       `json:"status"`
	Coop     []string                     `json:"coop"`
}

// ========== API 响应结构 ==========

type ModDashboard struct {
	ModMeta
	Difficulty     float64     `json:"difficulty"`
	CFDownloads    int64       `json:"cf_downloads"`
	TotalDownloads int64       `json:"total_downloads"`
	Popularity     float64     `json:"popularity"`
	GitHub         *GitHubInfo `json:"github,omitempty"`
	HasCodacy      bool        `json:"has_codacy"`
}

type GitHubInfo struct {
	OpenIssues   int          `json:"open_issues"`
	OpenPRs      int          `json:"open_prs"`
	Issues       []GitHubItem `json:"issues"`
	PullRequests []GitHubItem `json:"pull_requests"`
}

var ProjectPaths []string

func LoadProjectPaths(paths []string) {
	if len(paths) == 0 {
		cwd, _ := os.Getwd()
		paths = []string{cwd}
	}
	ProjectPaths = paths
}

type GitHubItem struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	State       string   `json:"state"`
	StateReason string   `json:"state_reason,omitempty"` // completed / not_planned / reopened (issues)
	Draft       bool     `json:"draft"`
	Merged      bool     `json:"merged"` // PR 是否已合并
	URL         string   `json:"url"`
	CreatedAt   string   `json:"created_at"`
	User        string   `json:"user"`
	Labels      []string `json:"labels"`
	Archived    bool     `json:"archived"`
}

// GitHubIssueDetail 单个 Issue 的完整内容（含 body）
type GitHubIssueDetail struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	State       string   `json:"state"`
	StateReason string   `json:"state_reason,omitempty"`
	HTMLURL     string   `json:"html_url"`
	CreatedAt   string   `json:"created_at"`
	User        string   `json:"user"`
	Labels      []string `json:"labels"`
}

// GitHubPRDetail 单个 PR 的完整内容（含 body + diff）
type GitHubPRDetail struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	State     string   `json:"state"`
	HTMLURL   string   `json:"html_url"`
	CreatedAt string   `json:"created_at"`
	User      string   `json:"user"`
	Labels    []string `json:"labels"`
	Diff      string   `json:"diff"`
	BaseRef   string   `json:"base_ref"`
	HeadRef   string   `json:"head_ref"`
}

type CFResponse struct {
	Data struct {
		DownloadCount int64 `json:"downloadCount"`
	} `json:"data"`
}

type CodacyFileEntry struct {
	Path        string  `json:"path"`
	Complexity  float64 `json:"complexity"`
	Duplication float64 `json:"duplication"`
	TotalIssues int     `json:"totalIssues"`
}

type codacyFilesResponse struct {
	Data        []CodacyFileEntry `json:"data"`
	NextPageURL string            `json:"nextPageUrl"`
}

// ========== 代办事项 ==========

type TodoItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"` // "bug" or "todo"
	Title     string `json:"title"`
	Content   string `json:"content,omitempty"`
	Project   string `json:"project,omitempty"` // 绑定的项目/模组名（可选）
	Resolved  bool   `json:"resolved"`
	CreatedAt string `json:"created_at"`
}

type TodoFileData struct {
	Items []TodoItem `json:"items"`
}

var todoFile = "data/todos.json"

// ========== 辅助函数 ==========

func dsEnabled(key string) bool {
	if ds, ok := config.DataSources[key]; ok {
		return ds.Enabled
	}
	return true
}

func dsConcurrency(key string) int {
	if ds, ok := config.DataSources[key]; ok && ds.Concurrency > 0 {
		return ds.Concurrency
	}
	return 2
}

func isRetryable(code int) bool {
	return code == 502 || code == 503
}

func maskKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// ========== Token 签名 ==========

// ========== 难度计算 ==========

func getDifficulty(id string) float64 {
	difficultyMu.RLock()
	defer difficultyMu.RUnlock()
	if d, ok := difficultyMap[id]; ok {
		return d
	}
	return 0
}

// renameDifficulty 让内存中的难度缓存跟随 mod_id 改名（落盘按主键不受影响）。
func renameDifficulty(oldID, newID string) {
	difficultyMu.Lock()
	defer difficultyMu.Unlock()
	if v, ok := difficultyMap[oldID]; ok {
		difficultyMap[newID] = v
	}
	delete(difficultyMap, oldID)
}

// clearDifficulty 删除某 mod_id 的内存难度缓存（DB 行由外键级联清理）。
func clearDifficulty(id string) {
	difficultyMu.Lock()
	defer difficultyMu.Unlock()
	delete(difficultyMap, id)
}

func finalDifficulty(meta ModMeta) float64 {
	codacy := getDifficulty(meta.ID)
	if codacy <= 0 {
		codacy = 0.1 // 防止 Codacy 为 0 导致整体乘积归零
	}

	// 架构系数：取首个架构的值，未匹配则默认 1.0
	archMult := 0.0
	for _, arch := range meta.ModMeta.Architecture {
		if v, ok := rateConfig.Architecture[arch]; ok {
			archMult += v
		}
	}
	archMult = archMult / float64(len(meta.ModMeta.Architecture))

	// 版本加成：Σ(版本状态值 × 加载器倍数)
	var versionSum float64
	mlm := rateConfig.mlm()

	for _, loaders := range meta.Versions {
		mult := 1.0
		if len(loaders) > 1 {
			mult = mlm
		}
		versionTotal := 0.0
		for _, status := range loaders {
			if v, ok := rateConfig.Version[status]; ok {
				versionTotal += v
			}
		}
		versionSum += mult * versionTotal / float64(len(loaders))
	}

	// 乘积公式：Codacy × Architecture × (1 + Σ)
	result := (codacy + archMult) * (1 + versionSum)
	return math.Round(result*10) / 10
}

// ========== 全局变量 ==========

var (
	config        Config
	authConfig    AuthConfig
	rateConfig    RateConfig
	modMetas      = make(map[string]ModMeta)
	modMetaMu     sync.RWMutex
	difficultyMap = make(map[string]float64)
	difficultyMu  sync.RWMutex
	cfLimiter     chan struct{}
	ghLimiter     chan struct{}
	cdLimiter     chan struct{}
)

type diskCacheEntry struct {
	Key      string      `json:"key"`
	Type     string      `json:"type"`
	IntVal   int64       `json:"int_val,omitempty"`
	StrVal   string      `json:"str_val,omitempty"`
	BoolVal  bool        `json:"bool_val,omitempty"`
	GhVal    *GitHubInfo `json:"gh_val,omitempty"`
	ExpireAt int64       `json:"expire_at"`
}
