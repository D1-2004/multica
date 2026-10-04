package service

import "github.com/jackc/pgx/v5/pgtype"

// TaskClaimAuthorization is populated only by the Host after authenticating
// execution access to each persisted runtime. An omitted authorization retains
// ordinary task behavior and excludes Employee Direct before queue mutation.
type TaskClaimAuthorization struct {
	EmployeeDirectRuntimeIDs []pgtype.UUID
}

func employeeDirectClaimRuntimeIDs(authorization []TaskClaimAuthorization) []pgtype.UUID {
	if len(authorization) == 0 {
		return nil
	}
	return authorization[0].EmployeeDirectRuntimeIDs
}
