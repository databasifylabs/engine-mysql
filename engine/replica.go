package mysql

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/go-sql-driver/mysql"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/requests"
)

const (
	replicaState        = "state/replica"
	errRestartByMonitor = 3707
	channelRunningQuery = `SELECT COUNT(*) FROM performance_schema.replication_connection_status c JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE CHANNEL_NAME = 'group' AND c.SERVICE_STATE = 'ON' AND a.SERVICE_STATE = 'ON'`
)

func (e cluster) followGroup(ctx context.Context, request requests.NodeConfigure, password string, report contract.Report) error {
	if request.Primary == nil {
		return command.Invalid("a read-only node needs the primary to follow")
	}
	primary := *request.Primary
	executed, _, err := e.db.Value(ctx, "SELECT @@gtid_executed")
	if err != nil {
		return err
	}
	if executed == "" {
		report("cloning")
		if err := family.Retry(ctx, onlineTimeout, func() error { return e.cloneFrom(ctx, primary, password) }); err != nil {
			return err
		}
	}
	report("following")
	statements := []struct {
		query string
		args  []any
	}{
		{"CHANGE REPLICATION SOURCE TO SOURCE_HOST = ?, SOURCE_PORT = ?, SOURCE_USER = 'databasify_repl', SOURCE_PASSWORD = ?, SOURCE_AUTO_POSITION = 1, SOURCE_SSL = 1, SOURCE_CONNECT_RETRY = 5, SOURCE_RETRY_COUNT = 3, SOURCE_CONNECTION_AUTO_FAILOVER = 1 FOR CHANNEL 'group'", []any{primary, clientPort, password}},
		{"SELECT asynchronous_connection_failover_add_managed('group', 'GroupReplication', ?, ?, ?, '', 80, 60)", []any{groupNameOf(request.DatabaseId), primary, clientPort}},
		{"START REPLICA FOR CHANNEL 'group'", nil},
	}
	for _, statement := range statements {
		if err := e.db.Exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if err := family.Until(ctx, onlineTimeout, func() (bool, error) { return e.channelRunning(ctx), nil }); err != nil {
		return err
	}
	_, err = e.root.Write(replicaState, "", 0o600, false)

	return err
}

func (e Engine) cloneFrom(ctx context.Context, primary string, password string) error {
	donor := primary + ":" + strconv.Itoa(clientPort)
	if err := e.db.Exec(ctx, "SET GLOBAL clone_valid_donor_list = ?", donor); err != nil {
		return err
	}
	err := e.db.Exec(ctx, "CLONE INSTANCE FROM 'databasify_repl'@?:? IDENTIFIED BY ? REQUIRE SSL", primary, clientPort, password)
	var server *mysql.MySQLError
	if errors.As(err, &server) && server.Number == errRestartByMonitor {
		err = e.supervisor.Do("restart", serverUnit)
	}
	if err != nil && !family.IsUnavailable(err) {
		return err
	}

	return family.Until(ctx, readyTimeout, func() (bool, error) {
		_, _, err := e.db.Value(ctx, "SELECT 1")

		return err == nil, nil
	})
}

func (e Engine) channelRunning(ctx context.Context) bool {
	running, _, err := e.db.Value(ctx, channelRunningQuery)

	return err == nil && running == "1"
}

func (e Engine) stopFollowing(ctx context.Context) error {
	if err := e.db.Exec(ctx, "STOP REPLICA FOR CHANNEL 'group'"); err != nil {
		return err
	}
	if err := e.db.Exec(ctx, "RESET REPLICA ALL FOR CHANNEL 'group'"); err != nil {
		return err
	}
	if err := os.Remove(e.root.Path(replicaState)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return e.db.Exec(ctx, "SET GLOBAL super_read_only = OFF")
}
