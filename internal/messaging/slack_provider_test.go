package messaging

import (
	"context"
	"errors"
	"testing"

	"github.com/akmatori/akmatori/internal/database"
	"github.com/slack-go/slack"
)

// fakeSlackClient captures the arguments passed to Slack so the slack
// provider's behaviour can be asserted without hitting the network.
type fakeSlackClient struct {
	postChannelID string
	postOptions   []slack.MsgOption

	updateChannelID string
	updateTimestamp string
	updateOptions   []slack.MsgOption

	postTSToReturn     string
	updateTSToReturn   string
	postErr, updateErr error
}

func (f *fakeSlackClient) PostMessageContext(_ context.Context, channelID string, options ...slack.MsgOption) (string, string, error) {
	f.postChannelID = channelID
	f.postOptions = options
	if f.postErr != nil {
		return "", "", f.postErr
	}
	ts := f.postTSToReturn
	if ts == "" {
		ts = "1700000000.000100"
	}
	return channelID, ts, nil
}

func (f *fakeSlackClient) UpdateMessageContext(_ context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error) {
	f.updateChannelID = channelID
	f.updateTimestamp = timestamp
	f.updateOptions = options
	if f.updateErr != nil {
		return "", "", "", f.updateErr
	}
	ts := f.updateTSToReturn
	if ts == "" {
		ts = timestamp
	}
	return channelID, ts, "ok", nil
}

func TestSlackProvider_Name(t *testing.T) {
	if got := (&SlackProvider{}).Name(); got != database.MessagingProviderSlack {
		t.Errorf("Name = %q, want %q", got, database.MessagingProviderSlack)
	}
}

func TestSlackProvider_PostMessage_ReturnsTS(t *testing.T) {
	fake := &fakeSlackClient{postTSToReturn: "1700000123.000400"}
	p := newSlackProviderFromClient(fake)

	got, err := p.PostMessage(context.Background(), &database.Channel{ExternalID: "C123"}, "hello")
	if err != nil {
		t.Fatalf("PostMessage error = %v", err)
	}
	if got.MessageID != "1700000123.000400" {
		t.Errorf("PostMessage MessageID = %q, want timestamp returned by slack", got.MessageID)
	}
	if fake.postChannelID != "C123" {
		t.Errorf("PostMessage channelID = %q, want C123", fake.postChannelID)
	}
}

func TestSlackProvider_PostMessage_NilChannel(t *testing.T) {
	p := newSlackProviderFromClient(&fakeSlackClient{})
	if _, err := p.PostMessage(context.Background(), nil, "hello"); err == nil {
		t.Errorf("PostMessage(nil channel) error = nil, want error")
	}
}

func TestSlackProvider_PostMessage_BlankExternalID(t *testing.T) {
	p := newSlackProviderFromClient(&fakeSlackClient{})
	if _, err := p.PostMessage(context.Background(), &database.Channel{}, "hello"); err == nil {
		t.Errorf("PostMessage(empty external_id) error = nil, want error")
	}
}

func TestSlackProvider_PostMessage_NoClient(t *testing.T) {
	p := NewSlackProvider(nil)
	_, err := p.PostMessage(context.Background(), &database.Channel{ExternalID: "C123"}, "hello")
	if err == nil {
		t.Errorf("PostMessage with absent client error = nil, want errSlackClientUnavailable")
	}
}

func TestSlackProvider_PostThreadReply_RequiresParent(t *testing.T) {
	p := newSlackProviderFromClient(&fakeSlackClient{})
	if _, err := p.PostThreadReply(context.Background(), &database.Channel{ExternalID: "C123"}, "", "hi"); err == nil {
		t.Errorf("PostThreadReply with empty parent error = nil, want error")
	}
}

func TestSlackProvider_PostThreadReply_PropagatesSlackErr(t *testing.T) {
	want := errors.New("slack down")
	fake := &fakeSlackClient{postErr: want}
	p := newSlackProviderFromClient(fake)

	_, err := p.PostThreadReply(context.Background(), &database.Channel{ExternalID: "C123"}, "1700.0001", "hi")
	if err == nil {
		t.Fatalf("PostThreadReply error = nil, want wrapped slack error")
	}
	if !errors.Is(err, want) {
		t.Errorf("PostThreadReply error = %v, want errors.Is(err, %v) to be true", err, want)
	}
}

func TestSlackProvider_UpdateMessage_PassesArgsToClient(t *testing.T) {
	fake := &fakeSlackClient{}
	p := newSlackProviderFromClient(fake)

	if err := p.UpdateMessage(context.Background(), &database.Channel{ExternalID: "C123"}, "1700.0001", "updated"); err != nil {
		t.Fatalf("UpdateMessage error = %v", err)
	}
	if fake.updateChannelID != "C123" {
		t.Errorf("UpdateMessage channelID = %q, want C123", fake.updateChannelID)
	}
	if fake.updateTimestamp != "1700.0001" {
		t.Errorf("UpdateMessage timestamp = %q, want 1700.0001", fake.updateTimestamp)
	}
}

func TestSlackProvider_UpdateMessage_RequiresMessageID(t *testing.T) {
	p := newSlackProviderFromClient(&fakeSlackClient{})
	if err := p.UpdateMessage(context.Background(), &database.Channel{ExternalID: "C123"}, "", "updated"); err == nil {
		t.Errorf("UpdateMessage with empty ID error = nil, want error")
	}
}
