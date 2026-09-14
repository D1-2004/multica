package dshhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type CloudStorageProvider struct {
	API  CloudCaller
	Spec ProvisionSpec
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

func provisionName(p Provision) string { return "multica-dsh-" + p.Intent.String() }
func provisionDescription(p Provision) string {
	return "multica-dsh:" + p.Intent.String() + ":" + p.WorkspaceID.String() + ":" + p.AgentID.String()
}
func roleARN(p Provision) string { return "acs:ram::" + p.Spec.AccountID + ":role/" + provisionName(p) }
func trustPolicy() string {
	return `{"Version":"1","Statement":[{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"Service":["fc.aliyuncs.com"]}}]}`
}
func accessPolicy(p Provision) string {
	raw, _ := json.Marshal(map[string]any{"Version": "1", "Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"nas:ClientMount", "nas:ClientWrite", "nas:ClientRootAccess"}, "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"nas:AccessPointArn": p.Resources[ProvisionAccessPoint]}}}}})
	return string(raw)
}
func equalPolicy(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

func (c CloudStorageProvider) check(ctx context.Context, p Provision) error {
	if c.API == nil || !reflect.DeepEqual(c.Spec, p.Spec) || !p.Spec.valid() || p.Intent == uuid.Nil || p.WorkspaceID == uuid.Nil || p.AgentID == uuid.Nil || p.Step < 0 || p.Step > provisionStepCount || len(p.Resources) != p.Step {
		return errors.New("invalid persisted DSH cloud provisioning scope")
	}
	var who struct {
		AccountID string `json:"AccountId"`
	}
	if err := c.API.Call(ctx, CloudCall{Service: "sts", Action: "GetCallerIdentity"}, &who); err != nil {
		return err
	}
	if who.AccountID != p.Spec.AccountID {
		return errors.New("DSH cloud credential account does not match placement")
	}
	return nil
}

func (c CloudStorageProvider) PrepareStorageResource(ctx context.Context, p Provision) (func(context.Context) (string, error), error) {
	if err := c.check(ctx, p); err != nil {
		return nil, err
	}
	if p.Step == provisionStepCount {
		return nil, errors.New("DSH storage provisioning already has all resources")
	}
	name := provisionName(p)
	call := CloudCall{Service: "nas", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID}}
	switch p.Step {
	case ProvisionSpace:
		call.Action = "CreateAgenticSpace"
		call.Query["Azone"] = p.Spec.Zone
		call.Query["ClientToken"] = p.StepIntent()
		call.Query["Description"] = provisionDescription(p)
		call.Query["FileSystemPath"] = p.Path()
		call.Query["Quota"] = map[string]any{"SizeLimit": p.Spec.SizeLimit, "FileCountLimit": p.Spec.FileCountLimit}
	case ProvisionAccessPoint:
		if _, err := c.space(ctx, p, p.Resources[ProvisionSpace]); err != nil {
			return nil, err
		}
		call.Action = "CreateAccessPoint"
		call.Query["AgenticSpaceId"] = p.Resources[ProvisionSpace]
		call.Query["VpcId"] = p.Spec.VPCID
		call.Query["VswId"] = p.Spec.VSwitchIDs[0]
		call.Query["EnabledRam"] = true
		call.Query["AccessPointName"] = name
		call.Query["Tag"] = []map[string]string{{"Key": "multica.dsh.intent", "Value": p.Intent.String()}, {"Key": "multica.dsh.workspace", "Value": p.WorkspaceID.String()}, {"Key": "multica.dsh.agent", "Value": p.AgentID.String()}}
	case ProvisionRole:
		call = CloudCall{Service: "ram", Action: "CreateRole", Query: map[string]any{"RoleName": name, "Description": provisionDescription(p), "AssumeRolePolicyDocument": trustPolicy(), "MaxSessionDuration": 3600}}
	case ProvisionPolicy:
		if _, err := c.access(ctx, p, p.Resources[ProvisionAccessPoint]); err != nil {
			return nil, err
		}
		call = CloudCall{Service: "ram", Action: "CreatePolicy", Query: map[string]any{"PolicyName": name, "Description": provisionDescription(p), "PolicyDocument": accessPolicy(p)}}
	case ProvisionPolicyAttachment:
		if _, err := c.role(ctx, p); err != nil {
			return nil, err
		}
		if _, err := c.policy(ctx, p); err != nil {
			return nil, err
		}
		call = CloudCall{Service: "ram", Action: "AttachPolicyToRole", Query: map[string]any{"RoleName": name, "PolicyName": name, "PolicyType": "Custom"}}
	case ProvisionVolume:
		ap, err := c.access(ctx, p, p.Resources[ProvisionAccessPoint])
		if err != nil {
			return nil, err
		}
		if _, err := c.attachment(ctx, p); err != nil {
			return nil, err
		}
		call = CloudCall{Service: "fcsandbox", Action: "CreateVolume", Body: map[string]any{"teamID": p.Spec.TeamID, "volumeName": name, "agenticFSVolumeConfig": map[string]any{"serverAddr": ap.DomainName + ":/", "userID": 1000, "groupID": 1000}}}
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
		case ProvisionAccessPoint:
			// CreateAccessPoint returns ID/domain, while DescribeAccessPoint
			// returns ARN. Preserve the known ID instead of losing its receipt.
			if !regexp.MustCompile(`^ap-[a-z0-9]+$`).MatchString(result.AccessPoint.AccessPointID) {
				return "", errors.New("DSH access point create returned no valid ID")
			}
			return "acs:nas:" + p.Spec.Region + ":" + p.Spec.AccountID + ":accesspoint/" + result.AccessPoint.AccessPointID, nil
		case ProvisionRole:
			return result.Role.Arn, nil
		case ProvisionPolicy:
			return result.Policy.PolicyName, nil
		case ProvisionPolicyAttachment:
			return name + ":" + name, nil
		case ProvisionVolume:
			return result.Volume.VolumeName, nil
		}
		return "", errors.New("invalid DSH cloud storage step")
	}, nil
}

