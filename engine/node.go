package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	initFile      = "conf/init.sql"
	readyTimeout  = 6 * time.Hour
	crashRestarts = 3
)

func (e nodes) Configure(ctx context.Context, request requests.NodeConfigure) (responses.NodeConfigure, error) {
	if request.Secrets.Admin == nil {
		return responses.NodeConfigure{}, command.Invalid("MySQL needs the admin secret")
	}
	effective, err := e.configs().Effective(request)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	for _, dir := range []string{"conf", "data", "run", "state"} {
		if err := e.root.Dir(dir); err != nil {
			return responses.NodeConfigure{}, err
		}
	}
	if request.Archive != nil && request.Secrets.Agent == nil {
		return responses.NodeConfigure{}, command.Invalid("MySQL backups need the agent secret")
	}
	encoded, err := manifestOf(string(e.root), request)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	state, err := json.Marshal(request)
	if err != nil {
		return responses.NodeConfigure{}, err
	}
	if err := e.bootFilesFor(request); err != nil {
		return responses.NodeConfigure{}, err
	}
	changed := false
	for _, file := range []struct {
		path        string
		content     string
		engineOwned bool
	}{
		{"conf/my.cnf", myCnfOf(string(e.root), e.line, effective), true},
		{"conf/admin.cnf", family.AdminCnfOf(socketPath, *request.Secrets.Admin), false},
		{"conf/processes.json", string(encoded), false},
		{family.NodeState, string(state), false},
		{agentCnf, agentCnfOf(string(e.root), family.ValueOr(request.Secrets.Agent)), false},
	} {
		written, err := e.root.Write(file.path, file.content, 0o600, file.engineOwned)
		if err != nil {
			return responses.NodeConfigure{}, err
		}
		changed = changed || written
	}
	if e.root.Empty("data") {
		if err := e.initialize(ctx, *request.Secrets.Admin); err != nil {
			return responses.NodeConfigure{}, err
		}
		changed = true
	}

	if err := e.supervisor.Do("daemon-reload"); err != nil {
		return responses.NodeConfigure{}, err
	}
	if request.Archive != nil {
		return responses.NodeConfigure{Changed: changed}, e.supervisor.Do("start", archiveUnits.Stream)
	}

	return responses.NodeConfigure{Changed: changed}, nil
}

func (e Engine) bootFilesFor(request requests.NodeConfigure) error {
	if request.Clustered && !request.ReadOnly && e.root.Exists(groupedState) {
		return e.bootReadOnly()
	}

	return e.root.Dir(filepath.Dir(groupedConf))
}

func (e Engine) initialize(ctx context.Context, password string) error {
	if _, err := e.root.Write(initFile, "ALTER USER 'root'@'localhost' IDENTIFIED BY "+family.SQLString(password)+";\n", 0o600, true); err != nil {
		return err
	}
	defer func() { _ = os.Remove(e.root.Path(initFile)) }()
	_, err := e.run(ctx, mysqldBinary, "--no-defaults", "--initialize-insecure", "--user="+engineUser, "--datadir="+e.root.Path("data"), "--init-file="+e.root.Path(initFile))

	return err
}

func (e nodes) Start(context.Context) error {
	if err := e.supervisor.Do("start", serverUnit); err != nil {
		return err
	}
	if request, err := family.Saved(e.root); err == nil && request.Archive != nil {
		return e.supervisor.Do("start", archiveUnits.Stream)
	}

	return nil
}

func (e nodes) Ready(ctx context.Context, _ requests.NodeReady, report contract.Report) error {
	report("waiting for MySQL")
	baseline := e.restarts()
	err := family.Until(ctx, readyTimeout, func() (bool, error) {
		if e.restarts()-baseline > crashRestarts {
			return false, errors.New("MySQL keeps stopping while it starts, see its log")
		}
		_, _, err := e.db.Value(ctx, "SELECT 1")

		return err == nil, nil
	})
	if err != nil {
		return err
	}

	return e.rejoinIfGrouped(ctx, report)
}

