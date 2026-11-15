// Package qqbot adapts the official BotGo SDK to bot.Sender. It has no
// dependency on dashboard issues, agents, notification storage or scheduling.
package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"modlist-dashboard/bot"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/errs"
	sdklog "github.com/tencent-connect/botgo/log"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/token"
	"golang.org/x/oauth2"
)

type Config struct {
	Enabled   bool   `yaml:"enabled"`
	AppID     string `yaml:"app_id" json:"-"`
	AppSecret string `yaml:"app_secret" json:"-"`
	Sandbox   bool   `yaml:"sandbox"`
}

type Sender struct {
	api    openapi.OpenAPI
	apiMu  sync.Mutex // BotGo mutates shared client authentication and trace state.
	source oauth2.TokenSource
	appID  string
}

// BotGo v0.2.1 omits msg_type when its value is zero. QQ's group/C2C
// message API requires an explicit msg_type: 0 for text, including replies.
type textMessage struct {
	Content string          `json:"content"`
	MsgType dto.MessageType `json:"msg_type"`
	MsgID   string          `json:"msg_id,omitempty"`
	MsgSeq  uint32          `json:"msg_seq,omitempty"`
}

func (textMessage) GetEventID() string        { return "" }
func (textMessage) GetSendType() dto.SendType { return dto.Text }

var configureLogger sync.Once

// The SDK logs raw credentials and access tokens at debug level, even when
// HTTP debug is off. Suppress these calls before creating a token source.
type quietLogger struct{ sdklog.Logger }

func (quietLogger) Debug(...interface{})          {}
func (quietLogger) Debugf(string, ...interface{}) {}

func New(cfg Config) (*Sender, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.AppSecret) == "" {
		return nil, fmt.Errorf("qqbot: 请配置 app_id 和 app_secret")
	}
	configureLogger.Do(func() { botgo.SetLogger(quietLogger{sdklog.DefaultLogger}) })
	newSource := func() oauth2.TokenSource {
		return token.NewQQBotTokenSource(&token.QQBotCredentials{AppID: cfg.AppID, AppSecret: cfg.AppSecret})
	}
	source := &loginTokenSource{
		source: newSource(), newSource: newSource,
		appID: cfg.AppID, secret: cfg.AppSecret, logf: log.Printf,
	}
	// Construction makes no network requests. The receiver authenticates on Run.
	api := botgo.NewOpenAPI(cfg.AppID, source)
	if cfg.Sandbox {
		api = botgo.NewSandboxOpenAPI(cfg.AppID, source)
	}
	return &Sender{api: api.WithTimeout(10 * time.Second).SetDebug(false), source: source, appID: cfg.AppID}, nil
}

func (s *Sender) gateway(ctx context.Context) (*dto.WebsocketAP, error) {
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.api.WS(ctx, nil, "")
}

func (s *Sender) Validate(target bot.Target) error {
	if strings.TrimSpace(target.ID) == "" {
		return fmt.Errorf("qqbot: 通知目标 ID 不能为空")
	}
	switch target.Type {
	case "channel", "group", "c2c":
		return nil
	default:
		return fmt.Errorf("qqbot: 目标类型必须为 channel、group 或 c2c")
	}
}

func (s *Sender) Send(ctx context.Context, target bot.Target, content string) error {
	if err := s.Validate(target); err != nil {
		return err
	}
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	message := &textMessage{Content: content, MsgType: dto.TextMsg}
	var err error
	switch target.Type {
	case "channel":
		_, err = s.api.PostMessage(ctx, target.ID, &dto.MessageToCreate{Content: content, MsgType: dto.TextMsg})
	case "group":
		_, err = s.api.PostGroupMessage(ctx, target.ID, message)
	case "c2c":
		_, err = s.api.PostC2CMessage(ctx, target.ID, message)
	}
	return err
}

// Command feedback is a reply to the actual incoming message. Regular
// notifications continue to use proactive messages without fabricated IDs.
func (s *Sender) ReplyGroup(ctx context.Context, groupID, messageID, content string) error {
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(messageID) == "" {
		return fmt.Errorf("qqbot: 回复必须提供群 OpenID 和原消息 ID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.apiMu.Lock()
	defer s.apiMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.api.PostGroupMessage(ctx, groupID, &textMessage{
		Content: content, MsgType: dto.TextMsg, MsgID: messageID, MsgSeq: 1,
	})
	// If a request was accepted but its response was lost, retrying the same
	// msg_id + msg_seq must count as delivered rather than send another reply.
	if qqErrorCode(err) == 40054005 {
		return nil
	}
	return err
}

func qqErrorCode(err error) int {
	var apiErr *errs.Err
	if !errors.As(err, &apiErr) {
		return 0
	}
	var body struct {
		Code    int `json:"code"`
		ErrCode int `json:"err_code"`
	}
	if json.Unmarshal([]byte(apiErr.Text()), &body) == nil {
		if body.ErrCode != 0 {
			return body.ErrCode
		}
		if body.Code != 0 {
			return body.Code
		}
	}
	return apiErr.Code()
}
