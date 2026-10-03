package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dwsclient"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeresource"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

const (
	resourceRequester = "open-requester"
	resourceSecret    = "x-oss-signature=RESOURCE-SECRET-SIG"
)

// fakeResourceDWS answers provider reads the way list_messages_by_ids and
// drive/download_file do, keyed by provider IDs. It records every call.
type fakeResourceDWS struct {
	mu        sync.Mutex
	messages  map[string]string
	files     map[string]dwsclient.MessageFile
	failFiles map[string]error
	failReads map[string]error
	reads     []string
	downloads []string
	inputs    []dingtalkresponse.ActionInput
	onFile    func(fileID string)
}

func newFakeResourceDWS() *fakeResourceDWS {
	return &fakeResourceDWS{messages: map[string]string{}, files: map[string]dwsclient.MessageFile{}, failFiles: map[string]error{}, failReads: map[string]error{}}
}

func (f *fakeResourceDWS) ReadMessageResources(_ context.Context, in dingtalkresponse.ActionInput, conversationID, messageID string) (dwsclient.MessageResources, error) {
	f.mu.Lock()
	f.reads = append(f.reads, messageID)
	f.inputs = append(f.inputs, in)
	raw, ok := f.messages[messageID]
	failure := f.failReads[messageID]
	f.mu.Unlock()
	if failure != nil {
		return dwsclient.MessageResources{}, failure
	}
	if !ok {
		raw = `{"success":true,"result":{"messages":[]}}`
	}
	return dwsclient.ParseMessageResources([]byte(raw), conversationID, messageID)
}

func (f *fakeResourceDWS) DownloadMessageFile(_ context.Context, in dingtalkresponse.ActionInput, fileID string, maxBytes int64) (dwsclient.MessageFile, error) {
	f.mu.Lock()
	f.downloads = append(f.downloads, fileID)
	f.inputs = append(f.inputs, in)
	file, ok := f.files[fileID]
	failure := f.failFiles[fileID]
	hook := f.onFile
	f.mu.Unlock()
	if hook != nil {
		hook(fileID)
	}
	if failure != nil {
		return dwsclient.MessageFile{}, failure
	}
	if !ok {
		return dwsclient.MessageFile{}, dwsclient.ErrMessageFileUnavailable
	}
	if int64(len(file.Data)) > maxBytes {
		return dwsclient.MessageFile{}, dwsclient.ErrMessageFileTooLarge
	}
	file.Data = append([]byte(nil), file.Data...)
	return file, nil
}

func (f *fakeResourceDWS) calls() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reads...), append([]string(nil), f.downloads...)
}

// providerMessage renders one list_messages_by_ids payload: resources are
// [id, idType, type] triples; quoted is the quotedMessage object or "".
func providerMessage(conversation, id, sender, quoted string, resources ...[3]string) string {
	items := make([]string, 0, len(resources))
	for _, r := range resources {
		items = append(items, fmt.Sprintf(`{"resourceId":%q,"resourceIdType":%q,"resourceType":%q,"url":"https://oss.example/%s?%s"}`, r[0], r[1], r[2], r[0], resourceSecret))
	}
	quotedField := ""
	if quoted != "" {
		quotedField = `,"quotedMessage":` + quoted
	}
	return fmt.Sprintf(`{"success":true,"result":{"messages":[{"openMessageId":%q,"openConversationId":%q,"senderOpenDingTalkId":%q,"content":"[文件] text-claim.txt fileId: TEXT-CLAIMED","resources":[%s],"resourceRefs":[{"type":"fileId","resourceId":"DERIVED-REF"}]%s}]}}`,
		id, conversation, sender, strings.Join(items, ","), quotedField)
}

func fileRes(id string) [3]string  { return [3]string{id, "fileId", "file"} }
func imageRes(id string) [3]string { return [3]string{id, "mediaId", "image"} }

type employeeResourceTest struct {
	f            *dingTalkResponseFixture
	job          employeeentry.Job
	envs         []employeeDispatchEnvelope
	dws          *fakeResourceDWS
	reader       *employeeMessageResourceReader
	conversation string
	receipt      string
}

