package dingtalk

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Enterprise address-book (corp contact) search goes through the legacy TOP
// gateway rather than oapi/api.dingtalk.com. It is the only search API that
// matches employees by 花名 (flower name), so SearchUsers tries it first —
// mirroring dingtalk-native-agent's adapter, which this file is ported from.
const (
	defaultTOPBase          = "https://eco.taobao.com/router/rest"
	corpContactSearchMethod = "dingtalk.corp.search.corpcontact.baseinfo"
)

// flexString tolerates the TOP envelope's habit of returning codes and
// booleans as either JSON strings or bare literals.
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*s = ""
		return nil
	}
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		*s = flexString(str)
		return nil
	}
	*s = flexString(raw)
	return nil
}

type corpContactRow struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	AliTmpDept  string `json:"ali_tmp_dept"`
	FlowerName  string `json:"flower_name"`
	JobNumber   string `json:"job_number"`
	UserID      string `json:"userid"`
	WorkStation string `json:"work_station"`
}

// corpContactRows accepts both a JSON array and the single-object shape the
// gateway emits when exactly one contact matches.
type corpContactRows []corpContactRow

func (r *corpContactRows) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*r = nil
		return nil
	}
	if trimmed[0] == '[' {
		var rows []corpContactRow
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return err
		}
		*r = rows
		return nil
	}
	var row corpContactRow
	if err := json.Unmarshal(trimmed, &row); err != nil {
		return err
	}
	*r = corpContactRows{row}
	return nil
}

type corpContactSearchResult struct {
	PageResult *struct {
		ValueList *struct {
			Rows corpContactRows `json:"group_contact_result"`
		} `json:"value_list"`
	} `json:"page_result"`
	DingOpenErrcode flexString `json:"ding_open_errcode"`
	ErrorMsg        string     `json:"error_msg"`
	Success         flexString `json:"success"`
}

type corpContactSearchResponse struct {
	ErrorResponse *struct {
		Code   flexString `json:"code"`
		Msg    string     `json:"msg"`
		SubMsg string     `json:"sub_msg"`
	} `json:"error_response"`
	Body *struct {
		Result *corpContactSearchResult `json:"result"`
	} `json:"dingtalk_corp_search_corpcontact_baseinfo_response"`
}

func (c *Client) searchCorpContacts(ctx context.Context, query string, limit int) ([]User, error) {
	size := limit
	if size < 1 {
		size = 1
	}
	if size > 100 {
		size = 100
	}
	var resp corpContactSearchResponse
	if err := c.postTOP(ctx, corpContactSearchMethod, map[string]string{
		"query":  query,
		"offset": "0",
		"size":   strconv.Itoa(size),
	}, &resp); err != nil {
		return nil, err
	}
	if e := resp.ErrorResponse; e != nil {
		msg := strings.TrimSpace(e.SubMsg)
		if msg == "" {
			msg = strings.TrimSpace(e.Msg)
		}
		return nil, &APIError{Code: string(e.Code), Message: msg}
	}
	var result *corpContactSearchResult
	if resp.Body != nil {
		result = resp.Body.Result
	}
	if result != nil {
		if code := string(result.DingOpenErrcode); code != "" && code != "0" {
			return nil, &APIError{Code: code, Message: result.ErrorMsg}
		}
		if string(result.Success) == "false" {
			msg := result.ErrorMsg
			if msg == "" {
				msg = "unsuccessful response"
			}
			return nil, &APIError{Code: "top_unsuccessful", Message: msg}
		}
	}

	var rows corpContactRows
	if result != nil && result.PageResult != nil && result.PageResult.ValueList != nil {
		rows = result.PageResult.ValueList.Rows
	}

	users := make([]User, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		userID := strings.TrimSpace(row.UserID)
		if userID == "" {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}

		display := strings.TrimSpace(row.FlowerName)
		if display == "" {
			display = strings.TrimSpace(row.Name)
		}
		if display == "" {
			display = userID
		}
		nameHint := ""
		if realName := strings.TrimSpace(row.Name); realName != "" && realName != display {
			nameHint = realName
		}
		jobNumber := ""
		if jn := strings.TrimSpace(row.JobNumber); jn != "" {
			jobNumber = "工号 " + jn
		}

		user, err := c.GetUser(ctx, userID)
		if err != nil {
			c.logger.Warn("dingtalk: enrich corp contact failed", "user_id", userID, "error", err)
			user = User{UserID: userID}
			user.Title = joinUnique(nameHint, row.Title, row.AliTmpDept, jobNumber, row.WorkStation)
		} else {
			user.Title = joinUnique(nameHint, user.Title, row.Title, row.AliTmpDept, jobNumber, row.WorkStation)
		}
		user.Name = display
		users = append(users, user)
		if len(users) >= limit {
			break
		}
	}
	return users, nil
}

func (c *Client) postTOP(ctx context.Context, method string, params map[string]string, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	form := map[string]string{
		"format":      "json",
		"method":      method,
		"app_key":     c.appKey,
		"partner_id":  "multica",
		"session":     token,
		"sign_method": "md5",
		"timestamp":   topTimestamp(time.Now()),
		"v":           "2.0",
	}
	for k, v := range params {
		form[k] = v
	}
	form["sign"] = topMD5Sign(form, c.appSecret)

	values := url.Values{}
	for k, v := range form {
		values.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.topBase, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &APIError{Status: res.StatusCode, Code: "http_error", Message: strings.TrimSpace(string(body))}
	}
	if out == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode dingtalk top response: %w", err)
	}
	return nil
}

// topMD5Sign implements the TOP gateway MD5 signature:
// MD5(secret + k1v1k2v2... + secret) over keys sorted lexically, upper hex.
func topMD5Sign(params map[string]string, appSecret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "sign" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(appSecret)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params[k])
	}
	b.WriteString(appSecret)
	return strings.ToUpper(fmt.Sprintf("%x", md5.Sum([]byte(b.String()))))
}

// topTimestamp renders the request time in the gateway's expected GMT+8 wall
// clock regardless of host timezone.
func topTimestamp(t time.Time) string {
	return t.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
}

func joinUnique(values ...string) string {
	seen := make(map[string]struct{}, len(values))
	parts := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		parts = append(parts, v)
	}
	return strings.Join(parts, " · ")
}
