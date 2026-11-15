package qqbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modlist-dashboard/bot"
)

type recordedReply struct{ group, message, content string }

func groupEvent(content string) string {
	messageID := sha256.Sum256([]byte(content))
	data, _ := json.Marshal(map[string]interface{}{"op": 0, "s": 2, "t": "GROUP_AT_MESSAGE_CREATE", "d": map[string]interface{}{
		"id": hex.EncodeToString(messageID[:]), "group_openid": "verified-group", "group_id": "legacy-wrong-id", "content": content,
		"author": map[string]string{"member_openid": "group-member"},
	}})
	return string(data)
}

func eventData(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var event gatewayPayload
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	return event.Data
}

func TestEveryGroupAtMessageReceivesAReply(t *testing.T) {
	code := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for _, test := range []struct {
		name, content, want string
		bindErr             error
		bindCalls           int
	}{
		{"bind without code", "/bind", "/bind 绑定码", nil, 0},
		{"invalid syntax", "/bind short", "指令格式", nil, 0},
		{"extra arguments", "/bind " + code + " extra", "指令格式", nil, 0},
		{"bind success", " <@!bot-id> /bind " + code, "绑定成功", nil, 1},
		{"invalid code", "/bind " + code, "绑定码无效、已使用或已过期", bot.ErrBindingCode, 1},
		{"already bound", "/bind " + code, "本群已经绑定", bot.ErrAlreadyBound, 1},
		{"storage failure", "/bind " + code, "暂时无法保存绑定", errors.New("test storage failure"), 1},
		{"help", "/help", "网站通知机器人", nil, 0},
		{"ordinary message", "你好", "网站通知机器人", nil, 0},
		{"mention only", " <@!bot-id> ", "网站通知机器人", nil, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			h := newGroupHandler(func(received string, target bot.Target) error {
				calls++
				if received != code || target != (bot.Target{Provider: "qq", Type: "group", ID: "verified-group"}) {
					t.Fatal("binding used incorrect identity")
				}
				return test.bindErr
			}, nil)
			replies := make(chan recordedReply, 1)
			h.reply = func(_ context.Context, group, message, content string) error {
				replies <- recordedReply{group, message, content}
				return nil
			}
			data := eventData(t, groupEvent(test.content))
			var message groupMessage
			json.Unmarshal(data, &message)
			if err := h.dispatch(context.Background(), data); err != nil {
				t.Fatal(err)
			}
			h.replyWG.Wait()
			select {
			case reply := <-replies:
				if reply.group != "verified-group" || reply.message != message.ID || !strings.Contains(reply.content, test.want) {
					t.Fatalf("bad reply: %+v", reply)
				}
				if strings.Contains(reply.content, code) || strings.Contains(reply.content, "test storage failure") {
					t.Fatal("reply exposed code or server internals")
				}
			case <-time.After(time.Second):
				t.Fatal("missing QQ reply")
			}
			if calls != test.bindCalls {
				t.Fatalf("bind called %d times; want %d", calls, test.bindCalls)
			}
		})
	}
}

func TestReplyDoesNotBlockDispatchAndReplayedEventsCannotRebind(t *testing.T) {
	var calls, attempts atomic.Int32
	h := newGroupHandler(func(string, bot.Target) error { calls.Add(1); return nil }, nil)
	started, release := make(chan struct{}), make(chan struct{})
	h.reply = func(_ context.Context, _, _ string, text string) error {
		if !strings.Contains(text, "绑定成功") {
			t.Error("retry lost original successful binding response")
		}
		if attempts.Add(1) == 1 {
			close(started)
			<-release
			return errors.New("temporary send failure")
		}
		return nil
	}
	data := eventData(t, groupEvent("/bind ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	dispatch := func() {
		t.Helper()
		if err := h.dispatch(context.Background(), data); err != nil {
			t.Fatal(err)
		}
	}
	dispatch() // Must return while the network reply is blocked.
	<-started
	dispatch()
	close(release)
	h.replyWG.Wait()
	dispatch()
	if calls.Load() != 1 || attempts.Load() != 2 {
		t.Fatalf("duplicate bind/reply: bind=%d attempts=%d", calls.Load(), attempts.Load())
	}
}

func TestCongestionWaitsWithoutConsumingCodeAndCancellationReleasesWait(t *testing.T) {
	var calls atomic.Int32
	h := newGroupHandler(func(string, bot.Target) error { calls.Add(1); return nil }, nil)
	for range cap(h.replySlots) {
		h.replySlots <- struct{}{}
	}
	data := eventData(t, groupEvent("/bind ABCDEFGHIJKLMNOPQRSTUVWXYZ"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.dispatch(ctx, data) }()
	select {
	case err := <-done:
		t.Fatalf("busy event was dropped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if calls.Load() != 0 {
		t.Fatal("busy processor consumed code")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected error: %v", err)
	}
	for range cap(h.replySlots) {
		<-h.replySlots
	}
	if err := h.dispatch(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("event after congestion not processed")
	}
}

func TestMalformedAndBotMessagesNeverBindOrReply(t *testing.T) {
	h := newGroupHandler(func(string, bot.Target) error { t.Error("unexpected bind"); return nil }, nil)
	h.reply = func(context.Context, string, string, string) error { t.Error("unexpected reply"); return nil }
	if err := h.dispatch(context.Background(), json.RawMessage(`{"id":`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	for _, data := range []string{`{}`, `{"id":"message"}`, `{"id":"message","group_openid":"group","author":{"bot":true}}`} {
		if err := h.dispatch(context.Background(), json.RawMessage(data)); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.replySlots) != 0 || len(h.replied) != 0 {
		t.Fatal("invalid message retained")
	}
}