func (e Engine) restarts() int {
	restarts, _ := strconv.Atoi(e.supervisor.Property(serverUnit, "NRestarts"))

	return restarts
}

func (e nodes) Wake(ctx context.Context, _ requests.NodeWake) error {
	_ = e.supervisor.Do("reset-failed", serverUnit)
	if err := e.supervisor.Do("start", serverUnit); err != nil {
		return err
	}

	return family.Until(ctx, onlineTimeout, func() (bool, error) {
		_, _, err := e.db.Value(ctx, "SELECT 1")

		return err == nil, nil
	})
}

func (e nodes) Bootstrap(ctx context.Context, request requests.NodeBootstrap) error {
	if request.Secrets.Replication == nil || request.Secrets.Agent == nil {
		return command.Invalid("MySQL needs the replication and agent secrets")
	}
	saved, err := family.Saved(e.root)
	if err != nil || !saved.Seed() {
		return err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"CREATE USER IF NOT EXISTS 'databasify_repl'@'%' IDENTIFIED WITH caching_sha2_password BY ? REQUIRE SSL", []any{*request.Secrets.Replication}},
		{"ALTER USER 'databasify_repl'@'%' IDENTIFIED BY ?", []any{*request.Secrets.Replication}},
		{"GRANT REPLICATION SLAVE, BACKUP_ADMIN, CLONE_ADMIN, GROUP_REPLICATION_STREAM, CONNECTION_ADMIN ON *.* TO 'databasify_repl'@'%'", nil},
		{"GRANT SELECT ON performance_schema.replication_group_members TO 'databasify_repl'@'%'", nil},
		{"CREATE USER IF NOT EXISTS 'databasify_agent'@'localhost' IDENTIFIED WITH caching_sha2_password BY ?", []any{*request.Secrets.Agent}},
		{"ALTER USER 'databasify_agent'@'localhost' IDENTIFIED BY ?", []any{*request.Secrets.Agent}},
		{"GRANT BACKUP_ADMIN, PROCESS, RELOAD, LOCK TABLES, REPLICATION CLIENT, REPLICATION SLAVE ON *.* TO 'databasify_agent'@'localhost'", nil},
		{"GRANT SELECT ON performance_schema.log_status TO 'databasify_agent'@'localhost'", nil},
		{"GRANT SELECT ON performance_schema.keyring_component_status TO 'databasify_agent'@'localhost'", nil},
		{"GRANT SELECT ON performance_schema.replication_group_members TO 'databasify_agent'@'localhost'", nil},
	}
	for _, statement := range statements {
		if err := e.db.Exec(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return contract.CreateDefaultAccount(ctx, e.Accounts(), request.Account)
}

func (e nodes) SyncConfig(context.Context) (responses.ConfigSync, error) {
	return responses.ConfigSync{}, nil
}

func (e nodes) ApplyConfig(ctx context.Context) error {
	request, err := family.Saved(e.root)
	if err != nil {
		return err
	}

	effective, err := e.configs().Effective(request)
	if err != nil {
		return err
	}

	return family.Settings{DB: e.db, Root: e.root, Set: "SET PERSIST", Forget: "RESET PERSIST IF EXISTS", Static: e.configs().Static()}.Apply(ctx, effective.Settings)
}

func (e nodes) InstallTLS(ctx context.Context, request requests.TlsInstall) error {
	return e.root.InstallTLS(ctx, request, false, e.ReloadTLS)
}

func (e nodes) Restart(context.Context) error {
	return e.supervisor.Do("restart", serverUnit)
}

func (e nodes) ReloadTLS(ctx context.Context) error {
	if !e.supervisor.Active(serverUnit) {
		return nil
	}

	return e.db.Exec(ctx, "ALTER INSTANCE RELOAD TLS")
}

func (e nodes) Reset(context.Context, requests.NodeReset) error {
	return contract.ErrUnsupported
}
