package contextcap

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The staffId behind an openDingTalkId (dws_open_identity_staff). An
// openDingTalkId is relative to the DingTalk account that sees it, so the
// mapping is kept per viewing account (viewerUID in orgID). Only a staffId
// the viewer's address book proved for exactly that openDingTalkId is
// stored; TriggerPersonKey then keys the person by it.

func validOpenIdentity(orgID, viewerUID, openDingTalkID string) bool {
	return ValidOrgID(orgID) && validScopeKey(viewerUID) && validScopeKey(openDingTalkID) &&
		!strings.EqualFold(openDingTalkID, "null")
}

// LookupOpenIDStaff returns the staffId kept for the person viewerUID sees
// as openDingTalkID in orgID, "" when none is known.
func LookupOpenIDStaff(ctx context.Context, db DBTX, orgID, viewerUID, openDingTalkID string) (string, error) {
	orgID, viewerUID, openDingTalkID = strings.TrimSpace(orgID), strings.TrimSpace(viewerUID), strings.TrimSpace(openDingTalkID)
	if !validOpenIdentity(orgID, viewerUID, openDingTalkID) {
		return "", nil
	}
	var staffID string
	err := db.QueryRow(ctx, `SELECT staff_id FROM dws_open_identity_staff
		WHERE org_id = $1 AND viewer_uid = $2 AND open_dingtalk_id = $3`, orgID, viewerUID, openDingTalkID).Scan(&staffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return staffID, err
}

// RememberOpenIDStaff keeps the staffId proved for the person viewerUID
// sees as openDingTalkID in orgID, replacing an older one.
func RememberOpenIDStaff(ctx context.Context, db DBTX, orgID, viewerUID, openDingTalkID, staffID string) error {
	orgID, viewerUID, openDingTalkID = strings.TrimSpace(orgID), strings.TrimSpace(viewerUID), strings.TrimSpace(openDingTalkID)
	staffID = strings.TrimSpace(staffID)
	if !validOpenIdentity(orgID, viewerUID, openDingTalkID) || TriggerPersonKey(staffID, "") != staffID {
		return ErrInvalidInput
	}
	_, err := db.Exec(ctx, `INSERT INTO dws_open_identity_staff (org_id, viewer_uid, open_dingtalk_id, staff_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, viewer_uid, open_dingtalk_id) DO UPDATE SET staff_id = EXCLUDED.staff_id, resolved_at = now()`,
		orgID, viewerUID, openDingTalkID, staffID)
	return err
}
