package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type codacyDiskData struct {
	AvgComplexity  float64 `json:"avg_complexity"`
	AvgDuplication float64 `json:"avg_duplication"`
	AvgIssues      float64 `json:"avg_issues"`
	Score          float64 `json:"score"` // 兼容旧缓存
	Hash           string  `json:"hash"`
	ExpireAt       int64   `json:"expire_at"`
}

// computeCodacyScore 用三个原始指标计算最终 Codacy 分数（0-10）
func computeCodacyScore(avgComplexity, avgDuplication, avgIssues float64) float64 {
	rawScore := avgComplexity*0.4 + avgDuplication*0.3 + avgIssues*0.3
	score := math.Min(10, math.Max(0, rawScore/5))
	return math.Round(score*10) / 10
}

// scoreFromDiskData 从磁盘数据计算分数，兼容旧版只存 score 的缓存
func scoreFromDiskData(d codacyDiskData) float64 {
	if d.AvgComplexity == 0 && d.AvgDuplication == 0 && d.AvgIssues == 0 && d.Score > 0 {
		return d.Score // 旧格式兼容
	}
	return computeCodacyScore(d.AvgComplexity, d.AvgDuplication, d.AvgIssues)
}

func startCodacyWorker() {
	ttl := config.Codacy.ttl()
	metas := sortedModMetas()
	log.Printf("[Codacy] Codacy Worker 启动，共 %d 个模组待分析", len(metas))
	var wg sync.WaitGroup
	var completed atomic.Int32
	var scored atomic.Int32

	ids := make([]string, 0, len(metas))
	for _, meta := range metas {
		ids = append(ids, meta.ID)
	}
	total := len(ids)

	for _, id := range ids {
		wg.Add(1)
		go func(modID string) {
			defer wg.Done()
			meta, ok := getModMeta(modID)
			if !ok {
				return
			}
			if meta.IDs.GH == "" {
				return
			}
			parts := strings.SplitN(meta.IDs.GH, "/", 2)
			if len(parts) != 2 {
				return
			}
			owner, repo := parts[0], parts[1]
			if authConfig.gitHubToken(owner) == "" {
				return
			}

			score := getCodacyScoreWithCache(modID, owner, repo, ttl)
			if score >= 0 {
				difficultyMu.Lock()
				difficultyMap[modID] = score
				difficultyMu.Unlock()
				if err := upsertModDifficulty(modID, score); err != nil {
					log.Printf("[X] [Codacy] 写回 mod_difficulty 失败 (%s): %v", modID, err)
				} else {
					scored.Add(1)
				}
				n := completed.Add(1)
				log.Printf("   [%d/%d] [Codacy] Codacy %s/%s -> 难度 %.1f", n, total, owner, repo, score)
			} else {
				completed.Add(1)
			}
		}(id)
	}
	wg.Wait()
	log.Printf("[Codacy] Codacy 分析完成，已更新 %d 个模组的维护难度", scored.Load())
}

func getCodacyScoreWithCache(modID, owner, repo string, ttl time.Duration) float64 {
	cacheKey := "codacy:" + owner + "/" + repo
	if v, ok := readCache(cacheKey); ok {
		return v.(float64)
	}
	if score, _, ok := readDiskCodacyScore(owner, repo, ttl); ok {
		writeCache(cacheKey, score, ttl)
		return score
	}

	hash := fetchLatestCommitHash(owner, repo)
	if hash == "" {
		return -1
	}

	if oldScore, oldHash, found := readDiskCodacyScore(owner, repo, 365*24*time.Hour); found && oldHash == hash {
		log.Printf("[Codacy] Codacy %s/%s hash 未变，复用旧分数 %.1f", owner, repo, oldScore)
		writeCache(cacheKey, oldScore, ttl)
		return oldScore
	}

	score, avgC, avgD, avgI := fetchCodacyScore(owner, repo)
	if score >= 0 {
		writeCache(cacheKey, score, ttl)
		saveCodacyScoreToDisk(owner, repo, avgC, avgD, avgI, hash, ttl)
		return score
	}

	if score == -2 && authConfig.gitHubToken(owner) != "" {
		addedKey := "codacy_added:" + owner + "/" + repo
		if wasAdded, found, _ := readCacheStale(addedKey); found && wasAdded.(bool) {
			log.Printf("[~] Codacy %s/%s 已添加但尚未分析完成，等待中...", owner, repo)
			return -1
		}
		log.Printf("[Clone] Codacy 未收录 %s/%s，尝试自动添加...", owner, repo)
		if addCodacyRepo(owner, repo) {
			log.Printf("[√] 已添加 %s/%s 到 Codacy，%v 后自动分析", owner, repo, config.Codacy.addWait())
			writeCacheDirect(addedKey, true, config.Codacy.addWait())
			go func(modID, o, r string) {
				time.Sleep(config.Codacy.addWait())
				log.Printf("[Codacy] Codacy 重新获取 %s/%s...", o, r)
				s, ac, ad, ai := fetchCodacyScore(o, r)
				if s >= 0 {
					difficultyMu.Lock()
					difficultyMap[modID] = s
					difficultyMu.Unlock()
					if err := upsertModDifficulty(modID, s); err != nil {
						log.Printf("[X] [Codacy] 写回 mod_difficulty 失败 (%s): %v", modID, err)
					}
					saveCodacyScoreToDisk(o, r, ac, ad, ai, "", config.Codacy.ttl())
					log.Printf("[√] Codacy %s/%s 分析完成，难度: %.1f", o, r, s)
				}
			}(modID, owner, repo)
		}
	}
	return -1
}

