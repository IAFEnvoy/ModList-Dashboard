package qqbot

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func TestRejectedCredentialForcesFreshTokenBeforeLocalExpiry(t *testing.T) {
	old := &oauth2.Token{AccessToken: "cached-token", Expiry: time.Now().Add(time.Hour)}
	fresh := &oauth2.Token{AccessToken: "fresh-token", Expiry: time.Now().Add(time.Hour)}
	source := &loginTokenSource{
		source:    oauth2.StaticTokenSource(old),
		newSource: func() oauth2.TokenSource { return oauth2.StaticTokenSource(fresh) },
		logf:      func(string, ...interface{}) {},
	}
	if token, err := source.Token(); err != nil || token.AccessToken != old.AccessToken {
		t.Fatalf("initial token: %v", err)
	}
	source.invalidate()
	if token, err := source.Token(); err != nil || token.AccessToken != fresh.AccessToken {
		t.Fatalf("rejected token remained cached: %v", err)
	}
}

func TestLoginLogsSuccessAndRefreshWithoutLoggingCachedLookups(t *testing.T) {
	var messages []string
	current := &oauth2.Token{AccessToken: "first-token", Expiry: time.Now().Add(time.Hour)}
	source := &loginTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) { return current, nil }), appID: "test-app", secret: "test-secret",
		logf: func(format string, args ...interface{}) { messages = append(messages, fmt.Sprintf(format, args...)) },
	}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[1], "登录成功") {
		t.Fatalf("login/cache logging incorrect: %v", messages)
	}
	current.Expiry = time.Now().Add(-time.Second)
	current = &oauth2.Token{AccessToken: "refreshed-token", Expiry: time.Now().Add(time.Hour)}
	if _, err := source.Token(); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || !strings.Contains(messages[3], "刷新登录凭证成功") {
		t.Fatalf("refresh not logged: %v", messages)
	}
	joined := strings.Join(messages, "\n")
	for _, secret := range []string{"test-secret", "first-token", "refreshed-token"} {
		if strings.Contains(joined, secret) {
			t.Fatal("authentication logs exposed credentials")
		}
	}
}

func TestLoginFailureRedactsSecretsInLogsAndReturnedErrors(t *testing.T) {
	var messages []string
	source := &loginTokenSource{
		source: tokenSourceFunc(func() (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "new-sensitive-token"}, fmt.Errorf("test-secret previous-sensitive-token new-sensitive-token\nrejected")
		}),
		appID: "test-app", secret: "test-secret", last: &oauth2.Token{AccessToken: "previous-sensitive-token", Expiry: time.Now().Add(-time.Second)},
		logf: func(format string, args ...interface{}) { messages = append(messages, fmt.Sprintf(format, args...)) },
	}
	token, err := source.Token()
	if token != nil || err == nil {
		t.Fatal("authentication failure accepted")
	}
	joined := strings.Join(messages, " ") + err.Error()
	if !strings.Contains(joined, "刷新登录凭证失败") {
		t.Fatal("failure was not logged")
	}
	for _, secret := range []string{"test-secret", "previous-sensitive-token", "new-sensitive-token"} {
		if strings.Contains(joined, secret) {
			t.Fatal("authentication failure exposed credentials")
		}
	}
	for _, token := range []*oauth2.Token{nil, {AccessToken: ""}, {AccessToken: "expired", Expiry: time.Now().Add(-time.Hour)}} {
		source.source = tokenSourceFunc(func() (*oauth2.Token, error) { return token, nil })
		if _, err := source.Token(); err == nil {
			t.Fatal("invalid SDK token accepted")
		}
	}
}
