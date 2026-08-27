# Database Modeling Research: Telemetry and Assets

Scope: PostgreSQL/TimescaleDB schema modeling for AmbaGrid telemetry persistence. This note is research only; it does not define or apply migrations.

## Repo Context

`proto/telemetry.proto` names the incoming hardware identity `device_id` and classifies telemetry with `DeviceType`: `DEVICE_TYPE_SMART_METER`, `DEVICE_TYPE_BATTERY_BMS`, and `DEVICE_TYPE_SOLAR_INVERTER`. The ontology, however, defines `Asset` as the durable operational object: "Batteries, inverters, solar arrays, feeders, and meters are assets." The storage model should preserve that boundary: use `device_*` language at the wire/adapter edge, then map it into `asset_*` language in the database.

## Primary Sources Consulted

- PostgreSQL constraints: https://www.postgresql.org/docs/18/ddl-constraints.html
- PostgreSQL enum types: https://www.postgresql.org/docs/current/datatype-enum.html
- PostgreSQL indexes: https://www.postgresql.org/docs/current/indexes.html
- PostgreSQL multicolumn indexes: https://www.postgresql.org/docs/15/indexes-multicolumn.html
- PostgreSQL unique indexes: https://www.postgresql.org/docs/current/indexes-unique.html
- PostgreSQL foreign key actions: https://www.postgresql.org/docs/18/ddl-constraints.html#DDL-CONSTRAINTS-FK
- PostgreSQL date/time functions: https://www.postgresql.org/docs/current/functions-datetime.html
- TimescaleDB `CREATE TABLE` hypertables: https://docs.timescale.com/api/latest/hypertable/create_table/
- TimescaleDB `create_hypertable()`: https://docs.timescale.com/api/latest/hypertable/create_hypertable/
- TimescaleDB hypertable performance: https://docs.timescale.com/use-timescale/latest/hypertables/improve-query-performance/
- TimescaleDB indexing: https://docs.timescale.com/use-timescale/latest/schema-management/indexing/
- TimescaleDB constraints: https://docs.timescale.com/use-timescale/latest/schema-management/about-constraints/
- SQLx migrations module: https://docs.rs/sqlx/latest/sqlx/migrate/
- SQLx `migrate!` macro: https://docs.rs/sqlx/latest/sqlx/macro.migrate.html
- SQLx CLI README: https://github.com/launchbadge/sqlx/blob/main/sqlx-cli/README.md

## Recommendations

### 1. Use `asset` in the database, reserve `device` for ingestion

Recommendation: database tables and foreign keys should use `asset_id`, `asset_type`, and `assets`. The engine should map `MetricPayload.device_id` to `assets.asset_id` and `MetricPayload.device_type` to `assets.asset_type`. If a generic readings interface is useful, expose it as an `asset_readings` view rather than a physical write target.

Rationale: this matches the repo ontology and avoids a future rename when non-meter equipment becomes first-class in the system. The protobuf can keep `device_id` because it describes the reporting hardware on the wire. The database should model the operational object that can receive alerts, belong to a site, and accumulate telemetry history.

Suggested names:

- `assets`
- `asset_readings` view
- `smart_meter_state`
- `smart_meter_readings`
- `battery_bms_state`
- `battery_bms_readings`
- `solar_inverter_state`
- `solar_inverter_readings`
- `alerts.asset_id`

### 2. Separate latest state from time-series readings

Recommendation: use ordinary PostgreSQL tables for latest state and TimescaleDB hypertables for append-heavy telemetry history.

The latest-state tables should be optimized for operational reads and upserts:

- `assets`: one row per physical asset, common attributes and common latest telemetry.
- `smart_meter_state`, `battery_bms_state`, `solar_inverter_state`: one row per asset for type-specific latest state.
- `alerts`: ordinary relational table unless alert event volume later requires time-series treatment.

The readings tables should be insert-only or mostly append-only:

- Per-type readings hypertables: one append-only row per telemetry message, including common fields such as `internal_temperature` plus type-specific fields.
- Optional `asset_readings` view: a `UNION ALL` read interface across per-type readings for generic history queries.

