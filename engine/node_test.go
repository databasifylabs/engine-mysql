package mysql

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/ctl"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/nodefs"
	"github.com/databasifylabs/engine-sdk/requests"
)

type fakeSupervisor struct{ calls *[]string }

func (f fakeSupervisor) Do(verb string, units ...string) error {
	*f.calls = append(*f.calls, verb+" "+strings.Join(units, " "))

	return nil
}

func (f fakeSupervisor) Run(transient ctl.Transient) error {
	return f.Do("run "+transient.Unit, transient.Argv...)
}

func (f fakeSupervisor) DoNoBlock(verb string, units ...string) error {
	return f.Do(verb, units...)
}

func (f fakeSupervisor) Kill(signal string, units ...string) error {
	return f.Do("kill "+signal, units...)
}

func (fakeSupervisor) Active(string) bool { return true }

func (fakeSupervisor) Property(string, string) string { return "0" }

func secret(value string) *string { return &value }

func clusteredRequest() requests.NodeConfigure {
	return requests.NodeConfigure{
		Clustered: true, Ip: "10.0.0.2", DatabaseId: "db1",
		Members:  []dtos.Member{{Ip: "10.0.0.1", Position: 1, Voting: true}, {Ip: "10.0.0.2", Position: 2, Voting: true}, {Ip: "10.0.0.3", Position: 3, Voting: false}},
		Settings: map[string]string{"max_connections": "300", "innodb_buffer_pool_size": "1G"},
		Secrets:  dtos.Secrets{Admin: secret("r'oot")},
	}
}

func TestGroupNameIsAStableUuidV5(t *testing.T) {
	if got := groupNameOf("db1"); got != "696be095-2002-5eec-b76b-8c80ccb6140c" {
		t.Fatalf("group name = %s", got)
	}
}

func TestMyCnfForAClusteredMember(t *testing.T) {
	conf := myCnfOf("/srv/mysql", "9.7", clusteredRequest())

	for _, line := range []string{
		"server_id=167772163\n", "report_host=10.0.0.2\n", "gtid_mode=ON\n", "plugin-load-add=mysql_clone.so\n", "plugin-load-add=group_replication.so\n",
		"group_replication_group_name=696be095-2002-5eec-b76b-8c80ccb6140c\n", "group_replication_local_address=10.0.0.2:33061\n",
		"group_replication_group_seeds=10.0.0.1:33061,10.0.0.2:33061\n", "group_replication_start_on_boot=OFF\n", "sql_require_primary_key=ON\n",
		"innodb_buffer_pool_size=1G\nmax_connections=300\n",
	} {
		if !strings.Contains(conf, line) {
			t.Errorf("my.cnf lacks %q:\n%s", line, conf)
		}
	}
	if strings.Contains(conf, "super_read_only") {
		t.Fatal("a voting member must not be read-only")
	}
}

func TestReadOnlyMemberStaysOutOfTheGroup(t *testing.T) {
	request := clusteredRequest()
	request.ReadOnly = true

	conf := myCnfOf("/srv/mysql", "9.7", request)

	if strings.Contains(conf, "group_replication") || !strings.Contains(conf, "super_read_only=ON\n") {
		t.Fatalf("my.cnf =\n%s", conf)
	}
}

func TestSettingsMustBeSingleOptions(t *testing.T) {
	for key, value := range map[string]string{"max_connections\nskip-grant-tables": "1", "max_connections": "1\nskip-grant-tables", "collation_server": "binary\nskip-grant-tables", "default_time_zone": "SYSTEM\nskip-grant-tables"} {
		if catalog().Check(map[string]string{key: value}) == nil {
			t.Errorf("%q=%q smuggled a second option", key, value)
		}
	}
}

