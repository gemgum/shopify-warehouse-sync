package models

import (
	"context"
	"time"
)

// What set a sync off. Recorded so that a noisy webhook producing hundreds of
// syncs that change nothing becomes visible.
const (
	TriggerManual  = "manual"
	TriggerWebhook = "webhook"
)

// SyncRun is one synchronisation, successful or not.
type SyncRun struct {
	ID         int64      `json:"id"`
	Shop       string     `json:"shop"`
	Trigger    string     `json:"trigger"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Checked    int        `json:"checked"`
	Updated    int        `json:"updated"`
	Error      *string    `json:"error"`
}

type SyncRepository interface {
	// Start writes the row **before** the work begins, so a sync that dies
	// halfway still leaves a trace.
	Start(ctx context.Context, shop, trigger string) (int64, error)

	Finish(ctx context.Context, runID int64, checked, updated int, failure error) error

	// LogChanges writes one row per SKU that changed.
	LogChanges(ctx context.Context, runID int64, updates []Update) error

	History(ctx context.Context, shop string, limit int) ([]SyncRun, error)
}
