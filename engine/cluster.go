package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	groupedState      = "state/grouped"
	groupedConf       = "conf/conf.d/grouped.cnf"
	onlineTimeout     = 30 * time.Minute
	errGroupRunning   = 3093
	memberStateQuery  = `SELECT MEMBER_STATE FROM performance_schema.replication_group_members WHERE MEMBER_ID = @@server_uuid`
	groupRunningQuery = `SELECT SERVICE_STATE FROM performance_schema.replication_connection_status WHERE CHANNEL_NAME = 'group_replication_applier'`
	memberCountQuery  = `SELECT COUNT(*), COALESCE(SUM(MEMBER_STATE = 'ONLINE'), 0) FROM performance_schema.replication_group_members`
	memberByHostQuery = `SELECT MEMBER_ID FROM performance_schema.replication_group_members WHERE MEMBER_HOST = ?`
)

type cluster struct{ Engine }

func (e Engine) Cluster() contract.Cluster { return cluster{e} }

func (e Engine) replicationPassword() (string, error) {
	request, err := family.Saved(e.root)
	if err != nil {
		return "", err
	}
	if request.Secrets.Replication == nil {
		return "", command.Invalid("MySQL clusters need the replication secret")
	}

	return *request.Secrets.Replication, nil
}

func (e cluster) Enable(ctx context.Context, _ requests.ClusterEnable, report contract.Report) error {
	request, err := family.Saved(e.root)
	if err != nil {
		return err
	}
	password, err := e.replicationPassword()
	if err != nil {
		return err
	}
	if request.ReadOnly {
		return e.followGroup(ctx, request, password, report)
	}
	if err := family.RequirePrimaryKeys(ctx, e.db, e.Capabilities().ReservedDatabases); err != nil {
		return err
	}
	if request.Primary == nil || *request.Primary == request.Ip {
		report("bootstrapping")
		if err := e.bootstrapGroup(ctx, password); err != nil {
			return err
		}
	} else {
		report("joining")
		if err := family.Retry(ctx, onlineTimeout, func() error { return e.joinGroup(ctx, password) }); err != nil {
			return err
		}
	}
	report("recovering")

	return e.onlineAndJoined(ctx, password)
}

func (e Engine) onlineAndJoined(ctx context.Context, password string) error {
	if err := e.waitOnline(ctx, password); err != nil {
		return err
	}
	if err := e.bootReadOnly(); err != nil {
		return err
	}
	_, err := e.root.Write(groupedState, "", 0o600, false)

	return err
}

func (e Engine) bootReadOnly() error {
	if err := e.root.Dir(filepath.Dir(groupedConf)); err != nil {
		return err
	}
	_, err := e.root.Write(groupedConf, "[mysqld]\nsuper_read_only=ON\n", 0o600, true)

	return err
}

func (e Engine) bootstrapGroup(ctx context.Context, password string) error {
	if err := e.db.Exec(ctx, "SET GLOBAL group_replication_bootstrap_group = ON"); err != nil {
		return err
	}
	err := e.startGroup(ctx, password)
	if resetErr := e.db.Exec(ctx, "SET GLOBAL group_replication_bootstrap_group = OFF"); err == nil {
		err = resetErr
	}

	return err
}

func (e Engine) joinGroup(ctx context.Context, password string) error {
	executed, _, err := e.db.Value(ctx, "SELECT @@gtid_executed")
	if err != nil {
		return err
	}
	if executed == "" {
		if err := e.db.Exec(ctx, "SET GLOBAL group_replication_clone_threshold = 1"); err != nil {
			return err
		}
	}

	return e.startGroup(ctx, password)
}

func (e Engine) startGroup(ctx context.Context, password string) error {
	err := e.db.Exec(ctx, "START GROUP_REPLICATION USER = 'databasify_repl', PASSWORD = ?", password)
	var server *mysql.MySQLError
	if errors.As(err, &server) && server.Number == errGroupRunning {
		return nil
	}

	return err
}

func (e Engine) waitOnline(ctx context.Context, password string) error {
	return family.Until(ctx, onlineTimeout, func() (bool, error) {
		state, found, err := e.db.Value(ctx, memberStateQuery)
		if err != nil {
			return false, nil
		}
		if state == "ERROR" {
			return false, errors.New("this member left the group with an error, see the MySQL log")
		}
		if (!found || state == "OFFLINE") && password != "" {
			_ = e.startGroup(ctx, password)

			return false, nil
		}

		return state == "ONLINE", nil
	})
}

func (e cluster) Disable(ctx context.Context) (responses.ClusterDisable, error) {
	if e.root.Exists(replicaState) {
		return responses.ClusterDisable{}, e.stopFollowing(ctx)
	}
	if err := e.db.Exec(ctx, "STOP GROUP_REPLICATION"); err != nil {
		return responses.ClusterDisable{}, err
	}
	if err := e.db.Exec(ctx, "SET GLOBAL super_read_only = OFF"); err != nil {
		return responses.ClusterDisable{}, err
	}
	for _, name := range []string{groupedConf, groupedState} {
		if err := os.Remove(e.root.Path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return responses.ClusterDisable{}, err
		}
	}

	return responses.ClusterDisable{}, nil
}

func (e cluster) Failover(ctx context.Context, request requests.ClusterFailover) error {
	member, found, err := e.db.Value(ctx, memberByHostQuery, request.Target)
	if err != nil {
		return err
	}
	if !found {
		return command.NotFound(request.Target + " is not a member of the group")
	}

	return e.db.Exec(ctx, "SELECT group_replication_set_as_primary(?)", member)
}

