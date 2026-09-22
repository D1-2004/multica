package userdecision

import "context"

// Session owns one isolated, short-lived authenticated DWS environment.
// The service renews it on reconnect, never persists its credentials.
type Session interface {
	Verify(context.Context, string, string) (corpID, initiatorID string, err error)
	Send(context.Context, Request) (string, error)
	Update(context.Context, Request, string, string) error
	Consume(context.Context, func(), func([]byte) error) error
	Close()
}
type Transport interface {
	Open(context.Context, Request) (Session, error)
}
