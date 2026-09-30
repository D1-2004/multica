package runtimeconfig

// UseDWSForTag returns runtime.use_dws_for_tag from the live snapshot
// without cloning it: DingTalk calls read it on every operation.
func (s *Service) UseDWSForTag() bool {
	if s == nil {
		return false
	}
	current := s.snapshot.Load()
	return current != nil && current.Config.Runtime.UseDWSForTag
}
