package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

// RecordCollectionReminders adapts a collection's requester-authorized reminder
// plan to per-invitation reminder policies, inside the transaction that created
// the collection (the reminder package rejects any other transaction).
func RecordCollectionReminders(ctx context.Context, tx pgx.Tx, scope taskinput.Scope, p CollectionReminderPolicy) error {
	if tx == nil || p.CollectionID == "" {
		return errors.New("collection reminders need the creating transaction")
	}
	invitations, err := taskinput.NewStore(tx).ListInvitations(ctx, scope, p.CollectionID)
	if err != nil {
		return err
	}
	for _, invitation := range invitations {
		if err := service.RecordInvitationReminderPolicyTx(ctx, tx, scope, invitation.ID, service.InvitationReminderPolicy{
			MaxCount:          p.MaxCount,
			FirstAfter:        p.FirstDelay,
			Interval:          p.Interval,
			AuthorityActorRef: p.RequesterRef,
			Source:            taskinput.Source{Namespace: employeeCollectionSourceNamespace, Key: p.CollectionID + "/reminder/" + invitation.ID},
			InstructionQuote:  p.InstructionQuote,
		}); err != nil {
			return err
		}
	}
	return nil
}
