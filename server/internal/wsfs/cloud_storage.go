package wsfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type CloudStorageProvider struct {
	API  dshhost.CloudCaller
	Spec dshhost.ProvisionSpec
}

type spaceInfo struct {
	AgenticSpaceID                             string `json:"AgenticSpaceId"`
	FileSystemID                               string `json:"FileSystemId"`
	FileSystemPath, Azone, Description, Status string
	Quota                                      struct{ SizeLimit, FileCountLimit int64 }
}
type accessInfo struct {
	ARN, AccessPointID, AccessPointName, AgenticSpaceID, FileSystemID, DomainName, Status, VpcID, VSwitchID string
	EnabledRAM                                                                                              bool `json:"EnabledRam"`
	RootPath                                                                                                string
}
type volumeInfo struct {
	VolumeID, VolumeName, TeamID, Status, StorageClass string
	AgenticFSVolumeConfig                              struct {
		ServerAddr      string
		UserID, GroupID int
	}
}

func provisionName(p WorkspaceProvision, lane string) string {
	return "multica-wsfs-" + p.Intent.String() + "-" + lane
}

func provisionDescription(p WorkspaceProvision, lane string) string {
	if lane == "" {
		return "multica-wsfs:" + p.Intent.String() + ":" + p.WorkspaceID.String()
	}
	return "multica-wsfs:" + p.Intent.String() + ":" + p.WorkspaceID.String() + ":" + lane
}

func roleARN(p WorkspaceProvision, lane string) string {
	return "acs:ram::" + p.Spec.AccountID + ":role/" + provisionName(p, lane)
}

func trustPolicy() string {
	return `{"Version":"1","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":["fc.aliyuncs.com"]}}]}`
}

