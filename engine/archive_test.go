package mysql

import (
	"context"
	"slices"
	"strings"
	"testing"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/nodefs"
)

func TestRestoreStartsAtTheBackupGtids(t *testing.T) {
	gtids, err := family.BackupGtids("tool_version = 8.4.0-7\nbinlog_pos = filename 'binlog.000007', position '254', GTID of the last change '116eb3f5-bba1-549f-8e2b-b34e73d9c2f3:1-28,86cda4b2-dc1a-11f1-bf89-1e721b02147d:1-23'\nlock_time = 0\n")

	if err != nil || gtids != "116eb3f5-bba1-549f-8e2b-b34e73d9c2f3:1-28,86cda4b2-dc1a-11f1-bf89-1e721b02147d:1-23" {
		t.Fatalf("gtids = %q, err = %v", gtids, err)
	}
}

func TestArchiveAddsTheStreamAndSchedules(t *testing.T) {
	request := clusteredRequest()
	request.Archive = &dtos.ArchiveSettings{RetentionDays: 7, BackupHour: 3}

	encoded, err := manifestOf("/srv/mysql", request)

	text := string(encoded)
	if err != nil || !strings.Contains(text, `"archive-stream"`) || !strings.Contains(text, `"archive-ship"`) || !strings.Contains(text, `"onCalendar": "03:00"`) {
		t.Fatalf("manifest = %s", text)
	}
}

func TestAgentCnfKeepsThePasswordOutOfArgv(t *testing.T) {
	cnf := agentCnfOf("/srv/mysql", "s3cret")

	if !strings.Contains(cnf, "user=databasify_agent\npassword=s3cret\n") || !strings.Contains(cnf, "[xtrabackup]\ndatadir=/srv/mysql/data\n") {
		t.Fatalf("cnf = %s", cnf)
	}
}

func TestConfigureStartsTheBinlogCopy(t *testing.T) {
	calls := []string{}
	engine := Engine{db: fakeDB{}, supervisor: fakeSupervisor{calls: &calls}, root: nodefs.Root(t.TempDir()), run: func(context.Context, string, ...string) (string, error) { return "", nil }}
	request := clusteredRequest()
	request.Secrets.Agent = secret("agent")
	request.Archive = &dtos.ArchiveSettings{RetentionDays: 7, BackupHour: 3}

	if _, err := engine.Nodes().Configure(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(calls, "start "+archiveUnits.Stream) {
		t.Fatalf("calls = %v", calls)
	}
}
