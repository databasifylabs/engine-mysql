package mysql

import (
	"context"
	"strings"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	groupRoleQuery = `SELECT MEMBER_ROLE FROM performance_schema.replication_group_members WHERE MEMBER_ID = @@server_uuid AND MEMBER_STATE = 'ONLINE'`

	replicatingQuery = `SELECT COUNT(*) FROM performance_schema.replication_connection_status c JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE c.SERVICE_STATE = 'ON' AND a.SERVICE_STATE = 'ON' AND CHANNEL_NAME NOT LIKE 'group_replication%'`
)

func (e nodes) Status(ctx context.Context) (responses.NodeStatus, error) {
	version, _, err := e.db.Value(ctx, "SELECT @@version")
	if family.IsUnavailable(err) {
		return responses.NodeStatus{Role: enums.NodeRoleOffline}, nil
	}
	if err != nil {
		return responses.NodeStatus{}, err
	}
	role, err := e.role(ctx)
	if err != nil {
		return responses.NodeStatus{}, err
	}
	release, _, _ := strings.Cut(version, "-")

	return responses.NodeStatus{Role: role, Version: &release}, nil
}

func (e Engine) role(ctx context.Context) (enums.NodeRole, error) {
	member, found, err := e.db.Value(ctx, groupRoleQuery)
	if err != nil {
		return "", err
	}
	if found {
		if member == "PRIMARY" {
			return enums.NodeRolePrimary, nil
		}

		return enums.NodeRoleReplica, nil
	}
	readOnly, _, err := e.db.Value(ctx, "SELECT @@super_read_only")
	if err != nil {
		return "", err
	}
	if readOnly == "0" {
		return enums.NodeRolePrimary, nil
	}
	channels, _, err := e.db.Value(ctx, replicatingQuery)
	if err != nil {
		return "", err
	}
	if channels != "0" {
		return enums.NodeRoleReplica, nil
	}

	return enums.NodeRoleUnknown, nil
}

func (e nodes) Metrics(ctx context.Context) (responses.NodeMetrics, error) {
	return family.Metrics(ctx, e.db)
}