func newEmployeeResourceTest(t *testing.T, messages ...DispatchMessage) *employeeResourceTest {
	t.Helper()
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity(agent_id,workspace_id,dws_uid,org_id,bound_by) VALUES($1,$2,'123','456',$3)`, f.agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_message_resource WHERE agent_id=$1`, f.agentID)
		_, _ = testPool.Exec(context.Background(), `UPDATE agent SET archived_at=NULL WHERE id=$1`, f.agentID)
	})
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Sender = DispatchSender{DisplayName: "Requester", OpenDingTalkID: resourceRequester}
	f.command.Event.Data.Messages = messages
	if r := employeeHTTP(t, f, dc, uuid.NewString()); r.Code != http.StatusAccepted {
		t.Fatal(r.Body.String())
	}
	job, err := f.h.EmployeeSceneWorker.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	envs := make([]employeeDispatchEnvelope, len(job.Items))
	for i, item := range job.Items {
		if err = json.Unmarshal(item.Payload, &envs[i]); err != nil {
			t.Fatal(err)
		}
	}
	dws := newFakeResourceDWS()
	reader := newEmployeeMessageResourceReader(f.h.EmployeeSceneWorker, dws)
	return &employeeResourceTest{f: f, job: job, envs: envs, dws: dws, reader: reader, conversation: f.command.Event.Data.Conversation.OpenConversationID, receipt: job.Items[0].ReceiptID}
}

