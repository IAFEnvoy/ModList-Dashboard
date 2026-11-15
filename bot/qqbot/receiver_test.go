package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modlist-dashboard/bot"

	"github.com/gorilla/websocket"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi"
	"golang.org/x/oauth2"
)

type gatewayAPI struct {
	openapi.OpenAPI
	info *dto.WebsocketAP
}

func (a *gatewayAPI) WS(context.Context, map[string]string, string) (*dto.WebsocketAP, error) {
	return a.info, nil
}

func localGateway(t *testing.T, handle func(*websocket.Conn)) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		handle(conn)
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func testReceiver(t *testing.T, url string, bind func(string, bot.Target) error) *Receiver {
	t.Helper()
	sender := &Sender{appID: "test-app", source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-access-token", TokenType: "QQBot"}),
		api: &gatewayAPI{info: &dto.WebsocketAP{URL: url, Shards: 1, SessionStartLimit: dto.SessionStartLimit{Total: 100, Remaining: 100, ResetAfter: 60000, MaxConcurrency: 1}}}}
	receiver, err := NewReceiver(sender, bind)
	if err != nil {
		t.Fatal(err)
	}
	receiver.handler.reply = nil
	return receiver
}

func readGateway(t *testing.T, conn *websocket.Conn) (gatewayPayload, bool) {
	t.Helper()
	var packet gatewayPayload
	if err := conn.ReadJSON(&packet); err != nil {
		t.Error(err)
		return packet, false
	}
	return packet, true
}

func TestWebSocketLoginBindReplyHeartbeatResumeAndCancellation(t *testing.T) {
	service, err := bot.New(bot.Config{Enabled: true}, filepath.Join(t.TempDir(), "bot.json"), map[string]bot.Sender{"qq": &Sender{}})
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := service.NewBindingCode("owner", "qq", "group")
	if err != nil {
		t.Fatal(err)
	}
	var connections, binds atomic.Int32
	resumed := make(chan struct{}, 1)
	event := groupEvent("/bind " + code)
	url := localGateway(t, func(conn *websocket.Conn) {
		connection := connections.Add(1)
		conn.WriteMessage(websocket.TextMessage, []byte(`{"op":10,"d":{"heartbeat_interval":50}}`))
		packet, ok := readGateway(t, conn)
		if !ok {
			return
		}
		if connection == 1 {
			var identity dto.WSIdentityData
			json.Unmarshal(packet.Data, &identity)
			if packet.OPCode != dto.WSIdentity || identity.Token != "QQBot test-access-token" || identity.Intents != dto.IntentGroupMessages || len(identity.Shard) != 2 || identity.Shard[1] != 1 {
				t.Error("invalid WebSocket authentication or group subscription")
			}
			// Before any dispatch sequence exists the heartbeat must contain null.
			conn.WriteMessage(websocket.TextMessage, []byte(`{"op":1,"d":null}`))
			packet, ok = readGateway(t, conn)
			if !ok {
				return
			}
			if packet.OPCode != dto.WSHeartbeat || string(packet.Data) != "null" {
				t.Errorf("initial heartbeat: %+v", packet)
			}
			conn.WriteMessage(websocket.TextMessage, []byte(`{"op":11}`))
			conn.WriteMessage(websocket.TextMessage, []byte(`{"op":0,"t":"READY","s":1,"d":{"session_id":"test-session","shard":[0,1]}}`))
			conn.WriteMessage(websocket.TextMessage, []byte(event))
		} else {
			var resume dto.WSResumeData
			json.Unmarshal(packet.Data, &resume)
			if packet.OPCode != dto.WSResume || resume.Token != "QQBot test-access-token" || resume.SessionID != "test-session" || resume.Seq != 2 {
				t.Errorf("invalid resume: %+v", resume)
			}
			// QQ replays events before RESUMED. This must not bind or reply twice.
			conn.WriteMessage(websocket.TextMessage, []byte(strings.Replace(event, `"s":2`, `"s":3`, 1)))
			conn.WriteMessage(websocket.TextMessage, []byte(`{"op":0,"t":"RESUMED","s":4,"d":{}}`))
		}
		for {
			var next gatewayPayload
			if err := conn.ReadJSON(&next); err != nil {
				return
			}
			if next.OPCode != dto.WSHeartbeat {
				t.Error("unexpected client opcode")
				return
			}
			conn.WriteMessage(websocket.TextMessage, []byte(`{"op":11}`))
			if connection == 1 {
				if string(next.Data) != "2" {
					t.Errorf("heartbeat lost sequence: %s", next.Data)
				}
				conn.WriteMessage(websocket.TextMessage, []byte(`{"op":7}`))
			} else {
				if string(next.Data) == "4" {
					select {
					case resumed <- struct{}{}:
					default:
					}
				}
			}
		}
	})
	r := testReceiver(t, url, func(code string, target bot.Target) error { binds.Add(1); return service.Bind(code, target) })
	var logMu sync.Mutex
	var logs []string
	r.logf = func(format string, args ...interface{}) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	replies := make(chan recordedReply, 4)
	releaseReply := make(chan struct{})
	r.handler.reply = func(ctx context.Context, group, message, content string) error {
		replies <- recordedReply{group, message, content}
		select {
		case <-releaseReply:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() { cancel() })
	select {
	case reply := <-replies:
		if reply.group != "verified-group" || !strings.Contains(reply.content, "绑定成功") {
			t.Fatalf("bad reply: %+v", reply)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WebSocket event did not bind and reply")
	}
	select {
	case <-resumed:
	case <-time.After(4 * time.Second):
		t.Fatal("session not resumed")
	}
	close(releaseReply) // Heartbeats and Resume completed while the reply was blocked.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket did not stop on cancellation")
	}
	if binds.Load() != 1 || len(replies) != 0 || connections.Load() != 2 {
		t.Fatal("replayed event repeated binding/reply or connection leaked")
	}
	status := service.BindingStatus("owner")
	if len(status.Bindings) != 1 || status.Bindings[0].Target.ID != "verified-group" {
		t.Fatalf("bad binding: %+v", status)
	}
	logMu.Lock()
	defer logMu.Unlock()
	text := strings.Join(logs, "\n")
	for _, want := range []string{"READY", "RESUMED", "连接中断", "接入已停止"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing log %s: %s", want, text)
		}
	}
	for _, secret := range []string{"test-access-token", code, "/bind "} {
		if strings.Contains(text, secret) {
			t.Error("connection logs leaked credentials or binding command")
		}
	}
}