func (c CloudStorageProvider) space(ctx context.Context, p Provision, id string) (spaceInfo, error) {
	var r struct{ AgenticSpace spaceInfo }
	err := c.API.Call(ctx, CloudCall{Service: "nas", Action: "GetAgenticSpace", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "AgenticSpaceId": id}}, &r)
	s := r.AgenticSpace
	if err != nil {
		return s, err
	}
	if id == "" || s.AgenticSpaceID != id || s.FileSystemID != p.Spec.FileSystemID || s.FileSystemPath != p.Path() || s.Description != provisionDescription(p) || s.Azone != p.Spec.Zone || s.Status != "Running" || s.Quota.SizeLimit != p.Spec.SizeLimit || s.Quota.FileCountLimit != p.Spec.FileCountLimit {
		return s, errors.New("DSH AgenticSpace ownership or readiness mismatch")
	}
	return s, nil
}

func (c CloudStorageProvider) access(ctx context.Context, p Provision, arn string) (accessInfo, error) {
	prefix := "acs:nas:" + p.Spec.Region + ":" + p.Spec.AccountID + ":accesspoint/"
	id := strings.TrimPrefix(arn, prefix)
	if !strings.HasPrefix(arn, prefix) || !regexp.MustCompile(`^ap-[a-z0-9]+$`).MatchString(id) {
		return accessInfo{}, errors.New("invalid DSH access point ARN")
	}
	var r struct{ AccessPoint accessInfo }
	if err := c.API.Call(ctx, CloudCall{Service: "nas", Action: "DescribeAccessPoint", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "AccessPointId": id}}, &r); err != nil {
		return accessInfo{}, err
	}
	a := r.AccessPoint
	domainPattern := `^` + regexp.QuoteMeta(id+"."+p.Spec.FileSystemID) + `-[a-z0-9]+\.` + regexp.QuoteMeta(p.Spec.Region+".nas.aliyuncs.com") + `$`
	if a.ARN != arn || a.AccessPointID != id || a.AccessPointName != provisionName(p) || a.AgenticSpaceID != p.Resources[ProvisionSpace] || a.FileSystemID != p.Spec.FileSystemID || a.VpcID != p.Spec.VPCID || a.VSwitchID != p.Spec.VSwitchIDs[0] || !a.EnabledRAM || a.RootPath != "/" || a.Status != "active" || !regexp.MustCompile(domainPattern).MatchString(a.DomainName) {
		return a, errors.New("DSH access point ownership or readiness mismatch")
	}
	return a, nil
}

