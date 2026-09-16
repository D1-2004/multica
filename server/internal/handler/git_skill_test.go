package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/gitrepo"
)

type skillRepositoryFixture struct { tree gitrepo.Tree; blobs map[string]string; failed string; reads []string }
func (f *skillRepositoryFixture) Info() gitrepo.Repository { return gitrepo.Repository{DefaultBranch:"main"} }
func (f *skillRepositoryFixture) ResolveCommit(context.Context,string)(string,error){ return gitSourceSHA1,nil }
func (f *skillRepositoryFixture) GetTree(_ context.Context,sha string)(gitrepo.Tree,error){ if sha!=gitSourceSHA1 { return gitrepo.Tree{},errors.New("mutable revision read") };return f.tree,nil }
func (f *skillRepositoryFixture) GetBlob(_ context.Context,sha string)([]byte,error){ f.reads=append(f.reads,sha);if sha==f.failed{return nil,errors.New("read denied")};return []byte(f.blobs[sha]),nil }
func (f *skillRepositoryFixture) ListBranches(context.Context)([]gitrepo.Branch,error){return []gitrepo.Branch{{Name:"release/v2"}},nil}
func (f *skillRepositoryFixture) ListTags(context.Context)([]gitrepo.Tag,error){return nil,nil}

func newSkillRepositoryFixture() *skillRepositoryFixture {
	return &skillRepositoryFixture{tree:gitrepo.Tree{Entries:[]gitrepo.TreeEntry{
		{Path:"skills/review/SKILL.md",Type:"blob",Mode:"100644",SHA:"manifest"},
		{Path:"skills/review/references/check.md",Type:"blob",Mode:"100644",SHA:"check"},
		{Path:"skills/other/SKILL.md",Type:"blob",Mode:"100644",SHA:"other"},
	}},blobs:map[string]string{"manifest":"---\nname: review\ndescription: Review changes\n---\nReview.","check":"Check all changes."}}
}

func TestRepositorySkillUsesScopedDirectoryAndPinnedCommit(t *testing.T){
	f:=newSkillRepositoryFixture()
	address,err:=gitrepo.ParseAddress("https://github.com/team/repo/tree/release/v2/skills/review")
	if err!=nil{t.Fatal(err)}
	result,err:=readRepositorySkill(t.Context(),f,address,"","","")
	if err!=nil{t.Fatal(err)}
	if result.name!="review" || len(result.files)!=1 || result.files[0].path!="references/check.md" || result.origin["commit_sha"]!=gitSourceSHA1 {t.Fatalf("unexpected imported skill: %#v",result)}
	for _,sha:=range f.reads{if sha=="other"{t.Fatal("read outside requested skill")}}
}

func TestRepositorySkillFailsInsteadOfReturningPartialContents(t *testing.T){
	for _,scenario:=range []string{"missing_file","truncated_tree","oversized_file","symlink","ambiguous_root"}{
		t.Run(scenario,func(t *testing.T){
			f:=newSkillRepositoryFixture();directory:="skills/review"
			switch scenario {case "missing_file":f.failed="check";case "truncated_tree":f.tree.Truncated=true;case "oversized_file":f.blobs["check"]=strings.Repeat("x",maxImportFileSize+1);case "symlink":f.tree.Entries[1].Mode="120000";case "ambiguous_root":directory=""}
			result,err:=readRepositorySkill(t.Context(),f,gitrepo.Address{URL:"https://github.com/team/repo"},"main",directory,"")
			if err==nil || result!=nil{t.Fatalf("incomplete import accepted: %#v, %v",result,err)}
		})
	}
}