func (r *employeeResourceTest) read(t *testing.T) employeeresource.Context {
	t.Helper()
	got, err := r.reader.Read(context.Background(), r.job, r.envs)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (r *employeeResourceTest) frozenCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_message_resource WHERE agent_id=$1`, r.f.agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func textFile(name, body string) dwsclient.MessageFile {
	return dwsclient.MessageFile{Name: name, ContentType: "text/plain", Data: []byte(body)}
}

func resourceMessage(id, text string) DispatchMessage {
	return DispatchMessage{OpenMsgID: id, Text: text, SenderOpenDingTalkID: resourceRequester, SenderDisplayName: "Requester"}
}

func assertResourceContextClean(t *testing.T, got employeeresource.Context, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range append(forbidden, "RESOURCE-SECRET-SIG", "oss.example", "TEXT-CLAIMED", "DERIVED-REF") {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("resource context leaks %q: %s", secret, raw)
		}
	}
}

// Only the current message's own resources and the own resources of the
// message it exactly quotes are read; text claims, derived references and a
// quote's nested resource list are never treated as attachments.
func TestEmployeeResourceCurrentMessageAndVerifiedReferenceOnly(t *testing.T) {
	quotedObject := `{"openMessageId":"msg-quoted","senderOpenDingTalkId":"open-colleague","resources":[{"resourceId":"F-NESTED","resourceIdType":"fileId","resourceType":"file"}]}`
	outer := resourceMessage("msg-outer", "[文件] text-claim.txt fileId: TEXT-CLAIMED 看看这个和引用的那个")
	outer.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: "msg-quoted", Text: "[文件] quoted.txt fileId: F-NESTED"}
	r := newEmployeeResourceTest(t, outer)
	r.dws.messages["msg-outer"] = providerMessage(r.conversation, "msg-outer", resourceRequester, quotedObject, fileRes("F-OWN"))
	r.dws.messages["msg-quoted"] = providerMessage(r.conversation, "msg-quoted", "open-colleague", "", fileRes("F-QUOTED"))
	r.dws.files["F-OWN"] = textFile("own.txt", "OWN-CODE-8812")
	r.dws.files["F-QUOTED"] = textFile("quoted.md", "QUOTED-CODE-5531")
	for _, leak := range []string{"TEXT-CLAIMED", "F-NESTED", "DERIVED-REF"} {
		r.dws.files[leak] = textFile("leak.txt", "LEAK-"+leak)
	}
	got := r.read(t)
	reads, downloads := r.dws.calls()
	if strings.Join(reads, ",") != "msg-outer,msg-quoted" || strings.Join(downloads, ",") != "F-OWN,F-QUOTED" {
		t.Fatalf("reads=%v downloads=%v", reads, downloads)
	}
	if got.Version != employeeresource.ContextVersion || len(got.Items) != 2 {
		t.Fatalf("context = %+v", got)
	}
	own, quoted := got.Items[0], got.Items[1]
	source := r.receipt + "/msg-outer"
	if own.Relation != employeeresource.Current || own.State != employeeresource.Available || own.Text != "OWN-CODE-8812" || own.Name != "own.txt" || own.SourceRef != source || own.Ref == "" {
		t.Fatalf("own = %+v", own)
	}
	if quoted.Relation != employeeresource.Quoted || quoted.State != employeeresource.Available || quoted.Text != "QUOTED-CODE-5531" || quoted.SourceRef != source || quoted.Ref == own.Ref {
		t.Fatalf("quoted = %+v", quoted)
	}
	if got.TextBytes != len("OWN-CODE-8812")+len("QUOTED-CODE-5531") {
		t.Fatalf("text bytes = %d", got.TextBytes)
	}
	assertResourceContextClean(t, got, "LEAK-", "F-OWN", "F-QUOTED", "F-NESTED")
	// Every read ran as the agent's bound identity in the scene's conversation.
	for _, in := range r.dws.inputs {
		if in.DWSUID != "123" || in.DWSOrgID != "456" || in.AgentID != r.f.agentID || in.ConversationID != r.conversation || in.SceneID != r.job.Scope.SceneID {
			t.Fatalf("provider identity = %+v", in)
		}
	}
	var relation, requester, principal, message string
	if err := testPool.QueryRow(context.Background(), `SELECT relation,requester_ref,principal_id::text,message_id FROM employee_message_resource WHERE agent_id=$1 AND resource_id='F-QUOTED'`, r.f.agentID).Scan(&relation, &requester, &principal, &message); err != nil {
		t.Fatal(err)
	}
	if relation != "quoted" || requester != "dingtalk:456:open_id:"+resourceRequester || principal != r.job.PrincipalID || message != "msg-quoted" {
		t.Fatalf("frozen binding = %s %s %s %s", relation, requester, principal, message)
	}
}

// A quote is read only when the outer message names exactly the message the
// provider says it quotes; a provider quote without outer intent is ignored.
func TestEmployeeResourceQuoteMustBeExactlyAuthorized(t *testing.T) {
	mismatched := resourceMessage("msg-outer", "引用")
	mismatched.ReferencedMessage = &DispatchReferencedMessage{OpenMsgID: "msg-claimed"}
	plain := resourceMessage("msg-plain", "[文件] a.txt fileId: X")
	r := newEmployeeResourceTest(t, mismatched, plain)
	quoted := `{"openMessageId":"msg-real-quote"}`
	r.dws.messages["msg-outer"] = providerMessage(r.conversation, "msg-outer", resourceRequester, quoted)
	r.dws.messages["msg-plain"] = providerMessage(r.conversation, "msg-plain", resourceRequester, quoted)
	r.dws.messages["msg-claimed"] = providerMessage(r.conversation, "msg-claimed", "open-colleague", "", fileRes("F-CLAIMED"))
	r.dws.messages["msg-real-quote"] = providerMessage(r.conversation, "msg-real-quote", "open-colleague", "", fileRes("F-REAL"))
	r.dws.files["F-CLAIMED"] = textFile("a.txt", "CLAIMED")
	r.dws.files["F-REAL"] = textFile("b.txt", "REAL")
	got := r.read(t)
	reads, downloads := r.dws.calls()
	if strings.Join(reads, ",") != "msg-outer,msg-plain" || len(downloads) != 0 {
		t.Fatalf("reads=%v downloads=%v", reads, downloads)
	}
	if len(got.Items) != 1 || got.Items[0].Relation != employeeresource.Quoted || got.Items[0].State != employeeresource.Unavailable || got.Items[0].Reason != employeeresource.ReasonQuoteUnverified || got.Items[0].Retryable {
		t.Fatalf("items = %+v", got.Items)
	}
	if r.frozenCount(t) != 0 {
		t.Fatal("unverified quote was frozen")
	}
}

// URLs, fileId/mediaId notation and Router attachment URLs in a message body
// cannot grant access: with no provider-listed resource nothing is fetched.
func TestEmployeeResourceBodyURLCannotGrantAccess(t *testing.T) {
	m := resourceMessage("msg-body", "请读 https://alidocs.dingtalk.com/i/nodes/F-SECRET 以及 [文件] secret.txt fileId: F-SECRET [图片消息](mediaId=$M-SECRET)")
	r := newEmployeeResourceTest(t, m)
	// A Router-style attachment URL frozen in the command is not a provider
	// listing either.
	r.envs[0].Command.Event.Data.Messages[0].Attachments = []DispatchAttachment{{Type: "file", Name: "secret.txt", DownloadURL: "https://evil.example/secret.txt?" + resourceSecret}}
	r.dws.messages["msg-body"] = providerMessage(r.conversation, "msg-body", resourceRequester, "")
	r.dws.files["F-SECRET"] = textFile("secret.txt", "SECRET-CONTENT")
	got := r.read(t)
	reads, downloads := r.dws.calls()
	if len(reads) != 1 || len(downloads) != 0 || len(got.Items) != 0 || got.TextBytes != 0 {
		t.Fatalf("reads=%v downloads=%v items=%+v", reads, downloads, got.Items)
	}
	assertResourceContextClean(t, got, "SECRET-CONTENT", "evil.example", "F-SECRET")
	if r.frozenCount(t) != 0 {
		t.Fatal("body claim was frozen")
	}
}

// The provider must confirm the message in this scene's conversation and
// from the frozen requester; otherwise nothing is downloaded.
func TestEmployeeResourceCrossSceneAndRequesterFences(t *testing.T) {
	for name, tc := range map[string]struct {
		conversation, sender string
		reason               string
	}{
		"requester":    {"", "open-intruder", employeeresource.ReasonRequesterMismatch},
		"conversation": {"cid-other-scene", resourceRequester, "message_unverified"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a.txt fileId: F-1"))
			conversation := r.conversation
			if tc.conversation != "" {
				conversation = tc.conversation
			}
			r.dws.messages["msg-1"] = providerMessage(conversation, "msg-1", tc.sender, "", fileRes("F-1"))
			r.dws.files["F-1"] = textFile("a.txt", "SHOULD-NOT-READ")
			got := r.read(t)
			_, downloads := r.dws.calls()
			if len(downloads) != 0 || len(got.Items) != 1 || got.Items[0].State != employeeresource.Unavailable || got.Items[0].Reason != tc.reason || got.Items[0].Text != "" {
				t.Fatalf("downloads=%v items=%+v", downloads, got.Items)
			}
			if r.frozenCount(t) != 0 {
				t.Fatal("unverified message was frozen")
			}
		})
	}
}

// Scope, tenant org, scene, bound identity, permission and the lease are all
// checked before any provider I/O or save; failures leave no frozen rows.
func TestEmployeeResourceScopeOrgPermissionAndTenantFences(t *testing.T) {
	setup := func(t *testing.T) *employeeResourceTest {
		r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a.txt fileId: F-1"))
		r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-1"))
		r.dws.files["F-1"] = textFile("a.txt", "CONTENT")
		return r
	}
	for name, mutate := range map[string]func(t *testing.T, r *employeeResourceTest){
		"envelope org": func(_ *testing.T, r *employeeResourceTest) { r.envs[0].Command.ExternalIdentity.DWS.OrgID = "999" },
		"job scene":    func(_ *testing.T, r *employeeResourceTest) { r.job.Scope.SceneID = uuid.NewString() },
		"envelope principal": func(_ *testing.T, r *employeeResourceTest) {
			r.envs[0].PrincipalID = uuid.NewString()
		},
		"conversation": func(_ *testing.T, r *employeeResourceTest) {
			r.envs[0].Command.Event.Data.Conversation.OpenConversationID = "cid-other-scene"
		},
		"identity changed": func(t *testing.T, r *employeeResourceTest) {
			if _, err := testPool.Exec(context.Background(), `UPDATE agent_dingtalk_identity SET dws_uid='777' WHERE agent_id=$1`, r.f.agentID); err != nil {
				t.Fatal(err)
			}
		},
		"tenant unbound": func(t *testing.T, r *employeeResourceTest) {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id=$1`, r.f.agentID); err != nil {
				t.Fatal(err)
			}
		},
		"permission revoked": func(t *testing.T, r *employeeResourceTest) {
			if _, err := testPool.Exec(context.Background(), `UPDATE agent SET archived_at=now() WHERE id=$1`, r.f.agentID); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := setup(t)
			mutate(t, r)
			if _, err := r.reader.Read(context.Background(), r.job, r.envs); err == nil {
				t.Fatal("fenced read succeeded")
			}
			reads, downloads := r.dws.calls()
			if len(reads) != 0 || len(downloads) != 0 || r.frozenCount(t) != 0 {
				t.Fatalf("fenced read touched the provider or saved: reads=%v downloads=%v", reads, downloads)
			}
		})
	}
	// Authority lost while the download is in flight: nothing is saved.
	for name, revoke := range map[string]string{
		"revoked during download": `UPDATE agent SET archived_at=now() WHERE id=$1`,
		"lease lost":              `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE agent_id=$1`,
	} {
		t.Run(name, func(t *testing.T) {
			r := setup(t)
			r.dws.onFile = func(string) {
				if _, err := testPool.Exec(context.Background(), revoke, r.f.agentID); err != nil {
					t.Error(err)
				}
			}
			if _, err := r.reader.Read(context.Background(), r.job, r.envs); err == nil {
				t.Fatal("read saved after authority was lost")
			}
			if r.frozenCount(t) != 0 {
				t.Fatal("resource frozen without current authority")
			}
		})
	}
}

