package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"modlist-dashboard/bot"

	"github.com/gorilla/websocket"
	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/errs"
	"golang.org/x/oauth2"
)

// Receiver uses BotGo's gateway API, token source and protocol types. Its
// transport is managed here because BotGo v0.2.1's default session manager has
// no cancellation API and its client logs complete Identify/Resume payloads.
type Receiver struct {
	appID   string
	source  oauth2.TokenSource
	gateway func(context.Context) (*dto.WebsocketAP, error)
	handler *groupHandler
	dialer  *websocket.Dialer
	logf    func(string, ...interface{})
	running atomic.Bool
}

func NewReceiver(sender *Sender, bind func(string, bot.Target) error) (*Receiver, error) {
	if sender == nil || sender.source == nil || bind == nil {
		return nil, errors.New("qqbot: WebSocket 缺少凭据或绑定处理器")
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	return &Receiver{appID: sender.appID, source: sender.source, gateway: sender.gateway,
		handler: newGroupHandler(bind, sender), dialer: &dialer, logf: log.Printf}, nil
}

// Run owns all connections and reply workers. Disabled modules never call it.
func (r *Receiver) Run(parent context.Context) error {
	if !r.running.CompareAndSwap(false, true) {
		return errors.New("qqbot: WebSocket 已在运行")
	}
	defer r.running.Store(false)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	r.logf("[QQBot] 正在启动 WebSocket 接入 (app_id=%s)", r.appID)
	info, err := r.discover(ctx)
	if err != nil {
		return nil // Discovery only stops on cancellation.
	}
	// This queue survives reconnects. Never acknowledge a sequence that was
	// dropped due to congestion; reconnect and resume from the last queued one.
	queue := make(chan json.RawMessage, 256)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case data := <-queue:
				if err := r.handler.dispatch(ctx, data); err != nil && ctx.Err() == nil {
					r.logf("[QQBot] 无法处理群消息: %v", err)
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-workerDone
		r.handler.replyWG.Wait()
		r.logf("[QQBot] WebSocket 接入已停止 (app_id=%s)", r.appID)
	}()
	gate := &identifyGate{remaining: info.SessionStartLimit.Remaining, total: info.SessionStartLimit.Total,
		resetAfter: time.Duration(info.SessionStartLimit.ResetAfter) * time.Millisecond}
	gate.resetAt = time.Now().Add(gate.resetAfter)
	results := make(chan error, info.Shards)
	for shard := uint32(0); shard < info.Shards; shard++ {
		go func(id uint32) { results <- r.runShard(ctx, info, id, gate, queue) }(shard)
	}
	var result error
	for range info.Shards {
		if err := <-results; err != nil && result == nil {
			result = err
			cancel()
		}
	}
	return result
}

func (r *Receiver) discover(ctx context.Context) (*dto.WebsocketAP, error) {
	delay := time.Second
	for ctx.Err() == nil {
		info, err := r.gateway(ctx)
		if err == nil && info != nil && info.URL != "" && info.Shards > 0 {
			if info.SessionStartLimit.Remaining >= info.Shards {
				return info, nil
			}
			err = errors.New("会话额度不足，等待平台重置")
			delay = max(delay, time.Duration(info.SessionStartLimit.ResetAfter)*time.Millisecond)
		} else if err == nil {
			err = errors.New("平台返回了无效的 WebSocket 网关")
		}
		if ctx.Err() != nil {
			break
		}
		r.logf("[QQBot] 获取 WebSocket 网关失败，%s 后重试: %v", delay, err)
		if !waitContext(ctx, delay) {
			break
		}
		delay = min(delay*2, time.Minute)
	}
	return nil, ctx.Err()
}

// Serialize fresh Identify requests at a conservative one per five seconds,
// shared by all shards. Resume requests do not consume new session quota.
type identifyGate struct {
	mu               sync.Mutex
	next, resetAt    time.Time
	remaining, total uint32
	resetAfter       time.Duration
}

func (g *identifyGate) wait(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		now := time.Now()
		if g.resetAfter > 0 && !now.Before(g.resetAt) {
			g.remaining = g.total
			g.resetAt = now.Add(g.resetAfter)
		}
		until := g.next
		if g.remaining == 0 {
			if g.resetAfter <= 0 || g.total == 0 {
				return errors.New("qqbot: 会话额度已耗尽，请检查 QQ 开放平台后重启")
			}
			until = maxTime(until, g.resetAt)
		}
		if until.After(now) {
			if !waitContext(ctx, until.Sub(now)) {
				return ctx.Err()
			}
			continue
		}
		g.remaining--
		g.next = time.Now().Add(5 * time.Second)
		return nil
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

type wsSession struct {
	id  string
	seq uint32
}

type gatewayError struct {
	err                   error
	reset, fatal, refresh bool
}

func (e *gatewayError) Error() string { return e.err.Error() }

func (r *Receiver) runShard(ctx context.Context, info *dto.WebsocketAP, shard uint32, gate *identifyGate, queue chan<- json.RawMessage) error {
	session := &wsSession{}
	delay := time.Second
	for ctx.Err() == nil {
		if session.id == "" {
			if err := gate.wait(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		ready, err := r.connect(ctx, info.URL, shard, info.Shards, session, queue)
		if ctx.Err() != nil {
			return nil
		}
		var gatewayErr *gatewayError
		if errors.As(err, &gatewayErr) {
			if gatewayErr.fatal {
				return err
			}
			if gatewayErr.refresh {
				if source, ok := r.source.(interface{ invalidate() }); ok {
					source.invalidate()
				}
			}
			if gatewayErr.reset {
				*session = wsSession{}
			}
		}
		if ready {
			delay = time.Second
		}
		r.logf("[QQBot] WebSocket 连接中断 (app_id=%s, shard=%d/%d)，%s 后重连: %v", r.appID, shard, info.Shards, delay, err)
		if !waitContext(ctx, delay) {
			return nil
		}
		delay = min(delay*2, time.Minute)
	}
	return nil
}

type gatewayPayload struct {
	dto.WSPayloadBase
	Data json.RawMessage `json:"d"`
}

func writeGateway(conn *websocket.Conn, op dto.OPCode, data interface{}) error {
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return conn.WriteJSON(struct {
		Op   dto.OPCode  `json:"op"`
		Data interface{} `json:"d"`
	}{op, data})
}

func (r *Receiver) connect(ctx context.Context, url string, shard, count uint32, session *wsSession, queue chan<- json.RawMessage) (ready bool, result error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	token, err := r.source.Token()
	if err != nil {
		return false, err
	}
	if token == nil || !token.Valid() {
		return false, errors.New("QQ 登录凭证无效")
	}
	conn, response, err := r.dialer.DialContext(ctx, url, nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetReadLimit(1 << 20)
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello gatewayPayload
	if err := conn.ReadJSON(&hello); err != nil {
		return false, connectionError(err)
	}
	var helloData dto.WSHelloData
	if hello.OPCode != dto.WSHello || json.Unmarshal(hello.Data, &helloData) != nil || helloData.HeartbeatInterval <= 0 {
		return false, errors.New("QQ WebSocket Hello 无效")
	}
	interval := time.Duration(helloData.HeartbeatInterval) * time.Millisecond
	conn.SetReadDeadline(time.Time{})
	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "QQBot"
	}
	auth := tokenType + " " + token.AccessToken
	if session.id == "" {
		err = writeGateway(conn, dto.WSIdentity, &dto.WSIdentityData{Token: auth, Intents: dto.IntentGroupMessages, Shard: []uint32{shard, count}})
	} else {
		err = writeGateway(conn, dto.WSResume, &dto.WSResumeData{Token: auth, SessionID: session.id, Seq: session.seq})
	}
	if err != nil {
		return false, err
	}
	readCtx, stopRead := context.WithCancel(ctx)
	packets := make(chan gatewayPayload, 1)
	readError := make(chan error, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			var packet gatewayPayload
			if err := conn.ReadJSON(&packet); err != nil {
				readError <- err
				return
			}
			select {
			case packets <- packet:
			case <-readCtx.Done():
				return
			}
		}
	}()
	defer func() { stopRead(); conn.Close(); <-readerDone }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	loginTimer := time.NewTimer(15 * time.Second)
	defer loginTimer.Stop()
	awaitingAck := false
	heartbeat := func() error {
		var seq interface{}
		if session.seq != 0 {
			seq = session.seq
		}
		return writeGateway(conn, dto.WSHeartbeat, seq)
	}
	for {
		select {
		case <-ctx.Done():
			return ready, ctx.Err()
		case <-loginTimer.C:
			if !ready {
				return ready, errors.New("QQ WebSocket 登录响应超时")
			}
		case err := <-readError:
			return ready, connectionError(err)
		case <-ticker.C:
			if awaitingAck {
				return ready, errors.New("QQ WebSocket 心跳确认超时")
			}
			if err := heartbeat(); err != nil {
				return ready, err
			}
			awaitingAck = true
		case packet := <-packets:
			switch packet.OPCode {
			case dto.WSHeartbeatAck:
				awaitingAck = false
			case dto.WSHeartbeat:
				if err := heartbeat(); err != nil {
					return ready, err
				}
			case dto.WSReconnect:
				return ready, errors.New("QQ 请求重连")
			case dto.WSInvalidSession:
				var resumable bool
				json.Unmarshal(packet.Data, &resumable)
				return ready, &gatewayError{err: errors.New("QQ 会话失效"), reset: !resumable}
			case dto.WSDispatchEvent:
				switch packet.Type {
				case "READY":
					var data dto.WSReadyData
					if json.Unmarshal(packet.Data, &data) != nil || data.SessionID == "" {
						return ready, errors.New("QQ READY 数据无效")
					}
					session.id, ready = data.SessionID, true
					loginTimer.Stop()
					r.logf("[QQBot] WebSocket 接入成功 READY (app_id=%s, shard=%d/%d)", r.appID, shard, count)
				case "RESUMED":
					if session.id == "" {
						return ready, errors.New("QQ 返回了无效的 RESUMED")
					}
					ready = true
					loginTimer.Stop()
					r.logf("[QQBot] WebSocket 会话恢复成功 RESUMED (app_id=%s, shard=%d/%d)", r.appID, shard, count)
				case "GROUP_AT_MESSAGE_CREATE":
					// Resume replays missed events before sending RESUMED. An
					// existing session permits these authenticated replay events.
					if !ready && session.id == "" {
						return ready, errors.New("QQ 在鉴权完成前发送消息事件")
					}
					select {
					case queue <- packet.Data:
					default:
						return ready, errors.New("QQ 消息队列已满，重连后续传")
					}
				}
				if packet.Seq != 0 {
					session.seq = packet.Seq
				}
			}
		}
	}
}

func connectionError(err error) error {
	var closed *websocket.CloseError
	if !errors.As(err, &closed) {
		return err
	}
	// Only log the code, never arbitrary platform payloads or close text.
	code := closed.Code
	return &gatewayError{err: fmt.Errorf("QQ WebSocket 关闭，code=%d", code),
		reset:   code == errs.WSCodeBackendNotAuthenticate || code == errs.WSCodeBackendAuthenticationFail || code == errs.WSCodeBackendSessionNoLongerValid || code == errs.WSCodeBackendInvalidSeq || code >= 4900 && code <= 4913,
		refresh: code == errs.WSCodeBackendAuthenticationFail,
		fatal:   code == errs.WSCodeBackendUnknownOpCode || code == errs.WSCodeBackendDecodeError || code >= errs.WSCodeBackendInvalidShard && code <= errs.WSCodeBackendDisallowdIntents || code == errs.WSCodeBackendBotOffline || code == errs.WSCodeBackendBotBanned}
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
