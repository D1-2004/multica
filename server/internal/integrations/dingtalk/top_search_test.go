package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// topSearchFixture fakes the three DingTalk gateways SearchUsers touches:
// api.dingtalk.com (token + contact search), oapi.dingtalk.com (legacy
// topapi/*), and the TOP router (corp contact search).
type topSearchFixture struct {
	mu                 sync.Mutex
	topForms           []url.Values
	contactSearchCalls int

	topHandler           func(w http.ResponseWriter)
	contactSearchHandler func(w http.ResponseWriter)
	userGetResults       map[string]string // userid → legacy result payload
}

func (fx *topSearchFixture) recordedTOPForms() []url.Values {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]url.Values(nil), fx.topForms...)
}

func (fx *topSearchFixture) contactSearches() int {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.contactSearchCalls
}

func newTopSearchClient(t *testing.T, fx *topSearchFixture) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			fmt.Fprint(w, `{"accessToken":"tok_test","expireIn":7200}`)
		case "/router/rest":
			_ = r.ParseForm()
			fx.mu.Lock()
			fx.topForms = append(fx.topForms, r.PostForm)
			fx.mu.Unlock()
			if fx.topHandler != nil {
				fx.topHandler(w)
				return
			}
			fmt.Fprint(w, `{}`)
		case "/v1.0/contact/users/search":
			fx.mu.Lock()
			fx.contactSearchCalls++
			fx.mu.Unlock()
			if fx.contactSearchHandler != nil {
				fx.contactSearchHandler(w)
				return
			}
			fmt.Fprint(w, `{"list":[]}`)
		case "/topapi/v2/user/get":
			var body struct {
				UserID string `json:"userid"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if result, ok := fx.userGetResults[body.UserID]; ok {
				fmt.Fprintf(w, `{"errcode":0,"errmsg":"ok","result":%s}`, result)
				return
			}
			fmt.Fprint(w, `{"errcode":60121,"errmsg":"user not found"}`)
		case "/topapi/v2/user/getbymobile":
			fmt.Fprint(w, `{"errcode":60121,"errmsg":"mobile not found"}`)
		case "/topapi/v2/department/listsubid", "/topapi/user/listsimple":
			fmt.Fprint(w, `{"errcode":88,"errmsg":"no permission"}`)
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return NewClient(Config{
		AppKey:      "key_test",
		AppSecret:   "secret_test",
		OpenAPIBase: srv.URL,
		OAPIBase:    srv.URL,
		TOPBase:     srv.URL + "/router/rest",
	})
}

func TestSearchUsersPrefersCorpContactResults(t *testing.T) {
	fx := &topSearchFixture{
		topHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"dingtalk_corp_search_corpcontact_baseinfo_response":{"result":{"success":true,"ding_open_errcode":0,"page_result":{"value_list":{"group_contact_result":[{"userid":"u1","name":"夏东翔","flower_name":"冬翔","title":"工程师","ali_tmp_dept":"钉钉","job_number":"103262"},{"userid":"u1","name":"重复行"},{"userid":"u2","name":"李雷"}]}}}}}`)
		},
		userGetResults: map[string]string{
			"u1": `{"userid":"u1","unionid":"un1","name":"夏东翔","title":"高级工程师","avatar":"http://cdn.example/u1.png"}`,
			"u2": `{"userid":"u2","name":"李雷"}`,
		},
	}
	client := newTopSearchClient(t, fx)

	users, err := client.SearchUsers(context.Background(), "冬翔", 10)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users after dedupe, got %d: %+v", len(users), users)
	}
	if users[0].Name != "冬翔" {
		t.Errorf("expected flower name as display name, got %q", users[0].Name)
	}
	if users[0].UnionID != "un1" || users[0].AvatarURL == "" {
		t.Errorf("expected enriched profile fields, got %+v", users[0])
	}
	wantTitle := "夏东翔 · 高级工程师 · 工程师 · 钉钉 · 工号 103262"
	if users[0].Title != wantTitle {
		t.Errorf("title mismatch:\n got %q\nwant %q", users[0].Title, wantTitle)
	}
	if users[1].UserID != "u2" || users[1].Name != "李雷" {
		t.Errorf("unexpected second user: %+v", users[1])
	}
	if fx.contactSearches() != 0 {
		t.Errorf("contact search should not run when corp search has results, got %d calls", fx.contactSearches())
	}
}

