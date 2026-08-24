-- Initial schema.
--
-- Three tables, and their order tells the service's story: a store installs the
-- app (shops), each synchronisation is one row (sync_runs), and every SKU that
-- changed gets a row of its own (inventory_changes).

create table if not exists shops (
    domain       text primary key,
    access_token text        not null,
    scopes       text        not null default '',
    installed_at timestamptz not null default now(),
    -- Revoked, not deleted. The sync history points at this row, and history
    -- pointing at a row that no longer exists cannot be traced.
    uninstalled_at timestamptz
);

-- One row per sync, written before the work begins.
--
-- Written up front so a sync that dies halfway still leaves a trace. If the row
-- were only written on completion, the failures would be the least visible
-- thing in the table — which is exactly what someone needs to see.
create table if not exists sync_runs (
    id          bigserial primary key,
    shop        text        not null references shops (domain),
    -- What set it off: 'manual' or 'webhook'. Kept apart so a noisy webhook
    -- producing hundreds of syncs is visible rather than blended in.
    trigger     text        not null,
    started_at  timestamptz not null default now(),
    finished_at timestamptz,
    checked     integer     not null default 0,
    updated     integer     not null default 0,
    error       text
);

create index if not exists sync_runs_shop_idx on sync_runs (shop, started_at desc);

-- One row per SKU that actually changed.
--
-- This is what answers "why does this show 4?". Without qty_before the answer
-- is only "because it was set to 4", which tells nobody anything.
create table if not exists inventory_changes (
    id         bigserial primary key,
    run_id     bigint      not null references sync_runs (id) on delete cascade,
    sku        text        not null,
    qty_before integer     not null,
    qty_after  integer     not null,
    changed_at timestamptz not null default now()
);

create index if not exists inventory_changes_sku_idx on inventory_changes (sku, changed_at desc);