func (c CloudStorageProvider) role(ctx context.Context, p Provision) (string, error) {
	var r struct {
		Role struct {
			Arn, RoleName, Description, AssumeRolePolicyDocument string
			IsServiceLinkedRole                                  bool
		}
	}
	if err := c.API.Call(ctx, CloudCall{Service: "ram", Action: "GetRole", Query: map[string]any{"RoleName": provisionName(p)}}, &r); err != nil {
		return "", err
	}
	if r.Role.Arn != roleARN(p) || r.Role.RoleName != provisionName(p) || r.Role.Description != provisionDescription(p) || r.Role.IsServiceLinkedRole || !equalPolicy(r.Role.AssumeRolePolicyDocument, trustPolicy()) {
		return "", errors.New("DSH role ownership or trust mismatch")
	}
	return r.Role.Arn, nil
}
func (c CloudStorageProvider) policy(ctx context.Context, p Provision) (string, error) {
	var r struct {
		Policy struct{ PolicyName, PolicyType, Description, DefaultVersion string }
	}
	q := map[string]any{"PolicyName": provisionName(p), "PolicyType": "Custom"}
	if err := c.API.Call(ctx, CloudCall{Service: "ram", Action: "GetPolicy", Query: q}, &r); err != nil {
		return "", err
	}
	if r.Policy.PolicyName != provisionName(p) || r.Policy.PolicyType != "Custom" || r.Policy.Description != provisionDescription(p) || r.Policy.DefaultVersion == "" {
		return "", errors.New("DSH permission policy ownership mismatch")
	}
	q["VersionId"] = r.Policy.DefaultVersion
	var v struct {
		PolicyVersion struct {
			PolicyDocument, VersionID string
			IsDefaultVersion          bool
		}
	}
	if err := c.API.Call(ctx, CloudCall{Service: "ram", Action: "GetPolicyVersion", Query: q}, &v); err != nil {
		return "", err
	}
	if v.PolicyVersion.VersionID != r.Policy.DefaultVersion || !v.PolicyVersion.IsDefaultVersion || !equalPolicy(v.PolicyVersion.PolicyDocument, accessPolicy(p)) {
		return "", errors.New("DSH permission policy exceeds the employee access point")
	}
	return r.Policy.PolicyName, nil
}
func (c CloudStorageProvider) attachment(ctx context.Context, p Provision) (string, error) {
	var r struct {
		Policies struct {
			Policy []struct{ PolicyName, PolicyType string }
		}
		IsTruncated bool
	}
	if err := c.API.Call(ctx, CloudCall{Service: "ram", Action: "ListPoliciesForRole", Query: map[string]any{"RoleName": provisionName(p)}}, &r); err != nil {
		return "", err
	}
	if r.IsTruncated || len(r.Policies.Policy) != 1 || r.Policies.Policy[0].PolicyName != provisionName(p) || r.Policies.Policy[0].PolicyType != "Custom" {
		return "", errors.New("DSH role does not have exactly its employee policy")
	}
	return provisionName(p) + ":" + provisionName(p), nil
}

