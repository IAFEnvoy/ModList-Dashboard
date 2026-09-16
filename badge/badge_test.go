package badge

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ========== 测试脚手架 ==========
//
// 徽章包不依赖 main：模组元数据、缓存、上游抓取全部注入。测试里用内存假实现，
// 既能精确断言"只刷新了哪一条缓存"，也不碰真实数据库与网络。

var testMu sync.Mutex

// newTestEnv 初始化一套内存假环境，返回可断言的句柄。
type testEnv struct {
	metas     map[string]ModMeta
	logos     map[string][]byte
	logosMime map[string]string
	cache     map[string]CacheEntry
	refreshes map[string]int // key -> 刷新次数
	cfFetches int
	ghFetches int
	// fetchValue 是假抓取返回的值（按 key 前缀区分）。
	fetchValue map[string]interface{}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	env := &testEnv{
		metas:      map[string]ModMeta{},
		logos:      map[string][]byte{},
		logosMime:  map[string]string{},
		cache:      map[string]CacheEntry{},
		refreshes:  map[string]int{},
		fetchValue: map[string]interface{}{},
	}

	Init(Deps{
		Assets:  badgeAssets,
		ModMeta: func(id string) (ModMeta, bool) { m, ok := env.metas[id]; return m, ok },
		ValidID: func(id string) bool {
			if id == "" {
				return false
			}
			for _, r := range id {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
					r == '.' || r == '_' || r == '-' {
					continue
				}
				return false
			}
			return true
		},
		ReadLogo: func(id string) ([]byte, string, bool, error) {
			data, ok := env.logos[id]
			if !ok {
				return nil, "", false, nil
			}
			return data, env.logosMime[id], true, nil
		},
		CacheTTL: func() time.Duration { return time.Hour },
		ReadCache: func(key string) CacheEntry {
			testMu.Lock()
			defer testMu.Unlock()
			e, ok := env.cache[key]
			if !ok {
				return CacheEntry{}
			}
			return e
		},
		WriteCache: func(key string, value interface{}, ttl time.Duration) {
			testMu.Lock()
			defer testMu.Unlock()
			env.cache[key] = CacheEntry{Value: value, Found: true}
		},
		RefreshAsync: func(key string, fetch func()) {
			testMu.Lock()
			env.refreshes[key]++
			testMu.Unlock()
			fetch()
		},
		RefreshGitHub: func(repoPath string, ttl time.Duration) {
			env.ghFetches++
			if v, ok := env.fetchValue[CacheKeyPrefix+repoPath]; ok {
				env.cache[CacheKeyPrefix+repoPath] = CacheEntry{Value: v, Found: true}
			}
		},
		FetchCFDownloads: func(cfID string) int64 {
			env.cfFetches++
			if v, ok := env.fetchValue["cf:"+cfID]; ok {
				if n, ok := v.(int64); ok {
					return n
				}
			}
			return 0
		},
		FetchGHReleaseCount: func(owner, repo string) int64 {
			if v, ok := env.fetchValue["ghdl:"+owner+"/"+repo]; ok {
				if n, ok := v.(int64); ok {
					return n
				}
			}
			return 0
		},
	})

	// 重置调用计数，方便断言"本次请求只刷新了一条"
	env.refreshes = map[string]int{}
	env.cfFetches, env.ghFetches = 0, 0
	t.Cleanup(func() { Init(Deps{}) })
	return env
}

func (e *testEnv) addMod(meta ModMeta) *testEnv {
	e.metas[meta.ID] = meta
	return e
}

func (e *testEnv) setLogo(id string, data []byte, mime string) *testEnv {
	e.logos[id] = data
	e.logosMime[id] = mime
	return e
}

// put 直接写入缓存（模拟已有缓存）。
func (e *testEnv) put(key string, value interface{}, stale bool) *testEnv {
	e.cache[key] = CacheEntry{Value: value, Found: true, Stale: stale}
	return e
}

func (e *testEnv) refreshCount(key string) int {
	testMu.Lock()
	defer testMu.Unlock()
	return e.refreshes[key]
}

// request 直接调用包内处理器。
func request(path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	Handler(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// requestWithHeader 带请求头调用包内处理器。
func requestWithHeader(path, name, value string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(name, value)
	Handler(rr, req)
	return rr
}

func testMod(id, cfID, ghRepo string) ModMeta {
	return ModMeta{ID: id, Name: id, CF: cfID, GH: ghRepo}
}

// webs 造一个最小可解析尺寸的 VP8L webp（仅用于头解析测试）。
func testWebP(width, height int) []byte {
	data := make([]byte, 32)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WEBP")
	copy(data[12:16], "VP8L")
	data[20] = 0x2F
	bits := uint32(width-1) | uint32(height-1)<<14
	data[21], data[22], data[23], data[24] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	return data
}

// ========== SVG 结构解析（校验几何与内容） ==========

type svgNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []svgNode  `xml:",any"`
}

func (n svgNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n svgNode) findTag(tag string) (svgNode, bool) {
	if n.XMLName.Local == tag {
		return n, true
	}
	for _, c := range n.Children {
		if found, ok := c.findTag(tag); ok {
			return found, true
		}
	}
	return svgNode{}, false
}

func (n svgNode) findWhere(tag, attr, value string) (svgNode, bool) {
	if n.XMLName.Local == tag && n.attr(attr) == value {
		return n, true
	}
	for _, c := range n.Children {
		if found, ok := c.findWhere(tag, attr, value); ok {
			return found, true
		}
	}
	return svgNode{}, false
}

func (n svgNode) countTag(tag string) int {
	count := 0
	if n.XMLName.Local == tag {
		count++
	}
	for _, c := range n.Children {
		count += c.countTag(tag)
	}
	return count
}

func parseSVG(t *testing.T, svg string) svgNode {
	t.Helper()
	var root svgNode
	if err := xml.Unmarshal([]byte(svg), &root); err != nil {
		t.Fatalf("badge svg is not well-formed XML: %v\n%s", err, snippet(svg, 300))
	}
	if root.XMLName.Local != "svg" {
		t.Fatalf("root element = %q, want svg", root.XMLName.Local)
	}
	return root
}

func mustFloat(t *testing.T, raw, what string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("cannot parse %s = %q: %v", what, raw, err)
	}
	return v
}

func mustAtoi(t *testing.T, raw string) int {
	t.Helper()
	v, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("cannot parse int %q: %v", raw, err)
	}
	return v
}

func almostEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}