func (e cluster) Quorum(ctx context.Context) (responses.ClusterQuorum, error) {
	rows, err := e.db.Rows(ctx, memberCountQuery)
	if err != nil || len(rows) == 0 {
		return responses.ClusterQuorum{}, nil
	}
	total, _ := strconv.ParseInt(rows[0][0], 10, 64)
	online, _ := strconv.ParseInt(rows[0][1], 10, 64)
	if saved, err := family.Saved(e.root); err == nil {
		total = max(total, family.VotersIn(saved.Members))
	}
	state, _, _ := e.db.Value(ctx, memberStateQuery)
	if state != "ONLINE" {
		online = 0
	}

	return responses.ClusterQuorum{Total: total, Online: online}, nil
}

func (e cluster) ForceQuorum(ctx context.Context, request requests.ClusterForceQuorum) (responses.ClusterForceQuorum, error) {
	if !request.OthersDown {
		return responses.ClusterForceQuorum{}, command.Failure{Code: enums.ErrorCodeBusy, Message: "forcing the group while other members may be alive would split it; confirm the others are down"}
	}
	saved, err := family.Saved(e.root)
	if err != nil {
		return responses.ClusterForceQuorum{}, err
	}
	if !e.groupRunning(ctx) {
		return responses.ClusterForceQuorum{Leader: saved.Ip}, e.bootstrapStoppedGroup(ctx)
	}
	if err := e.db.Exec(ctx, "SET GLOBAL group_replication_force_members = ?", saved.Ip+":"+strconv.Itoa(groupPort)); err != nil {
		return responses.ClusterForceQuorum{}, err
	}
	if err := e.onlineAndJoined(ctx, ""); err != nil {
		return responses.ClusterForceQuorum{}, err
	}

	return responses.ClusterForceQuorum{Leader: saved.Ip}, e.db.Exec(ctx, "SET GLOBAL group_replication_force_members = ''")
}

func (e Engine) groupRunning(ctx context.Context) bool {
	state, _, err := e.db.Value(ctx, groupRunningQuery)

	return err == nil && state == "ON"
}

func (e Engine) bootstrapStoppedGroup(ctx context.Context) error {
	password, err := e.replicationPassword()
	if err != nil {
		return err
	}
	if err := e.bootstrapGroup(ctx, password); err != nil {
		return err
	}

	return e.onlineAndJoined(ctx, password)
}

func (e cluster) Position(ctx context.Context) (responses.ClusterPosition, error) {
	position := int64(-1)
	executed, _, err := e.db.Value(ctx, "SELECT @@gtid_executed")
	if err == nil {
		position = transactionsIn(executed)
	}

	return responses.ClusterPosition{Position: &position}, nil
}

func transactionsIn(gtidSet string) int64 {
	compact := strings.ReplaceAll(strings.ReplaceAll(gtidSet, "\n", ""), " ", "")
	if compact == "" {
		return -1
	}
	var total int64
	for set := range strings.SplitSeq(compact, ",") {
		parts := strings.Split(set, ":")
		for _, interval := range parts[1:] {
			first, last, ranged := strings.Cut(interval, "-")
			start, _ := strconv.ParseInt(first, 10, 64)
			end := start
			if ranged {
				end, _ = strconv.ParseInt(last, 10, 64)
			}
			total += end - start + 1
		}
	}

	return total
}

func (e cluster) Rejoin(ctx context.Context, request requests.ClusterRejoin, report contract.Report) error {
	if request.ReadOnly {
		report("starting replication")
		if err := e.db.Exec(ctx, "START REPLICA FOR CHANNEL 'group'"); err != nil {
			return err
		}

		return family.Until(ctx, onlineTimeout, func() (bool, error) { return e.channelRunning(ctx), nil })
	}
	password, err := e.replicationPassword()
	if err != nil {
		return err
	}
	report("joining the group")
	if err := e.joinGroup(ctx, password); err != nil {
		return err
	}
	report("waiting until the member is online")

	return e.onlineAndJoined(ctx, password)
}

func (e cluster) UpdateMembership(ctx context.Context, request requests.ClusterMembership) error {
	return e.db.Exec(ctx, "SET GLOBAL group_replication_group_seeds = ?", seedsOf(request.Members))
}

func (e cluster) Admit(ctx context.Context, request requests.MemberAdmit) error {
	return e.changeSeeds(ctx, func(seeds []string) []string {
		return append(seeds, request.Ip+":"+strconv.Itoa(groupPort))
	})
}

func (e cluster) Promote(context.Context, requests.MemberPromote) error {
	return nil
}

func (e cluster) Forget(ctx context.Context, request requests.MemberForget) error {
	return e.changeSeeds(ctx, func(seeds []string) []string {
		return slices.DeleteFunc(seeds, func(seed string) bool { return seed == request.Ip+":"+strconv.Itoa(groupPort) })
	})
}

func (e Engine) changeSeeds(ctx context.Context, change func([]string) []string) error {
	current, _, err := e.db.Value(ctx, "SELECT @@group_replication_group_seeds")
	if err != nil {
		return err
	}
	seeds := slices.DeleteFunc(strings.Split(current, ","), func(seed string) bool { return seed == "" })
	seeds = slices.Compact(change(seeds))

	return e.db.Exec(ctx, "SET GLOBAL group_replication_group_seeds = ?", strings.Join(seeds, ","))
}

func (e Engine) rejoinIfGrouped(ctx context.Context, report contract.Report) error {
	if !e.root.Exists(groupedState) {
		return nil
	}
	password, err := e.replicationPassword()
	if err != nil {
		return err
	}
	report("joining the group")
	if err := e.startGroup(ctx, password); err != nil {
		return fmt.Errorf("could not rejoin the group: %w", err)
	}

	return e.onlineAndJoined(ctx, password)
}

func (e cluster) Watch(context.Context) error { return contract.ErrUnsupported }
