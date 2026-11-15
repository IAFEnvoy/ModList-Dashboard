package qqbot

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Log authentication transitions, not each cached-token lookup. The SDK keeps
// credentials lazy; creating a disabled/idle bot never makes a login request.
type loginTokenSource struct {
	mu            sync.Mutex
	source        oauth2.TokenSource
	newSource     func() oauth2.TokenSource
	appID, secret string
	last          *oauth2.Token
	logf          func(string, ...interface{})
}

// The gateway can reject a token before its local expiry (for example after
// credentials are rotated). The next attempt must fetch a fresh credential.
func (s *loginTokenSource) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.newSource != nil {
		s.source = s.newSource()
		s.last = nil
	}
}

func (s *loginTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	needsLogin := s.last == nil || !s.last.Valid()
	action := "登录"
	if s.last != nil {
		action = "刷新登录凭证"
	}
	if needsLogin {
		s.logf("[QQBot] 正在%s (app_id=%s)", action, s.appID)
	}
	current, err := s.source.Token()
	if err == nil && (current == nil || !current.Valid()) {
		err = fmt.Errorf("QQ 返回了无效或已过期的登录凭证")
	}
	if err != nil {
		// Never include AppSecret or any known token in authentication logs.
		reason := err.Error()
		for _, value := range []string{s.secret, tokenValue(s.last), tokenValue(current)} {
			if value != "" {
				reason = strings.ReplaceAll(reason, value, "[REDACTED]")
			}
		}
		reason = strings.ReplaceAll(strings.ReplaceAll(reason, "\n", " "), "\r", " ")
		s.logf("[QQBot] %s失败 (app_id=%s): %s", action, s.appID, reason)
		return nil, fmt.Errorf("qqbot: %s失败: %s", action, reason)
	}
	if needsLogin || tokenValue(current) != tokenValue(s.last) {
		expires := "未提供"
		if !current.Expiry.IsZero() {
			expires = current.Expiry.Format(time.RFC3339)
		}
		s.logf("[QQBot] %s成功 (app_id=%s, expires_at=%s)", action, s.appID, expires)
	}
	s.last = current
	return current, nil
}

func tokenValue(token *oauth2.Token) string {
	if token == nil {
		return ""
	}
	return token.AccessToken
}