// assertBadgeGeometry 校验画布宽度与各处宽度相关坐标一致。
func assertBadgeGeometry(t *testing.T, svg string, wantIcon bool) {
	t.Helper()
	root := parseSVG(t, svg)

	width := mustFloat(t, root.attr("width"), "svg width")
	if height := mustFloat(t, root.attr("height"), "svg height"); !almostEqual(height, badgeHeight) {
		t.Fatalf("svg height = %v, want %d", height, badgeHeight)
	}
	fields := strings.Fields(root.attr("viewBox"))
	if len(fields) != 4 || fields[0] != "0" || fields[1] != "0" {
		t.Fatalf("viewBox = %q, want '0 0 W 56'", root.attr("viewBox"))
	}
	if vb := mustFloat(t, fields[2], "viewBox width"); !almostEqual(vb, width) {
		t.Fatalf("svg width %v != viewBox width %v", width, vb)
	}

	bg := root.Children[0]
	if bg.XMLName.Local != "rect" {
		t.Fatalf("first child = %q, want rect", bg.XMLName.Local)
	}
	if got := mustFloat(t, bg.attr("width"), "background width"); !almostEqual(got, width) {
		t.Fatalf("background width = %v, want %v", got, width)
	}

	border, ok := root.findWhere("rect", "x", "1.05")
	if !ok {
		t.Fatal("missing border rect")
	}
	if got := mustFloat(t, border.attr("width"), "border width"); !almostEqual(got, width-2.1) {
		t.Fatalf("border width = %v, want %v", got, width-2.1)
	}

	filter, ok := root.findTag("filter")
	if !ok {
		t.Fatal("missing drop-shadow filter")
	}
	shadowX := mustFloat(t, filter.attr("x"), "filter x")
	shadowWidth := mustFloat(t, filter.attr("width"), "filter width")
	if !almostEqual(shadowX+shadowWidth, width-6.4) {
		t.Fatalf("filter right edge = %v, want %v", shadowX+shadowWidth, width-6.4)
	}
	textLeft := float64(badgeTextLeftPlain)
	if wantIcon {
		textLeft = badgeTextLeft
	}
	if !almostEqual(shadowX, textLeft-5.6) {
		t.Fatalf("filter x = %v, want %v", shadowX, textLeft-5.6)
	}

	gradient, ok := root.findTag("linearGradient")
	if !ok {
		t.Fatal("missing background gradient")
	}
	x1 := mustFloat(t, gradient.attr("x1"), "gradient x1")
	x2 := mustFloat(t, gradient.attr("x2"), "gradient x2")
	if !almostEqual(x1, width/2) || !almostEqual(x2, width/2) {
		t.Fatalf("gradient x = %v/%v, want %v", x1, x2, width/2)
	}

	if texts := root.countTag("text"); texts < 2 {
		t.Fatalf("text elements = %d, want >= 2", texts)
	}
	clips := root.countTag("clipPath")
	if wantIcon && clips != 1 {
		t.Fatalf("clipPath count = %d, want 1 for an icon badge", clips)
	}
	if !wantIcon && clips != 0 {
		t.Fatalf("clipPath count = %d, want 0 for an icon-less badge", clips)
	}
}

func snippet(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// ========== 路由与 404 ==========

func TestBadgeUnknownSlugReturns404AndTouchesNothing(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", "owner/known"))

	for _, path := range []string{
		"/api/badge/curseforge/downloads/other-mod",
		"/api/badge/github/downloads/other-mod",
		"/api/badge/github/issues/other-mod",
		"/api/badge/github/prs/other-mod",
		"/api/badge/powered-by/other-mod",
		"/api/badge/license/other-mod",
	} {
		if rr := request(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rr.Code)
		}
	}
	if len(env.cache) != 0 {
		t.Fatalf("unknown slug created %d cache entries, want 0", len(env.cache))
	}
	if env.cfFetches != 0 || env.ghFetches != 0 {
		t.Fatalf("unknown slug triggered upstream work: cf=%d gh=%d", env.cfFetches, env.ghFetches)
	}
}

func TestBadgeRejectsMalformedPaths(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", "owner/known"))

	for _, path := range []string{
		"/api/badge",
		"/api/badge/",
		"/api/badge/curseforge",
		"/api/badge/curseforge/downloads",
		"/api/badge/curseforge/downloads/known-mod/extra",
		"/api/badge/curseforge/unknown/known-mod",
		"/api/badge/unknown/downloads/known-mod",
		"/api/badge/discord/extra",
		"/api/badge/link",
		"/api/badge/link/nope",
		"/api/badge/nope",
	} {
		if rr := request(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rr.Code)
		}
	}
}

func TestBadgeMethodNotAllowed(t *testing.T) {
	newTestEnv(t)
	rr := httptest.NewRecorder()
	Handler(rr, httptest.NewRequest(http.MethodPost, "/api/badge/loader/fabric", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rr.Code)
	}
}

// ========== 数值格式化 ==========

func TestBadgeFormatsCountsWithThousandsSeparators(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", ""))

	cases := []struct {
		count int64
		want  string
	}{
		{0, "0"}, {1, "1"}, {999, "999"}, {1000, "1,000"}, {1500, "1,500"},
		{134724, "134,724"}, {1_000_000, "1,000,000"},
		{8_800_000, "8,800,000"}, {2_500_000_000, "2,500,000,000"},
	}
	for _, tc := range cases {
		env.put("cf:12345", tc.count, false)
		body := request("/api/badge/curseforge/downloads/known-mod").Body.String()
		if !strings.Contains(body, ">"+tc.want+"</tspan>") {
			t.Fatalf("count %d: want %q in svg", tc.count, tc.want)
		}
		// 不允许 K/M/B 缩写
		for _, suffix := range []string{"K</tspan>", "M</tspan>", "B</tspan>"} {
			if strings.Contains(body, suffix) {
				t.Fatalf("count %d: compact notation %q must not be used", tc.count, suffix)
			}
		}
		assertBadgeGeometry(t, body, true)
	}
}

func TestInsertThousandsSeparators(t *testing.T) {
	cases := map[string]string{
		"0": "0", "12": "12", "999": "999", "1000": "1,000", "12345": "12,345",
		"134724": "134,724", "1000000": "1,000,000", "123456789": "123,456,789",
	}
	for in, want := range cases {
		if got := insertThousandsSeparators(in); got != want {
			t.Fatalf("insertThousandsSeparators(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeBadgeXML(t *testing.T) {
	got := escapeBadgeXML(`a&b<c>d"e'f`)
	want := "a&amp;b&lt;c&gt;d&quot;e&apos;f"
	if got != want {
		t.Fatalf("escapeBadgeXML = %q, want %q", got, want)
	}
}

func TestSplitGitHubRepo(t *testing.T) {
	owner, repo, ok := splitGitHubRepo("Anvil-Dev/AnvilCraft")
	if !ok || owner != "Anvil-Dev" || repo != "AnvilCraft" {
		t.Fatalf("splitGitHubRepo = %q %q %v", owner, repo, ok)
	}
	for _, bad := range []string{"", "owner", "owner/", "/repo", "a/b/c"} {
		if _, _, ok := splitGitHubRepo(bad); ok {
			t.Fatalf("splitGitHubRepo(%q) accepted an invalid repo path", bad)
		}
	}
}

// ========== 缓存策略：先返旧值，只刷新这一条 ==========

func TestStaleEntryServesOldValueAndRefreshesOnlyThatKey(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("mod-a", "111", ""))
	env.addMod(testMod("mod-b", "222", ""))

	env.put("cf:111", int64(1234), true) // 已过期
	env.put("cf:222", int64(5678), false)
	env.fetchValue["cf:111"] = int64(9999)

	body := request("/api/badge/curseforge/downloads/mod-a").Body.String()
	if !strings.Contains(body, ">1,234</tspan>") {
		t.Fatalf("stale entry did not serve the previous value: %q", snippet(body, 200))
	}

	if got := env.refreshCount("cf:111"); got != 1 {
		t.Fatalf("cf:111 refreshes = %d, want 1", got)
	}
	if got := env.refreshCount("cf:222"); got != 0 {
		t.Fatalf("cf:222 was refreshed too (%d)", got)
	}
	if env.cfFetches != 1 {
		t.Fatalf("curseforge fetches = %d, want 1", env.cfFetches)
	}
	// mod-b 的缓存必须原封不动
	if e, ok := env.cache["cf:222"]; !ok || e.Value.(int64) != 5678 {
		t.Fatalf("mod-b cache changed: %+v", e)
	}
}

func TestFreshEntryTriggersNoRefresh(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("mod-a", "111", ""))
	env.put("cf:111", int64(1000), false)

	for i := 0; i < 3; i++ {
		body := request("/api/badge/curseforge/downloads/mod-a").Body.String()
		if !strings.Contains(body, ">1,000</tspan>") {
			t.Fatalf("request %d did not serve cached value", i+1)
		}
	}
	if env.cfFetches != 0 || env.refreshCount("cf:111") != 0 {
		t.Fatalf("fresh cache triggered work: fetches=%d refreshes=%d", env.cfFetches, env.refreshCount("cf:111"))
	}
}

func TestMissingEntryRendersNAAndFillsOnlyItsOwnCache(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("mod-a", "111", ""))
	env.addMod(testMod("mod-b", "222", ""))
	env.fetchValue["cf:111"] = int64(4200)

	body := request("/api/badge/curseforge/downloads/mod-a").Body.String()
	if !strings.Contains(body, ">N/A</tspan>") {
		t.Fatalf("empty cache should render N/A: %q", snippet(body, 200))
	}
	if got := env.refreshCount("cf:111"); got != 1 {
		t.Fatalf("cf:111 refreshes = %d, want 1", got)
	}
	if got := env.refreshCount("cf:222"); got != 0 {
		t.Fatalf("mod-b was refreshed without being requested (%d)", got)
	}
	if e, ok := env.cache["cf:111"]; !ok || e.Value.(int64) != 4200 {
		t.Fatalf("refresh did not fill cf:111: %+v", e)
	}
	// 缓存填好后第二次请求返回真实值
	if body := request("/api/badge/curseforge/downloads/mod-a").Body.String(); !strings.Contains(body, ">4,200</tspan>") {
		t.Fatalf("second request did not use the refreshed cache: %q", snippet(body, 200))
	}
}

