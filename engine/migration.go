package mysql

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/databasifylabs/engine-sdk/version"

	"github.com/go-sql-driver/mysql"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	migrationUnit    = "mysql-migration"
	mysqlshBinary    = "/usr/bin/mysqlsh"
	errNoSuchChannel = 3074
	channelLagQuery  = `SELECT c.SERVICE_STATE, a.SERVICE_STATE, GTID_SUBSET(c.RECEIVED_TRANSACTION_SET, @@gtid_executed), IFNULL(TIMESTAMPDIFF(SECOND, (SELECT MAX(LAST_APPLIED_TRANSACTION_ORIGINAL_COMMIT_TIMESTAMP) FROM performance_schema.replication_applier_status_by_worker WHERE CHANNEL_NAME = 'migration'), c.LAST_QUEUED_TRANSACTION_ORIGINAL_COMMIT_TIMESTAMP), 0) FROM performance_schema.replication_connection_status c JOIN performance_schema.replication_applier_status a USING (CHANNEL_NAME) WHERE c.CHANNEL_NAME = 'migration'`
)

type migration struct{ Engine }

func (e Engine) Migration() contract.Migration { return migration{e} }

func (e Engine) migrator() family.Migrator {
	return family.Migrator{DB: e.db, Root: e.root, Supervisor: e.supervisor, Unit: migrationUnit, Reserved: e.Capabilities().ReservedDatabases, Session: map[string]string{"information_schema_stats_expiry": "0"}}
}

func (e migration) Check(ctx context.Context, request requests.MigrationCheck) (responses.MigrationCheck, error) {
	report, _, err := e.migrator().Inventory(ctx, request)
	if err != nil || !report.Reachable {
		return report, err
	}
	source := e.migrator().Source(request.Source)
	report.CanReplicate, report.Warnings = e.replicationReadiness(ctx, source, *report.Version, report.Warnings)
	report.Problems = copyProblemsOf(ctx, source)

	return report, nil
}

type sourceValues interface {
	Value(ctx context.Context, query string, args ...any) (string, bool, error)
}

func copyProblemsOf(ctx context.Context, source sourceValues) []string {
	if _, _, err := source.Value(ctx, "SELECT 1 FROM mysql.role_edges LIMIT 1"); err != nil {
		return []string{"MySQL Shell copies only as a source user with SELECT on mysql.* (it reads the roles), grant it and check again"}
	}

	return nil
}

func (e Engine) replicationReadiness(ctx context.Context, source family.RemoteDB, release string, warnings []string) (bool, []string) {
	gtidMode, _, _ := source.Value(ctx, "SELECT @@gtid_mode")
	grants, _ := source.Rows(ctx, "SHOW GRANTS")
	granted := strings.ToUpper(fmt.Sprint(grants))
	canReplicate := gtidMode == "ON" && (strings.Contains(granted, "REPLICATION SLAVE") || strings.Contains(granted, "ALL PRIVILEGES ON *.*"))
	if gtidMode != "ON" {
		warnings = append(warnings, "the source runs with gtid_mode "+gtidMode+", so only a one-shot copy is possible")
	}
	local, _, _ := e.db.Value(ctx, "SELECT @@version")
	if version.NewerThan(release, local) {
		warnings = append(warnings, "the source runs "+release+", newer than this server; copying to an older version is not supported")
	}

	return canReplicate, warnings
}

func (e migration) Start(_ context.Context, request requests.MigrationStart) error {
	return e.migrator().Start(request)
}

func copyArguments(source dtos.MigrationSource, databases []string, continuous bool) []string {
	sslMode := "PREFERRED"
	if source.Tls {
		sslMode = "REQUIRED"
	}
	args := []string{"--passwords-from-stdin", "--uri=" + url.PathEscape(source.Username) + "@" + source.Host + ":" + strconv.FormatInt(source.Port, 10), "--ssl-mode=" + sslMode, "--", "util", "copy-schemas"}
	args = append(args, databases...)

	args = append(args, "--host=127.0.0.1", "--port="+strconv.Itoa(clientPort), "--user=root", "--threads=4")
	if continuous {
		args = append(args, "--updateGtidSet=append")
	}

	return args
}

func (e migration) Copy(ctx context.Context) error {
	source, requested, continuous := family.SourceFromEnvironment()
	databases, err := e.migrator().Databases(ctx, source, requested)
	if err != nil {
		return err
	}
	if err := e.db.Exec(ctx, "SET GLOBAL local_infile = ON"); err != nil {
		return err
	}
	defer func() { _ = e.db.Exec(context.WithoutCancel(ctx), "SET GLOBAL local_infile = OFF") }()
	shell := exec.CommandContext(ctx, mysqlshBinary, copyArguments(source, databases, continuous)...)
	shell.Env = append(os.Environ(), "LC_ALL=C.UTF-8")
	shell.Stdin = strings.NewReader(source.Password + "\n" + family.PasswordIn(e.root.Path("conf/admin.cnf")) + "\n")
	shell.Stdout, shell.Stderr = os.Stdout, os.Stderr
	if err := shell.Run(); err != nil {
		return fmt.Errorf("MySQL Shell could not copy the schemas: %w", err)
	}
	if !continuous {
		return nil
	}

	return e.follow(ctx, source, databases)
}

func (e Engine) follow(ctx context.Context, source dtos.MigrationSource, databases []string) error {
	ssl := 0
	if source.Tls {
		ssl = 1
	}
	if err := e.db.Exec(ctx, "CHANGE REPLICATION SOURCE TO SOURCE_HOST = ?, SOURCE_PORT = ?, SOURCE_USER = ?, SOURCE_PASSWORD = ?, SOURCE_AUTO_POSITION = 1, SOURCE_SSL = ? FOR CHANNEL 'migration'",
		source.Host, source.Port, source.Username, source.Password, ssl); err != nil {
		return err
	}
	if err := e.db.Exec(ctx, "CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE = ("+wildTables(databases)+") FOR CHANNEL 'migration'"); err != nil {
		return err
	}

	return e.db.Exec(ctx, "START REPLICA FOR CHANNEL 'migration'")
}

func wildTables(databases []string) string {
	patterns := make([]string, 0, len(databases))
	for _, database := range databases {
		patterns = append(patterns, "'"+strings.ReplaceAll(database, "'", "''")+".%'")
	}

	return strings.Join(patterns, ", ")
}

func (e migration) Progress(ctx context.Context) (responses.MigrationProgress, error) {
	return e.migrator().Progress(func() (int64, bool) { return e.channelLag(ctx) }), nil
}

func (e Engine) channelLag(ctx context.Context) (int64, bool) {
	rows, err := e.db.Rows(ctx, channelLagQuery)
	if err != nil || len(rows) == 0 || rows[0][0] != "ON" || rows[0][1] != "ON" {
		return 0, false
	}
	if rows[0][2] == "1" {
		return 0, true
	}
	lag, _ := strconv.ParseInt(rows[0][3], 10, 64)

	return max(lag, 0), true
}

func (e migration) Stop(ctx context.Context) error {
	e.migrator().StopCopy()
	for _, statement := range []string{"STOP REPLICA FOR CHANNEL 'migration'", "RESET REPLICA ALL FOR CHANNEL 'migration'"} {
		err := e.db.Exec(ctx, statement)
		var server *mysql.MySQLError
		if err != nil && !(errors.As(err, &server) && server.Number == errNoSuchChannel) {
			return err
		}
	}

	return nil
}
