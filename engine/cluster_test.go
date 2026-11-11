package mysql

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/nodefs"
	"github.com/databasifylabs/engine-sdk/requests"
)

func clusterEngine(t *testing.T, rows map[string][][]string, primary string) (Engine, *[]string) {
	t.Helper()
	sent := []string{}
	calls := []string{}
	engine := Engine{db: fakeDB{rows: rows, sent: &sent}, supervisor: fakeSupervisor{calls: &calls}, root: nodefs.Root(t.TempDir())}
	request := clusteredRequest()
	request.Secrets.Replication = secret("rep")
	request.Primary = &primary
	state, _ := json.Marshal(request)
	if err := engine.root.Dir("state"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.root.Write(family.NodeState, string(state), 0o600, false); err != nil {
		t.Fatal(err)
	}

	return engine, &sent
}

func TestSeedBootstrapsTheGroup(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"ONLINE"}}}, "10.0.0.2")

	if err := engine.Cluster().Enable(context.Background(), requests.ClusterEnable{Key: "k"}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	want := []string{"SET GLOBAL group_replication_bootstrap_group = ON", "START GROUP_REPLICATION USER = 'databasify_repl', PASSWORD = ?", "SET GLOBAL group_replication_bootstrap_group = OFF"}
	if !slices.Equal(*sent, want) || !engine.root.Exists(groupedState) {
		t.Fatalf("sent = %q", *sent)
	}
}

func TestAnEmptyJoinerRecoversByClone(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"ONLINE"}}, "@@gtid_executed": {{""}}}, "10.0.0.1")

	if err := engine.Cluster().Enable(context.Background(), requests.ClusterEnable{Key: "k"}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(*sent, []string{"SET GLOBAL group_replication_clone_threshold = 1", "START GROUP_REPLICATION USER = 'databasify_repl', PASSWORD = ?"}) {
		t.Fatalf("sent = %q", *sent)
	}
}

func TestForceQuorumRefusesWithoutOthersDown(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{}, "10.0.0.1")

	_, err := engine.Cluster().ForceQuorum(context.Background(), requests.ClusterForceQuorum{OthersDown: false})

	if codeOf(err) != enums.ErrorCodeBusy || len(*sent) != 0 {
		t.Fatalf("err = %v, sent = %q", err, *sent)
	}
}

func TestPositionCountsGtidIntervals(t *testing.T) {
	cases := map[string]int64{
		"3e11fa47-71ca-11e1-9e33-c80aa9429562:1-100:200-249":                                150,
		"3e11fa47-71ca-11e1-9e33-c80aa9429562:1-5,\n11111111-71ca-11e1-9e33-c80aa9429562:7": 6,
		"": -1,
	}
	for set, want := range cases {
		if got := transactionsIn(set); got != want {
			t.Errorf("%q: %d, want %d", set, got, want)
		}
	}
}

func TestAdmitAddsTheSeed(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"@@group_replication_group_seeds": {{"10.0.0.1:33061"}}}, "10.0.0.1")

	if err := engine.Cluster().Admit(context.Background(), requests.MemberAdmit{Ip: "10.0.0.4"}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(*sent, []string{"SET GLOBAL group_replication_group_seeds = ?"}) {
		t.Fatalf("sent = %q", *sent)
	}
}

func TestAMemberBackFromCloneStartsTheGroupAgain(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"OFFLINE"}}}, "10.0.0.1")
	engine.db = sequenceDB{fakeDB: engine.db.(fakeDB), states: []string{"OFFLINE", "ONLINE"}}

	if err := engine.waitOnline(context.Background(), "rep"); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(*sent, []string{"START GROUP_REPLICATION USER = 'databasify_repl', PASSWORD = ?"}) {
		t.Fatalf("sent = %q", *sent)
	}
}

type sequenceDB struct {
	fakeDB
	states []string
}

func (s sequenceDB) Value(ctx context.Context, query string, args ...any) (string, bool, error) {
	if query != memberStateQuery || len(s.states) == 0 {
		return s.fakeDB.Value(ctx, query, args...)
	}
	state := s.states[0]
	copy(s.states, s.states[1:])
	s.states[len(s.states)-1] = "ONLINE"

	return state, true, nil
}

func TestQuorumCountsConfiguredVotersWhenAMemberWasExpelled(t *testing.T) {
	engine, _ := clusterEngine(t, map[string][][]string{"COUNT(*), COALESCE": {{"1", "1"}}, "MEMBER_STATE FROM": {{"ONLINE"}}}, "10.0.0.1")

	quorum, err := engine.Cluster().Quorum(context.Background())

	if err != nil || quorum.Total != 2 || quorum.Online != 1 {
		t.Fatalf("quorum = %+v, err = %v", quorum, err)
	}
}