func TestWithoutPlatformIDReturnsNAAndCachesNothing(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("no-ids", "", ""))

	for _, path := range []string{
		"/api/badge/curseforge/downloads/no-ids",
		"/api/badge/github/downloads/no-ids",
		"/api/badge/github/issues/no-ids",
		"/api/badge/github/prs/no-ids",
	} {
		rr := request(path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), ">N/A</tspan>") {
			t.Fatalf("%s: want N/A for a mod without platform ids", path)
		}
	}
	if len(env.cache) != 0 {
		t.Fatalf("mod without platform ids created %d cache entries", len(env.cache))
	}
	if env.cfFetches != 0 || env.ghFetches != 0 {
		t.Fatalf("mod without ids triggered upstream work")
	}

	// 缺少 repo 的 gh 字段同样只返回 N/A
	env.addMod(testMod("bad-gh", "", "owner-only"))
	if body := request("/api/badge/github/issues/bad-gh").Body.String(); !strings.Contains(body, ">N/A</tspan>") {
		t.Fatal("malformed gh id should render N/A")
	}
}

func TestGitHubRepoCacheIsIndependentFromDownloadsCache(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", "owner/known"))
	env.put(CacheKeyPrefix+"owner/known", testGHInfo(), false)
	env.put("ghdl:owner/known", int64(99), false)

	if body := request("/api/badge/github/issues/known-mod").Body.String(); !strings.Contains(body, ">GitHub Issues</tspan>") {
		t.Fatalf("issues badge did not read the repo cache: %q", snippet(body, 200))
	}
	if body := request("/api/badge/github/downloads/known-mod").Body.String(); !strings.Contains(body, ">99</tspan>") {
		t.Fatalf("downloads badge did not read ghdl: entry: %q", snippet(body, 200))
	}
	if env.ghFetches != 0 || env.cfFetches != 0 {
		t.Fatalf("cached badges triggered upstream work")
	}
}

// ========== 派生计数与 issue/PR 徽章 ==========

// testGHInfo 构造覆盖全部状态的 issue/PR，派生计数由 RefreshDerivedCounts 计算。
func testGHInfo() *GitHubInfo {
	info := &GitHubInfo{
		Issues: []GitHubItem{
			{Number: 1, State: "open"},
			{Number: 2, State: "closed", StateReason: "completed"},
			{Number: 3, State: "closed", StateReason: "not_planned"},
			{Number: 4, State: "closed"},
		},
		PullRequests: []GitHubItem{
			{Number: 1, State: "open"},
			{Number: 2, State: "closed", Merged: true},
			{Number: 3, State: "open", Draft: true},
			{Number: 4, State: "closed"},
		},
	}
	info.OpenIssues, info.OpenPRs = 1, 1
	info.RefreshDerivedCounts()
	return info
}

// ghStatusItem 是从渲染结果里解析出的一个「图标 + 数字」组合。
type ghStatusItem struct {
	IconX  float64
	Fill   string
	Count  int
	HasTag bool
}

// parseGHStatusItems 解析各状态项的图标与数字，按 x 顺序返回。
// 只认「icon 在前、数字紧随其后」的配对，因此断言的是真实配对关系。
func parseGHStatusItems(t *testing.T, svg string) []ghStatusItem {
	t.Helper()
	iconRe := regexp.MustCompile(
		`<g transform="translate\(([\d.]+) [\d.]+\)"><path fill-rule="evenodd" clip-rule="evenodd" fill="(#[0-9A-Fa-f]{6})" d="[^"]*"/></g>`)
	countRe := regexp.MustCompile(
		`<text x="([\d.]+)" y="[\d.]+" font-size="15" font-weight="800" fill="#[0-9A-Fa-f]{6}">(\d+)</text>`)

	type box struct {
		x     float64
		fill  string
		count int
		icon  bool
	}
	var boxes []box
	for _, m := range iconRe.FindAllStringSubmatch(svg, -1) {
		x, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("bad icon x %q: %v", m[1], err)
		}
		boxes = append(boxes, box{x: x, fill: m[2], icon: true})
	}
	for _, m := range countRe.FindAllStringSubmatch(svg, -1) {
		x, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("bad count x %q: %v", m[1], err)
		}
		n, _ := strconv.Atoi(m[2])
		boxes = append(boxes, box{x: x, count: n})
	}
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].x < boxes[j].x })

	var items []ghStatusItem
	for i := 0; i+1 < len(boxes); i++ {
		a, b := boxes[i], boxes[i+1]
		if a.icon && !b.icon {
			if gap := b.x - a.x; gap <= 0 || gap > float64(ghStateIconSize)+float64(ghStateItemGap)+1 {
				t.Fatalf("count x=%.2f does not follow its icon x=%.2f (gap %.2f)", b.x, a.x, gap)
			}
			items = append(items, ghStatusItem{IconX: a.x, Fill: a.fill, Count: b.count, HasTag: true})
			i++
		}
	}
	return items
}

func TestGitHubStatusBadgesShowAllStates(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", "owner/known"))
	env.put(CacheKeyPrefix+"owner/known", testGHInfo(), false)

	issueBody := request("/api/badge/github/issues/known-mod").Body.String()
	if !strings.Contains(issueBody, ">GitHub Issues</tspan>") {
		t.Fatalf("missing issues title: %q", snippet(issueBody, 200))
	}
	// 徽章上不再出现状态文字
	for _, label := range []string{"Open", "Done", "Not planned", "Closed", "Merged", "Draft"} {
		if strings.Contains(issueBody, ">"+label+"</text>") {
			t.Fatalf("issues badge should not contain the label %q", label)
		}
	}
	issueItems := parseGHStatusItems(t, issueBody)
	if len(issueItems) != 3 {
		t.Fatalf("issues badge: got %d icon+count pairs, want 3", len(issueItems))
	}
	// not planned 与 closed 共用同一个灰色叉号，合并成一项：1 + 1 = 2
	wantIssueCounts := []int{1, 1, 2}
	wantIssueFills := []string{ghStateIssueOpen.Fill, ghStateIssueCompleted.Fill, ghStateIssueClosed.Fill}
	for i, item := range issueItems {
		if item.Count != wantIssueCounts[i] {
			t.Fatalf("issues item %d: count = %d, want %d", i, item.Count, wantIssueCounts[i])
		}
		if item.Fill != wantIssueFills[i] {
			t.Fatalf("issues item %d: fill = %s, want %s", i, item.Fill, wantIssueFills[i])
		}
	}
	if n := strings.Count(issueBody, `fill="`+ghStateIssueClosed.Fill+`"`); n != 2 {
		t.Fatalf("issues badge: gray closed styling appears %d times, want 2 (one icon + one count, not two icons)", n)
	}

	prBody := request("/api/badge/github/prs/known-mod").Body.String()
	if !strings.Contains(prBody, ">GitHub Pull Requests</tspan>") {
		t.Fatalf("missing prs title: %q", snippet(prBody, 200))
	}
	prItems := parseGHStatusItems(t, prBody)
	if len(prItems) != 4 {
		t.Fatalf("prs badge: got %d icon+count pairs, want 4", len(prItems))
	}
	wantPRFills := []string{ghStatePROpen.Fill, ghStatePRMerged.Fill, ghStatePRDraft.Fill, ghStatePRClosed.Fill}
	for i, item := range prItems {
		if item.Count != 1 {
			t.Fatalf("prs item %d: count = %d, want 1", i, item.Count)
		}
		if item.Fill != wantPRFills[i] {
			t.Fatalf("prs item %d: fill = %s, want %s", i, item.Fill, wantPRFills[i])
		}
	}

	// 图标必须带 evenodd：这些 Octicon 的孔洞靠反向绕线挖出，缺了会变成实心块
	for name, body := range map[string]string{"issues": issueBody, "prs": prBody} {
		if n := strings.Count(body, `fill-rule="evenodd"`); n < 4 {
			t.Fatalf("%s badge: only %d evenodd paths, want >= 4", name, n)
		}
		if err := xml.Unmarshal([]byte(body), new(svgNode)); err != nil {
			t.Fatalf("%s badge is not well-formed XML: %v", name, err)
		}
		assertBadgeGeometry(t, body, true)
	}

	if env.ghFetches != 0 {
		t.Fatalf("cached status badges triggered %d GitHub fetches", env.ghFetches)
	}
}