func TestSearchUsersCorpContactSingleObjectRow(t *testing.T) {
	fx := &topSearchFixture{
		topHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"dingtalk_corp_search_corpcontact_baseinfo_response":{"result":{"success":"true","page_result":{"value_list":{"group_contact_result":{"userid":"u9","flower_name":"须莫"}}}}}}`)
		},
		userGetResults: map[string]string{
			"u9": `{"userid":"u9","name":"胡伟男"}`,
		},
	}
	client := newTopSearchClient(t, fx)

	users, err := client.SearchUsers(context.Background(), "须莫", 10)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) != 1 || users[0].UserID != "u9" || users[0].Name != "须莫" {
		t.Fatalf("unexpected result: %+v", users)
	}
}

func TestSearchUsersFallsBackToContactSearch(t *testing.T) {
	fx := &topSearchFixture{
		topHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"error_response":{"code":15,"msg":"Remote service error","sub_msg":"权限不足"}}`)
		},
		contactSearchHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"list":["u2"]}`)
		},
		userGetResults: map[string]string{
			"u2": `{"userid":"u2","name":"李雷","title":"设计师"}`,
		},
	}
	client := newTopSearchClient(t, fx)

	users, err := client.SearchUsers(context.Background(), "李雷", 10)
	if err != nil {
		t.Fatalf("SearchUsers should fall back, not fail: %v", err)
	}
	if len(users) != 1 || users[0].UserID != "u2" || users[0].Title != "设计师" {
		t.Fatalf("unexpected fallback result: %+v", users)
	}
}

func TestSearchUsersReturnsEmptyWhenAllTiersFail(t *testing.T) {
	fx := &topSearchFixture{
		topHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"error_response":{"code":15,"msg":"Remote service error"}}`)
		},
		contactSearchHandler: func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"code":"InternalError"}`)
		},
	}
	client := newTopSearchClient(t, fx)

	users, err := client.SearchUsers(context.Background(), "王五", 10)
	if err != nil {
		t.Fatalf("all-tier failure should degrade to empty, got error: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected no users, got %+v", users)
	}
}

func TestSearchUsersMobileLookupErrorsPropagate(t *testing.T) {
	client := newTopSearchClient(t, &topSearchFixture{})

	if _, err := client.SearchUsers(context.Background(), "13800138000", 10); err == nil {
		t.Fatal("expected mobile lookup failure to propagate")
	}
}

func TestSearchUsersSignsTOPRequest(t *testing.T) {
	fx := &topSearchFixture{
		topHandler: func(w http.ResponseWriter) {
			fmt.Fprint(w, `{"dingtalk_corp_search_corpcontact_baseinfo_response":{"result":{"success":true,"page_result":{"value_list":{"group_contact_result":[{"userid":"u1","name":"张三"}]}}}}}`)
		},
		userGetResults: map[string]string{
			"u1": `{"userid":"u1","name":"张三"}`,
		},
	}
	client := newTopSearchClient(t, fx)

	if _, err := client.SearchUsers(context.Background(), "张三", 5); err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	forms := fx.recordedTOPForms()
	if len(forms) != 1 {
		t.Fatalf("expected exactly one TOP request, got %d", len(forms))
	}
	form := forms[0]
	if got := form.Get("method"); got != corpContactSearchMethod {
		t.Errorf("method = %q", got)
	}
	if got := form.Get("session"); got != "tok_test" {
		t.Errorf("session = %q, want access token", got)
	}
	if form.Get("v") != "2.0" || form.Get("sign_method") != "md5" || form.Get("format") != "json" {
		t.Errorf("unexpected protocol params: %v", form)
	}
	if got := form.Get("size"); got != "5" {
		t.Errorf("size = %q, want 5", got)
	}
	params := make(map[string]string, len(form))
	for key := range form {
		if key == "sign" {
			continue
		}
		params[key] = form.Get(key)
	}
	if want := topMD5Sign(params, "secret_test"); form.Get("sign") != want {
		t.Errorf("sign mismatch: got %q want %q", form.Get("sign"), want)
	}
}