Rationale: TimescaleDB hypertables are PostgreSQL tables automatically partitioned by time, and TimescaleDB specifically recommends hypertables for time-series data. PostgreSQL tables remain the right fit for identity, inventory, links, and current operational state.

### 3. Prefer constrained `text` values over PostgreSQL enum types for early domain values

Recommendation: start with `text` plus named `CHECK` constraints for values that may still evolve, such as `asset_type`, alert `severity`, and alert `status`.

Example shape:

```sql
asset_type text not null
  constraint assets_asset_type_check
  check (asset_type in ('smart_meter', 'battery_bms', 'solar_inverter'))
```

PostgreSQL enum types are valid for static ordered sets, but the docs note that existing enum values cannot be removed and enum ordering cannot be changed without dropping and recreating the type. For this project, `asset_type` may expand as the hardware model grows, so check constraints are easier to evolve in early migrations.

Use named constraints so future migrations can drop and replace them cleanly. PostgreSQL docs also note that constraints provide finer control than types alone and raise an error when invalid data is stored.

### 4. Add domain range checks for physical measurements where the bounds are uncontroversial

Recommendation: add conservative checks for values with obvious validity ranges:

- `battery_soc_pct between 0 and 100`
- `solar_irradiance >= 0`
- `voltage >= 0`
- `total_kwh >= 0`
- `current >= 0`
- `frequency >= 0`

Be cautious with upper bounds and signed measurements until the hardware semantics are explicit. For example, voltage should be non-negative, but the database should not yet decide that every valid deployment must stay under a specific voltage ceiling. Active power may need to represent export/import direction in future hardware integrations. Constraints should protect facts, not encode assumptions that adapters may later violate legitimately.

PostgreSQL warns that `CHECK` constraints should not depend on other table rows; use them for per-row invariants only.

### 5. Use nullable fields in type-specific tables only where the payload can genuinely omit a value

Recommendation: avoid one wide `assets` table full of nullable meter/BMS/inverter columns. Put common latest-state fields in `assets`, and put common historical fields inside each type-specific readings hypertable alongside the type-specific fields.

Within a type table, decide nullability from telemetry semantics:

- If every smart meter reading must include electrical metrics, make those columns `not null`.
- If adapter integrations may produce partial readings, keep readings nullable but consider stricter latest-state validation at the application layer.

This gives the database a clearer shape and avoids enforcing impossible constraints across mixed asset types.

### 6. Index for the expected queries, and keep hypertable indexes time-aware

Recommendation: for latest-state relational tables:

- `assets(asset_id)` as primary key.
- `assets(site_id, asset_type)` for operator views filtered by site and type.
- `assets(site_id, last_seen_at)` for site-scoped stale-device scans. Defer a global `assets(last_seen_at)` index until fleet-wide stale scans become a real query path, such as a NOC view across all sites or an automated global offline sweep.
- `alerts(status, site_id, opened_at desc)` for open-alert dashboards.
- `alerts(asset_id, opened_at desc)` for asset detail pages.

For hypertables:

- Prefer `(asset_id, time desc)` on readings tables because common queries will fetch the recent history for one asset.
- Use TimescaleDB's default time index unless measurements show it is redundant.
- For sparse nullable measurement columns, use partial indexes only when queries filter on that column. TimescaleDB's indexing docs recommend `WHERE column IS NOT NULL` indexes for sparse nullable data.
- Expect real analytics queries, but do not pre-index every measurement value. Early analytics are more likely to be asset/site-scoped time aggregates such as hourly average battery SOC, overnight minimum SOC, inverter irradiance versus output, and temperature trends. Start with asset/time indexes, then add measurement-value indexes only when repeated queries filter directly on thresholds such as `battery_soc_pct < 20` or `solar_irradiance > 900`.
- For repeated analytics dashboards, consider TimescaleDB continuous aggregates before adding many raw-table btree indexes. Continuous aggregates and later compression/downsampling are better fits for recurring portfolio-level reporting than speculative indexes on every sensor column.

