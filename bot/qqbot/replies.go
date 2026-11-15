package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"modlist-dashboard/bot"
)

type groupMessage struct {
	ID          string `json:"id"`
	GroupOpenID string `json:"group_openid"`
	Content     string `json:"content"`
	Author      struct {
		Bot bool `json:"bot"`
	} `json:"author"`
}

type groupMessageKey struct{ GroupID, MessageID string }

type groupHandler struct {
	bind       func(string, bot.Target) error
	reply      func(context.Context, string, string, string) error
	replySlots chan struct{}
	replyMu    sync.Mutex
	replied    map[groupMessageKey]time.Time
	replyWG    sync.WaitGroup
}

func newGroupHandler(bind func(string, bot.Target) error, sender *Sender) *groupHandler {
	h := &groupHandler{bind: bind, replySlots: make(chan struct{}, 4), replied: make(map[groupMessageKey]time.Time)}
	if sender != nil {
		h.reply = sender.ReplyGroup
	}
	return h
}

var bindCommand = regexp.MustCompile(`^/bind\s+([A-Za-z2-7]{26})$`)
var leadingMention = regexp.MustCompile(`^<@!?[^<>\s]+>\s*`)

func (h *groupHandler) dispatch(ctx context.Context, data json.RawMessage) error {
	var message groupMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return err
	}
	return h.handleGroupMessage(ctx, message)
}

const bindingUsage = "请先登录网站，在 Agent → 设置 → QQ 群通知绑定中生成绑定码，再在目标群里 @我并发送：/bind 绑定码。绑定码 10 分钟内有效，仅限使用一次。"
const groupHelp = "我是网站通知机器人，用于推送新增 Issue 和预处理报告。" + bindingUsage

// Replies use the message API independently of the event transport. Wait for
// capacity before consuming a binding code; WebSocket events have no HTTP ACK.
func (h *groupHandler) handleGroupMessage(ctx context.Context, message groupMessage) error {
	if message.ID == "" || message.GroupOpenID == "" || message.Author.Bot {
		return nil
	}
	key := groupMessageKey{message.GroupOpenID, message.ID}
	select {
	case h.replySlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-h.replySlots
		return err
	}
	now := time.Now()
	h.replyMu.Lock()
	defer h.replyMu.Unlock()
	for existing, expiry := range h.replied {
		if !expiry.After(now) {
			delete(h.replied, existing)
		}
	}
	if _, exists := h.replied[key]; exists {
		<-h.replySlots
		return nil
	}
	// Bound replay memory, evicting the oldest record under sustained traffic.
	if len(h.replied) >= 4096 {
		var oldest groupMessageKey
		var expiry time.Time
		for existing, until := range h.replied {
			if expiry.IsZero() || until.Before(expiry) {
				oldest, expiry = existing, until
			}
		}
		delete(h.replied, oldest)
	}
	text := h.groupResponse(message)
	h.replied[key] = now.Add(10 * time.Minute)
	if h.reply == nil {
		<-h.replySlots
		return nil
	}
	h.replyWG.Add(1)
	go func() { defer h.replyWG.Done(); h.sendGroupReply(ctx, key, text) }()
	return nil
}

func (h *groupHandler) groupResponse(message groupMessage) string {
	content := leadingMention.ReplaceAllString(strings.TrimSpace(message.Content), "")
	parts := strings.Fields(content)
	if len(parts) == 0 || parts[0] != "/bind" {
		return groupHelp
	}
	match := bindCommand.FindStringSubmatch(content)
	if match == nil {
		return "绑定指令格式：/bind 绑定码。" + bindingUsage
	}
	err := h.bind(match[1], bot.Target{Provider: "qq", Type: "group", ID: message.GroupOpenID})
	switch {
	case err == nil:
		return "绑定成功，本群将接收网站配置的通知。请群主在机器人的群消息设置中允许主动推送。"
	case errors.Is(err, bot.ErrBindingCode):
		return "绑定码无效、已使用或已过期。请登录网站，在 Agent → 设置中重新生成绑定码后再发送。"
	case errors.Is(err, bot.ErrAlreadyBound):
		return "本群已经绑定或已配置为通知目标，无需重复绑定。可登录网站查看自己的绑定记录。"
	default:
		// Do not log the command or code, and do not expose storage errors in QQ.
		log.Printf("[Bot] 无法保存 QQ 群绑定: %v", err)
		return "暂时无法保存绑定，请稍后重新发送指令。绑定结果可在网站的 Agent → 设置中确认。"
	}
}

func (h *groupHandler) sendGroupReply(parent context.Context, key groupMessageKey, text string) {
	defer func() { <-h.replySlots }()
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = h.reply(ctx, key.GroupID, key.MessageID, text); err == nil {
			return
		}
		if attempt == 2 || ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 500 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
		if ctx.Err() != nil {
			break
		}
	}
	log.Printf("[Bot] QQ 群消息回复失败，message_id=%s: %v", key.MessageID, err)
}
