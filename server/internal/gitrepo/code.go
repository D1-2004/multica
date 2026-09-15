package gitrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const CodeAPIBase = "https://code.alibaba-inc.com"

type CodeConfig struct {
	APIBase string
	HTTPClient *http.Client
}

type CodeClient struct {
	base *url.URL
	client *http.Client
	token string
}

type CodeAccount struct {
	ID int64 `json:"id"`
	Username string `json:"username"`
}

func NewCodeClient(config CodeConfig, token string) (*CodeClient,error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token,"code_pat_") || len(token)>4096 || strings.ContainsAny(token,"\r\n\t ") { return nil,errors.New("a Code personal access token (code_pat_) is required") }
	base := config.APIBase
	if base=="" { base=CodeAPIBase }
	u,err := url.Parse(base)
	if err!=nil || (u.Scheme!="https" && u.Scheme!="http") || u.Host=="" || u.User!=nil || u.RawQuery!="" || u.Fragment!="" || strings.Trim(u.Path,"/")!="" { return nil,errors.New("invalid configured Code API origin") }
	c := cloneHTTPClient(config.HTTPClient)
	c.CheckRedirect=func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}
	return &CodeClient{base:u,client:c,token:token},nil
}

func(c *CodeClient) read(ctx context.Context, endpoint string, out any) error {
	// Endpoints are assembled by this adapter. Never use response-provided URLs.
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,strings.TrimRight(c.base.String(),"/")+endpoint,nil)
	if err!=nil{return errors.New("invalid Code API request")}
	req.Header.Set("PRIVATE-TOKEN",c.token)
	req.Header.Set("Accept","application/json")
	resp,err:=c.client.Do(req)
	if err!=nil {
		if ctx.Err()!=nil{return ctx.Err()}
		return errors.New("Code repository service could not be reached")
	}
	defer resp.Body.Close()
	body,err:=readLimited(resp.Body,defaultResponseLimit)
	if err!=nil{return errors.New("Code API response exceeds the read limit or is incomplete")}
	if resp.StatusCode<200 || resp.StatusCode>=300 {
		message:="Code repository request failed"
		// Only expose documented error identifiers, never an upstream response body.
		var detail struct { Error string `json:"error"` }
		_ = json.Unmarshal(body,&detail)
		switch detail.Error {
		case "pat_expired_or_revoked","pat_insufficient_scope","pat_repository_access_denied","pat_repository_not_found","pat_username_mismatch": message=detail.Error
		default:
			switch resp.StatusCode {case 401:message="Code token is invalid, expired or revoked";case 403:message="Code token lacks permission for this repository";case 404:message="Code repository or revision was not found";case 429:message="Code request rate limit exceeded"}
		}
		return &APIError{StatusCode:resp.StatusCode,Message:message}
	}
	if err=json.Unmarshal(body,out);err!=nil{return errors.New("Code API returned an invalid response")}
	return nil
}

func(c *CodeClient) Account(ctx context.Context)(CodeAccount,error) {
	var account CodeAccount
	if err:=c.read(ctx,"/api/v3/user",&account);err!=nil{return account,err}
	if account.ID<=0 || strings.TrimSpace(account.Username)==""{return CodeAccount{},errors.New("Code did not return an authenticated account")}
	return account,nil
}

func(c *CodeClient) Open(ctx context.Context,address Address)(Remote,error) {
	if address.Provider!=AlibabaCode{return nil,errors.New("Git connection does not match repository host")}
	var project struct {
		ID int64 `json:"id"`
		Name string `json:"name"`
		Path string `json:"path_with_namespace"`
		DefaultBranch string `json:"default_branch"`
		Visibility int `json:"visibility_level"`
	}
	if err:=c.read(ctx,"/api/v3/projects/"+url.PathEscape(address.Repository),&project);err!=nil{return nil,err}
	if project.ID<=0 || project.Path=="" || project.DefaultBranch==""{return nil,errors.New("Code repository response is incomplete")}
	if !strings.EqualFold(project.Path,address.Repository){return nil,errors.New("Code repository identity changed; use its current URL")}
	return &codeRemote{client:c,metadata:Repository{ID:project.ID,Name:project.Name,FullName:project.Path,DefaultBranch:project.DefaultBranch,Private:project.Visibility!=20,HTMLURL:address.URL},blobs:map[string]codeBlob{}},nil
}

type codeBlob struct { commit,path string; lfs bool }
type codeRemote struct {
	client *CodeClient
	metadata Repository
	mu sync.Mutex
	blobs map[string]codeBlob
}

func(r *codeRemote)Info()Repository{return r.metadata}
func(r *codeRemote) endpoint(version,operation string)string{return "/api/"+version+"/projects/"+strconv.FormatInt(r.metadata.ID,10)+"/repository/"+operation}