// A retry of the same source reuses the frozen read: same ref, hash and text
// even when the provider now serves different bytes; concurrent readers
// converge on one frozen row.
func TestEmployeeResourceRetryKeepsFrozenHashAndSource(t *testing.T) {
	r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a.txt fileId: F-1"))
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-1"))
	r.dws.files["F-1"] = textFile("a.txt", "VERSION-ONE")
	first := r.read(t)
	r.dws.files["F-1"] = textFile("a.txt", "VERSION-TWO")
	second := r.read(t)
	_, downloads := r.dws.calls()
	if len(downloads) != 1 || len(first.Items) != 1 || len(second.Items) != 1 {
		t.Fatalf("downloads=%v first=%+v second=%+v", downloads, first, second)
	}
	if !reflect.DeepEqual(first.Items[0], second.Items[0]) || !strings.Contains(second.Items[0].Text, "VERSION-ONE") || second.Items[0].SHA256 == "" {
		t.Fatalf("retry changed the frozen read: %+v vs %+v", first.Items[0], second.Items[0])
	}
}

// Two attempts of one job racing on separate connections freeze exactly one
// read, even when the provider serves different bytes to each.
func TestEmployeeResourceConcurrentAttemptsFreezeOneRead(t *testing.T) {
	race := newEmployeeResourceTest(t, resourceMessage("msg-2", "[文件] b.txt fileId: F-2"))
	race.dws.messages["msg-2"] = providerMessage(race.conversation, "msg-2", resourceRequester, "", fileRes("F-2"))
	var served sync.Mutex
	version := 0
	race.dws.onFile = func(string) {
		served.Lock()
		defer served.Unlock()
		version++
		race.dws.mu.Lock()
		race.dws.files["F-2"] = textFile("b.txt", fmt.Sprintf("RACE-%d", version))
		race.dws.mu.Unlock()
	}
	race.dws.files["F-2"] = textFile("b.txt", "RACE-0")
	results := make([]employeeresource.Context, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = race.reader.Read(context.Background(), race.job, race.envs)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil || len(results[i].Items) != 1 {
			t.Fatalf("racer %d: %+v %v", i, results[i], err)
		}
	}
	if !reflect.DeepEqual(results[0].Items[0], results[1].Items[0]) || race.frozenCount(t) != 1 {
		t.Fatalf("racers diverged: %+v vs %+v (rows %d)", results[0].Items[0], results[1].Items[0], race.frozenCount(t))
	}
}

