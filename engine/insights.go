package mysql

import (
	"context"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/responses"
)

func (e nodes) Insights(ctx context.Context) (responses.QueryInsights, error) {
	return family.Insights(ctx, e.db, e.Capabilities().ReservedDatabases)
}
