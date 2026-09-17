package gitrepo

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Service is the only repository acquisition boundary used by Agents and Skills.
// The caller authorizes workspace membership before calling it. Credentials are
// looked up in that workspace and never returned with repository contents.
type Service struct {
	Queries *db.Queries
	GitHub *GitHubAppClient
}

type Connection struct { ID pgtype.UUID; Provider, AccountLogin string }

type Access struct {
	Connection Connection
	Address Address
	Remote Remote
}

type AccessError struct { Status int; Code, Message string }
func (e *AccessError) Error() string { return e.Message }

func (s Service) Open(ctx context.Context, workspace pgtype.UUID, rawURL, connectionID string) (Access, error) {
	address, err := ParseAddress(rawURL)
	if err != nil { return Access{}, &AccessError{http.StatusBadRequest,"invalid_repository",err.Error()} }
	var connection db.GitConnection
	if connectionID != "" {
		var id pgtype.UUID
		if err := id.Scan(connectionID); err != nil || !id.Valid { return Access{}, &AccessError{http.StatusBadRequest,"invalid_connection","invalid connection_id"} }
		connection, err = s.Queries.GetGitConnection(ctx, db.GetGitConnectionParams{ID:id, WorkspaceID:workspace})
		if err != nil { return Access{}, &AccessError{http.StatusNotFound,"connection_unavailable","Git connection is unavailable in this workspace"} }
		if connection.Provider != address.Provider { return Access{}, &AccessError{http.StatusBadRequest,"connection_mismatch","Git connection does not match repository host"} }
	} else {
		connections, err := s.Queries.ListGitConnections(ctx, workspace)
		if err != nil { return Access{}, errors.New("failed to read Git connections") }
		for _, candidate := range connections {
			if candidate.Provider != address.Provider { continue }
			if connection.ID.Valid { return Access{}, &AccessError{http.StatusConflict,"connection_required","multiple Git identities match this host; select a connection"} }
			connection = candidate
		}
	}
	client := s.GitHub
	if client == nil {
		if connection.ID.Valid { return Access{}, ErrGitHubUnavailable }
		client = PublicGitHub(nil)
	}
	remote, err := client.Open(ctx,address,connection.InstallationID.Int64)
	if err != nil { return Access{}, err }
	return Access{Connection:Connection{ID:connection.ID,Provider:connection.Provider,AccountLogin:connection.AccountLogin}, Address:address, Remote:remote}, nil
}