Rationale: PostgreSQL docs emphasize that indexes improve lookup speed but add system overhead, so they should be used sensibly. PostgreSQL multicolumn B-tree indexes are most effective when queries constrain the leading columns. TimescaleDB also cautions that indexes not involving the time column can hurt ingest speed on time-series data.

### 7. Avoid primary keys or unique constraints on hypertables unless they include `time`

Recommendation: do not add a plain `primary key (asset_id)` or `unique (asset_id, sequence)` to readings hypertables. If uniqueness is required for idempotency, use a key that includes the partitioning time column, such as `(time, asset_id)` plus another source sequence if available.

Rationale: TimescaleDB requires unique and primary constraints on hypertables to include the partitioning column. The docs also state that time-series data uses unique indexes less often than relational data. Since the current payload does not appear to include a stable event UUID or sequence number, the safer initial choice is append-only readings with no artificial uniqueness claim.

### 8. Avoid foreign keys on append-heavy readings hypertables for the first migration

Recommendation: do not put foreign keys on the first readings hypertables. The engine should upsert `assets` before inserting readings, while the database keeps cheap per-row checks such as non-empty `asset_id` and fixed `asset_type`. TimescaleDB supports foreign keys from hypertables to regular tables, but the write-path cost is not free and hypertable-to-hypertable foreign keys are not supported.

For the initial design:

- Latest-state tables should have `asset_id references assets(asset_id) on delete cascade`, because type-specific state cannot exist independently of the asset identity.
- `alerts.asset_id references assets(asset_id)` should usually be `on delete restrict` or default `no action`, because deleting an asset should not silently erase operational incident history.
- Readings tables should omit `references assets(asset_id)` in the first migration because every telemetry message writes to a hypertable and foreign key checks sit directly on that hot path. This also makes bulk backfills and late-arriving telemetry easier to handle.

PostgreSQL docs distinguish `CASCADE` for dependent component rows from `RESTRICT`/`NO ACTION` for independent objects. They also note that foreign keys do not automatically create an index on the referencing columns, so add indexes where deletes, updates, or joins need them.

### 9. Use `timestamptz` consistently for event time and operational timestamps

Recommendation:

- Readings: `time timestamptz not null`, sourced from `MetricPayload.timestamp_utc`.
- Latest state: `last_seen_at timestamptz not null`, reflecting telemetry event time.
- Audit fields: `created_at timestamptz not null default now()` and `updated_at timestamptz not null`.
- Alerts: `opened_at timestamptz not null`, `resolved_at timestamptz`.

TimescaleDB recommends `timestamptz` rather than `timestamp` for hypertable time columns. PostgreSQL date/time docs define `current_timestamp`/`now()` as transaction-time values with time zone. Use telemetry event time for facts about the field device, and database time only for record creation/update bookkeeping.

### 10. Create hypertables explicitly and set the chunk interval intentionally

Recommendation: create readings tables as TimescaleDB hypertables partitioned by `time`.

For TimescaleDB 2.20 and later, the docs show native hypertable creation with `CREATE TABLE ... WITH (tsdb.hypertable, tsdb.partition_column='time')`. For compatibility with older self-hosted TimescaleDB releases, create a regular table and call `create_hypertable(..., if_not_exists => true)`.

Initial practical migration style for a physical hypertable:

```sql
create table smart_meter_readings (
  time timestamptz not null,
  asset_id text not null check (asset_id <> ''),
  internal_temperature real,
  voltage real,
  current real,
  active_power real,
  frequency real,
  total_kwh double precision
);

select create_hypertable('smart_meter_readings', by_range('time'), if_not_exists => true);
```

Chunk interval should be chosen from expected ingest volume. TimescaleDB's performance docs say the default chunk interval is 7 days and recommend sizing chunks so one active chunk, including indexes, is about 25% of main memory before processing. For early development, keep the default or use a simple 1-day/7-day interval, then tune from real ingest volume.

### 11. Treat compression/columnstore as a later retention optimization

