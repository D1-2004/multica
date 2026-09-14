package dshhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type storageAPIFixture struct {
	p      Provision
	calls  []CloudCall
	mutate func(string, map[string]any)
}

func storageFixture() (*storageAPIFixture, CloudStorageProvider) {
	p := Provision{Key: Key{uuid.New(), uuid.New()}, Intent: uuid.New(), Spec: provisionSpec(), State: "planned", Step: provisionStepCount}
	p.Resources = []string{"agentic-abc", "acs:nas:cn-beijing:123:accesspoint/ap-abc", roleARN(p), provisionName(p), provisionName(p) + ":" + provisionName(p), provisionName(p)}
	f := &storageAPIFixture{p: p}
	return f, CloudStorageProvider{API: f, Spec: p.Spec}
}
func (f *storageAPIFixture) Call(_ context.Context, c CloudCall, out any) error {
	f.calls = append(f.calls, c)
	p := f.p
	space := map[string]any{"AgenticSpaceId": p.Resources[0], "FileSystemId": p.Spec.FileSystemID, "FileSystemPath": p.Path(), "Description": provisionDescription(p), "Azone": p.Spec.Zone, "Status": "Running", "Quota": map[string]any{"SizeLimit": p.Spec.SizeLimit, "FileCountLimit": p.Spec.FileCountLimit}}
	ap := map[string]any{"ARN": p.Resources[1], "AccessPointId": "ap-abc", "AccessPointName": provisionName(p), "AgenticSpaceId": p.Resources[0], "FileSystemId": p.Spec.FileSystemID, "VpcId": p.Spec.VPCID, "VSwitchId": p.Spec.VSwitchIDs[0], "RootPath": "/", "EnabledRam": true, "Status": "active", "DomainName": "ap-abc.fs-suffix.cn-beijing.nas.aliyuncs.com"}
	role := map[string]any{"Arn": roleARN(p), "RoleName": provisionName(p), "Description": provisionDescription(p), "AssumeRolePolicyDocument": trustPolicy()}
	policy := map[string]any{"PolicyName": provisionName(p), "PolicyType": "Custom", "Description": provisionDescription(p), "DefaultVersion": "v1"}
	volume := map[string]any{"volumeID": "volume-id", "volumeName": provisionName(p), "teamID": p.Spec.TeamID, "status": "AVAILABLE", "storageClass": "AGENTIC_FS", "agenticFSVolumeConfig": map[string]any{"serverAddr": ap["DomainName"].(string) + ":/", "userID": 1000, "groupID": 1000}}
	var r map[string]any
	switch c.Action {
	case "GetCallerIdentity":
		r = map[string]any{"AccountId": p.Spec.AccountID}
	case "GetAgenticSpace":
		r = map[string]any{"AgenticSpace": space}
	case "DescribeAgenticSpaces":
		r = map[string]any{"AgenticSpaces": map[string]any{"AgenticSpace": []any{space}}}
	case "DescribeAccessPoint", "CreateAccessPoint":
		r = map[string]any{"AccessPoint": ap}
	case "ListAccessPoints":
		r = map[string]any{"AccessPoints": []any{ap}}
	case "GetRole", "CreateRole":
		r = map[string]any{"Role": role}
	case "GetPolicy", "CreatePolicy":
		r = map[string]any{"Policy": policy}
	case "GetPolicyVersion":
		r = map[string]any{"PolicyVersion": map[string]any{"VersionId": "v1", "IsDefaultVersion": true, "PolicyDocument": accessPolicy(p)}}
	case "ListPoliciesForRole":
		r = map[string]any{"Policies": map[string]any{"Policy": []any{policy}}}
	case "ListVolumes":
		r = map[string]any{"volumes": []any{volume}}
	case "CreateVolume":
		r = map[string]any{"volume": volume}
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

func TestCloudStorageVerifiesChainAndRejectsDrift(t *testing.T) {
	for _, name := range []string{"valid", "account", "home", "ram_disabled", "role_trust", "policy_scope", "extra_policy", "volume_uid", "volume_team"} {
		t.Run(name, func(t *testing.T) {
			f, c := storageFixture()
			f.mutate = func(action string, r map[string]any) {
				switch {
				case name == "account" && action == "GetCallerIdentity":
					r["AccountId"] = "456"
				case name == "home" && action == "GetAgenticSpace":
					r["AgenticSpace"].(map[string]any)["FileSystemPath"] = "/another/"
				case name == "ram_disabled" && action == "DescribeAccessPoint":
					r["AccessPoint"].(map[string]any)["EnabledRam"] = false
				case name == "role_trust" && action == "GetRole":
					r["Role"].(map[string]any)["AssumeRolePolicyDocument"] = `{"Version":"1","Statement":[]}`
				case name == "policy_scope" && action == "GetPolicyVersion":
					r["PolicyVersion"].(map[string]any)["PolicyDocument"] = strings.ReplaceAll(accessPolicy(f.p), f.p.Resources[1], "*")
				case name == "extra_policy" && action == "ListPoliciesForRole":
					ps := r["Policies"].(map[string]any)
					ps["Policy"] = append(ps["Policy"].([]any), map[string]any{"PolicyName": "AdministratorAccess", "PolicyType": "System"})
				case name == "volume_uid" && action == "ListVolumes":
					r["volumes"].([]any)[0].(map[string]any)["agenticFSVolumeConfig"].(map[string]any)["userID"] = 0
				case name == "volume_team" && action == "ListVolumes":
					r["volumes"].([]any)[0].(map[string]any)["teamID"] = "other"
				}
			}
			s, err := c.VerifyStorage(context.Background(), f.p)
			if name == "valid" {
				if err != nil || s.VolumeName != provisionName(f.p) {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("accepted drift")
			}
		})
	}
}

func TestCloudStoragePreparedWritesAndLostOutcomeLookups(t *testing.T) {
	for step := range provisionStepCount {
		f, c := storageFixture()
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
		call := f.calls[len(f.calls)-1]
		if step == ProvisionSpace && (call.Query["ClientToken"] != p.StepIntent() || call.Query["FileSystemPath"] != p.Path()) {
			t.Fatal("space omitted intent or home")
		}
		if step == ProvisionAccessPoint && (call.Query["EnabledRam"] != true || call.Query["AgenticSpaceId"] != p.Resources[0] || call.Query["PosixUserId"] != nil) {
			t.Fatal("incorrect Agentic AP contract")
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

func TestCloudStorageDuplicateLookupFailsClosed(t *testing.T) {
	f, c := storageFixture()
	p := f.p
	p.Step = ProvisionAccessPoint
	p.Resources = p.Resources[:p.Step]
	f.mutate = func(action string, r map[string]any) {
		if action == "ListAccessPoints" {
			a := r["AccessPoints"].([]any)
			r["AccessPoints"] = append(a, a[0])
		}
	}
	if _, err := c.FindStorageResource(context.Background(), p); !errors.Is(err, ErrPending) {
		t.Fatal("duplicate AP accepted", err)
	}
}

func TestCloudStorageFailedPreflightDoesNotClaimIntent(t *testing.T) {
	f, c := storageFixture()
	p := f.p
	p.Step = ProvisionPolicy
	p.Resources = p.Resources[:p.Step]
	s := &provisionMemory{p: p}
	f.mutate = func(action string, r map[string]any) {
		if action == "DescribeAccessPoint" {
			r["AccessPoint"].(map[string]any)["Status"] = "creating"
		}
	}
	if _, err := (Provisioner{s, c}).Ensure(context.Background(), p.Key, p.Spec); err == nil {
		t.Fatal("AP not ready was accepted")
	}
	if s.p.State != "planned" {
		t.Fatal("unsent request stranded the intent")
	}
}
