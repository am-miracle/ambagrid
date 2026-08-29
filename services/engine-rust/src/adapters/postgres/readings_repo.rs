use sqlx::{PgPool, query};

use crate::{
    domain::asset::{AssetState, Reading},
    ports::{PortError, ReadingsSink},
};

#[derive(Debug, Clone)]
pub struct PostgresReadingsRepository {
    pool: PgPool,
}

impl PostgresReadingsRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

impl ReadingsSink for PostgresReadingsRepository {
    async fn append_reading(&self, reading: &Reading) -> Result<(), PortError> {
        match &reading.state {
            AssetState::SmartMeter(state) => {
                query(
                    r#"
                    INSERT INTO smart_meter_readings (
                        time, asset_id, internal_temperature, reported_household_id,
                        relay_closed, voltage, current, active_power, frequency, total_kwh
                    )
                    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
                    "#,
                )
                .bind(reading.observed_at)
                .bind(&reading.asset.asset_id)
                .bind(reading.asset.internal_temperature)
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
                    INSERT INTO battery_bms_readings (
                        time, asset_id, internal_temperature, battery_soc_pct
                    )
                    VALUES ($1, $2, $3, $4)
                    "#,
                )
                .bind(reading.observed_at)
                .bind(&reading.asset.asset_id)
                .bind(reading.asset.internal_temperature)
                .bind(state.battery_soc_pct)
                .execute(&self.pool)
                .await
                .map_err(PortError::storage)?;
            }
            AssetState::SolarInverter(state) => {
                query(
                    r#"
                    INSERT INTO solar_inverter_readings (
                        time, asset_id, internal_temperature, solar_irradiance
                    )
                    VALUES ($1, $2, $3, $4)
                    "#,
                )
                .bind(reading.observed_at)
                .bind(&reading.asset.asset_id)
                .bind(reading.asset.internal_temperature)
                .bind(state.solar_irradiance)
                .execute(&self.pool)
                .await
                .map_err(PortError::storage)?;
            }
        }

        Ok(())
    }
}
