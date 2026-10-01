package inboundcoord

import (
	"regexp"
	"strings"
)

// ConfigLinkPagePath is the page a context configuration link opens; the
// link's bearer token follows it (contextcap.NewLinkToken, base64url).
const ConfigLinkPagePath = "/dingtalk/configure?link="

// ConfigLinkPlaceholder replaces a configuration link in stored text.
const ConfigLinkPlaceholder = "[configuration link]"

var (
	// configLinkURLPattern matches a configuration link URL: the optional
	// origin, the page path and the token.
	configLinkURLPattern = regexp.MustCompile(`(?:https?://\S*?)?` + regexp.QuoteMeta(ConfigLinkPagePath) + `[A-Za-z0-9_%-]+`)
	// configLinkEncodedPattern matches the same URL percent-encoded once, as
	// it appears inside the dingtalk://…?url= deep link.
	configLinkEncodedPattern = regexp.MustCompile(`(?i)(?:https?%3A%2F%2F[A-Za-z0-9._~%-]*?)?%2Fdingtalk%2Fconfigure%3Flink%3D[A-Za-z0-9_%-]+`)
)

// RedactConfigLinks replaces every configuration link URL in text, plain or
// percent-encoded, with ConfigLinkPlaceholder. A link is a bearer token (a
// personal one hands over that person's scope), so the Coordinator's stored
// copies beyond the DingTalk reply that delivers it (transcripts, plans,
// issue descriptions, history read back into the model) keep only the
// placeholder. An executor task's own messages keep the link: delivery reads
// them back.
func RedactConfigLinks(text string) string {
	if !strings.Contains(text, "configure") {
		return text
	}
	text = configLinkURLPattern.ReplaceAllString(text, ConfigLinkPlaceholder)
	return configLinkEncodedPattern.ReplaceAllString(text, ConfigLinkPlaceholder)
}

// WithoutConfigLinks returns a copy of d whose reply text and coordination
// action replies carry no configuration link, for storage beyond the reply
// that delivers it.
func (d Decision) WithoutConfigLinks() Decision {
	d.UserText = RedactConfigLinks(redactConfigLink(d.UserText, d.configLinkURL))
	if len(d.CoordinationActions) > 0 {
		actions := append([]CoordinationAction(nil), d.CoordinationActions...)
		for i := range actions {
			actions[i].Reply = RedactConfigLinks(redactConfigLink(actions[i].Reply, d.configLinkURL))
		}
		d.CoordinationActions = actions
	}
	return d
}
