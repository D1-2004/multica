package wsfs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func testSpec() dshhost.ProvisionSpec {
	return dshhost.ProvisionSpec{AccountID: "123", Region: "cn-beijing", Zone: "cn-beijing-k", TeamID: "team", FileSystemID: "fs", VPCID: "vpc", SecurityGroupID: "sg", VSwitchIDs: []string{"vsw"}, SizeLimit: 10 << 30, FileCountLimit: 10000}
}

type wsStorageAPI struct {
	p      WorkspaceProvision
	calls  []dshhost.CloudCall
	mutate func(string, map[string]any)
}

func wsStorageFixture() (*wsStorageAPI, CloudStorageProvider) {
	p := WorkspaceProvision{WorkspaceID: uuid.New(), Intent: uuid.New(), Spec: testSpec(), State: "planned", Step: provisionStepCount}
	p.Resources = []string{
		"agentic-ws",
		"acs:nas:cn-beijing:123:accesspoint/ap-ro1",
		"acs:nas:cn-beijing:123:accesspoint/ap-rw1",
		roleARN(p, "ro"),
		roleARN(p, "rw"),
		provisionName(p, "ro"),
		provisionName(p, "rw"),
		provisionName(p, "ro") + ":" + provisionName(p, "ro"),
		provisionName(p, "rw") + ":" + provisionName(p, "rw"),
		provisionName(p, "ro"),
		provisionName(p, "rw"),
	}
	f := &wsStorageAPI{p: p}
	return f, CloudStorageProvider{API: f, Spec: p.Spec}
}

func (f *wsStorageAPI) laneAP(lane string) map[string]any {
	id := "ap-" + lane + "1"
	arn := "acs:nas:cn-beijing:123:accesspoint/" + id
	return map[string]any{
		"ARN": arn, "AccessPointId": id, "AccessPointName": provisionName(f.p, lane),
		"AgenticSpaceId": f.p.Resources[0], "FileSystemId": f.p.Spec.FileSystemID,
		"VpcId": f.p.Spec.VPCID, "VSwitchId": f.p.Spec.VSwitchIDs[0],
		"RootPath": "/files", "EnabledRam": true, "Status": "active",
		"DomainName": id + ".fs-suffix." + f.p.Spec.Region + ".nas.aliyuncs.com",
	}
}