// A provider that later lists a different resource for the same message is
// read afresh; the frozen read of the replaced ID is never returned for it.
func TestEmployeeResourceResourceIDSwapped(t *testing.T) {
	r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a.txt fileId: F-OLD"))
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-OLD"))
	r.dws.files["F-OLD"] = textFile("a.txt", "OLD-CONTENT")
	r.dws.files["F-NEW"] = textFile("a.txt", "NEW-CONTENT")
	first := r.read(t)
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-NEW"))
	second := r.read(t)
	if len(second.Items) != 1 || second.Items[0].Text != "NEW-CONTENT" || second.Items[0].Ref == first.Items[0].Ref {
		t.Fatalf("swapped resource = %+v (first %+v)", second.Items, first.Items)
	}
}

// Limits, oversize, corruption and type mismatch each get an explicit
// state; nothing is silently dropped and only allowed files are downloaded.
func TestEmployeeResourceLimitsOversizeCorruptAndMismatch(t *testing.T) {
	r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] many"))
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "",
		fileRes("F-OK"), imageRes("I-1"), fileRes("F-BIG"), fileRes("F-JSON"), imageRes("I-2"), fileRes("F-PNG"), fileRes("F-5"), imageRes("I-3"), [3]string{"V-1", "mediaId", "video"})
	r.dws.files["F-OK"] = textFile("ok.csv", "a,b\n1,2\n")
	r.dws.files["F-BIG"] = textFile("big.txt", strings.Repeat("x", 64))
	r.dws.failFiles["F-BIG"] = dwsclient.ErrMessageFileTooLarge
	r.dws.files["F-JSON"] = textFile("bad.json", `{"broken":`)
	r.dws.files["F-PNG"] = dwsclient.MessageFile{Name: "photo.txt", Data: []byte("\x89PNG\r\n\x1a\n\x00\x00")}
	r.dws.files["F-5"] = textFile("five.txt", "never fetched")
	got := r.read(t)
	_, downloads := r.dws.calls()
	if strings.Join(downloads, ",") != "F-OK,F-BIG,F-JSON,F-PNG" {
		t.Fatalf("downloads = %v", downloads)
	}
	want := []struct {
		kind   string
		state  employeeresource.State
		reason string
	}{
		{"file", employeeresource.Available, ""},
		{"image", employeeresource.Unsupported, employeeresource.ReasonVisionUnavailable},
		{"file", employeeresource.Unavailable, employeeresource.ReasonTooLarge},
		{"file", employeeresource.Unavailable, employeeresource.ReasonCorruptContent},
		{"image", employeeresource.Unsupported, employeeresource.ReasonVisionUnavailable},
		{"file", employeeresource.Unavailable, employeeresource.ReasonTypeMismatch},
		{"file", employeeresource.Unavailable, employeeresource.ReasonTooManyFiles},
		{"image", employeeresource.Unavailable, employeeresource.ReasonTooManyImages},
		{"video", employeeresource.Unsupported, employeeresource.ReasonUnsupportedType},
	}
	if len(got.Items) != len(want) {
		t.Fatalf("items = %+v", got.Items)
	}
	for i, w := range want {
		item := got.Items[i]
		if item.Kind != w.kind || item.State != w.state || item.Reason != w.reason || item.Retryable || item.Ref == "" {
			t.Fatalf("item %d = %+v, want %+v", i, item, w)
		}
		if w.state != employeeresource.Available && item.Text != "" {
			t.Fatalf("item %d exposes text %q", i, item.Text)
		}
	}
	if r.frozenCount(t) != len(want) {
		t.Fatalf("frozen rows = %d", r.frozenCount(t))
	}
}