func fetchCodacyScore(owner, repo string) (float64, float64, float64, float64) {
	provider := config.Codacy.Provider
	if provider == "" {
		provider = "gh"
	}

	fileEntries, err := fetchCodacyFiles(provider, owner, repo)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return -2, 0, 0, 0
		}
		log.Printf("[X] Codacy 获取文件列表失败 (%s/%s): %v", owner, repo, err)
		return -1, 0, 0, 0
	}
	if len(fileEntries) == 0 {
		return -1, 0, 0, 0
	}

	var sumComplexity, sumDuplication float64
	var sumIssues int
	for _, f := range fileEntries {
		sumComplexity += f.Complexity
		sumDuplication += f.Duplication
		sumIssues += f.TotalIssues
	}
	n := float64(len(fileEntries))
	avgComplexity := sumComplexity / n
	avgDuplication := sumDuplication / n
	avgIssues := float64(sumIssues) / n

	score := computeCodacyScore(avgComplexity, avgDuplication, avgIssues)

	log.Printf("[Codacy] Codacy 原始数据 %s/%s: 文件数=%d, avgComplexity=%.2f, avgDuplication=%.2f%%, avgIssues=%.2f, score=%.1f",
		owner, repo, len(fileEntries), avgComplexity, avgDuplication, avgIssues, score)

	return score, avgComplexity, avgDuplication, avgIssues
}

func fetchCodacyFiles(provider, owner, repo string) ([]CodacyFileEntry, error) {
	token := authConfig.Codacy.APIToken
	baseURL := "https://api.codacy.com/api/v3"
	var allFiles []CodacyFileEntry
	nextURL := fmt.Sprintf("%s/organizations/%s/%s/repositories/%s/files", baseURL, provider, owner, repo)

	for nextURL != "" {
		cdLimiter <- struct{}{}
		req, _ := http.NewRequest("GET", nextURL, nil)
		req.Header.Set("api-token", token)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		<-cdLimiter
		if err != nil {
			return nil, err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 404 {
			return nil, fmt.Errorf("404")
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		var page codacyFilesResponse
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		allFiles = append(allFiles, page.Data...)
		nextURL = page.NextPageURL
	}
	return allFiles, nil
}

func addCodacyRepo(owner, repo string) bool {
	token := authConfig.Codacy.APIToken
	provider := config.Codacy.Provider
	if provider == "" {
		provider = "gh"
	}

	body := fmt.Sprintf(`{"provider":"%s","repositoryFullPath":"%s/%s"}`, provider, owner, repo)

	cdLimiter <- struct{}{}
	req, _ := http.NewRequest("POST", "https://api.codacy.com/api/v3/repositories", strings.NewReader(body))
	req.Header.Set("api-token", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	<-cdLimiter
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200 || resp.StatusCode == 201
}

func codacyScorePath(owner, repo string) string {
	return filepath.Join(config.Server.cacheDir(), "codacy", owner, repo, "score.json")
}

func readDiskCodacyScore(owner, repo string, ttl time.Duration) (float64, string, bool) {
	data, err := os.ReadFile(codacyScorePath(owner, repo))
	if err != nil {
		return 0, "", false
	}
	var d codacyDiskData
	if err := json.Unmarshal(data, &d); err != nil {
		return 0, "", false
	}
	if time.Now().After(time.Unix(d.ExpireAt, 0)) {
		return 0, d.Hash, false
	}
	return scoreFromDiskData(d), d.Hash, true
}

func saveCodacyScoreToDisk(owner, repo string, avgComplexity, avgDuplication, avgIssues float64, hash string, ttl time.Duration) {
	p := codacyScorePath(owner, repo)
	os.MkdirAll(filepath.Dir(p), 0755)
	d := codacyDiskData{
		AvgComplexity:  avgComplexity,
		AvgDuplication: avgDuplication,
		AvgIssues:      avgIssues,
		Hash:           hash,
		ExpireAt:       time.Now().Add(ttl).Unix(),
	}
	data, _ := json.Marshal(d)
	os.WriteFile(p, data, 0644)
}

func codacyScoreFileExists(owner, repo string) bool {
	_, err := os.Stat(codacyScorePath(owner, repo))
	return err == nil
}
