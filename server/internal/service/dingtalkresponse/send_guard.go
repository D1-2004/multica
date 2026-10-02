package dingtalkresponse

const suppressedSendCodePrefix = "host_send_suppressed:"

// SuppressSendError permanently prevents a new provider submission after its
// authority was revoked. Already submitted/unknown actions still reconcile.
type SuppressSendError struct{ Reason string }

func (e *SuppressSendError) Error() string { return "response send suppressed: " + e.Reason }