func equalPolicy(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

func accessPolicy(apARN string, write bool) string {
	actions := []string{"nas:ClientMount", "nas:ClientRootAccess"}
	if write {
		actions = []string{"nas:ClientMount", "nas:ClientWrite", "nas:ClientRootAccess"}
	}
	raw, _ := json.Marshal(map[string]any{"Version": "1", "Statement": []any{map[string]any{"Effect": "Allow", "Action": actions, "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"nas:AccessPointArn": apARN}}}}})
	return string(raw)
}

func laneForStep(step int) string {
	switch step {
	case ProvisionAccessPointRO, ProvisionRoleRO, ProvisionPolicyRO, ProvisionAttachRO, ProvisionVolumeRO:
		return "ro"
	case ProvisionAccessPointRW, ProvisionRoleRW, ProvisionPolicyRW, ProvisionAttachRW, ProvisionVolumeRW:
		return "rw"
	}
	return ""
}

func apStep(lane string) int {
	if lane == "rw" {
		return ProvisionAccessPointRW
	}
	return ProvisionAccessPointRO
}

func (c CloudStorageProvider) check(ctx context.Context, p WorkspaceProvision) error {
	if c.API == nil || !reflect.DeepEqual(c.Spec, p.Spec) || p.Spec.Validate() != nil || p.Intent == uuid.Nil || p.WorkspaceID == uuid.Nil || p.Step < 0 || p.Step > provisionStepCount || len(p.Resources) != p.Step {
		return errors.New("invalid persisted workspace filesystem provisioning scope")
	}
	var who struct {
		AccountID string `json:"AccountId"`
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "sts", Action: "GetCallerIdentity"}, &who); err != nil {
		return err
	}
	if who.AccountID != p.Spec.AccountID {
		return errors.New("workspace filesystem cloud credential account does not match placement")
	}
	return nil
}

func (c CloudStorageProvider) PrepareStorageResource(ctx context.Context, p WorkspaceProvision) (func(context.Context) (string, error), error) {
	if err := c.check(ctx, p); err != nil {
		return nil, err
	}
	if p.Step == provisionStepCount {
		return nil, errors.New("workspace filesystem provisioning already has all resources")
	}
	lane := laneForStep(p.Step)
	name := provisionName(p, lane)
	call := dshhost.CloudCall{Service: "nas", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID}}
	switch p.Step {
	case ProvisionSpace:
		call.Action = "CreateAgenticSpace"
		call.Query["Azone"] = p.Spec.Zone
		call.Query["ClientToken"] = p.StepIntent()
		call.Query["Description"] = provisionDescription(p, "")
		call.Query["FileSystemPath"] = p.Path()
		call.Query["Quota"] = map[string]any{"SizeLimit": p.Spec.SizeLimit, "FileCountLimit": p.Spec.FileCountLimit}
	case ProvisionAccessPointRO, ProvisionAccessPointRW:
		if _, err := c.space(ctx, p, p.Resources[ProvisionSpace]); err != nil {
			return nil, err
		}
		call.Action = "CreateAccessPoint"
		call.Query["AgenticSpaceId"] = p.Resources[ProvisionSpace]
		call.Query["VpcId"] = p.Spec.VPCID
		call.Query["VswId"] = p.Spec.VSwitchIDs[0]
		call.Query["EnabledRam"] = true
		call.Query["RootPath"] = "/files"
		call.Query["AccessPointName"] = name
		call.Query["Tag"] = []map[string]string{
			{"Key": "multica.wsfs.intent", "Value": p.Intent.String()},
			{"Key": "multica.wsfs.workspace", "Value": p.WorkspaceID.String()},
			{"Key": "multica.wsfs.lane", "Value": lane},
		}
	case ProvisionRoleRO, ProvisionRoleRW:
		call = dshhost.CloudCall{Service: "ram", Action: "CreateRole", Query: map[string]any{"RoleName": name, "Description": provisionDescription(p, lane), "AssumeRolePolicyDocument": trustPolicy(), "MaxSessionDuration": 3600}}
	case ProvisionPolicyRO, ProvisionPolicyRW:
		apARN := p.Resources[apStep(lane)]
		if _, err := c.access(ctx, p, apARN, lane); err != nil {
			return nil, err
		}
		call = dshhost.CloudCall{Service: "ram", Action: "CreatePolicy", Query: map[string]any{"PolicyName": name, "Description": provisionDescription(p, lane), "PolicyDocument": accessPolicy(apARN, lane == "rw")}}
	case ProvisionAttachRO, ProvisionAttachRW:
		if _, err := c.role(ctx, p, lane); err != nil {
			return nil, err
		}
		if _, err := c.policy(ctx, p, lane); err != nil {
			return nil, err
		}
		call = dshhost.CloudCall{Service: "ram", Action: "AttachPolicyToRole", Query: map[string]any{"RoleName": name, "PolicyName": name, "PolicyType": "Custom"}}
	case ProvisionVolumeRO, ProvisionVolumeRW:
		ap, err := c.access(ctx, p, p.Resources[apStep(lane)], lane)
		if err != nil {
			return nil, err
		}
		if _, err := c.attachment(ctx, p, lane); err != nil {
			return nil, err
		}
		call = dshhost.CloudCall{Service: "fcsandbox", Action: "CreateVolume", Body: map[string]any{"teamID": p.Spec.TeamID, "volumeName": name, "agenticFSVolumeConfig": map[string]any{"serverAddr": ap.DomainName + ":/", "userID": 1000, "groupID": 1000}}}
	}
	return func(ctx context.Context) (string, error) {
		var result struct {
			AgenticSpaceID string `json:"AgenticSpaceId"`
			AccessPoint    accessInfo
			Role           struct{ Arn string }
			Policy         struct{ PolicyName string }
			Volume         volumeInfo
		}
		if err := c.API.Call(ctx, call, &result); err != nil {
			return "", err
		}
		switch p.Step {
		case ProvisionSpace:
			return result.AgenticSpaceID, nil
		case ProvisionAccessPointRO, ProvisionAccessPointRW:
			if !regexp.MustCompile(`^ap-[a-z0-9]+$`).MatchString(result.AccessPoint.AccessPointID) {
				return "", errors.New("workspace filesystem access point create returned no valid ID")
			}
			return "acs:nas:" + p.Spec.Region + ":" + p.Spec.AccountID + ":accesspoint/" + result.AccessPoint.AccessPointID, nil
		case ProvisionRoleRO, ProvisionRoleRW:
			return result.Role.Arn, nil
		case ProvisionPolicyRO, ProvisionPolicyRW:
			return result.Policy.PolicyName, nil
		case ProvisionAttachRO, ProvisionAttachRW:
			return name + ":" + name, nil
		case ProvisionVolumeRO, ProvisionVolumeRW:
			return result.Volume.VolumeName, nil
		}
		return "", errors.New("invalid workspace filesystem storage step")
	}, nil
}

func (c CloudStorageProvider) space(ctx context.Context, p WorkspaceProvision, id string) (spaceInfo, error) {
	var r struct{ AgenticSpace spaceInfo }
	err := c.API.Call(ctx, dshhost.CloudCall{Service: "nas", Action: "GetAgenticSpace", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "AgenticSpaceId": id}}, &r)
	s := r.AgenticSpace
	if err != nil {
		return s, err
	}
	if id == "" || s.AgenticSpaceID != id || s.FileSystemID != p.Spec.FileSystemID || s.FileSystemPath != p.Path() || s.Description != provisionDescription(p, "") || s.Azone != p.Spec.Zone || s.Status != "Running" || s.Quota.SizeLimit != p.Spec.SizeLimit || s.Quota.FileCountLimit != p.Spec.FileCountLimit {
		return s, errors.New("workspace filesystem AgenticSpace ownership or readiness mismatch")
	}
	return s, nil
}

func (c CloudStorageProvider) access(ctx context.Context, p WorkspaceProvision, arn, lane string) (accessInfo, error) {
	prefix := "acs:nas:" + p.Spec.Region + ":" + p.Spec.AccountID + ":accesspoint/"
	id := strings.TrimPrefix(arn, prefix)
	if !strings.HasPrefix(arn, prefix) || !regexp.MustCompile(`^ap-[a-z0-9]+$`).MatchString(id) {
		return accessInfo{}, errors.New("invalid workspace filesystem access point ARN")
	}
	var r struct{ AccessPoint accessInfo }
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "nas", Action: "DescribeAccessPoint", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "AccessPointId": id}}, &r); err != nil {
		return accessInfo{}, err
	}
	a := r.AccessPoint
	domainPattern := `^` + regexp.QuoteMeta(id+"."+p.Spec.FileSystemID) + `-[a-z0-9]+\.` + regexp.QuoteMeta(p.Spec.Region+".nas.aliyuncs.com") + `$`
	if a.ARN != arn || a.AccessPointID != id || a.AccessPointName != provisionName(p, lane) || a.AgenticSpaceID != p.Resources[ProvisionSpace] || a.FileSystemID != p.Spec.FileSystemID || a.VpcID != p.Spec.VPCID || a.VSwitchID != p.Spec.VSwitchIDs[0] || !a.EnabledRAM || a.RootPath != "/files" || a.Status != "active" || !regexp.MustCompile(domainPattern).MatchString(a.DomainName) {
		return a, errors.New("workspace filesystem access point ownership or readiness mismatch")
	}
	return a, nil
}