func TestGitHubStatusCountsFollowData(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "", "owner/known"))

	info := &GitHubInfo{
		Issues: []GitHubItem{
			{State: "open"}, {State: "open"}, {State: "open"},
			{State: "closed", StateReason: "completed"},
			{State: "closed", StateReason: "not_planned"},
			{State: "closed", StateReason: "not_planned"},
		},
		PullRequests: []GitHubItem{
			{State: "closed", Merged: true},
			{State: "closed", Merged: true},
		},
	}
	info.RefreshDerivedCounts()
	env.put(CacheKeyPrefix+"owner/known", info, false)

	issueItems := parseGHStatusItems(t, request("/api/badge/github/issues/known-mod").Body.String())
	wantIssue := []int{3, 1, 2}
	if len(issueItems) != len(wantIssue) {
		t.Fatalf("issues badge: got %d pairs, want %d", len(issueItems), len(wantIssue))
	}
	for i, want := range wantIssue {
		if issueItems[i].Count != want {
			t.Fatalf("issues item %d: count = %d, want %d", i, issueItems[i].Count, want)
		}
	}

	prItems := parseGHStatusItems(t, request("/api/badge/github/prs/known-mod").Body.String())
	wantPR := []int{0, 2, 0, 0}
	if len(prItems) != len(wantPR) {
		t.Fatalf("prs badge: got %d pairs, want %d", len(prItems), len(wantPR))
	}
	for i, want := range wantPR {
		if prItems[i].Count != want {
			t.Fatalf("prs item %d: count = %d, want %d", i, prItems[i].Count, want)
		}
	}
}

// TestIssueClosedSegmentMergesNotPlanned 守住「not planned 与 closed 合并」：
// 两者图标与颜色完全相同，分开渲染会出现两个一模一样的灰叉，所以徽章上只出一项，
// 数值为两者之和（原始细分仍留存在缓存载荷里）。
func TestIssueClosedSegmentMergesNotPlanned(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "", "owner/known"))

	info := &GitHubInfo{
		Issues: []GitHubItem{
			{State: "open"},
			{State: "closed", StateReason: "completed"},
			{State: "closed", StateReason: "not_planned"},
			{State: "closed", StateReason: "not_planned"},
			{State: "closed", StateReason: "duplicate"},
			{State: "closed"},
		},
	}
	info.RefreshDerivedCounts()
	env.put(CacheKeyPrefix+"owner/known", info, false)

	if info.NotPlannedIssues != 2 || info.ClosedIssues != 2 {
		t.Fatalf("counts = not_planned %d / closed %d, want 2/2", info.NotPlannedIssues, info.ClosedIssues)
	}

	body := request("/api/badge/github/issues/known-mod").Body.String()
	items := parseGHStatusItems(t, body)
	if len(items) != 3 {
		t.Fatalf("issues badge: got %d pairs, want 3 (not planned must not get its own icon)", len(items))
	}
	if items[2].Count != 4 {
		t.Fatalf("gray closed count = %d, want 4 (2 not planned + 2 closed)", items[2].Count)
	}
	if n := strings.Count(body, `fill="`+ghStateIssueClosed.Fill+`"`); n != 2 {
		t.Fatalf("gray closed styling appears %d times, want 2 (one icon + one count)", n)
	}
}

// svgEps SVG 坐标按两位小数输出，比较时留出的舍入余量。
const svgEps = 0.005

// TestGHStatusIconsSitAboveCountBaseline 守住图标的垂直位置：
// 数字基线是文字排版位置，图标以几何中心对齐基线会显得偏低，因此留出上抬量。
func TestGHStatusIconsSitAboveCountBaseline(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "", "owner/known"))
	env.put(CacheKeyPrefix+"owner/known", testGHInfo(), false)

	const baseY = 28.5 + 15.1818
	iconRe := regexp.MustCompile(`<g transform="translate\(([\d.]+) ([\d.]+)\)"><path fill-rule="evenodd"`)
	countRe := regexp.MustCompile(`<text x="[\d.]+" y="([\d.]+)" font-size="15"`)

	for _, path := range []string{"/api/badge/github/issues/known-mod", "/api/badge/github/prs/known-mod"} {
		body := request(path).Body.String()

		icons := iconRe.FindAllStringSubmatch(body, -1)
		if len(icons) == 0 {
			t.Fatalf("%s: no status icons found", path)
		}
		for _, m := range icons {
			top := mustFloat(t, m[2], "icon y")
			center := top + float64(ghStateIconSize)/2
			if center >= baseY {
				t.Fatalf("%s: icon center %.2f is not above the count baseline %.2f", path, center, baseY)
			}
			// 抬升量 = ghStateIconTopOffset + ghStateIconLift，改常数必须同步改文档与测试
			if want := baseY - (ghStateIconTopOffset + ghStateIconLift); math.Abs(center-want) > svgEps {
				t.Fatalf("%s: icon center = %.2f, want %.2f (topOffset %d + lift %d)",
					path, center, want, ghStateIconTopOffset, ghStateIconLift)
			}
		}

		counts := countRe.FindAllStringSubmatch(body, -1)
		if len(counts) != len(icons) {
			t.Fatalf("%s: %d counts for %d icons", path, len(counts), len(icons))
		}
		for _, m := range counts {
			if y := mustFloat(t, m[1], "count y"); math.Abs(y-baseY) > svgEps {
				t.Fatalf("%s: count baseline = %.3f, want %.3f (icons shift, numbers must not)", path, y, baseY)
			}
		}
	}
}

