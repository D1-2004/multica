package dingtalkresponse

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

var errMessageResourcesUnavailable = errors.New("DWS message resource reads are unavailable")

// ReadMessageResources reads one exact message as the action's DWS identity:
// a provider read, never a send. Call outside a database transaction and
// recheck scope and authority before persisting anything derived from it.
func (s *Service) ReadMessageResources(ctx context.Context, in ActionInput, conversationID, messageID string) (dwsclient.MessageResources, error) {
	reader, ok := s.messageResourceProvider()
	if !ok {
		return dwsclient.MessageResources{}, errMessageResourcesUnavailable
	}
	return reader.ReadMessageResources(ctx, in, conversationID, messageID)
}

// DownloadMessageFile downloads a message file as the action's DWS identity.
// The caller must already have proven fileID is an own resource of an
// authorized message; the signed URL stays inside dwsclient.
func (s *Service) DownloadMessageFile(ctx context.Context, in ActionInput, fileID string, maxBytes int64) (dwsclient.MessageFile, error) {
	reader, ok := s.messageResourceProvider()
	if !ok {
		return dwsclient.MessageFile{}, errMessageResourcesUnavailable
	}
	return reader.DownloadMessageFile(ctx, in, fileID, maxBytes)
}

type messageResourceProvider interface {
	ReadMessageResources(context.Context, ActionInput, string, string) (dwsclient.MessageResources, error)
	DownloadMessageFile(context.Context, ActionInput, string, int64) (dwsclient.MessageFile, error)
}

func (s *Service) messageResourceProvider() (messageResourceProvider, bool) {
	if s == nil || s.provider == nil {
		return nil, false
	}
	reader, ok := s.provider.(messageResourceProvider)
	return reader, ok
}

func (p *dwsProvider) ReadMessageResources(ctx context.Context, in ActionInput, conversationID, messageID string) (dwsclient.MessageResources, error) {
	cli, err := p.cliFor(in)
	if err != nil {
		return dwsclient.MessageResources{}, err
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return dwsclient.MessageResources{}, err
	}
	defer cleanup()
	return cli.ReadMessageResources(ctx, dir, conversationID, messageID)
}

func (p *dwsProvider) DownloadMessageFile(ctx context.Context, in ActionInput, fileID string, maxBytes int64) (dwsclient.MessageFile, error) {
	cli, err := p.cliFor(in)
	if err != nil {
		return dwsclient.MessageFile{}, err
	}
	dir, cleanup, err := p.authenticateWith(ctx, cli, in)
	if err != nil {
		return dwsclient.MessageFile{}, err
	}
	defer cleanup()
	return cli.DownloadMessageFile(ctx, dir, fileID, maxBytes)
}