func (c CloudStorageProvider) role(ctx context.Context, p WorkspaceProvision, lane string) (string, error) {
	name := provisionName(p, lane)
	var r struct {
		Role struct {
			Arn, RoleName, Description, AssumeRolePolicyDocument string
			IsServiceLinkedRole                                  bool
		}
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "GetRole", Query: map[string]any{"RoleName": name}}, &r); err != nil {
		return "", err
	}
	if r.Role.Arn != roleARN(p, lane) || r.Role.RoleName != name || r.Role.Description != provisionDescription(p, lane) || r.Role.IsServiceLinkedRole || !equalPolicy(r.Role.AssumeRolePolicyDocument, trustPolicy()) {
		return "", errors.New("workspace filesystem role ownership or trust mismatch")
	}
	return r.Role.Arn, nil
}

func (c CloudStorageProvider) policy(ctx context.Context, p WorkspaceProvision, lane string) (string, error) {
	name := provisionName(p, lane)
	var r struct {
		Policy struct{ PolicyName, PolicyType, Description, DefaultVersion string }
	}
	q := map[string]any{"PolicyName": name, "PolicyType": "Custom"}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "GetPolicy", Query: q}, &r); err != nil {
		return "", err
	}
	if r.Policy.PolicyName != name || r.Policy.PolicyType != "Custom" || r.Policy.Description != provisionDescription(p, lane) || r.Policy.DefaultVersion == "" {
		return "", errors.New("workspace filesystem permission policy ownership mismatch")
	}
	q["VersionId"] = r.Policy.DefaultVersion
	var v struct {
		PolicyVersion struct {
			PolicyDocument, VersionID string
			IsDefaultVersion          bool
		}
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "GetPolicyVersion", Query: q}, &v); err != nil {
		return "", err
	}
	want := accessPolicy(p.Resources[apStep(lane)], lane == "rw")
	if v.PolicyVersion.VersionID != r.Policy.DefaultVersion || !v.PolicyVersion.IsDefaultVersion || !equalPolicy(v.PolicyVersion.PolicyDocument, want) {
		return "", errors.New("workspace filesystem permission policy exceeds its access point")
	}
	if lane == "ro" && strings.Contains(v.PolicyVersion.PolicyDocument, "nas:ClientWrite") {
		return "", errors.New("workspace filesystem RO policy must not include ClientWrite")
	}
	return r.Policy.PolicyName, nil
}

