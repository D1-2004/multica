package handler

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
)

// DingTalk H5 micro-app identity used to sign dd.config for the context
// capability configuration page. Both are public identifiers, not secrets;
// the jsapi_ticket is fetched with the corp app credentials of the direct
// DingTalk client and never leaves the server.
const (
	dingTalkH5CorpIDEnv  = "DINGTALK_H5_CORP_ID"
	dingTalkH5AgentIDEnv = "DINGTALK_H5_AGENT_ID"

	dingTalkJSAPIMaxURLLength = 2048
)

// dingTalkJSAPIClient returns the configured DingTalk client when it can
// fetch jsapi tickets and convert chat ids (the direct corp client, not the
// private agent).
func (h *Handler) dingTalkJSAPIClient() (dingtalk.JSAPIClient, bool) {
	if h.DingTalk == nil {
		return nil, false
	}
	client, ok := h.DingTalk.(dingtalk.JSAPIClient)
	if !ok || !client.JSAPISupported() {
		return nil, false
	}
	return client, true
}

func dingTalkH5AppIdentity() (corpID, agentID string) {
	return strings.TrimSpace(os.Getenv(dingTalkH5CorpIDEnv)), strings.TrimSpace(os.Getenv(dingTalkH5AgentIDEnv))
}

// dingTalkJSAPIReady reports whether GetDingTalkJSAPIConfig can sign: the H5
// app identity, the direct corp client and an app origin are configured.
func (h *Handler) dingTalkJSAPIReady() bool {
	corpID, agentID := dingTalkH5AppIdentity()
	if corpID == "" || agentID == "" || len(h.dingTalkJSAPIAppOrigins()) == 0 {
		return false
	}
	_, ok := h.dingTalkJSAPIClient()
	return ok
}

// dingTalkJSAPIAppOrigins returns the lowercased scheme://host origins the
// JSAPI signer may sign pages for (MULTICA_APP_URL, FRONTEND_ORIGIN).
func (h *Handler) dingTalkJSAPIAppOrigins() []string {
	cfg := h.currentConfig()
	allowed := []string{}
	for _, candidate := range []string{cfg.AppURL, cfg.FrontendOrigin} {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" {
			continue
		}
		if parsed, err := url.Parse(candidate); err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http") {
			allowed = append(allowed, strings.ToLower(parsed.Scheme+"://"+parsed.Host))
		}
	}
	return allowed
}

// dingTalkJSAPISignedURL validates the page URL a dd.config signature is for
// and returns the exact string DingTalk signs: scheme://authority + path +
// "?" + the percent-decoded query, as in DingTalk's reference signer. The URL
// must be absolute http(s) without userinfo or fragment and on the app
// origin, so the endpoint cannot sign pages of other sites. Callers check
// that an app origin is configured (dingTalkJSAPIAppOrigins) first.
func (h *Handler) dingTalkJSAPISignedURL(raw string) (string, error) {
	if raw == "" || len(raw) > dingTalkJSAPIMaxURLLength || strings.Contains(raw, "#") || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("invalid url")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("invalid url")
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	match := false
	for _, candidate := range h.dingTalkJSAPIAppOrigins() {
		if candidate == origin {
			match = true
			break
		}
	}
	if !match {
		return "", errors.New("url is not on the app origin")
	}
	signed := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.RawQuery != "" {
		query, err := url.QueryUnescape(u.RawQuery)
		if err != nil {
			return "", errors.New("invalid url")
		}
		signed += "?" + query
	}
	return signed, nil
}

// dingTalkJSAPISignature is sha1("jsapi_ticket=<t>&noncestr=<n>&timestamp=<ts>&url=<url>")
// in lowercase hex, the dd.config signature.
func dingTalkJSAPISignature(ticket, nonce, timestamp, signedURL string) string {
	sum := sha1.Sum([]byte("jsapi_ticket=" + ticket + "&noncestr=" + nonce + "&timestamp=" + timestamp + "&url=" + signedURL))
	return hex.EncodeToString(sum[:])
}

// GetDingTalkJSAPIConfig returns a dd.config signature for ?url= (the page URL
// without its #fragment). Any authenticated human may call it; it answers 404
// while context_capabilities is off and 503 when H5 signing is not configured
// (DINGTALK_H5_CORP_ID / DINGTALK_H5_AGENT_ID unset, no direct corp client, or
// no app origin to restrict the signed pages to).
func (h *Handler) GetDingTalkJSAPIConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	corpID, agentID := dingTalkH5AppIdentity()
	client, ok := h.dingTalkJSAPIClient()
	if corpID == "" || agentID == "" || !ok || len(h.dingTalkJSAPIAppOrigins()) == 0 {
		writeError(w, http.StatusServiceUnavailable, "DingTalk JSAPI signing is not configured")
		return
	}
	signedURL, err := h.dingTalkJSAPISignedURL(strings.TrimSpace(r.URL.Query().Get("url")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid url")
		return
	}
	ticket, err := client.JSAPITicket(r.Context())
	if err != nil {
		if !errors.Is(err, dingtalk.ErrUnsupported) {
			slog.WarnContext(r.Context(), "dingtalk jsapi ticket unavailable", "error", err)
		}
		writeError(w, http.StatusServiceUnavailable, "DingTalk JSAPI signing is unavailable")
		return
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "signature unavailable")
		return
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	writeJSON(w, http.StatusOK, map[string]string{
		"corp_id":    corpID,
		"agent_id":   agentID,
		"time_stamp": timestamp,
		"nonce_str":  nonce,
		"signature":  dingTalkJSAPISignature(ticket, nonce, timestamp, signedURL),
	})
}
