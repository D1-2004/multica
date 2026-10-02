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

// configLinkEncoded is a configuration link URL percent-encoded once or
// twice, or partly (the separators of the query alone): the optional origin,
// the page path and the token.
const configLinkEncoded = `(?:https?(?:%3A|%253A)(?:%2F|%252F){2}[A-Za-z0-9._~%-]*?)?` +
	`(?:%2F|%252F)dingtalk(?:%2F|%252F)configure(?:%3F|%253F)link(?:=|%3D|%253D)[A-Za-z0-9_%-]+`

var (
	// configLinkDeepLinkPattern matches the whole DingTalk deep link around
	// an encoded configuration link (ConfigLinkDeepLink), so a redacted
	// Markdown link keeps no live-looking target. The trailing pc_slide may
	// be JSON-escaped.
	configLinkDeepLinkPattern = regexp.MustCompile(`(?i)dingtalk://dingtalkclient/page/link\?url=` + configLinkEncoded +
		`(?:(?:&|\\u0026)pc_slide=true)?`)
	// configLinkURLPattern matches a configuration link URL: the optional
	// origin, the page path and the token.
	configLinkURLPattern = regexp.MustCompile(`(?:https?://\S*?)?` + regexp.QuoteMeta(ConfigLinkPagePath) + `[A-Za-z0-9_%-]+`)
	// configLinkEncodedPattern matches the same URL percent-encoded, as it
	// appears inside a deep link or a URL parameter.
	configLinkEncodedPattern = regexp.MustCompile(`(?i)` + configLinkEncoded)
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
	text = configLinkDeepLinkPattern.ReplaceAllString(text, ConfigLinkPlaceholder)
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