func (c CloudStorageProvider) attachment(ctx context.Context, p WorkspaceProvision, lane string) (string, error) {
	name := provisionName(p, lane)
	var r struct {
		Policies struct {
			Policy []struct{ PolicyName, PolicyType string }
		}
		IsTruncated bool
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "ListPoliciesForRole", Query: map[string]any{"RoleName": name}}, &r); err != nil {
		return "", err
	}
	if r.IsTruncated || len(r.Policies.Policy) != 1 || r.Policies.Policy[0].PolicyName != name || r.Policies.Policy[0].PolicyType != "Custom" {
		return "", errors.New("workspace filesystem role does not have exactly its lane policy")
	}
	return name + ":" + name, nil
}

func (c CloudStorageProvider) FindStorageResource(ctx context.Context, p WorkspaceProvision) (string, error) {
	if err := c.check(ctx, p); err != nil {
		return "", err
	}
	lane := laneForStep(p.Step)
	switch p.Step {
	case ProvisionRoleRO, ProvisionRoleRW:
		return c.role(ctx, p, lane)
	case ProvisionPolicyRO, ProvisionPolicyRW:
		return c.policy(ctx, p, lane)
	case ProvisionAttachRO, ProvisionAttachRW:
		return c.attachment(ctx, p, lane)
	}
	var matches []string
	next := ""
	seen := map[string]bool{}
	for range 64 {
		switch p.Step {
		case ProvisionSpace:
			var r struct {
				AgenticSpaces struct{ AgenticSpace []spaceInfo }
				NextToken     string
			}
			if err := c.API.Call(ctx, dshhost.CloudCall{Service: "nas", Action: "DescribeAgenticSpaces", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "MaxResults": 100, "NextToken": next}}, &r); err != nil {
				return "", err
			}
			for _, s := range r.AgenticSpaces.AgenticSpace {
				if s.FileSystemPath == p.Path() || s.Description == provisionDescription(p, "") {
					if _, err := c.space(ctx, p, s.AgenticSpaceID); err != nil {
						return "", err
					}
					matches = append(matches, s.AgenticSpaceID)
				}
			}
			next = r.NextToken
		case ProvisionAccessPointRO, ProvisionAccessPointRW:
			var r struct {
				AccessPoints []accessInfo
				NextToken    string
			}
			if err := c.API.Call(ctx, dshhost.CloudCall{Service: "nas", Action: "ListAccessPoints", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "MaxResults": 100, "NextToken": next}}, &r); err != nil {
				return "", err
			}
			for _, a := range r.AccessPoints {
				if a.AccessPointName == provisionName(p, lane) {
					if _, err := c.access(ctx, p, a.ARN, lane); err != nil {
						return "", err
					}
					matches = append(matches, a.ARN)
				}
			}
			next = r.NextToken
		case ProvisionVolumeRO, ProvisionVolumeRW:
			name := provisionName(p, lane)
			var r struct {
				Volumes   []volumeInfo
				NextToken string
			}
			if err := c.API.Call(ctx, dshhost.CloudCall{Service: "fcsandbox", Action: "ListVolumes", Query: map[string]any{"teamID": p.Spec.TeamID, "volumeName": name, "maxResults": 100, "nextToken": next}}, &r); err != nil {
				return "", err
			}
			ap, err := c.access(ctx, p, p.Resources[apStep(lane)], lane)
			if err != nil {
				return "", err
			}
			for _, v := range r.Volumes {
				if v.VolumeName != name {
					continue
				}
				if v.VolumeID == "" || v.TeamID != p.Spec.TeamID || v.Status != "AVAILABLE" || v.StorageClass != "AGENTIC_FS" || v.AgenticFSVolumeConfig.ServerAddr != ap.DomainName+":/" || v.AgenticFSVolumeConfig.UserID != 1000 || v.AgenticFSVolumeConfig.GroupID != 1000 {
					return "", errors.New("workspace filesystem volume ownership or mount mismatch")
				}
				matches = append(matches, v.VolumeName)
			}
			next = r.NextToken
		default:
			return "", errors.New("invalid workspace filesystem storage lookup step")
		}
		if next == "" {
			if len(matches) != 1 {
				return "", fmt.Errorf("%w: expected exactly one workspace filesystem resource", dshhost.ErrPending)
			}
			return matches[0], nil
		}
		if seen[next] {
			return "", errors.New("workspace filesystem pagination repeated a cursor")
		}
		seen[next] = true
	}
	return "", errors.New("workspace filesystem listing exceeded its reconciliation bound")
}

