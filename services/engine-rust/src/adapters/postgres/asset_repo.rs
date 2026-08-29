use sqlx::{PgPool, query};

use crate::{
    domain::asset::{Asset, AssetState, Reading},
    ports::{AssetRepository, PortError},
};

#[derive(Debug, Clone)]
pub struct PostgresAssetRepository {
    pool: PgPool,
}

impl PostgresAssetRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl AssetRepository for PostgresAssetRepository {
    async fn upsert_asset(&self, asset: &Asset) -> Result<(), PortError> {
        query(
            r#"
            INSERT INTO assets (
                asset_id, site_id, asset_type, internal_temperature, last_seen_at
            )
            VALUES ($1, $2, $3, $4, $5)
            ON CONFLICT (asset_id) DO UPDATE SET
                site_id = EXCLUDED.site_id,
                asset_type = EXCLUDED.asset_type,
                internal_temperature = EXCLUDED.internal_temperature,
                last_seen_at = EXCLUDED.last_seen_at
            "#,
        )
        .bind(&asset.asset_id)
        .bind(&asset.site_id)
        .bind(asset.asset_type.as_str())
        .bind(asset.internal_temperature)
        .bind(asset.last_seen_at)
        .execute(&self.pool)
        .await
        .map_err(PortError::storage)?;

        Ok(())
    }

    async fn upsert_state(&self, reading: &Reading) -> Result<(), PortError> {
        match &reading.state {
            AssetState::SmartMeter(state) => {
                query(
                    r#"
                    INSERT INTO smart_meter_state (
                        asset_id, reported_household_id, relay_closed, voltage, current,
                        active_power, frequency, total_kwh
                    )
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
                    ON CONFLICT (asset_id) DO UPDATE SET
                        reported_household_id = EXCLUDED.reported_household_id,
                        relay_closed = EXCLUDED.relay_closed,
                        voltage = EXCLUDED.voltage,
                        current = EXCLUDED.current,
                        active_power = EXCLUDED.active_power,
                        frequency = EXCLUDED.frequency,
                        total_kwh = EXCLUDED.total_kwh
                    "#,
                )
                .bind(&reading.asset.asset_id)
                .bind(&state.reported_household_id)
                .bind(state.relay_closed)
                .bind(state.voltage)
                .bind(state.current)
                .bind(state.active_power)
                .bind(state.frequency)
                .bind(state.total_kwh)
                .execute(&self.pool)
                .await
                .map_err(PortError::storage)?;
            }
            AssetState::BatteryBms(state) => {
                query(
                    r#"
                    INSERT INTO battery_bms_state (asset_id, battery_soc_pct)
                    VALUES ($1, $2)
                    ON CONFLICT (asset_id) DO UPDATE SET
                        battery_soc_pct = EXCLUDED.battery_soc_pct
                    "#,
                )
                .bind(&reading.asset.asset_id)
                .bind(state.battery_soc_pct)
                .execute(&self.pool)
                .await
                .map_err(PortError::storage)?;
            }
            AssetState::SolarInverter(state) => {
                query(
                    r#"
                    INSERT INTO solar_inverter_state (asset_id, solar_irradiance)
                    VALUES ($1, $2)
                    ON CONFLICT (asset_id) DO UPDATE SET
                        solar_irradiance = EXCLUDED.solar_irradiance
                    "#,
                )
                .bind(&reading.asset.asset_id)
                .bind(state.solar_irradiance)
                .execute(&self.pool)
                .await
                .map_err(PortError::storage)?;
            }
        }

        Ok(())
    }
}
