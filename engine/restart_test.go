package mysql

import (
	"slices"
	"testing"
)

func TestRestartRestartsTheServer(t *testing.T) {
	calls := []string{}
	engine := Engine{supervisor: fakeSupervisor{calls: &calls}}

	if err := engine.Nodes().Restart(t.Context()); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(calls, []string{"restart " + serverUnit}) {
		t.Fatalf("calls = %v", calls)
	}
}
