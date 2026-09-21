package wsfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func taskRoleName(roleID uuid.UUID) string {
	return "wsfst-" + roleID.String()
}

func taskRoleARN(accountID string, roleID uuid.UUID) string {
	return "acs:ram::" + accountID + ":role/" + taskRoleName(roleID)
}

func taskRoleDescription(workspaceID, agentID uuid.UUID, generation int64, roleID uuid.UUID) string {
	return fmt.Sprintf("multica-wsfs-task:%s:%s:%d:%s", workspaceID, agentID, generation, roleID)
}

func compositePolicy(employeeAP, sharedAP, access string) string {
	employee := map[string]any{"Effect": "Allow", "Action": []string{"nas:ClientMount", "nas:ClientWrite", "nas:ClientRootAccess"}, "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"nas:AccessPointArn": employeeAP}}}
	sharedActions := []string{"nas:ClientMount", "nas:ClientRootAccess"}
	if access == AccessWrite {
		sharedActions = []string{"nas:ClientMount", "nas:ClientWrite", "nas:ClientRootAccess"}
	}
	shared := map[string]any{"Effect": "Allow", "Action": sharedActions, "Resource": "*", "Condition": map[string]any{"StringEquals": map[string]string{"nas:AccessPointArn": sharedAP}}}
	raw, _ := json.Marshal(map[string]any{"Version": "1", "Statement": []any{employee, shared}})
	return string(raw)
}

func (s Store) BeginGrantRole(ctx context.Context, workspaceID, agentID uuid.UUID, generation int64, access string) (uuid.UUID, string, string, error) {
	if workspaceID == uuid.Nil || agentID == uuid.Nil || generation < 1 || (access != AccessRead && access != AccessWrite) {
		return uuid.Nil, "", "", errors.New("invalid workspace filesystem grant role")
	}
	var roleID uuid.UUID
	var arn, policy string
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_filesystem_grant_role
 (workspace_id, agent_id, generation, role_id, access, role_arn, policy_name)
 VALUES ($1,$2,$3, gen_random_uuid(), $4, '', '')
 ON CONFLICT (workspace_id, agent_id, generation) DO NOTHING
 RETURNING role_id, role_arn, policy_name`, workspaceID, agentID, generation, access).Scan(&roleID, &arn, &policy)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.DB.QueryRow(ctx, `SELECT role_id, role_arn, policy_name FROM workspace_filesystem_grant_role
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3`, workspaceID, agentID, generation).Scan(&roleID, &arn, &policy)
	}
	return roleID, arn, policy, err
}

func (s Store) CompleteGrantRole(ctx context.Context, roleID uuid.UUID, arn, policy string) error {
	if roleID == uuid.Nil || arn == "" || policy == "" {
		return errors.New("invalid workspace filesystem grant role receipt")
	}
	_, err := s.DB.Exec(ctx, `UPDATE workspace_filesystem_grant_role SET role_arn=$2, policy_name=$3
 WHERE role_id=$1 AND (role_arn='' OR role_arn=$2)`, roleID, arn, policy)
	return err
}

func (s Store) SetGrantTaskRole(ctx context.Context, workspaceID, agentID uuid.UUID, generation int64, arn, policy string) error {
	result, err := s.DB.Exec(ctx, `UPDATE workspace_filesystem_grant
 SET task_role_arn=$4, task_policy_name=$5, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND access IN ('read','write')`,
		workspaceID, agentID, generation, arn, policy)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return dshhost.ErrChanged
	}
	return nil
}

func (c CloudStorageProvider) EnsureGrantRole(ctx context.Context, store Store, employee dshhost.Host, binding Binding, grant Grant) (Grant, error) {
	access := grant.effectiveAccess()
	if access != AccessRead && access != AccessWrite {
		return grant, nil
	}
	if grant.TaskRoleARN != "" {
		return grant, nil
	}
	if employee.AgentID == uuid.Nil || employee.AccessPointARN == "" || employee.WorkspaceID != grant.WorkspaceID {
		return grant, errors.New("composite task role requires the employee access point")
	}
	if err := c.checkGrant(ctx, employee.WorkspaceID); err != nil {
		return grant, err
	}
	sharedAP := binding.ROAccessPoint
	if access == AccessWrite {
		sharedAP = binding.RWAccessPoint
	}
	if sharedAP == "" || sharedAP == employee.AccessPointARN {
		return grant, errors.New("invalid shared access point for composite role")
	}
	roleID, arn, policy, err := store.BeginGrantRole(ctx, grant.WorkspaceID, grant.AgentID, grant.Generation, access)
	if err != nil {
		return grant, err
	}
	if arn != "" {
		grant.TaskRoleARN = arn
		if err := store.SetGrantTaskRole(ctx, grant.WorkspaceID, grant.AgentID, grant.Generation, arn, policy); err != nil {
			return grant, err
		}
		return grant, nil
	}
	name := taskRoleName(roleID)
	desc := taskRoleDescription(grant.WorkspaceID, grant.AgentID, grant.Generation, roleID)
	wantARN := taskRoleARN(c.Spec.AccountID, roleID)
	doc := compositePolicy(employee.AccessPointARN, sharedAP, access)
	arn, err = c.ensureRAMRole(ctx, name, desc, wantARN)
	if err != nil {
		return grant, err
	}
	policy, err = c.ensureRAMPolicy(ctx, name, desc, doc)
	if err != nil {
		return grant, err
	}
	if err := c.ensureRAMAttachment(ctx, name); err != nil {
		return grant, err
	}
	if err := store.CompleteGrantRole(ctx, roleID, arn, policy); err != nil {
		return grant, err
	}
	if err := store.SetGrantTaskRole(ctx, grant.WorkspaceID, grant.AgentID, grant.Generation, arn, policy); err != nil {
		return grant, err
	}
	grant.TaskRoleARN = arn
	return grant, nil
}

func (c CloudStorageProvider) checkGrant(ctx context.Context, workspaceID uuid.UUID) error {
	if c.API == nil || c.Spec.Validate() != nil || workspaceID == uuid.Nil {
		return errors.New("invalid workspace filesystem grant-role configuration")
	}
	var who struct {
		AccountID string `json:"AccountId"`
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "sts", Action: "GetCallerIdentity"}, &who); err != nil {
		return err
	}
	if who.AccountID != c.Spec.AccountID {
		return errors.New("workspace filesystem cloud credential account does not match placement")
	}
	return nil
}

func (c CloudStorageProvider) ensureRAMRole(ctx context.Context, name, desc, wantARN string) (string, error) {
	arn, err := c.getRAMRole(ctx, name, desc, wantARN)
	if err == nil {
		return arn, nil
	}
	var result struct {
		Role struct{ Arn string }
	}
	createErr := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "CreateRole", Query: map[string]any{
		"RoleName": name, "Description": desc, "AssumeRolePolicyDocument": trustPolicy(), "MaxSessionDuration": 3600,
	}}, &result)
	if createErr == nil && result.Role.Arn != "" {
		return result.Role.Arn, nil
	}
	arn, err = c.getRAMRole(ctx, name, desc, wantARN)
	if err != nil {
		return "", fmt.Errorf("%w: composite role creation outcome is unconfirmed", dshhost.ErrPending)
	}
	return arn, nil
}

func (c CloudStorageProvider) getRAMRole(ctx context.Context, name, desc, wantARN string) (string, error) {
	var r struct {
		Role struct {
			Arn, RoleName, Description, AssumeRolePolicyDocument string
			IsServiceLinkedRole                                  bool
		}
	}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "GetRole", Query: map[string]any{"RoleName": name}}, &r); err != nil {
		return "", err
	}
	if r.Role.Arn != wantARN || r.Role.RoleName != name || r.Role.Description != desc || r.Role.IsServiceLinkedRole || !equalPolicy(r.Role.AssumeRolePolicyDocument, trustPolicy()) {
		return "", errors.New("composite task role ownership or trust mismatch")
	}
	return r.Role.Arn, nil
}

func (c CloudStorageProvider) ensureRAMPolicy(ctx context.Context, name, desc, doc string) (string, error) {
	if got, err := c.getRAMPolicy(ctx, name, desc, doc); err == nil {
		return got, nil
	}
	var result struct {
		Policy struct{ PolicyName string }
	}
	createErr := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "CreatePolicy", Query: map[string]any{
		"PolicyName": name, "Description": desc, "PolicyDocument": doc,
	}}, &result)
	if createErr == nil && result.Policy.PolicyName != "" {
		return result.Policy.PolicyName, nil
	}
	got, err := c.getRAMPolicy(ctx, name, desc, doc)
	if err != nil {
		return "", fmt.Errorf("%w: composite policy creation outcome is unconfirmed", dshhost.ErrPending)
	}
	return got, nil
}

func (c CloudStorageProvider) getRAMPolicy(ctx context.Context, name, desc, doc string) (string, error) {
	var r struct {
		Policy struct{ PolicyName, PolicyType, Description, DefaultVersion string }
	}
	q := map[string]any{"PolicyName": name, "PolicyType": "Custom"}
	if err := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "GetPolicy", Query: q}, &r); err != nil {
		return "", err
	}
	if r.Policy.PolicyName != name || r.Policy.PolicyType != "Custom" || r.Policy.Description != desc || r.Policy.DefaultVersion == "" {
		return "", errors.New("composite task policy ownership mismatch")
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
	if v.PolicyVersion.VersionID != r.Policy.DefaultVersion || !v.PolicyVersion.IsDefaultVersion || !equalPolicy(v.PolicyVersion.PolicyDocument, doc) {
		return "", errors.New("composite task policy document mismatch")
	}
	return r.Policy.PolicyName, nil
}

func (c CloudStorageProvider) ensureRAMAttachment(ctx context.Context, name string) error {
	if _, err := c.listRAMAttachment(ctx, name); err == nil {
		return nil
	}
	attachErr := c.API.Call(ctx, dshhost.CloudCall{Service: "ram", Action: "AttachPolicyToRole", Query: map[string]any{
		"RoleName": name, "PolicyName": name, "PolicyType": "Custom",
	}}, &struct{}{})
	if _, err := c.listRAMAttachment(ctx, name); err != nil {
		if attachErr != nil {
			return fmt.Errorf("%w: composite policy attachment is unconfirmed", dshhost.ErrPending)
		}
		return err
	}
	return nil
}

func (c CloudStorageProvider) listRAMAttachment(ctx context.Context, name string) (string, error) {
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
		return "", errors.New("composite task role does not have exactly its policy")
	}
	return name + ":" + name, nil
}