// The window shares one text budget: a later file is partial at a character
// boundary, while a transient provider failure is unavailable, retryable and
// not frozen, so a later attempt can still read it.
func TestEmployeeResourcePartialVersusUnavailable(t *testing.T) {
	r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a"), resourceMessage("msg-2", "[文件] b"), resourceMessage("msg-3", "[文件] c"))
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-1"))
	r.dws.messages["msg-2"] = providerMessage(r.conversation, "msg-2", resourceRequester, "", fileRes("F-2"))
	r.dws.messages["msg-3"] = providerMessage(r.conversation, "msg-3", resourceRequester, "", fileRes("F-3"))
	first := strings.Repeat("甲", 4000)  // 12000 bytes
	second := strings.Repeat("乙", 4000) // 12000 bytes, only 4384 fit
	r.dws.files["F-1"] = textFile("one.md", first)
	r.dws.files["F-2"] = textFile("two.md", second)
	r.dws.files["F-3"] = textFile("three.txt", "迟到-CODE") // one byte left cannot hold its first character
	r.dws.failFiles["F-3"] = errors.New("provider timeout")
	got := r.read(t)
	if len(got.Items) != 3 || got.TextBytes > 16<<10 {
		t.Fatalf("items=%d text=%d", len(got.Items), got.TextBytes)
	}
	a, b, c := got.Items[0], got.Items[1], got.Items[2]
	if a.State != employeeresource.Available || a.Text != first {
		t.Fatalf("first = %s/%s", a.State, a.Reason)
	}
	// 16384-12000 = 4384 bytes remain; 1461 whole characters fit.
	if b.State != employeeresource.Partial || b.Reason != employeeresource.ReasonTextTruncated || len(b.Text) != 4383 || !strings.HasPrefix(second, b.Text) ||
		b.Range == nil || b.Range.End != len(b.Text) || b.Range.Total != len(second) {
		t.Fatalf("second = %s/%s len=%d range=%+v", b.State, b.Reason, len(b.Text), b.Range)
	}
	if c.State != employeeresource.Unavailable || c.Reason != employeeresource.ReasonProviderUnavailable || !c.Retryable || c.Ref != "" || c.Text != "" ||
		c.SourceRef != r.receipt+"/msg-3" || c.Relation != employeeresource.Current || c.Kind != "file" {
		t.Fatalf("third = %+v", c)
	}
	if r.frozenCount(t) != 2 {
		t.Fatalf("frozen = %d", r.frozenCount(t))
	}
	// The provider recovers: the retry freezes the third file, reusing the
	// first two without downloading them again.
	delete(r.dws.failFiles, "F-3")
	retry := r.read(t)
	_, downloads := r.dws.calls()
	if strings.Join(downloads, ",") != "F-1,F-2,F-3,F-3" || !reflect.DeepEqual(retry.Items[0], a) || !reflect.DeepEqual(retry.Items[1], b) {
		t.Fatalf("downloads=%v", downloads)
	}
	if c := retry.Items[2]; c.State != employeeresource.Partial || c.Reason != employeeresource.ReasonTextBudget || c.Retryable || c.Ref == "" {
		t.Fatalf("recovered third = %+v", c)
	}
}

// Logs and the context carry IDs, states and hashes only: never signed URLs,
// provider resource IDs, file names or file content.
func TestEmployeeResourceLogsCarryNoContentOrSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r := newEmployeeResourceTest(t, resourceMessage("msg-1", "[文件] a.txt fileId: F-PRIVATE"))
	r.dws.messages["msg-1"] = providerMessage(r.conversation, "msg-1", resourceRequester, "", fileRes("F-PRIVATE"))
	r.dws.files["F-PRIVATE"] = textFile("salary-2026.txt", "PRIVATE-BODY-123")
	got := r.read(t)
	if len(got.Items) != 1 || got.Items[0].Text != "PRIVATE-BODY-123" {
		t.Fatalf("items = %+v", got.Items)
	}
	if !strings.Contains(logs.String(), "employee_message_resource") {
		t.Fatalf("no resource log: %s", logs.String())
	}
	for _, secret := range []string{"PRIVATE-BODY-123", "salary-2026", "F-PRIVATE", "RESOURCE-SECRET-SIG", "oss.example"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaks %q: %s", secret, logs.String())
		}
	}
}
