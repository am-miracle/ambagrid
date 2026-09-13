// Reads asset state from Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"api-go/internal/domain"
	"api-go/internal/page"
)

// Each asset has at most one matching type-specific state row.
const assetSelect = `
SELECT a.asset_id,
       a.site_id,
       a.asset_type,
       a.internal_temperature,
       a.last_seen_at,
       a.updated_at,
       m.reported_household_id,
       m.relay_closed,
       m.voltage,
       m.current,
       m.active_power,
       m.frequency,
       m.total_kwh,
       m.updated_at,
       b.battery_soc_pct,
       b.updated_at,
       s.solar_irradiance,
       s.updated_at
FROM assets a
LEFT JOIN smart_meter_state m ON m.asset_id = a.asset_id
LEFT JOIN battery_bms_state b ON b.asset_id = a.asset_id
LEFT JOIN solar_inverter_state s ON s.asset_id = a.asset_id`

const getAssetSQL = assetSelect + `
WHERE a.asset_id = $1`

func (s *Store) ListAssets(ctx context.Context, query domain.AssetQuery) (page.Page[domain.Asset], error) {
	var afterAssetID *string
	if query.Cursor != "" {
		fields, err := page.Decode(query.Cursor, 1)
		if err != nil {
			return page.Page[domain.Asset]{}, err
		}
		afterAssetID = &fields[0]
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	sql, args := buildListAssetsQuery(query.Filter, afterAssetID, query.Limit+1)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return page.Page[domain.Asset]{}, fmt.Errorf("query assets: %w", err)
	}
	defer rows.Close()

	assets := make([]domain.Asset, 0, query.Limit+1)
	for rows.Next() {
		asset, err := scanAsset(rows)
		if err != nil {
			return page.Page[domain.Asset]{}, err
		}
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return page.Page[domain.Asset]{}, fmt.Errorf("read assets: %w", err)
	}

	return page.Build(assets, query.Limit, func(a domain.Asset) string {
		return page.Encode(a.AssetID)
	}), nil
}

// Asset ID provides stable, indexed keyset ordering.
func buildListAssetsQuery(filter domain.AssetFilter, afterAssetID *string, limit int) (string, []any) {
	b := newPredicates()

	if filter.SiteID != nil {
		b.add("a.site_id = ", *filter.SiteID)
	}
	if filter.AssetType != nil {
		b.add("a.asset_type = ", string(*filter.AssetType))
	}
	if afterAssetID != nil {
		b.add("a.asset_id > ", *afterAssetID)
	}

	return b.finish(assetSelect, "ORDER BY a.asset_id", limit)
}

func (s *Store) GetAsset(ctx context.Context, assetID string) (domain.Asset, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	asset, err := scanAsset(s.pool.QueryRow(ctx, getAssetSQL, assetID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Asset{}, fmt.Errorf("asset %q: %w", assetID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Asset{}, err
	}
	return asset, nil
}

func scanAsset(row scanner) (domain.Asset, error) {
	var (
		asset      domain.Asset
		assetType  string
		meter      domain.SmartMeterState
		meterAt    *time.Time
		battery    domain.BatteryBMSState
		batteryAt  *time.Time
		inverter   domain.SolarInverterState
		inverterAt *time.Time
	)

	err := row.Scan(
		&asset.AssetID,
		&asset.SiteID,
		&assetType,
		&asset.InternalTemperature,
		&asset.LastSeenAt,
		&asset.UpdatedAt,
		&meter.ReportedHouseholdID,
		&meter.RelayClosed,
		&meter.Voltage,
		&meter.Current,
		&meter.ActivePower,
		&meter.Frequency,
		&meter.TotalKWh,
		&meterAt,
		&battery.BatterySOCPct,
		&batteryAt,
		&inverter.SolarIrradiance,
		&inverterAt,
	)
	if err != nil {
		return domain.Asset{}, err
	}

	asset.AssetType = domain.AssetType(assetType)

	// Leave the state block nil until that asset type has reported metrics.
	switch {
	case asset.AssetType == domain.AssetTypeSmartMeter && meterAt != nil:
		meter.UpdatedAt = *meterAt
		asset.SmartMeter = &meter
	case asset.AssetType == domain.AssetTypeBatteryBMS && batteryAt != nil:
		battery.UpdatedAt = *batteryAt
		asset.BatteryBMS = &battery
	case asset.AssetType == domain.AssetTypeSolarInverter && inverterAt != nil:
		inverter.UpdatedAt = *inverterAt
		asset.SolarInverter = &inverter
	}

	return asset, nil
}
