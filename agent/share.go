package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

// HandleAgentShare 分享创建 API（需要登录）。
// POST /api/agent/share
func HandleAgentShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}

	var body struct {
		SessionID string `json:"session_id"`
		Blocks    []int  `json:"blocks"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.SessionID == "" || len(body.Blocks) == 0 {
		http.Error(w, `{"error":"bad request"}`, 400)
		return
	}
	s := GetSession(body.SessionID)
	if s == nil {
		http.Error(w, `{"error":"session not found"}`, 404)
		return
	}
	sort.Ints(body.Blocks)
	uniq := body.Blocks[:1]
	for i := 1; i < len(body.Blocks); i++ {
		if body.Blocks[i] != body.Blocks[i-1] {
			uniq = append(uniq, body.Blocks[i])
		}
	}
	ranges := compressRanges(uniq)
	shareID := genShareID()

	if err := dbCreateShare(shareID, body.SessionID, ranges); err != nil {
		log.Printf("[Share] 创建失败: %v", err)
		http.Error(w, `{"error":"create share failed"}`, 500)
		return
	}
	log.Printf("[Share] 创建分享 %s -> session=%s ranges=%s", shareID, body.SessionID, ranges)
	json.NewEncoder(w).Encode(map[string]string{"share_id": shareID})
}

// HandleAgentShareGet 分享数据查询（公开，无需登录）。
// GET /api/agent/share/<id>
func HandleAgentShareGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "GET" {
		http.Error(w, `{"error":"method not allowed"}`, 405)
		return
	}
	sid := r.URL.Path[len("/api/agent/share/"):]
	if sid == "" || strings.Contains(sid, "/") {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	sessionID, rangesStr, err := dbGetShare(sid)
	if err != nil || sessionID == "" {
		http.Error(w, `{"error":"share not found"}`, 404)
		return
	}
	s := GetSession(sessionID)
	if s == nil {
		http.Error(w, `{"error":"session not found"}`, 404)
		return
	}
	indices := expandRanges(rangesStr)
	var msgs []ChatMessage
	for _, idx := range indices {
		if idx >= 0 && idx < len(s.Messages) {
			msgs = append(msgs, s.Messages[idx])
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"share_id":   sid,
		"session_id": sessionID,
		"title":      s.Title,
		"messages":   msgs,
		"ranges":     rangesStr,
	})
}

// HandleSharePage 提供分享查看页面（static/share.html）。
func HandleSharePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data, err := os.ReadFile("static/share.html")
	if err != nil {
		http.Error(w, "share page not found", 500)
		return
	}
	w.Write(data)
}

// compressRanges 将有序无重索引压缩为区域字符串。
func compressRanges(indices []int) string {
	if len(indices) == 0 {
		return ""
	}
	var parts []string
	start := indices[0]
	end := indices[0]
	for i := 1; i < len(indices); i++ {
		if indices[i] == end+1 {
			end = indices[i]
		} else {
			parts = append(parts, formatRange(start, end))
			start = indices[i]
			end = indices[i]
		}
	}
	parts = append(parts, formatRange(start, end))
	return strings.Join(parts, ",")
}

func formatRange(start, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func expandRanges(ranges string) []int {
	if ranges == "" {
		return nil
	}
	var result []int
	for _, part := range strings.Split(ranges, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			ends := strings.SplitN(part, "-", 2)
			s, _ := strconv.Atoi(strings.TrimSpace(ends[0]))
			e, _ := strconv.Atoi(strings.TrimSpace(ends[1]))
			if s > e {
				s, e = e, s
			}
			for i := s; i <= e; i++ {
				result = append(result, i)
			}
		} else {
			v, _ := strconv.Atoi(part)
			result = append(result, v)
		}
	}
	sort.Ints(result)
	return result
}

func genShareID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