func TestReadOnlyNodeUsesManagedFailover(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"@@gtid_executed": {{"uuid:1-5"}}, "CHANNEL_NAME = 'group'": {{"1"}}}, "10.0.0.1")
	request, _ := family.Saved(engine.root)
	request.ReadOnly = true
	state, _ := json.Marshal(request)
	if _, err := engine.root.Write(family.NodeState, string(state), 0o600, false); err != nil {
		t.Fatal(err)
	}

	if err := engine.Cluster().Enable(context.Background(), requests.ClusterEnable{Key: "k"}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if len(*sent) != 3 || !strings.Contains((*sent)[0], "SOURCE_CONNECT_RETRY = 5, SOURCE_RETRY_COUNT = 3, SOURCE_CONNECTION_AUTO_FAILOVER = 1 FOR CHANNEL 'group'") || !strings.Contains((*sent)[1], "asynchronous_connection_failover_add_managed('group', 'GroupReplication'") || !engine.root.Exists(replicaState) {
		t.Fatalf("sent = %q", *sent)
	}
}

func TestAJoinedMemberBootsReadOnlyUntilItRejoins(t *testing.T) {
	engine, _ := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"ONLINE"}}}, "10.0.0.2")

	if err := engine.Cluster().Enable(context.Background(), requests.ClusterEnable{Key: "k"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if engine.root.Read(groupedConf) != "[mysqld]\nsuper_read_only=ON\n" {
		t.Fatalf("grouped.cnf = %q", engine.root.Read(groupedConf))
	}
	if _, err := engine.Cluster().Disable(context.Background()); err != nil {
		t.Fatal(err)
	}

	if engine.root.Exists(groupedConf) || !strings.Contains(myCnfOf("/srv/mysql", "8.4", clusteredRequest()), "\n!includedir /srv/mysql/conf/conf.d\n") {
		t.Fatal("the read-only boot file outlived cluster disable or my.cnf does not include it")
	}
}

func TestConfigureKeepsAJoinedMemberBootingReadOnly(t *testing.T) {
	engine, _ := clusterEngine(t, nil, "10.0.0.2")
	if _, err := engine.root.Write(groupedState, "", 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := engine.root.Dir("data"); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.root.Write("data/ibdata1", "x", 0o600, false); err != nil {
		t.Fatal(err)
	}
	request := clusteredRequest()
	request.Secrets.Admin = secret("admin")

	if _, err := engine.Nodes().Configure(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	if !engine.root.Exists(groupedConf) {
		t.Fatal("a member that already joined the group must boot read-only")
	}
}

func TestWakeStartsMySQLSoItsPositionCanBeRead(t *testing.T) {
	calls := []string{}
	engine := Engine{db: fakeDB{rows: map[string][][]string{}}, supervisor: fakeSupervisor{calls: &calls}, root: nodefs.Root(t.TempDir())}

	if err := engine.Nodes().Wake(context.Background(), requests.NodeWake{}); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(calls, "start "+serverUnit) || slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, "stop") || strings.HasPrefix(call, "kill") }) {
		t.Fatalf("calls = %q", calls)
	}
}

func TestForceQuorumBootstrapsAGroupThatIsNotRunning(t *testing.T) {
	engine, sent := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"ONLINE"}}, "group_replication_applier": {{"OFF"}}}, "10.0.0.2")

	if _, err := engine.Cluster().ForceQuorum(context.Background(), requests.ClusterForceQuorum{OthersDown: true}); err != nil {
		t.Fatal(err)
	}

	want := []string{"SET GLOBAL group_replication_bootstrap_group = ON", "START GROUP_REPLICATION USER = 'databasify_repl', PASSWORD = ?", "SET GLOBAL group_replication_bootstrap_group = OFF"}
	if !slices.Equal(*sent, want) {
		t.Fatalf("sent = %q", *sent)
	}
}

func TestARejoinedMemberBootsReadOnly(t *testing.T) {
	engine, _ := clusterEngine(t, map[string][][]string{"MEMBER_STATE FROM": {{"ONLINE"}}}, "10.0.0.1")

	if err := engine.Cluster().Rejoin(context.Background(), requests.ClusterRejoin{Key: "k"}, func(string) {}); err != nil {
		t.Fatal(err)
	}

	if !engine.root.Exists(groupedConf) || !engine.root.Exists(groupedState) {
		t.Fatal("a member back in the group must boot read-only")
	}
}