// TestGHStatusRowSpacingIsCapped 守住行内间距：
// 剩余空间均分是有上限的，项数少时（issue 只有三项）不能让中间空出一大片。
// 同时整行必须留在画布内——右侧要能容下末项自身那个当作右内边距的间距。
func TestGHStatusRowSpacingIsCapped(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "", "owner/known"))
	env.put(CacheKeyPrefix+"owner/known", testGHInfo(), false)

	iconRe := regexp.MustCompile(`<g transform="translate\(([\d.]+) [\d.]+\)"><path fill-rule="evenodd"`)
	countRe := regexp.MustCompile(`<text x="([\d.]+)" y="[\d.]+" font-size="15" font-weight="800" fill="#[0-9A-Fa-f]{6}">(\d+)</text>`)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/badge/github/issues/known-mod", 3},
		{"/api/badge/github/prs/known-mod", 4},
	} {
		body := request(tc.path).Body.String()
		width := mustFloat(t, regexp.MustCompile(`<svg width="(\d+)"`).FindStringSubmatch(body)[1], "svg width")

		iconX := []float64{}
		for _, m := range iconRe.FindAllStringSubmatch(body, -1) {
			iconX = append(iconX, mustFloat(t, m[1], "icon x"))
		}
		if len(iconX) != tc.want {
			t.Fatalf("%s: got %d icons, want %d", tc.path, len(iconX), tc.want)
		}

		counts := countRe.FindAllStringSubmatch(body, -1)
		if len(counts) != tc.want {
			t.Fatalf("%s: got %d counts, want %d", tc.path, len(counts), tc.want)
		}

		// 真实间距 = 下一项起点 - 上一项终点（图标 + 数字宽度），不是图标到图标的距离
		for i := 0; i+1 < len(iconX); i++ {
			itemWidth := ghStateIconSize + ghStateItemGap + ghStateCountWidth(mustAtoi(t, counts[i][2]))
			gap := iconX[i+1] - iconX[i] - itemWidth
			if gap < float64(ghStateGroupGap)-svgEps {
				t.Fatalf("%s: gap after item %d = %.2f, want >= %d", tc.path, i, gap, ghStateGroupGap)
			}
			if gap > float64(ghStateMaxGroupGap)+svgEps {
				t.Fatalf("%s: gap after item %d = %.2f, want <= %d (issue 只有三项，不该拉满整行)", tc.path, i, gap, ghStateMaxGroupGap)
			}
		}

		// 末项必须完整留在画布内：数字宽度 + 右内边距
		last := counts[len(counts)-1]
		lastRight := mustFloat(t, last[1], "last count x") + ghStateCountWidth(mustAtoi(t, last[2]))
		if slack := width - lastRight; slack < float64(badgePaddingRight)-svgEps {
			t.Fatalf("%s: only %.2f px after the last count (canvas %.0f), want >= %d", tc.path, slack, width, badgePaddingRight)
		}
	}
}

func TestDerivedGitHubCounts(t *testing.T) {
	info := &GitHubInfo{
		Issues: []GitHubItem{
			{State: "open"},
			{State: "closed", StateReason: "completed"},
			{State: "closed", StateReason: "not_planned"},
			{State: "closed", StateReason: "duplicate"},
			{State: "closed"},
		},
		PullRequests: []GitHubItem{
			{State: "open"},
			{State: "open", Draft: true},
			{State: "closed", Merged: true},
			{State: "closed"},
			{State: "closed", Merged: true, Draft: true}, // merged 优先
		},
	}
	info.RefreshDerivedCounts()

	open, completed, notPlanned, closed := info.CountIssueStates()
	if open != 1 || completed != 1 || notPlanned != 1 || closed != 2 {
		t.Fatalf("issue states = %d/%d/%d/%d, want 1/1/1/2", open, completed, notPlanned, closed)
	}
	prOpen, merged, draft, prClosed := info.CountPRStates()
	if prOpen != 1 || merged != 2 || draft != 1 || prClosed != 1 {
		t.Fatalf("pr states = %d/%d/%d/%d, want 1/2/1/1", prOpen, merged, draft, prClosed)
	}
	if info.NotPlannedIssues != 1 || info.ClosedIssues != 2 || info.DraftPRs != 1 || info.ClosedPRs != 1 {
		t.Fatalf("derived fields not stored: %+v", info)
	}
}

// ========== 响应缓存头 ==========
//
// 徽章内容固定但代码会变：响应不带缓存头时浏览器会按启发式规则直接复用旧图，
// 于是「改完代码刷新页面还是旧徽章」。这里守住 no-cache + ETag 这套组合还在。

func TestBadgeResponsesAreRevalidated(t *testing.T) {
	env := newTestEnv(t)
	// 带 wiki 链接，docs 徽章才会出图
	env.addMod(ModMeta{ID: "known-mod", Name: "known-mod", CF: "12345", GH: "owner/known", Wiki: "https://example.com/wiki"})

	for _, path := range []string{
		"/api/badge/loader/fabric",
		"/api/badge/docs/known-mod",
		"/api/badge/license/known-mod",
		"/api/badge/powered-by/known-mod",
		"/api/badge/curseforge/downloads/known-mod",
	} {
		first := request(path)
		if first.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, first.Code)
		}

		cc := first.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-cache") {
			t.Fatalf("%s: Cache-Control = %q, want it to contain no-cache", path, cc)
		}
		if strings.Contains(cc, "max-age=") {
			t.Fatalf("%s: Cache-Control = %q must not carry a long max-age", path, cc)
		}

		etag := first.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: missing ETag", path)
		}

		again := requestWithHeader(path, "If-None-Match", etag)
		if again.Code != http.StatusNotModified {
			t.Fatalf("%s: status with If-None-Match = %d, want 304", path, again.Code)
		}
		if again.Body.Len() != 0 {
			t.Fatalf("%s: 304 response carried %d bytes", path, again.Body.Len())
		}

		// 另一版 ETag 必须照常返回完整内容
		stale := requestWithHeader(path, "If-None-Match", `"deadbeefdeadbeef"`)
		if stale.Code != http.StatusOK {
			t.Fatalf("%s: status with a foreign ETag = %d, want 200", path, stale.Code)
		}
		if stale.Body.Len() == 0 {
			t.Fatalf("%s: 200 response was empty", path)
		}
	}
}

// TestNeoForgeIconImageIsSized 守住 NeoForge 图标的内嵌图片。
//
// 上游用 <pattern patternContentUnits="objectBoundingBox"> + <image width="128"> 来铺图，
// 但 objectBoundingBox 的内容单位下，带 width/height 的 <image> 只会占 1 个单位，
// 浏览器实际只画出约 1px 的碎点，再被 40x40 的 clipPath 切掉——看起来就是个灰方块。
// 因此生成器把「图案填充矩形」改写成了直接拉伸的 <image>：坐标与尺寸必须落在图标区内，
// 内嵌的也必须是张真图（而不是被缩成 1x1 的占位）。
func TestNeoForgeIconImageIsSized(t *testing.T) {
	if strings.Contains(badgeIconNeoForge, "patternContentUnits") {
		t.Fatal("NeoForge icon still paints through a pattern: the image collapses under objectBoundingBox units")
	}

	imgRe := regexp.MustCompile(`<image\b[^>]*\bx="([\d.]+)"[^>]*\by="([\d.]+)"[^>]*\bwidth="([\d.]+)"[^>]*\bheight="([\d.]+)"[^>]*preserveAspectRatio="none"[^>]*\bxlink:href="data:image/png;base64,([A-Za-z0-9+/=]+)"`)
	m := imgRe.FindStringSubmatch(badgeIconNeoForge)
	if m == nil {
		t.Fatalf("no stretched <image> in the NeoForge icon: %s", snippet(badgeIconNeoForge, 160))
	}

	x := mustFloat(t, m[1], "image x")
	y := mustFloat(t, m[2], "image y")
	w := mustFloat(t, m[3], "image width")
	h := mustFloat(t, m[4], "image height")
	// 坐标是图标区内的局部坐标（外面还有一层 translate(12 8)），所以按 0..40 校验
	if x < 0 || y < 0 {
		t.Fatalf("image starts at (%.2f, %.2f), outside the icon area", x, y)
	}
	if x+w > badgeIconSize+svgEps || y+h > badgeIconSize+svgEps {
		t.Fatalf("image %.1fx%.1f at (%.2f, %.2f) overflows the %dx%d icon area", w, h, x, y, badgeIconSize, badgeIconSize)
	}
	if w != h {
		t.Fatalf("image is %.1fx%.1f, want a square so the logo is not distorted", w, h)
	}

	// 内嵌 PNG 必须是张真图：解析 IHDR 拿回真实像素尺寸
	data, err := base64.StdEncoding.DecodeString(m[5])
	if err != nil {
		t.Fatalf("image data URI is not valid base64: %v", err)
	}
	if len(data) < 24 || string(data[12:16]) != "IHDR" {
		t.Fatalf("embedded image is not a PNG (len=%d)", len(data))
	}
	pw := int(binary.BigEndian.Uint32(data[16:20]))
	ph := int(binary.BigEndian.Uint32(data[20:24]))
	if pw <= 1 || ph <= 1 {
		t.Fatalf("embedded PNG is %dx%d: too small to be the logo", pw, ph)
	}
}

