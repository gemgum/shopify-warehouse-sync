package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// Sync satisfies models.SyncRepository.
type Sync struct{}

var _ models.SyncRepository = Sync{}

func (Sync) Start(ctx context.Context, shop, trigger string) (int64, error) {
	const q = `insert into sync_runs (shop, trigger) values ($1, $2) returning id`

	return One(ctx, func(row pgx.Row) (int64, error) {
		var id int64
		return id, row.Scan(&id)
	}, q, shop, trigger)
}

// Finish closes a sync's row. failure may be nil.
//
// The error text is stored verbatim, Shopify's own wording included. This row
// never leaves over HTTP unfiltered — and whoever reads it while chasing a
// problem is exactly who needs the original sentence.
func (Sync) Finish(ctx context.Context, runID int64, checked, updated int, failure error) error {
	var message *string
	if failure != nil {
		text := failure.Error()
		message = &text
	}

	const q = `update sync_runs
		set finished_at = now(), checked = $2, updated = $3, error = $4
		where id = $1`

	return Exec(ctx, q, runID, checked, updated, message)
}

// LogChanges writes one row per SKU that changed.
//
// One statement for the whole batch rather than one per row: a large sync can
// change thousands of SKUs, and thousands of round trips would make the
// recording the slowest part of a sync that ought to be quick.
//
// `unnest` keeps the parameter count at four no matter how long the batch is —
// placeholders assembled one by one would hit Postgres's 65535-parameter limit
// somewhere around sixteen thousand rows.
func (Sync) LogChanges(ctx context.Context, runID int64, updates []models.Update) error {
	if len(updates) == 0 {
		return nil
	}

	skus := make([]string, len(updates))
	before := make([]int32, len(updates))
	after := make([]int32, len(updates))
	for i, u := range updates {
		skus[i] = u.SKU
		before[i] = int32(u.Quantity)
		after[i] = int32(u.NewQuantity)
	}

	const q = `
		insert into inventory_changes (run_id, sku, qty_before, qty_after)
		select $1, * from unnest($2::text[], $3::int[], $4::int[])`

	return Exec(ctx, q, runID, skus, before, after)
}

// History returns a store's most recent syncs, newest first.
func (Sync) History(ctx context.Context, shop string, limit int) ([]models.SyncRun, error) {
	const q = `
		select id, shop, trigger, started_at, finished_at, checked, updated, error
		from sync_runs
		where shop = $1
		order by started_at desc
		limit $2`

	return Many(ctx, func(row pgx.Rows) (models.SyncRun, error) {
		var r models.SyncRun
		err := row.Scan(&r.ID, &r.Shop, &r.Trigger, &r.StartedAt,
			&r.FinishedAt, &r.Checked, &r.Updated, &r.Error)
		return r, err
	}, q, shop, limit)
}
