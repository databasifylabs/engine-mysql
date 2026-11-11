package mysql

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/databasifylabs/engine-sdk/dtos"
)

func TestCopyArgvKeepsThePasswordOut(t *testing.T) {
	source := dtos.MigrationSource{Host: "10.0.0.9", Port: 3306, Username: "mi gr", Password: "s3cret", Tls: true}

	args := copyArguments(source, []string{"shop", "audit"}, true)

	joined := strings.Join(args, " ")
	if strings.Contains(joined, "s3cret") || !strings.Contains(joined, "--uri=mi%20gr@10.0.0.9:3306 --ssl-mode=REQUIRED -- util copy-schemas shop audit --host=127.0.0.1 --port=25060 --user=root") || !slices.Contains(args, "--updateGtidSet=append") {
		t.Fatalf("args = %q", args)
	}
}

func TestAOneTimeCopyLeavesTheTargetGtidsAlone(t *testing.T) {
	args := copyArguments(dtos.MigrationSource{Host: "10.0.0.9", Port: 3306, Username: "app"}, []string{"shop"}, false)

	if slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "--updateGtidSet") }) {
		t.Fatalf("args = %q", args)
	}
}

func TestFollowSetsTheChannelFilter(t *testing.T) {
	if got := wildTables([]string{"shop", "o'brien"}); got != "'shop.%', 'o''brien.%'" {
		t.Fatalf("filter = %s", got)
	}
}

func TestStopIgnoresAMissingChannel(t *testing.T) {
	calls := []string{}
	engine := Engine{db: fakeDB{err: &mysql.MySQLError{Number: 3074, Message: "no channel"}}, supervisor: fakeSupervisor{calls: &calls}}

	if err := engine.Migration().Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type deniedSource struct{ denied string }

func (s deniedSource) Value(_ context.Context, query string, _ ...any) (string, bool, error) {
	if strings.Contains(query, s.denied) {
		return "", false, errors.New("Error 1142: SELECT command denied to user 'app'@'%' for table 'role_edges'")
	}

	return "1", true, nil
}

func TestCopyNeedsTheSourceUserToReadTheRoles(t *testing.T) {
	problems := copyProblemsOf(context.Background(), deniedSource{denied: "mysql.role_edges"})
	allowed := copyProblemsOf(context.Background(), deniedSource{denied: "nothing"})

	if len(problems) != 1 || !strings.Contains(problems[0], "SELECT on mysql.*") || len(allowed) != 0 {
		t.Fatalf("problems = %q, allowed = %q", problems, allowed)
	}
}
