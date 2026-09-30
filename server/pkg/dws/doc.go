// Package dws is the backend client for DingTalk Workspace (DWS): it calls
// DWS MCP tools directly over the MCP gateway's stateless JSON-RPC endpoint
// instead of spawning the dws CLI.
//
// A Client is one authenticated DingTalk identity. Create it from a
// single-use OAuth authorization code:
//
//	c, err := dws.New(ctx, dws.Config{Env: dws.EnvStaging}, dws.AuthCode{Code: code, ClientID: id})
//	var ie *dws.InitError
//	if errors.As(err, &ie) {
//		// ie.Stage says what failed; ie.CodeSpent says whether a retry needs
//		// a new code; ie.Temporary says whether retrying can help.
//	}
//
// New validates the input, exchanges the code, then confirms the identity
// with one tool call, so a returned Client is known to work. The Client keeps
// its token alive with the refresh token and reports ErrSessionExpired once
// that is no longer possible; mint a new code then (Pool does this).
//
// The Client exposes service objects: Messages, Groups, Cards and Contacts.
// Turn composes them into one reply to one inbound message: Ack, Progress on
// a streaming card, Final.
//
// One tool call is one POST to {gateway}/server/{serverId} with a
// "tools/call" envelope and the user access token in x-user-access-token.
// There is no initialize handshake and no MCP session.
package dws
