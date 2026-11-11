package mysql

import (
	"reflect"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
)

func TestCapabilitiesListTheMysqlPortsOnEveryLine(t *testing.T) {
	want := dtos.Ports{Client: 25060, Peers: []int{33061}}
	for _, line := range []string{"8.4", "9.7"} {
		if got := (Engine{line: line}).Capabilities().Ports; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: ports = %+v", line, got)
		}
	}
}
