package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// ========== CurseForge ==========

func getCachedCFDownloads(cfID string, ttl time.Duration) int64 {
	if cfID == "" || !dsEnabled("curseforge") {
		return 0
	}
	cacheKey := "cf:" + cfID
	return staleWhileRevalidateInt(cacheKey, ttl, func() int64 {
		return fetchCFDownloads(cfID)
	}).(int64)
}

func fetchCFDownloads(cfID string) int64 {
	url := fmt.Sprintf("https://api.curseforge.com/v1/mods/%s", cfID)
	maxAttempts := config.Retry.attempts()
	baseDelay := config.Retry.delayMs()

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		cfLimiter <- struct{}{}

		if attempt > 1 {
			delay := time.Duration(baseDelay<<(attempt-2)) * time.Millisecond
			time.Sleep(delay)
			log.Printf("[~] CurseForge 重试 %d/%d (%s)，等待 %v", attempt, maxAttempts, cfID, delay)
		}

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("x-api-key", authConfig.CurseForge.APIKey)
		req.Header.Set("Accept", "application/json")

		resp, err := http.DefaultClient.Do(req)
		<-cfLimiter

		if err != nil {
			lastErr = err
			log.Printf("[X] CurseForge 网络错误 (%s)%s: %v", cfID, attemptLabel(attempt), err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			var cfResp CFResponse
			if err := json.Unmarshal(body, &cfResp); err != nil {
				log.Printf("[X] CurseForge JSON 解析失败 (%s): %v", cfID, err)
				return 0
			}
			log.Printf("[√] CurseForge (%s): %d downloads", cfID, cfResp.Data.DownloadCount)
			return cfResp.Data.DownloadCount
		}

		bodyPreview := string(body)
		if len(bodyPreview) > 200 {
			bodyPreview = bodyPreview[:200] + "..."
		}
		if !isRetryable(resp.StatusCode) {
			log.Printf("[X] CurseForge 返回 %d (%s)，不重试", resp.StatusCode, cfID)
			return 0
		}
		log.Printf("[X] CurseForge 返回 %d (%s)%s: %s", resp.StatusCode, cfID, attemptLabel(attempt), bodyPreview)
		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	log.Printf("[X] CurseForge (%s) 全部 %d 次尝试失败，最后错误: %v", cfID, maxAttempts, lastErr)
	return 0
}