func (c CloudStorageProvider) VerifyStorage(ctx context.Context, p WorkspaceProvision) (Binding, error) {
	if p.Step != provisionStepCount || len(p.Resources) != provisionStepCount {
		return Binding{}, errors.New("incomplete workspace filesystem storage chain")
	}
	if err := c.check(ctx, p); err != nil {
		return Binding{}, err
	}
	if _, err := c.space(ctx, p, p.Resources[ProvisionSpace]); err != nil {
		return Binding{}, err
	}
	if _, err := c.access(ctx, p, p.Resources[ProvisionAccessPointRO], "ro"); err != nil {
		return Binding{}, err
	}
	if _, err := c.access(ctx, p, p.Resources[ProvisionAccessPointRW], "rw"); err != nil {
		return Binding{}, err
	}
	for _, step := range []int{ProvisionRoleRO, ProvisionRoleRW, ProvisionPolicyRO, ProvisionPolicyRW, ProvisionAttachRO, ProvisionAttachRW, ProvisionVolumeRO, ProvisionVolumeRW} {
		prior := p
		prior.Step = step
		prior.Resources = p.Resources[:step]
		id, err := c.FindStorageResource(ctx, prior)
		if err != nil {
			return Binding{}, err
		}
		if id != p.Resources[step] {
			return Binding{}, errors.New("workspace filesystem resource chain changed during verification")
		}
	}
	return Binding{
		WorkspaceID:   p.WorkspaceID,
		ROVolumeName:  p.Resources[ProvisionVolumeRO],
		RWVolumeName:  p.Resources[ProvisionVolumeRW],
		RORoleARN:     p.Resources[ProvisionRoleRO],
		RWRoleARN:     p.Resources[ProvisionRoleRW],
		ROAccessPoint: p.Resources[ProvisionAccessPointRO],
		RWAccessPoint: p.Resources[ProvisionAccessPointRW],
	}, nil
}
