package mysql

import (
	"context"
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
)

func TestInsightsConvertsPicoseconds(t *testing.T) {
	engine := Engine{db: fakeDB{rows: map[string][][]string{
		"events_statements_summary_by_digest": {{"SELECT * FROM `events` WHERE `id` = ?", "40", "18446744073709551615", "2500000000"}},
	}}}

	insights, err := engine.Nodes().Insights(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := dtos.Statement{Text: "SELECT * FROM `events` WHERE `id` = ?", Calls: 40, TotalMs: 18446744073, MeanMs: 2}
	if len(insights.Statements) != 1 || insights.Statements[0] != want {
		t.Fatalf("got %+v", insights.Statements)
	}
}
