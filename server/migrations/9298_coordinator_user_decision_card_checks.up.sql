-- Refresh existing waiting cards in place without changing choices or form data.
UPDATE coordinator_user_decision SET card_update_pending=true, available_at=now(), updated_at=now()
WHERE state='waiting' AND sent_at IS NOT NULL;
