package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"modlist-dashboard/bot"
)

func TestAuthenticatedWebsiteBindingCodesAreSingleUseAndOwnerScoped(t *testing.T) {
	root := t.TempDir()
	previousService, previousAuth, previousDB := botService, authConfig, userDB
	t.Cleanup(func() {
		CloseUserDB()
		botService, authConfig, userDB = previousService, previousAuth, previousDB
	})
	authConfig.Admin.Username, authConfig.Admin.Password = "binding-admin", "test-password"
	if err := initUserDBAt(filepath.Join(root, "users.db")); err != nil {
		t.Fatal(err)
	}
	if err := createUser("other-user", "test-password"); err != nil {
		t.Fatal(err)
	}
	token, err := signToken("binding-admin")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := signToken("other-user")
	if err != nil {
		t.Fatal(err)
	}
	botService, err = bot.New(bot.Config{Enabled: true, NotifyNewIssues: true, NotifyReports: true}, filepath.Join(root, "state.json"), map[string]bot.Sender{"qq": &integrationBotSender{sent: make(chan string, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, auth, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/admin/bot-bindings", strings.NewReader(body))
		if auth != "" {
			r.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		withAuth(handleBotBindings)(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, auth := range []string{"", "invalid"} {
			if w := request(method, auth, `{"provider":"qq","type":"group","id":"group"}`); w.Code != 401 {
				t.Fatalf("unauthenticated %s: %d", method, w.Code)
			}
		}
	}
	w := request(http.MethodPost, token, "")
	var code struct {
		Code string `json:"code"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &code) != nil || code.Code == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("code generation failed: %d %s", w.Code, w.Body)
	}
	if w := request(http.MethodGet, otherToken, ""); strings.Contains(w.Body.String(), "pending_expires_at") || strings.Contains(w.Body.String(), code.Code) {
		t.Fatal("other account saw private code state")
	}
	if w := request(http.MethodGet, token, ""); strings.Contains(w.Body.String(), code.Code) {
		t.Fatal("GET revealed plaintext code")
	}
	target := bot.Target{Provider: "qq", Type: "group", ID: "verified-group"}
	if err := botService.Bind(code.Code, target); err != nil {
		t.Fatal(err)
	}
	if err := botService.Bind(code.Code, target); err == nil {
		t.Fatal("reused code accepted")
	}
	target.ID = "different-group"
	if err := botService.Bind(code.Code, target); err == nil {
		t.Fatal("code bound another group")
	}
	status := botService.BindingStatus("binding-admin")
	if len(status.Bindings) != 1 || status.Bindings[0].Target.ID != "verified-group" || status.PendingExpiresAt != nil {
		t.Fatalf("bad binding: %+v", status)
	}
	if w := request(http.MethodGet, otherToken, ""); strings.Contains(w.Body.String(), "verified-group") {
		t.Fatal("other user saw binding")
	}
	body := `{"provider":"qq","type":"group","id":"verified-group"}`
	if w := request(http.MethodDelete, otherToken, body); w.Code != 404 {
		t.Fatal("other user could remove binding")
	}
	if w := request(http.MethodDelete, token, body); w.Code != 204 {
		t.Fatalf("unbind failed: %s", w.Body)
	}
	if len(botService.BindingStatus("binding-admin").Bindings) != 0 {
		t.Fatal("binding remained after removal")
	}
	if w := request(http.MethodDelete, token, `{}`); w.Code != 400 {
		t.Fatal("invalid deletion accepted")
	}
	botService = nil
	if w := request(http.MethodGet, token, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"qq_enabled":false`) {
		t.Fatal("disabled state not exposed")
	}
	if w := request(http.MethodPost, token, ""); w.Code != 503 {
		t.Fatal("disabled module generated code")
	}
}