func (f *wsStorageAPI) Call(_ context.Context, c dshhost.CloudCall, out any) error {
	f.calls = append(f.calls, c)
	p := f.p
	space := map[string]any{"AgenticSpaceId": p.Resources[0], "FileSystemId": p.Spec.FileSystemID, "FileSystemPath": p.Path(), "Description": provisionDescription(p, ""), "Azone": p.Spec.Zone, "Status": "Running", "Quota": map[string]any{"SizeLimit": p.Spec.SizeLimit, "FileCountLimit": p.Spec.FileCountLimit}}
	apRO, apRW := f.laneAP("ro"), f.laneAP("rw")
	role := func(lane string) map[string]any {
		return map[string]any{"Arn": roleARN(p, lane), "RoleName": provisionName(p, lane), "Description": provisionDescription(p, lane), "AssumeRolePolicyDocument": trustPolicy()}
	}
	policy := func(lane string) map[string]any {
		return map[string]any{"PolicyName": provisionName(p, lane), "PolicyType": "Custom", "Description": provisionDescription(p, lane), "DefaultVersion": "v1"}
	}
	volume := func(lane string, ap map[string]any) map[string]any {
		return map[string]any{"volumeID": "volume-" + lane, "volumeName": provisionName(p, lane), "teamID": p.Spec.TeamID, "status": "AVAILABLE", "storageClass": "AGENTIC_FS", "agenticFSVolumeConfig": map[string]any{"serverAddr": ap["DomainName"].(string) + ":/", "userID": 1000, "groupID": 1000}}
	}
	var r map[string]any
	switch c.Action {
	case "GetCallerIdentity":
		r = map[string]any{"AccountId": p.Spec.AccountID}
	case "GetAgenticSpace":
		r = map[string]any{"AgenticSpace": space}
	case "DescribeAgenticSpaces":
		r = map[string]any{"AgenticSpaces": map[string]any{"AgenticSpace": []any{space}}}
	case "DescribeAccessPoint":
		id, _ := c.Query["AccessPointId"].(string)
		ap := apRO
		if id == "ap-rw1" {
			ap = apRW
		}
		r = map[string]any{"AccessPoint": ap}
	case "CreateAccessPoint":
		name, _ := c.Query["AccessPointName"].(string)
		id := "ap-ro1"
		if strings.HasSuffix(name, "-rw") {
			id = "ap-rw1"
		}
		r = map[string]any{"AccessPoint": map[string]any{"AccessPointId": id}}
	case "ListAccessPoints":
		r = map[string]any{"AccessPoints": []any{apRO, apRW}}
	case "GetRole", "CreateRole":
		name, _ := c.Query["RoleName"].(string)
		lane := "ro"
		if strings.HasSuffix(name, "-rw") {
			lane = "rw"
		}
		r = map[string]any{"Role": role(lane)}
	case "GetPolicy", "CreatePolicy":
		name, _ := c.Query["PolicyName"].(string)
		lane := "ro"
		if strings.HasSuffix(name, "-rw") {
			lane = "rw"
		}
		r = map[string]any{"Policy": policy(lane)}
	case "GetPolicyVersion":
		name, _ := c.Query["PolicyName"].(string)
		lane := "ro"
		if strings.HasSuffix(name, "-rw") {
			lane = "rw"
		}
		r = map[string]any{"PolicyVersion": map[string]any{"VersionId": "v1", "IsDefaultVersion": true, "PolicyDocument": accessPolicy(p.Resources[apStep(lane)], lane == "rw")}}
	case "ListPoliciesForRole":
		name, _ := c.Query["RoleName"].(string)
		lane := "ro"
		if strings.HasSuffix(name, "-rw") {
			lane = "rw"
		}
		r = map[string]any{"Policies": map[string]any{"Policy": []any{policy(lane)}}}
	case "ListVolumes":
		name, _ := c.Query["volumeName"].(string)
		lane := "ro"
		ap := apRO
		if strings.HasSuffix(name, "-rw") {
			lane, ap = "rw", apRW
		}
		r = map[string]any{"volumes": []any{volume(lane, ap)}}
	case "CreateVolume":
		body, _ := c.Body.(map[string]any)
		name, _ := body["volumeName"].(string)
		lane := "ro"
		ap := apRO
		if strings.HasSuffix(name, "-rw") {
			lane, ap = "rw", apRW
		}
		r = map[string]any{"volume": volume(lane, ap)}
	case "CreateAgenticSpace":
		r = map[string]any{"AgenticSpaceId": p.Resources[0]}
	case "AttachPolicyToRole":
		r = map[string]any{"RequestId": "request"}
	default:
		return errors.New("unexpected test action")
	}
	if f.mutate != nil {
		f.mutate(c.Action, r)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestWorkspacePathHasNoAgent(t *testing.T) {
	ws := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	p := WorkspaceProvision{WorkspaceID: ws}
	if p.Path() != "/multica_11111111-1111-1111-1111-111111111111/" {
		t.Fatalf("path=%s", p.Path())
	}
	if strings.Count(p.Path(), "_") != 1 {
		t.Fatal("workspace path must not include an agent id")
	}
}

func TestWorkspaceCloudStorageVerifiesROWithoutClientWrite(t *testing.T) {
	f, c := wsStorageFixture()
	s, err := c.VerifyStorage(context.Background(), f.p)
	if err != nil || s.ROVolumeName != provisionName(f.p, "ro") || s.RWVolumeName != provisionName(f.p, "rw") {
		t.Fatal(err, s)
	}
	f.mutate = func(action string, r map[string]any) {
		if action == "GetPolicyVersion" {
			r["PolicyVersion"].(map[string]any)["PolicyDocument"] = strings.ReplaceAll(accessPolicy(f.p.Resources[ProvisionAccessPointRO], true), f.p.Resources[ProvisionAccessPointRW], f.p.Resources[ProvisionAccessPointRO])
		}
	}
	if _, err := c.VerifyStorage(context.Background(), f.p); err == nil {
		t.Fatal("RO policy with ClientWrite was accepted")
	}
}

func TestWorkspaceAccessPointUsesFilesRoot(t *testing.T) {
	f, c := wsStorageFixture()
	p := f.p
	p.Step = ProvisionAccessPointRO
	p.Resources = p.Resources[:p.Step]
	create, err := c.PrepareStorageResource(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(context.Background()); err != nil {
		t.Fatal(err)
	}
	var createCall dshhost.CloudCall
	for _, call := range f.calls {
		if call.Action == "CreateAccessPoint" {
			createCall = call
		}
	}
	if createCall.Query["RootPath"] != "/files" || createCall.Query["EnabledRam"] != true {
		t.Fatalf("AP create: %+v", createCall.Query)
	}
	if createCall.Query["FileSystemPath"] != nil {
		t.Fatal("access point must not set FileSystemPath")
	}
}

func TestWorkspaceCloudStoragePreparedWritesAndLostOutcomeLookups(t *testing.T) {
	for step := range provisionStepCount {
		f, c := wsStorageFixture()
		p := f.p
		p.Step = step
		p.Resources = p.Resources[:step]
		create, err := c.PrepareStorageResource(context.Background(), p)
		if err != nil {
			t.Fatal(step, err)
		}
		for _, call := range f.calls {
			if strings.HasPrefix(call.Action, "Create") || call.Action == "AttachPolicyToRole" {
				t.Fatal("prepare mutated cloud")
			}
		}
		id, err := create(context.Background())
		if err != nil || id != f.p.Resources[step] {
			t.Fatalf("step %d: %s %v", step, id, err)
		}
		if step == ProvisionSpace {
			call := f.calls[len(f.calls)-1]
			if call.Query["ClientToken"] != p.StepIntent() || call.Query["FileSystemPath"] != p.Path() {
				t.Fatal("space omitted intent or path")
			}
		}
		f.calls = nil
		got, err := c.FindStorageResource(context.Background(), p)
		if err != nil || got != id {
			t.Fatal(step, got, err)
		}
		for _, call := range f.calls {
			if strings.HasPrefix(call.Action, "Create") || call.Action == "AttachPolicyToRole" {
				t.Fatal("reconciliation repeated a write")
			}
		}
	}
}
