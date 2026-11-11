package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/databasifylabs/engine-sdk/command"
	"github.com/databasifylabs/engine-sdk/enums"
)

type fakeDB struct {
	rows map[string][][]string
	err  error
	sent *[]string
}

func (f fakeDB) find(query string) [][]string {
	for fragment, rows := range f.rows {
		if strings.Contains(query, fragment) {
			return rows
		}
	}

	return [][]string{}
}

func (f fakeDB) Exec(_ context.Context, query string, args ...any) error {
	if f.sent != nil {
		*f.sent = append(*f.sent, query)
	}

	return f.err
}

func (f fakeDB) Rows(_ context.Context, query string, _ ...any) ([][]string, error) {
	if f.err != nil {
		return nil, f.err
	}

	return f.find(query), nil
}

func (f fakeDB) Value(ctx context.Context, query string, args ...any) (string, bool, error) {
	rows, err := f.Rows(ctx, query, args...)
	if err != nil || len(rows) == 0 {
		return "", false, err
	}

	return rows[0][0], true, nil
}

func statusWith(t *testing.T, rows map[string][][]string) enums.NodeRole {
	t.Helper()
	rows["@@version"] = [][]string{{"9.7.2-commercial"}}
	status, err := Engine{db: fakeDB{rows: rows}}.Nodes().Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Version == nil || *status.Version != "9.7.2" {
		t.Fatalf("version = %v", status.Version)
	}

	return status.Role
}

func TestStatusFromGroupMembers(t *testing.T) {
	if role := statusWith(t, map[string][][]string{"MEMBER_ROLE": {{"PRIMARY"}}}); role != enums.NodeRolePrimary {
		t.Fatalf("role = %s", role)
	}
	if role := statusWith(t, map[string][][]string{"MEMBER_ROLE": {{"SECONDARY"}}}); role != enums.NodeRoleReplica {
		t.Fatalf("role = %s", role)
	}
}

func TestStatusOutsideAGroup(t *testing.T) {
	cases := map[string]struct {
		readOnly, channels string
		want               enums.NodeRole
	}{
		"writable":          {"0", "0", enums.NodeRolePrimary},
		"read-only replica": {"1", "1", enums.NodeRoleReplica},
		"read-only alone":   {"1", "0", enums.NodeRoleUnknown},
	}
	for name, c := range cases {
		role := statusWith(t, map[string][][]string{"@@super_read_only": {{c.readOnly}}, "replication_connection_status": {{c.channels}}})
		if role != c.want {
			t.Errorf("%s: role = %s", name, role)
		}
	}
}

func TestStatusOfflineWhenSocketRefuses(t *testing.T) {
	status, err := Engine{db: fakeDB{err: command.Unavailable("connect: connection refused")}}.Nodes().Status(context.Background())

	if err != nil || status.Role != enums.NodeRoleOffline || status.Version != nil {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
}

func TestStatusFailsOnOtherErrors(t *testing.T) {
	_, err := Engine{db: fakeDB{err: errors.New("boom")}}.Nodes().Status(context.Background())

	if err == nil {
		t.Fatal("status hid an internal error")
	}
}

func TestMetricsReadStatusAndSize(t *testing.T) {
	db := fakeDB{rows: map[string][][]string{
		"global_status":             {{"Threads_connected", "4"}, {"Questions", "120"}},
		"information_schema.tables": {{"3145728"}},
	}}

	metrics, err := Engine{db: db}.Nodes().Metrics(context.Background())

	if err != nil || metrics.Connections != 4 || metrics.Queries != 120 || metrics.DataMb != 3 {
		t.Fatalf("metrics = %+v, err = %v", metrics, err)
	}
}

func codeOf(err error) enums.ErrorCode {
	var failure command.Failure
	if errors.As(err, &failure) {
		return failure.Code
	}

	return ""
}
