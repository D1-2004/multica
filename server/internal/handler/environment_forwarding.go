package handler

import (
	"github.com/multica-ai/multica/server/internal/forwarding"
	"net/url"
)

// connectorOAuthPublicReturn recognizes only the configured configuration
// page. Adding its entire origin to the return-to allow-list would allow
// unrelated production pages to inherit a pre-release OAuth result.
func (h *Handler) connectorOAuthPublicReturn(raw string) (origin, target string) {
	base := h.currentConfig().ForwardPublicBaseURL
	if base == "" {
		return "", ""
	}
	origin, target, err := forwarding.ParsePublicBase(base)
	if err != nil {
		return "", ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme+"://"+u.Host != origin || u.Path != forwarding.Prefix+target+"/dingtalk/configure" || u.RawPath != "" {
		return "", ""
	}
	return origin, target
}