func TestWebSocketProtocolFailuresPreserveUnqueuedSequence(t *testing.T) {
	for _, test := range []struct {
		name         string
		frames       []string
		want         string
		reset, fatal bool
	}{
		{"invalid session", []string{`{"op":9,"d":false}`}, "会话失效", true, false},
		{"resumable session", []string{`{"op":9,"d":true}`}, "会话失效", false, false},
		{"before authentication", []string{groupEvent("/bind")}, "鉴权完成前", false, false},
		{"full queue", []string{`{"op":0,"t":"READY","s":1,"d":{"session_id":"s"}}`, groupEvent("/bind")}, "队列已满", false, false},
		{"missed heartbeat ACK", []string{`{"op":0,"t":"READY","s":1,"d":{"session_id":"s"}}`}, "心跳确认超时", false, false},
		{"disallowed intent", nil, "code=4014", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			url := localGateway(t, func(conn *websocket.Conn) {
				conn.WriteMessage(websocket.TextMessage, []byte(`{"op":10,"d":{"heartbeat_interval":20}}`))
				if _, ok := readGateway(t, conn); !ok {
					return
				}
				for _, frame := range test.frames {
					conn.WriteMessage(websocket.TextMessage, []byte(frame))
				}
				if test.fatal {
					conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4014, "do not log this body"), time.Now().Add(time.Second))
					return
				}
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
				}
			})
			r := testReceiver(t, url, func(string, bot.Target) error { t.Error("unexpected bind"); return nil })
			session := &wsSession{}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := r.connect(ctx, url, 0, 1, session, make(chan json.RawMessage))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected error: %v", err)
			}
			var gatewayErr *gatewayError
			if test.reset || test.fatal {
				if !errors.As(err, &gatewayErr) || gatewayErr.reset != test.reset || gatewayErr.fatal != test.fatal {
					t.Fatalf("wrong recovery: %v", err)
				}
			}
			if session.seq > 1 {
				t.Fatal("unqueued message sequence was acknowledged")
			}
		})
	}
}

func TestReceiverDiscoveryRetryAndCancellation(t *testing.T) {
	r := testReceiver(t, "", func(string, bot.Target) error { return nil })
	called := make(chan struct{}, 1)
	r.gateway = func(context.Context) (*dto.WebsocketAP, error) {
		called <- struct{}{}
		return nil, errors.New("local gateway unavailable")
	}
	r.logf = func(string, ...interface{}) {}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	<-called
	if err := r.Run(ctx); err == nil {
		t.Fatal("duplicate receiver allowed")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery retry ignored cancellation")
	}
}

func TestIdentifyGateWaitsForQuotaResetAndCanCancel(t *testing.T) {
	gate := &identifyGate{remaining: 0, total: 10, resetAfter: time.Hour, resetAt: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quota wait ignored cancellation: %v", err)
	}
	gate.resetAt = time.Now().Add(-time.Second)
	if err := gate.wait(context.Background()); err != nil || gate.remaining != 9 {
		t.Fatalf("quota did not reset: %v", err)
	}
}
