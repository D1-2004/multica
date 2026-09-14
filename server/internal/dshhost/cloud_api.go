package dshhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	openapi "github.com/alibabacloud-go/openapi-util/service"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/google/uuid"
)

// CloudCredentials stays in process memory and is never persisted in a plan.
type CloudCredentials struct{ AccessKeyID, AccessKeySecret, SecurityToken string }
type CloudCredentialSource func(context.Context) (CloudCredentials, error)

type CloudCall struct {
	Service, Action string
	Query           map[string]any
	Body            any
}
type CloudCaller interface {
	Call(context.Context, CloudCall, any) error
}

type ACSClient struct {
	region      string
	credentials CloudCredentialSource
	http        *http.Client
}

// Use the official SDK signer with a bounded, context-aware HTTP transport.
// SDK retry loops and redirects must not repeat a persisted creation step.
func NewACSClient(region string, source CloudCredentialSource) (*ACSClient, error) {
	if !regexp.MustCompile(`^[a-z]+-[a-z0-9]+(?:-[0-9]+)?$`).MatchString(region) || source == nil {
		return nil, errors.New("invalid DSH cloud region or credential source")
	}
	return &ACSClient{region: region, credentials: source, http: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *ACSClient) Call(ctx context.Context, call CloudCall, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	host, version, path, method := "", "", "/", http.MethodPost
	switch call.Service {
	case "nas":
		host = "nas." + c.region + ".aliyuncs.com"
		version = "2017-06-26"
	case "ram":
		host = "ram.aliyuncs.com"
		version = "2015-05-01"
	case "sts":
		host = "sts." + c.region + ".aliyuncs.com"
		version = "2015-04-01"
	case "fcsandbox":
		host = "fcsandbox." + c.region + ".aliyuncs.com"
		version = "2026-05-09"
		path = "/pop/2026-05-09/volumes"
		if call.Action == "ListVolumes" {
			method = http.MethodGet
		} else if call.Action != "CreateVolume" {
			return errors.New("unsupported DSH volume action")
		}
	default:
		return errors.New("unsupported DSH cloud service")
	}
	creds, err := c.credentials(ctx)
	if err != nil || creds.AccessKeyID == "" || creds.AccessKeySecret == "" {
		return errors.New("DSH cloud credentials unavailable")
	}
	var body []byte
	if call.Body != nil {
		body, err = json.Marshal(call.Body)
		if err != nil {
			return errors.New("invalid DSH cloud request body")
		}
	}
	signed := tea.NewRequest()
	signed.Protocol = tea.String("https")
	signed.Method = tea.String(method)
	signed.Pathname = tea.String(path)
	signed.Query = openapi.Query(call.Query)
	signed.Headers = map[string]*string{"host": tea.String(host), "x-acs-version": tea.String(version), "x-acs-action": tea.String(call.Action), "x-acs-date": openapi.GetTimestamp(), "x-acs-signature-nonce": tea.String(uuid.NewString()), "accept": tea.String("application/json"), "user-agent": tea.String("Multica-DSH-Storage/1")}
	if call.Body != nil {
		signed.Headers["content-type"] = tea.String("application/json; charset=utf-8")
	}
	algorithm := tea.String("ACS3-HMAC-SHA256")
	hash := openapi.HexEncode(openapi.Hash(body, algorithm))
	signed.Headers["x-acs-content-sha256"] = hash
	if creds.SecurityToken != "" {
		signed.Headers["x-acs-accesskey-id"] = tea.String(creds.AccessKeyID)
		signed.Headers["x-acs-security-token"] = tea.String(creds.SecurityToken)
	}
	signed.Headers["authorization"] = openapi.GetAuthorization(signed, algorithm, hash, tea.String(creds.AccessKeyID), tea.String(creds.AccessKeySecret))
	query := url.Values{}
	for k, v := range signed.Query {
		query.Set(k, tea.StringValue(v))
	}
	u := url.URL{Scheme: "https", Host: host, Path: path, RawQuery: query.Encode()}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid DSH cloud request")
	}
	for k, v := range signed.Headers {
		if k != "host" {
			req.Header.Set(k, tea.StringValue(v))
		}
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("DSH cloud request outcome unconfirmed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("DSH cloud %s returned HTTP %d", call.Action, res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return errors.New("invalid DSH cloud response size")
	}
	// FC can encode an API failure in an HTTP 200 envelope.
	var envelope struct {
		Success        *bool `json:"success"`
		HTTPStatusCode int   `json:"httpStatusCode"`
	}
	if json.Unmarshal(raw, &envelope) != nil || (envelope.Success != nil && !*envelope.Success) || envelope.HTTPStatusCode >= 400 {
		return errors.New("DSH cloud operation did not succeed")
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		return errors.New("invalid DSH cloud response")
	}
	return nil
}
