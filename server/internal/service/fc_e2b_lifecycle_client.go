package service

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
	"strings"
	"time"
)

var errFCE2BSandboxNotFound = errors.New("FC sandbox no longer exists")
var fcE2BSandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type fcE2BSandboxInfo struct {
	ID string `json:"sandboxID"`
	State string `json:"state"`
	EndAt time.Time `json:"endAt"`
}

type fcE2BSandboxClient struct {
	apiURL string
	apiKey string
	http *http.Client
}

func newFCE2BSandboxClient(cfg FCE2BConfig) *fcE2BSandboxClient {
	return &fcE2BSandboxClient{
		apiURL: strings.TrimRight(cfg.APIURL, "/"), apiKey: cfg.APIKey,
		http: &http.Client{Timeout: 10*time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *fcE2BSandboxClient) request(ctx context.Context, method, id, suffix string, body []byte) ([]byte, error) {
	base, err := url.Parse(c.apiURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && base.Scheme != "http") || c.apiKey == "" || !fcE2BSandboxIDPattern.MatchString(id) {
		return nil, errors.New("invalid FC sandbox lifecycle configuration or identifier")
	}
	request, err := http.NewRequestWithContext(ctx, method, c.apiURL + "/sandboxes/" + id + suffix, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create FC sandbox lifecycle request failed")
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, errors.New("FC sandbox lifecycle transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, errFCE2BSandboxNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("FC sandbox lifecycle returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return nil, errors.New("invalid FC sandbox lifecycle response")
	}
	return data, nil
}

func (c *fcE2BSandboxClient) get(ctx context.Context, id string) (fcE2BSandboxInfo, error) {
	data, err := c.request(ctx, http.MethodGet, id, "", nil)
	if err != nil {
		return fcE2BSandboxInfo{}, err
	}
	var info fcE2BSandboxInfo
	if err := json.Unmarshal(data, &info); err != nil || info.ID != id || info.EndAt.IsZero() || (info.State != "running" && info.State != "paused") {
		return fcE2BSandboxInfo{}, errors.New("invalid FC sandbox lifecycle metadata")
	}
	return info, nil
}

func (c *fcE2BSandboxClient) ensureTTL(ctx context.Context, id string, minimum, target time.Duration) (fcE2BSandboxInfo, error) {
	info, err := c.get(ctx, id)
	if err != nil {
		return info, err
	}
	return c.extend(ctx, info, minimum, target)
}

func (c *fcE2BSandboxClient) extend(ctx context.Context, info fcE2BSandboxInfo, minimum, target time.Duration) (fcE2BSandboxInfo, error) {
	if minimum <= 0 || target < minimum || target/time.Second > 2147483647 {
		return info, errors.New("invalid FC sandbox renewal duration")
	}
	if info.State != "running" {
		return info, errors.New("FC sandbox is not running")
	}
	if time.Until(info.EndAt) >= minimum {
		return info, nil
	}
	body, _ := json.Marshal(map[string]int64{"timeout": int64(target/time.Second)})
	if _, err := c.request(ctx, http.MethodPost, info.ID, "/timeout", body); err != nil {
		return info, err
	}
	confirmed, err := c.get(ctx, info.ID)
	if err != nil {
		return info, err
	}
	if confirmed.State != "running" || time.Until(confirmed.EndAt) < minimum || confirmed.EndAt.Before(info.EndAt) {
		return confirmed, errors.New("FC sandbox renewal did not confirm sufficient lifetime")
	}
	return confirmed, nil
}