// ========== 状态图标完整性 ==========

func TestGHStateIconsAreComplete(t *testing.T) {
	cases := []struct {
		name     string
		icon     ghStateIcon
		minLen   int
		contains string
	}{
		{"issue_open", ghStateIssueOpen, 122, "8 0a8 8 0 1 1 0 16"},
		{"issue_completed", ghStateIssueCompleted, 191, "M16 8A8 8 0 1 1 0 8"},
		{"issue_closed", ghStateIssueClosed, 206, "M1.5 8a6.5 6.5 0 1 0 13 0"},
		{"pr_open", ghStatePROpen, 412, "-1.5 0V5a1 1 0 0 0-1-1h-1v1.646"},
		{"pr_merged", ghStatePRMerged, 285, "M4.25 13."},
		{"pr_draft", ghStatePRDraft, 387, "ZM3.25 12a.75.75 0 1 0 0 1"},
		{"pr_closed", ghStatePRClosed, 575, ".205.729l-.97.97.97.97"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.icon.Path) < tc.minLen {
				t.Fatalf("path truncated: %d chars, want >= %d", len(tc.icon.Path), tc.minLen)
			}
			if !strings.HasSuffix(strings.TrimSpace(tc.icon.Path), "Z") {
				t.Fatalf("path does not end with Z (tail=%.20q)", tc.icon.Path)
			}
			if !strings.Contains(tc.icon.Path, tc.contains) {
				t.Fatalf("path is missing its distinctive segment %q", tc.contains)
			}
			if !strings.HasPrefix(tc.icon.Fill, "#") || len(tc.icon.Fill) != 7 {
				t.Fatalf("unexpected fill %q", tc.icon.Fill)
			}
		})
	}
}

func TestGHStateIconPathUsesEvenodd(t *testing.T) {
	markup := ghStateIconPath(ghStateIssueOpen)
	if !strings.Contains(markup, `fill-rule="evenodd"`) {
		t.Fatalf("icon markup must carry fill-rule=evenodd: %s", markup)
	}
	if !strings.Contains(markup, ghStateIssueOpen.Path) {
		t.Fatal("icon markup does not embed the full path")
	}
}

// ========== 模板几何 ==========

func TestBadgeGeometryMatchesUpstreamTemplate(t *testing.T) {
	svg := renderBadgeTemplate(badgeSpec{
		Title: "CurseForge Downloads", Value: "134,724", ValueColor: "#F16436",
		Icon: badgeIconCurseForge, BgStart: "#2C130B", BgEnd: "#210D08",
	})
	assertBadgeGeometry(t, svg, true)

	root := parseSVG(t, svg)
	if got := root.attr("width"); got != strconv.Itoa(badgeWidthWithIcon) {
		t.Fatalf("svg width = %q, want %d", got, badgeWidthWithIcon)
	}
	border, _ := root.findWhere("rect", "x", "1.05")
	if got := border.attr("width"); got != "248.90" {
		t.Fatalf("border width = %q, want 248.90 (upstream)", got)
	}
	filter, _ := root.findTag("filter")
	if got := filter.attr("x"); got != "54.40" {
		t.Fatalf("filter x = %q, want 54.40 (upstream)", got)
	}
	if got := filter.attr("width"); got != "190.20" {
		t.Fatalf("filter width = %q, want 190.20 (upstream)", got)
	}
	gradient, _ := root.findTag("linearGradient")
	if got := gradient.attr("x1"); got != "125.50" {
		t.Fatalf("gradient x1 = %q, want 125.50 (upstream)", got)
	}
}

func TestBadgeGeometryStaysConsistentAcrossContent(t *testing.T) {
	specs := []struct {
		name     string
		spec     badgeSpec
		wantIcon bool
	}{
		{"short value", badgeSpec{Title: "Open Issues", Value: "0", ValueColor: "#3FB950", Icon: badgeIconIssue, BgStart: "#202020", BgEnd: "#000000"}, true},
		{"long number", badgeSpec{Title: "CurseForge Downloads", Value: "134,724", ValueColor: "#F16436", Icon: badgeIconCurseForge, BgStart: "#2C130B", BgEnd: "#210D08"}, true},
		{"very long title", badgeSpec{Title: strings.Repeat("Wide ", 20), Value: "1", ValueColor: "#FFFFFF", Icon: badgeIconGitHub, BgStart: "#202020", BgEnd: "#000000"}, true},
		{"no icon", badgeSpec{Title: "Custom", Value: "42", ValueColor: "#FFFFFF", BgStart: "#202020", BgEnd: "#000000"}, false},
	}
	for _, tc := range specs {
		t.Run(tc.name, func(t *testing.T) {
			assertBadgeGeometry(t, renderBadgeTemplate(tc.spec), tc.wantIcon)
		})
	}
}

func TestBadgeWidensForLongContent(t *testing.T) {
	icon := renderBadgeTemplate(badgeSpec{
		Title: "CurseForge Downloads", Value: "1.2M", ValueColor: "#F16436",
		Icon: badgeIconCurseForge, BgStart: "#2C130B", BgEnd: "#210D08",
	})
	if got := parseSVG(t, icon).attr("width"); got != strconv.Itoa(badgeWidthWithIcon) {
		t.Fatalf("icon badge width = %q, want %d", got, badgeWidthWithIcon)
	}

	wide := renderBadgeTemplate(badgeSpec{
		Title: strings.Repeat("X", 40), Value: "1", ValueColor: "#FFFFFF",
		Icon: badgeIconGitHub, BgStart: "#202020", BgEnd: "#000000",
	})
	wideWidth, _ := strconv.Atoi(parseSVG(t, wide).attr("width"))
	if wideWidth <= badgeWidthWithIcon {
		t.Fatalf("long content width = %d, want > %d", wideWidth, badgeWidthWithIcon)
	}
	assertBadgeGeometry(t, wide, true)

	noIcon := renderBadgeTemplate(badgeSpec{
		Title: "Custom", Value: "42", ValueColor: "#FFFFFF", BgStart: "#202020", BgEnd: "#000000",
	})
	if got := parseSVG(t, noIcon).attr("width"); got != strconv.Itoa(badgeWidthNoIcon) {
		t.Fatalf("icon-less badge width = %q, want %d", got, badgeWidthNoIcon)
	}
}

func TestBadgeEscapesTextContent(t *testing.T) {
	svg := renderBadgeTemplate(badgeSpec{
		Title: `a&b<c>"d'`, Value: "1", ValueColor: "#FFFFFF",
		Icon: badgeIconGitHub, BgStart: "#202020", BgEnd: "#000000",
	})
	if err := xml.Unmarshal([]byte(svg), new(svgNode)); err != nil {
		t.Fatalf("escaped svg is not well-formed: %v", err)
	}
	if strings.Contains(svg, "a&b<c>") {
		t.Fatal("raw XML metacharacters leaked into the svg")
	}
	if !strings.Contains(svg, "a&amp;b&lt;c&gt;") {
		t.Fatalf("escaped title missing: %s", snippet(svg, 300))
	}

	cjk := renderBadgeTemplate(badgeSpec{
		Title: "下载量", Value: "42", ValueColor: "#FFFFFF",
		Icon: badgeIconGitHub, BgStart: "#202020", BgEnd: "#000000",
	})
	if !strings.Contains(cjk, "下载量") {
		t.Fatal("cjk title was not preserved verbatim")
	}
	assertBadgeGeometry(t, cjk, true)
}