func(r *codeRemote)ResolveCommit(ctx context.Context,ref string)(string,error){
	if ref==""{ref="refs/heads/"+r.metadata.DefaultBranch}
	if !ValidRef(ref) {
		return "", errors.New("invalid Git revision")
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		// The documented v4 tag page includes the target commit, including for
		// annotated tags. Do not depend on the untyped v3 single-tag response.
		tags, err := r.ListTags(ctx)
		if err != nil {
			return "", err
		}
		name := strings.TrimPrefix(ref, "refs/tags/")
		for _, tag := range tags {
			if tag.Name == name {
				return tag.Commit.SHA, nil
			}
		}
		return "", &APIError{StatusCode: http.StatusNotFound, Message: "Code tag was not found"}
	}
	var result struct { ID string `json:"id"`; Commit struct{ID string `json:"id"`} `json:"commit"` }
	var endpoint string
	switch {
	case strings.HasPrefix(ref,"refs/heads/"): endpoint=r.endpoint("v3","branches/"+url.PathEscape(strings.TrimPrefix(ref,"refs/heads/")))
	default: endpoint=r.endpoint("v3","commits/"+url.PathEscape(ref))
	}
	if err:=r.client.read(ctx,endpoint,&result);err!=nil{return "",err}
	sha:=result.ID
	if strings.HasPrefix(ref,"refs/"){sha=result.Commit.ID}
	if !IsCommitSHA(sha){return "",errors.New("Code did not return a complete commit SHA")}
	return sha,nil
}

func IsCommitSHA(value string)bool{
	if len(value)!=40 && len(value)!=64{return false}
	for _,c:=range value{if !(c>='0'&&c<='9'||c>='a'&&c<='f'){return false}}
	return true
}

func(r *codeRemote)GetTree(ctx context.Context,sha string)(Tree,error){
	if !IsCommitSHA(sha){return Tree{},errors.New("a complete commit SHA is required to read repository files")}
	var entries []struct { ID string `json:"id"`; Path string `json:"path"`; Mode string `json:"mode"`; Type string `json:"type"`; Size int64 `json:"size"`; LFS *struct {OID string `json:"oid"`} `json:"lfs_pointer"` }
	q:=url.Values{"ref_name":{sha},"type":{"RECURSIVE"},"need_lfs_info":{"true"}}
	if err:=r.client.read(ctx,r.endpoint("v4","tree")+"?"+q.Encode(),&entries);err!=nil{return Tree{},err}
	if len(entries)>100000{return Tree{},errors.New("repository tree exceeds the file count limit")}
	tree:=Tree{SHA:sha,Entries:make([]TreeEntry,0,len(entries))}
	r.mu.Lock();defer r.mu.Unlock()
	for _,entry:=range entries{
		if entry.ID=="" || entry.Path=="" || entry.Mode==""{return Tree{},errors.New("Code returned an incomplete repository entry")}
		tree.Entries=append(tree.Entries,TreeEntry{SHA:entry.ID,Path:entry.Path,Mode:entry.Mode,Type:entry.Type,Size:entry.Size})
		if entry.Type=="blob"{r.blobs[entry.ID]=codeBlob{commit:sha,path:entry.Path,lfs:entry.LFS!=nil && entry.LFS.OID!=""}}
	}
	return tree,nil
}

func(r *codeRemote)GetBlob(ctx context.Context,sha string)([]byte,error){
	r.mu.Lock();blob,ok:=r.blobs[sha];r.mu.Unlock()
	if !ok{return nil,errors.New("file does not belong to the selected repository snapshot")}
	if blob.lfs{return nil,errors.New("Git LFS content is not supported in Agent or skill configuration; commit the file contents directly")}
	q:=url.Values{"ref":{blob.commit},"filepath":{blob.path}}
	var result struct {Content *string `json:"content"`}
	if err:=r.client.read(ctx,r.endpoint("v4","blobs")+"?"+q.Encode(),&result);err!=nil{return nil,err}
	if result.Content==nil{return nil,errors.New("Code file response is missing content")}
	return []byte(*result.Content),nil
}

type codeRef struct { Name string `json:"name"`; Protected bool `json:"protected"`; Commit struct { ID string `json:"id"` } `json:"commit"` }

func(r *codeRemote) listRefs(ctx context.Context,version,operation string)([]codeRef,error){
	result:=make([]codeRef,0)
	for page:=1;page<=maxRepositoryPages;page++{
		var response struct { Amount *int `json:"amount"`; List []codeRef `json:"list"` }
		if err:=r.client.read(ctx,r.endpoint(version,operation)+fmt.Sprintf("?per_page=100&page=%d&sort=name_asc",page),&response);err!=nil{return nil,err}
		if response.Amount==nil || *response.Amount<0 {return nil,errors.New("Code ref pagination response is incomplete")}
		for _,item:=range response.List { if item.Name=="" || !IsCommitSHA(item.Commit.ID) {return nil,errors.New("Code returned an incomplete revision")} }
		result=append(result,response.List...)
		if len(result)>=*response.Amount{return result,nil}
		if len(response.List)==0{return nil,errors.New("Code ref pagination stopped before all revisions were returned")}
	}
	return nil,errors.New("Code ref pagination limit exceeded")
}

func(r *codeRemote)ListBranches(ctx context.Context)([]Branch,error){
	items,err:=r.listRefs(ctx,"v3","branches");if err!=nil{return nil,err}
	result:=make([]Branch,0,len(items))
	for _,item:=range items{branch:=Branch{Name:item.Name,Protected:item.Protected};branch.Commit.SHA=item.Commit.ID;result=append(result,branch)}
	return result,nil
}

func(r *codeRemote)ListTags(ctx context.Context)([]Tag,error){
	items,err:=r.listRefs(ctx,"v4","tags");if err!=nil{return nil,err}
	result:=make([]Tag,0,len(items))
	for _,item:=range items{tag:=Tag{Name:item.Name};tag.Commit.SHA=item.Commit.ID;result=append(result,tag)}
	return result,nil
}
