package dingtalkresponse

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

type resourceOnlyProvider struct {
	Provider
	reads, downloads []string
	in               ActionInput
}

func (p *resourceOnlyProvider) ReadMessageResources(_ context.Context, in ActionInput, conversationID, messageID string) (dwsclient.MessageResources, error) {
	p.in = in
	p.reads = append(p.reads, conversationID+"/"+messageID)
	return dwsclient.MessageResources{MessageID: messageID, ConversationID: conversationID}, nil
}

func (p *resourceOnlyProvider) DownloadMessageFile(_ context.Context, in ActionInput, fileID string, maxBytes int64) (dwsclient.MessageFile, error) {
	p.in = in
	p.downloads = append(p.downloads, fileID)
	return dwsclient.MessageFile{Name: "a.txt", Data: []byte("x")}, nil
}

// Resource reads use the action's own identity and are unavailable, not
// silently empty, when no provider can perform them.
func TestEmployeeResourceServiceForwardsAgentIdentityReads(t *testing.T) {
	var missing *Service
	if _, err := missing.ReadMessageResources(context.Background(), ActionInput{}, "cid", "msg"); err == nil {
		t.Fatal("nil service read succeeded")
	}
	if _, err := NewService(nil, nil, nil).DownloadMessageFile(context.Background(), ActionInput{}, "file", 1); err == nil {
		t.Fatal("provider-less download succeeded")
	}
	provider := &resourceOnlyProvider{}
	service := NewService(nil, provider, nil)
	in := ActionInput{AgentID: "agent", DWSUID: "uid", DWSOrgID: "org"}
	got, err := service.ReadMessageResources(context.Background(), in, "cid", "msg")
	if err != nil || got.MessageID != "msg" || provider.reads[0] != "cid/msg" || provider.in.DWSUID != "uid" {
		t.Fatalf("read = %+v %v %+v", got, err, provider)
	}
	file, err := service.DownloadMessageFile(context.Background(), in, "file", 10)
	if err != nil || string(file.Data) != "x" || provider.downloads[0] != "file" {
		t.Fatalf("download = %+v %v", file, err)
	}
}