Recommendation: do not enable TimescaleDB compression/columnstore in the first schema unless retention and backfill behavior are known.

When enabled later, segment by `asset_id` for per-asset history queries and order by `time desc`. TimescaleDB docs recommend segmenting compressed data by an identifier such as `device_id` when queries often filter by that identifier. For AmbaGrid, the storage identifier should be `asset_id`.

### 12. Use SQLx migrations owned by the Rust engine

Recommendation: put SQLx migrations under the Rust engine crate, for example:

```text
services/engine-rust/migrations/
```

Use SQLx because it supports plain SQL migration files while still integrating naturally with Rust code. That matters for TimescaleDB, where migrations need direct SQL for extensions, hypertables, indexes, and constraints.

SQLx migration filenames should follow its migration source format: `<VERSION>_<DESCRIPTION>.sql` where the version parses as a positive integer. The SQLx CLI can create migrations with `sqlx migrate add <name>` and run pending scripts with `sqlx migrate run`. The `migrate!` macro embeds migrations in the binary and defaults to `./migrations` relative to the crate root.

If using embedded migrations, add a stable Rust build script or equivalent setup so builds notice new migration files. SQLx docs call this out because adding migration files alone may not cause recompilation.

## Suggested Initial Schema Direction

This is the shape the first migration should aim for, pending implementation:

```text
assets
  asset_id text primary key
  site_id text not null
  asset_type text not null check (...)
  internal_temperature real
  last_seen_at timestamptz not null
  created_at timestamptz not null default now()
  updated_at timestamptz not null

smart_meter_state
  asset_id text primary key references assets(asset_id) on delete cascade
  reported_household_id text
  relay_closed boolean
  voltage real
  current real
  active_power real
  frequency real
  total_kwh double precision

smart_meter_readings hypertable(time)
  time timestamptz not null
  asset_id text not null check (asset_id <> '')
  internal_temperature real
  reported_household_id text
  relay_closed boolean
  voltage real
  current real
  active_power real
  frequency real
  total_kwh double precision

battery_bms_state
  asset_id text primary key references assets(asset_id) on delete cascade
  battery_soc_pct real check (battery_soc_pct between 0 and 100)

battery_bms_readings hypertable(time)
  time timestamptz not null
  asset_id text not null check (asset_id <> '')
  internal_temperature real
  battery_soc_pct real check (battery_soc_pct between 0 and 100)

solar_inverter_state
  asset_id text primary key references assets(asset_id) on delete cascade
  solar_irradiance real check (solar_irradiance >= 0)

solar_inverter_readings hypertable(time)
  time timestamptz not null
  asset_id text not null check (asset_id <> '')
  internal_temperature real
  solar_irradiance real check (solar_irradiance >= 0)

asset_readings view
  union all common columns from each type-specific readings hypertable

alerts
  alert_id uuid primary key
  asset_id text not null references assets(asset_id)
  site_id text not null
  severity text not null check (...)
  status text not null check (...)
  reason text not null
  opened_at timestamptz not null
  resolved_at timestamptz
```

## Key Conclusions

- Use `asset` for persisted domain objects and `device` only for wire/ingestion terminology.
- Keep latest operational state in ordinary PostgreSQL tables and historical telemetry in TimescaleDB hypertables.
- Normalize type-specific telemetry into `smart_meter_*`, `battery_bms_*`, and `solar_inverter_*` tables rather than using one wide nullable meter table.
- Use named `CHECK` constraints for evolving domain values before committing to PostgreSQL enum types.
- Use `timestamptz` for all telemetry and operational event times.
- Index latest-state tables for dashboards and stale-asset scans; index hypertables mainly around `(asset_id, time desc)`.
- Avoid hypertable uniqueness unless the unique key includes `time`.
- Cascade from `assets` to dependent latest-state rows, but avoid cascading deletes into alerts or long-term readings unless the product explicitly wants asset deletion to erase history.
- Use SQLx migrations in `services/engine-rust/migrations/` so TimescaleDB-specific SQL stays explicit and reviewable.