func (c CloudStorageProvider) FindStorageResource(ctx context.Context, p Provision) (string, error) {
	if err := c.check(ctx, p); err != nil {
		return "", err
	}
	switch p.Step {
	case ProvisionRole:
		return c.role(ctx, p)
	case ProvisionPolicy:
		return c.policy(ctx, p)
	case ProvisionPolicyAttachment:
		return c.attachment(ctx, p)
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
			if err := c.API.Call(ctx, CloudCall{Service: "nas", Action: "DescribeAgenticSpaces", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "MaxResults": 100, "NextToken": next}}, &r); err != nil {
				return "", err
			}
			for _, s := range r.AgenticSpaces.AgenticSpace {
				if s.FileSystemPath == p.Path() || s.Description == provisionDescription(p) {
					if _, err := c.space(ctx, p, s.AgenticSpaceID); err != nil {
						return "", err
					}
					matches = append(matches, s.AgenticSpaceID)
				}
			}
			next = r.NextToken
		case ProvisionAccessPoint:
			var r struct {
				AccessPoints []accessInfo
				NextToken    string
			}
			if err := c.API.Call(ctx, CloudCall{Service: "nas", Action: "ListAccessPoints", Query: map[string]any{"FileSystemId": p.Spec.FileSystemID, "MaxResults": 100, "NextToken": next}}, &r); err != nil {
				return "", err
			}
			for _, a := range r.AccessPoints {
				if a.AccessPointName == provisionName(p) {
					if _, err := c.access(ctx, p, a.ARN); err != nil {
						return "", err
					}
					matches = append(matches, a.ARN)
				}
			}
			next = r.NextToken
		case ProvisionVolume:
			var r struct {
				Volumes   []volumeInfo
				NextToken string
			}
			if err := c.API.Call(ctx, CloudCall{Service: "fcsandbox", Action: "ListVolumes", Query: map[string]any{"teamID": p.Spec.TeamID, "volumeName": provisionName(p), "maxResults": 100, "nextToken": next}}, &r); err != nil {
				return "", err
			}
			ap, err := c.access(ctx, p, p.Resources[ProvisionAccessPoint])
			if err != nil {
				return "", err
			}
			for _, v := range r.Volumes {
				if v.VolumeName != provisionName(p) {
					continue
				}
				if v.VolumeID == "" || v.TeamID != p.Spec.TeamID || v.Status != "AVAILABLE" || v.StorageClass != "AGENTIC_FS" || v.AgenticFSVolumeConfig.ServerAddr != ap.DomainName+":/" || v.AgenticFSVolumeConfig.UserID != 1000 || v.AgenticFSVolumeConfig.GroupID != 1000 {
					return "", errors.New("DSH volume ownership or mount mismatch")
				}
				matches = append(matches, v.VolumeName)
			}
			next = r.NextToken
		default:
			return "", errors.New("invalid DSH storage lookup step")
		}
		if next == "" {
			if len(matches) != 1 {
				return "", fmt.Errorf("%w: expected exactly one DSH resource", ErrPending)
			}
			return matches[0], nil
		}
		if seen[next] {
			return "", errors.New("DSH storage pagination repeated a cursor")
		}
		seen[next] = true
	}
	return "", errors.New("DSH storage listing exceeded its reconciliation bound")
}

func (c CloudStorageProvider) VerifyStorage(ctx context.Context, p Provision) (Storage, error) {
	if p.Step != provisionStepCount || len(p.Resources) != provisionStepCount {
		return Storage{}, errors.New("incomplete DSH storage chain")
	}
	if err := c.check(ctx, p); err != nil {
		return Storage{}, err
	}
	if _, err := c.space(ctx, p, p.Resources[ProvisionSpace]); err != nil {
		return Storage{}, err
	}
	if _, err := c.access(ctx, p, p.Resources[ProvisionAccessPoint]); err != nil {
		return Storage{}, err
	}
	// Reconcile each identity using the state before that operation, without
	// changing the durable step or granting another create attempt.
	for _, step := range []int{ProvisionRole, ProvisionPolicy, ProvisionPolicyAttachment, ProvisionVolume} {
		prior := p
		prior.Step = step
		prior.Resources = p.Resources[:step]
		id, err := c.FindStorageResource(ctx, prior)
		if err != nil {
			return Storage{}, err
		}
		if id != p.Resources[step] {
			return Storage{}, errors.New("DSH resource chain changed during verification")
		}
	}
	return Storage{FileSystemID: p.Spec.FileSystemID, SpaceID: p.Resources[ProvisionSpace], AccessPointARN: p.Resources[ProvisionAccessPoint], RoleARN: p.Resources[ProvisionRole], VolumeName: p.Resources[ProvisionVolume], VPCID: p.Spec.VPCID, SecurityGroupID: p.Spec.SecurityGroupID, VSwitchIDs: p.Spec.VSwitchIDs}, nil
}