// ========== 图标几何不变量 ==========

var badgeIcons = map[string]string{
	"curseforge": badgeIconCurseForge,
	"github":     badgeIconGitHub,
	"issue":      badgeIconIssue,
	"pr":         badgeIconPR,
	"scale":      badgeIconScale,
}

var badgeGElementRe = regexp.MustCompile(`<g\b([^>]*)>`)

var badgeIconTransformRe = regexp.MustCompile(
	`translate\(\s*(-?[\d.]+)[\s,]+(-?[\d.]+)\s*\)(?:\s*scale\(\s*(-?[\d.]+)\s*\))?`)

// clip-path 与 transform 不能写在同一个元素上：浏览器解析 clip-path 时用的仍是
// 该元素变换前的坐标系，裁剪区不会跟着图形平移，图标会被裁掉大半。
func TestBadgeIconClipNotOnTransformedElement(t *testing.T) {
	for name, icon := range badgeIcons {
		for _, m := range badgeGElementRe.FindAllStringSubmatch(icon, -1) {
			attrs := m[1]
			if strings.Contains(attrs, "clip-path=") && strings.Contains(attrs, "transform=") {
				t.Fatalf("icon %q: clip-path and transform on the same <g%s>", name, attrs)
			}
		}
	}
}

// transform 必须把图形映射进图标区并铺满。
func TestBadgeIconsFitInsideClipArea(t *testing.T) {
	for name, icon := range badgeIcons {
		transforms := regexp.MustCompile(`transform="([^"]*)"`).FindAllStringSubmatch(icon, -1)
		if len(transforms) == 0 {
			continue
		}
		tx, ty, scale := 0.0, 0.0, 1.0
		for _, tr := range transforms {
			m := badgeIconTransformRe.FindStringSubmatch(tr[1])
			if m == nil {
				t.Fatalf("icon %q: cannot parse transform %q", name, tr[1])
			}
			tx += mustFloat(t, m[1], "translate x") * scale
			ty += mustFloat(t, m[2], "translate y") * scale
			if m[3] != "" {
				s := mustFloat(t, m[3], "scale")
				if s <= 0 {
					t.Fatalf("icon %q: non-positive scale %v", name, s)
				}
				scale *= s
			}
		}
		const srcSize = 24
		left, top := tx, ty
		right, bottom := tx+srcSize*scale, ty+srcSize*scale

		const eps = 0.51
		if left < badgeIconLeft-eps || top < badgeIconTop-eps ||
			right > badgeIconRight+eps || bottom > badgeIconBottom+eps {
			t.Fatalf("icon %q: maps to [%g,%g]x[%g,%g], outside [%d,%d]x[%d,%d]",
				name, left, right, top, bottom,
				badgeIconLeft, badgeIconRight, badgeIconTop, badgeIconBottom)
		}
		if right-left < badgeIconSize-eps || bottom-top < badgeIconSize-eps {
			t.Fatalf("icon %q: mapped size %gx%g does not fill %dx%d",
				name, right-left, bottom-top, badgeIconSize, badgeIconSize)
		}
	}
}

func TestBadgeIconClipMatchesSharedGeometry(t *testing.T) {
	m := regexp.MustCompile(
		`<rect width="(\d+)" height="(\d+)" fill="white" transform="translate\((\d+) (\d+)\)"/>`).
		FindStringSubmatch(badgeIconClip)
	if m == nil {
		t.Fatalf("cannot parse badgeIconClip: %s", badgeIconClip)
	}
	if w, h := mustFloat(t, m[1], "w"), mustFloat(t, m[2], "h"); w != badgeIconSize || h != badgeIconSize {
		t.Fatalf("clip size = %vx%v, want %dx%d", w, h, badgeIconSize, badgeIconSize)
	}
	if x, y := mustFloat(t, m[3], "x"), mustFloat(t, m[4], "y"); x != badgeIconLeft || y != badgeIconTop {
		t.Fatalf("clip origin = (%v,%v), want (%d,%d)", x, y, badgeIconLeft, badgeIconTop)
	}
}

// ========== 加载器 / 文档 / Discord / 许可证 / Powered by ==========

func TestLoaderBadgesRenderTwoLines(t *testing.T) {
	env := newTestEnv(t)
	for name, b := range badgeLoaderBadges {
		path := "/api/badge/loader/" + name
		rr := request(path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rr.Code)
		}
		body := rr.Body.String()
		if err := xml.Unmarshal([]byte(body), new(svgNode)); err != nil {
			t.Fatalf("%s: not well-formed XML: %v", path, err)
		}
		if !strings.Contains(body, ">"+b.Title+"</tspan>") {
			t.Fatalf("%s: missing first line %q", path, b.Title)
		}
		if !strings.Contains(body, ">"+b.Value+"</tspan>") {
			t.Fatalf("%s: missing second line %q", path, b.Value)
		}
		if strings.Contains(body, b.Title+" "+b.Value) {
			t.Fatalf("%s: text was not split into two lines", path)
		}
		if accent := badgeAssetStyle[b.Style].Accent; !strings.Contains(body, `fill="`+accent+`"`) {
			t.Fatalf("%s: second line does not use accent %q", path, accent)
		}
		assertBadgeGeometry(t, body, true)
	}
	if env.ghFetches != 0 || env.cfFetches != 0 {
		t.Fatal("loader badges triggered upstream work")
	}
}

func TestLoaderBadgeUnknownIs404(t *testing.T) {
	newTestEnv(t)
	for _, path := range []string{"/api/badge/loader/quilt", "/api/badge/loader/rift", "/api/badge/loader/"} {
		if rr := request(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rr.Code)
		}
	}
}