func TestApplyResetsRemovedKeys(t *testing.T) {
	sent := []string{}
	calls := []string{}
	engine := Engine{db: fakeDB{sent: &sent}, supervisor: fakeSupervisor{calls: &calls}, root: nodefs.Root(t.TempDir())}
	request := clusteredRequest()
	request.Settings = map[string]string{"max_connections": "300"}
	state, _ := json.Marshal(request)
	for _, dir := range []string{"state"} {
		if err := engine.root.Dir(dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.root.Write(family.NodeState, string(state), 0o600, false); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.root.Write(family.AppliedState, `["max_connections","wait_timeout"]`, 0o600, false); err != nil {
		t.Fatal(err)
	}

	if err := engine.Nodes().ApplyConfig(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"SET PERSIST max_connections = ?", "SET PERSIST innodb_buffer_pool_size = ?", "SET GLOBAL wait_timeout = DEFAULT", "RESET PERSIST IF EXISTS wait_timeout"} {
		if !slices.Contains(sent, want) {
			t.Errorf("sent lacks %q: %q", want, sent)
		}
	}
	if slices.Contains(sent, "SET PERSIST innodb_read_io_threads = ?") {
		t.Fatal("a restart-only setting was set on the running server")
	}
}

func TestReadOnlyVariablesAreLeftForTheRestart(t *testing.T) {
	if !family.RestartOnly(&mysql.MySQLError{Number: 1238, Message: "read only"}) || family.RestartOnly(&mysql.MySQLError{Number: 1193, Message: "unknown"}) {
		t.Fatal("only read-only variables wait for a restart")
	}
}

func bootstrapEngine(t *testing.T, configured requests.NodeConfigure) (Engine, *[]string) {
	t.Helper()
	sent := []string{}
	engine := Engine{db: fakeDB{sent: &sent}, root: nodefs.Root(t.TempDir())}
	if err := engine.root.Dir("state"); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(configured)
	if _, err := engine.root.Write(family.NodeState, string(state), 0o600, false); err != nil {
		t.Fatal(err)
	}

	return engine, &sent
}

func TestBootstrapOnlyRunsOnTheSeed(t *testing.T) {
	request := requests.NodeBootstrap{Secrets: dtos.Secrets{Replication: secret("rep"), Agent: secret("agent")}}
	joiner := clusteredRequest()
	joiner.Primary = secret("10.0.0.1")
	engine, sent := bootstrapEngine(t, joiner)

	if err := engine.Nodes().Bootstrap(context.Background(), request); err != nil || len(*sent) != 0 {
		t.Fatalf("err = %v, sent = %v", err, *sent)
	}
}

func TestBootstrapCreatesInternalUsers(t *testing.T) {
	seed := clusteredRequest()
	seed.Primary = &seed.Ip
	engine, sentPointer := bootstrapEngine(t, seed)
	request := requests.NodeBootstrap{Secrets: dtos.Secrets{Replication: secret("rep"), Agent: secret("agent")}}

	if err := engine.Nodes().Bootstrap(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(*sentPointer, "\n")
	for _, fragment := range []string{"'databasify_repl'@'%'", "GROUP_REPLICATION_STREAM", "performance_schema.replication_group_members TO 'databasify_repl'@'%'", "'databasify_agent'@'localhost'", "performance_schema.log_status"} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("bootstrap lacks %q", fragment)
		}
	}
	if strings.Contains(joined, "rep") && strings.Contains(joined, "BY rep") {
		t.Fatal("a password was written into the query text")
	}
}

func TestSqlStringEscapes(t *testing.T) {
	if got := family.SQLString(`a'b\c`); got != `'a''b\\c'` {
		t.Fatalf("sqlString = %s", got)
	}
}

func TestServerIDsAreUniqueAcrossDatabases(t *testing.T) {
	first, second := clusteredRequest(), clusteredRequest()
	second.Ip, second.DatabaseId = "10.0.0.7", "other"

	if serverIDOf(first) == serverIDOf(second) || serverIDOf(first) == streamServerIDOf(first) || serverIDOf(first) == 0 {
		t.Fatalf("ids = %d, %d, stream %d", serverIDOf(first), serverIDOf(second), streamServerIDOf(first))
	}
}

func TestMysql84OffersNativePasswords(t *testing.T) {
	capabilities := Engine{line: "8.4"}.Capabilities()

	if !slices.Equal(capabilities.PasswordEncryptions, []string{"caching_sha2_password", "mysql_native_password"}) || *capabilities.DefaultPasswordEncryption != "caching_sha2_password" {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if !strings.Contains(myCnfOf("/srv/mysql", "8.4", clusteredRequest()), "mysql_native_password=ON\n") {
		t.Fatal("MySQL 8.4 must load the native password plugin")
	}
}

func TestMysql9HasOnlyCachingSha2(t *testing.T) {
	capabilities := Engine{line: "9.7"}.Capabilities()

	if !slices.Equal(capabilities.PasswordEncryptions, []string{"caching_sha2_password"}) {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if strings.Contains(myCnfOf("/srv/mysql", "9.7", clusteredRequest()), "mysql_native_password") {
		t.Fatal("MySQL 9 removed the native password plugin")
	}
}
