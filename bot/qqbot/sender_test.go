package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"modlist-dashboard/bot"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/errs"
	"github.com/tencent-connect/botgo/openapi"
	"github.com/tencent-connect/botgo/openapi/options"
)

type fakeAPI struct {
	openapi.OpenAPI
	method, id string
	message    *dto.MessageToCreate
	body       map[string]json.RawMessage
	failure    error
}

func (a *fakeAPI) record(method, id string, message interface{}) error {
	a.method, a.id = method, id
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	a.message = &dto.MessageToCreate{}
	if err := json.Unmarshal(data, a.message); err != nil {
		return err
	}
	a.body = make(map[string]json.RawMessage)
	if err := json.Unmarshal(data, &a.body); err != nil {
		return err
	}
	return a.failure
}

func (a *fakeAPI) PostMessage(_ context.Context, id string, msg *dto.MessageToCreate, _ ...options.Option) (*dto.Message, error) {
	return &dto.Message{}, a.record("channel", id, msg)
}

func (a *fakeAPI) PostGroupMessage(_ context.Context, id string, msg dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	return &dto.Message{}, a.record("group", id, msg)
}

func (a *fakeAPI) PostC2CMessage(_ context.Context, id string, msg dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	return &dto.Message{}, a.record("c2c", id, msg)
}

func TestRoutesTargetsThroughBotGoWithoutFabricatingReplyIDs(t *testing.T) {
	api := &fakeAPI{}
	sender := &Sender{api: api}
	for _, kind := range []string{"channel", "group", "c2c"} {
		if err := sender.Send(context.Background(), bot.Target{Type: kind, ID: "target-openid"}, "report"); err != nil {
			t.Fatal(err)
		}
		if api.method != kind || api.id != "target-openid" || api.message.Content != "report" || api.message.MsgID != "" || api.message.EventID != "" {
			t.Fatalf("unexpected SDK request: %+v", api)
		}
		if kind != "channel" && string(api.body["msg_type"]) != "0" {
			t.Fatal("text notification omitted required msg_type: 0")
		}
	}
	for _, target := range []bot.Target{{Type: "unknown", ID: "id"}, {Type: "group"}} {
		if err := sender.Send(context.Background(), target, "report"); err == nil {
			t.Fatal("invalid target was accepted")
		}
	}
}

func TestMissingCredentialsAreRejected(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing credentials were accepted")
	}
}

func TestBindingConfirmationUsesActualIncomingMessageAsReply(t *testing.T) {
	api := &fakeAPI{}
	sender := &Sender{api: api}
	if err := sender.ReplyGroup(context.Background(), "group-openid", "incoming-msg", "bound"); err != nil {
		t.Fatal(err)
	}
	if api.method != "group" || api.id != "group-openid" || api.message.MsgID != "incoming-msg" || api.message.MsgSeq != 1 {
		t.Fatalf("invalid binding confirmation reply: %+v", api.message)
	}
	if string(api.body["msg_type"]) != "0" {
		t.Fatal("text reply omitted required msg_type: 0")
	}
}

func TestGroupReplyRejectsMissingReferencesAndHandlesDuplicateAcceptance(t *testing.T) {
	api := &fakeAPI{}
	sender := &Sender{api: api}
	for _, ids := range [][2]string{{"", "message"}, {"group", ""}} {
		if sender.ReplyGroup(context.Background(), ids[0], ids[1], "reply") == nil || api.message != nil {
			t.Fatal("missing reference sent a proactive message")
		}
	}
	for _, failure := range []error{errs.New(400, `{"code":40054005}`, "test-trace"), errs.New(400, `{"err_code":40054005}`, "test-trace")} {
		api.failure = failure
		if err := sender.ReplyGroup(context.Background(), "group", "message", "reply"); err != nil {
			t.Fatal("accepted duplicate not treated as delivered")
		}
	}
	api.failure = errs.New(403, `{"code":40034105}`, "test-trace")
	if err := sender.ReplyGroup(context.Background(), "group", "message", "reply"); err == nil {
		t.Fatal("real API failure suppressed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sender.ReplyGroup(ctx, "group", "message", "reply"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled reply was attempted")
	}
}