func TestDocumentationBadgeFollowsModWikiLink(t *testing.T) {
	env := newTestEnv(t)
	withWiki := testMod("with-wiki", "", "")
	withWiki.Wiki = "https://example.com/docs"
	env.addMod(withWiki)
	env.addMod(testMod("no-wiki", "", ""))

	rr := request("/api/badge/docs/with-wiki")
	if rr.Code != http.StatusOK {
		t.Fatalf("docs badge status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">"+badgeDocsBadge.Title+"</tspan>") {
		t.Fatalf("docs badge missing first line %q", badgeDocsBadge.Title)
	}
	if !strings.Contains(body, ">"+badgeDocsBadge.Value+"</tspan>") {
		t.Fatalf("docs badge missing second line %q", badgeDocsBadge.Value)
	}
	if strings.Contains(body, badgeDocsBadge.Title+" "+badgeDocsBadge.Value) {
		t.Fatal("docs badge text was not split into two lines")
	}

	link := request("/api/badge/link/docs/with-wiki")
	if link.Code != http.StatusOK || !strings.Contains(link.Body.String(), "https://example.com/docs") {
		t.Fatalf("docs link bad: %d %q", link.Code, snippet(link.Body.String(), 160))
	}
	if rr := request("/api/badge/docs/no-wiki"); rr.Code != http.StatusNotFound {
		t.Fatalf("docs without wiki status = %d, want 404", rr.Code)
	}
	if rr := request("/api/badge/docs/unknown-mod"); rr.Code != http.StatusNotFound {
		t.Fatalf("docs unknown mod status = %d, want 404", rr.Code)
	}
}

func TestWikiURLNormalization(t *testing.T) {
	cases := map[string]string{
		"https://example.com/docs": "https://example.com/docs",
		"http://example.com":       "http://example.com",
		"example.com/docs":         "https://example.com/docs",
		"//example.com/docs":       "https://example.com/docs",
	}
	for in, want := range cases {
		got, ok := safeHTTPURL(in)
		if !ok || got != want {
			t.Fatalf("safeHTTPURL(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "   ", "javascript:alert(1)", "a b c", "<script>", "ftp://x.com"} {
		if got, ok := safeHTTPURL(bad); ok {
			t.Fatalf("safeHTTPURL(%q) accepted: %q", bad, got)
		}
	}
}

func TestDiscordBadgeRequiresConfiguredInvite(t *testing.T) {
	newTestEnv(t)

	// 未配置：徽章与落地页都 404，避免生成点不动的死链
	if rr := request("/api/badge/discord"); rr.Code != http.StatusNotFound {
		t.Fatalf("unconfigured discord badge = %d, want 404", rr.Code)
	}
	if rr := request("/api/badge/link/discord"); rr.Code != http.StatusNotFound {
		t.Fatalf("unconfigured discord link = %d, want 404", rr.Code)
	}

	// 配置合法链接：两者可用
	d := deps
	d.DiscordInvite = "https://discord.gg/example"
	Init(d)
	if rr := request("/api/badge/discord"); rr.Code != http.StatusOK {
		t.Fatalf("discord badge = %d, want 200", rr.Code)
	}
	rr := request("/api/badge/link/discord")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "https://discord.gg/example") {
		t.Fatalf("discord link bad: %d %q", rr.Code, snippet(rr.Body.String(), 160))
	}

	// 伪协议必须拒绝
	d.DiscordInvite = "javascript:alert(1)"
	Init(d)
	if rr := request("/api/badge/link/discord"); rr.Code != http.StatusNotFound {
		t.Fatalf("javascript: invite = %d, want 404", rr.Code)
	}
}

func TestLicenseBadgeRendersModLicense(t *testing.T) {
	env := newTestEnv(t)
	licensed := testMod("licensed-mod", "", "")
	licensed.License = "LGPL-3.0"
	env.addMod(licensed)
	env.addMod(testMod("unlicensed-mod", "", ""))

	body := request("/api/badge/license/licensed-mod").Body.String()
	if !strings.Contains(body, ">License</tspan>") {
		t.Fatalf("license badge missing title: %q", snippet(body, 200))
	}
	if !strings.Contains(body, ">LGPL-3.0</tspan>") {
		t.Fatalf("license badge missing value: %q", snippet(body, 200))
	}
	assertBadgeGeometry(t, body, true)

	if got := request("/api/badge/license/unlicensed-mod").Body.String(); !strings.Contains(got, ">Unknown</tspan>") {
		t.Fatalf("unlicensed mod should render Unknown: %q", snippet(got, 200))
	}
	if rr := request("/api/badge/license/nope"); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown mod = %d, want 404", rr.Code)
	}
}

func TestLicenseBadgeEscapesText(t *testing.T) {
	env := newTestEnv(t)
	meta := testMod("weird-license", "", "")
	meta.License = `MIT & <BSD>"`
	env.addMod(meta)

	body := request("/api/badge/license/weird-license").Body.String()
	if err := xml.Unmarshal([]byte(body), new(svgNode)); err != nil {
		t.Fatalf("license badge is not well-formed: %v", err)
	}
	if strings.Contains(body, "MIT & <BSD>") {
		t.Fatal("license text was not XML-escaped")
	}
}

func TestPoweredByBadgeShowsModIdentity(t *testing.T) {
	env := newTestEnv(t)
	meta := testMod("logo-mod", "", "")
	meta.Name = "Logo Mod"
	env.addMod(meta)
	env.setLogo("logo-mod", testWebP(256, 256), "image/webp")

	rr := request("/api/badge/powered-by/logo-mod")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">Powered by</tspan>") {
		t.Fatalf("missing 'Powered by' line: %q", snippet(body, 300))
	}
	if !strings.Contains(body, ">Logo Mod</tspan>") {
		t.Fatalf("missing mod name: %q", snippet(body, 300))
	}
	if !strings.Contains(body, "data:image/webp;base64,") {
		t.Fatalf("mod logo was not embedded: %q", snippet(body, 300))
	}
	if strings.Contains(body, ">CurseForge</tspan>") || strings.Contains(body, ">GitHub</tspan>") {
		t.Fatal("powered-by should not show a platform name")
	}
	if n := strings.Count(body, "data:image/webp;base64,"); n != 2 {
		t.Fatalf("expected the data URI on both href and xlink:href, got %d", n)
	}
	assertBadgeGeometry(t, body, true)
}

func TestPoweredByBadgeWithoutLogoStillRenders(t *testing.T) {
	env := newTestEnv(t)
	meta := testMod("no-logo", "", "")
	meta.Name = "No Logo Mod"
	env.addMod(meta)

	rr := request("/api/badge/powered-by/no-logo")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, ">No Logo Mod</tspan>") {
		t.Fatalf("missing mod name: %q", snippet(body, 300))
	}
	if strings.Contains(body, "base64,") {
		t.Fatal("no logo should mean no embedded image")
	}
	assertBadgeGeometry(t, body, false)
}

func TestPoweredByLinkTargetsProjectPage(t *testing.T) {
	env := newTestEnv(t)
	cf := testMod("cf-mod", "12345", "")
	gh := testMod("gh-mod", "", "owner/repo")
	wiki := testMod("wiki-mod", "", "")
	wiki.Wiki = "https://example.com/docs"
	bare := testMod("bare-mod", "", "")
	env.addMod(cf).addMod(gh).addMod(wiki).addMod(bare)

	cases := map[string]string{
		"/api/badge/link/powered-by/cf-mod":   "https://www.curseforge.com/minecraft/mc-mods/12345",
		"/api/badge/link/powered-by/gh-mod":   "https://github.com/owner/repo",
		"/api/badge/link/powered-by/wiki-mod": "https://example.com/docs",
	}
	for path, want := range cases {
		rr := request(path)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), want) {
			t.Fatalf("%s: link does not contain %q", path, want)
		}
	}
	if rr := request("/api/badge/link/powered-by/bare-mod"); rr.Code != http.StatusNotFound {
		t.Fatalf("mod without project link = %d, want 404", rr.Code)
	}
}

func TestPlatformWordmarkBadgesAreGone(t *testing.T) {
	env := newTestEnv(t)
	env.addMod(testMod("known-mod", "12345", "owner/known"))
	for _, path := range []string{
		"/api/badge/curseforge/powered-by/known-mod",
		"/api/badge/github/powered-by/known-mod",
	} {
		if rr := request(path); rr.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404 (wordmark badges were removed)", path, rr.Code)
		}
	}
}

func TestBadgeModNameTruncation(t *testing.T) {
	if got := badgeModName("Avaritia's Delight"); got != "Avaritia's Delight" {
		t.Fatalf("short name was altered: %q", got)
	}
	if got := badgeModName("  padded  "); got != "padded" {
		t.Fatalf("name was not trimmed: %q", got)
	}
	long := strings.Repeat("字", badgeModNameMaxRunes+10)
	got := badgeModName(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long name was not truncated: %q", got)
	}
	if n := len([]rune(got)); n != badgeModNameMaxRunes {
		t.Fatalf("truncated to %d runes, want %d", n, badgeModNameMaxRunes)
	}
}

// ========== 图片尺寸解析 ==========

func TestImageDimensions(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), func() []byte {
		b := make([]byte, 8)
		b[3], b[7] = 64, 48
		return b
	}()...)
	cases := []struct {
		name   string
		data   []byte
		w, h   int
		wantOK bool
	}{
		{"VP8L", testWebP(256, 256), 256, 256, true},
		{"VP8L 非方形", testWebP(120, 80), 120, 80, true},
		{"PNG", png, 64, 48, true},
		{"无法识别", []byte("not an image at all"), 0, 0, false},
		{"空数据", nil, 0, 0, false},
	}
	for _, tc := range cases {
		w, h, ok := imageDimensions(tc.data)
		if ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if ok && (w != tc.w || h != tc.h) {
			t.Fatalf("%s: got %dx%d, want %dx%d", tc.name, w, h, tc.w, tc.h)
		}
	}
}

var _ = fmt.Sprintf
