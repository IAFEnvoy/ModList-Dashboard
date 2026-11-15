package qqbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/openapi/options"
	"golang.org/x/oauth2"
	"modlist-dashboard/bot"
)

type localGroupAPI struct {
	openapi.OpenAPI
	url string
}

func (a *localGroupAPI) PostGroupMessage(ctx context.Context, id string, message dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	return a.OpenAPI.PostGroupMessage(ctx, id, message, options.WithURL(a.url+"/v2/groups/{group_id}/messages"))
}

func TestGroupEventSendsReplyThroughRealSDKToLocalHTTPServer(t *testing.T) {
	type request struct {
		path, auth string
		body       map[string]json.RawMessage
	}
	requests := make(chan request, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		requests <- request{r.URL.Path, r.Header.Get("Authorization"), body}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"sent-reply","timestamp":"2026-11-15T00:00:00Z"}`))
	}))
	defer server.Close()
	api := botgo.NewOpenAPI("test-app", oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "local-test-token", TokenType: "QQBot"})).SetDebug(false)
	sender := &Sender{api: &localGroupAPI{OpenAPI: api, url: server.URL}}
	h := newGroupHandler(func(string, bot.Target) error { t.Error("bare /bind attempted binding"); return nil }, sender)
	body := groupEvent("/bind")
	var event struct {
		Data groupMessage `json:"d"`
	}
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	if err := h.dispatch(context.Background(), eventData(t, body)); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-requests:
		if request.path != "/v2/groups/verified-group/messages" || request.auth != "QQBot local-test-token" {
			t.Fatalf("incorrect SDK route/auth: %+v", request)
		}
		if string(request.body["msg_type"]) != "0" || string(request.body["msg_seq"]) != "1" || string(request.body["msg_id"]) != `"`+event.Data.ID+`"` || !strings.Contains(string(request.body["content"]), "/bind 绑定码") {
			t.Fatalf("invalid SDK reply JSON: %+v", request.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event did not produce a real message API request")
	}
	// The HTTP request can be observed before the worker reads its response.
	deadline := time.After(time.Second)
	for len(h.replySlots) > 0 {
		select {
		case <-deadline:
			t.Fatal("reply did not complete")
		case <-time.After(time.Millisecond):
		}
	}
}
