use crate::domain::rules::ThresholdPolicy;

#[derive(Debug, Clone, PartialEq)]
pub struct Config {
    pub database_url: String,
    pub kafka_brokers: Vec<String>,
    pub telemetry_topic: String,
    pub telemetry_group_id: String,
    pub alert_opened_topic: String,
    pub alert_resolved_topic: String,
    pub threshold_policy: ThresholdPolicy,
}

impl Config {
    pub fn from_env() -> Result<Self, ConfigError> {
        Self::from_lookup(|key| std::env::var(key).ok())
    }

    fn from_lookup(lookup: impl Fn(&str) -> Option<String>) -> Result<Self, ConfigError> {
        let database_url = env_string(&lookup, "DATABASE_URL", None)?;
        let kafka_brokers = env_csv(&lookup, "KAFKA_BROKERS", "localhost:9092");
        let telemetry_topic = env_string(&lookup, "TELEMETRY_TOPIC", Some("telemetry.ingested"))?;
        let telemetry_group_id = env_string(&lookup, "TELEMETRY_GROUP_ID", Some("engine-rust"))?;
        let alert_opened_topic = env_string(&lookup, "ALERT_OPENED_TOPIC", Some("alert.opened"))?;
        let alert_resolved_topic =
            env_string(&lookup, "ALERT_RESOLVED_TOPIC", Some("alert.resolved"))?;

        if kafka_brokers.is_empty() {
            return Err(ConfigError::Invalid(
                "KAFKA_BROKERS must not be empty".to_string(),
            ));
        }

        let defaults = ThresholdPolicy::default();
        let threshold_policy = ThresholdPolicy {
            smart_meter_critical_c: env_f32(
                &lookup,
                "SMART_METER_CRITICAL_TEMP_C",
                defaults.smart_meter_critical_c,
            )?,
            battery_bms_critical_c: env_f32(
                &lookup,
                "BATTERY_BMS_CRITICAL_TEMP_C",
                defaults.battery_bms_critical_c,
            )?,
            solar_inverter_critical_c: env_f32(
                &lookup,
                "SOLAR_INVERTER_CRITICAL_TEMP_C",
                defaults.solar_inverter_critical_c,
            )?,
            recovery_margin_c: env_f32(
                &lookup,
                "ALERT_RECOVERY_MARGIN_C",
                defaults.recovery_margin_c,
            )?,
        };

        Ok(Self {
            database_url,
            kafka_brokers,
            telemetry_topic,
            telemetry_group_id,
            alert_opened_topic,
            alert_resolved_topic,
            threshold_policy,
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum ConfigError {
    #[error("{0} must be set")]
    Missing(&'static str),
    #[error("{0}")]
    Invalid(String),
}

fn env_string(
    lookup: &impl Fn(&str) -> Option<String>,
    key: &'static str,
    fallback: Option<&str>,
) -> Result<String, ConfigError> {
    let value = lookup(key).unwrap_or_default();
    let trimmed = value.trim();
    if trimmed.is_empty() {
        fallback
            .map(str::to_string)
            .ok_or(ConfigError::Missing(key))
    } else {
        Ok(trimmed.to_string())
    }
}

fn env_csv(
    lookup: &impl Fn(&str) -> Option<String>,
    key: &'static str,
    fallback: &str,
) -> Vec<String> {
    let raw = lookup(key).unwrap_or_else(|| fallback.to_string());
    raw.split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(str::to_string)
        .collect()
}

fn env_f32(
    lookup: &impl Fn(&str) -> Option<String>,
    key: &'static str,
    fallback: f32,
) -> Result<f32, ConfigError> {
    let value = lookup(key).unwrap_or_default();
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return Ok(fallback);
    }
    trimmed
        .parse()
        .map_err(|_| ConfigError::Invalid(format!("{key} must be a number: {trimmed:?}")))
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use super::*;

    fn lookup_fn(vars: HashMap<&'static str, &'static str>) -> impl Fn(&str) -> Option<String> {
        move |key| vars.get(key).map(|value| value.to_string())
    }

    #[test]
    fn env_csv_trims_empty_values() {
        let lookup = lookup_fn(HashMap::from([(
            "KAFKA_BROKERS",
            " redpanda:9092, ,localhost:9092 ",
        )]));

        assert_eq!(
            env_csv(&lookup, "KAFKA_BROKERS", "fallback:9092"),
            vec!["redpanda:9092", "localhost:9092"]
        );
    }

    #[test]
    fn env_f32_falls_back_when_unset() {
        let lookup = lookup_fn(HashMap::new());

        assert_eq!(
            env_f32(&lookup, "SMART_METER_CRITICAL_TEMP_C", 70.0).unwrap(),
            70.0
        );
    }

    #[test]
    fn env_f32_parses_a_set_value() {
        let lookup = lookup_fn(HashMap::from([("SMART_METER_CRITICAL_TEMP_C", " 68.5 ")]));

        assert_eq!(
            env_f32(&lookup, "SMART_METER_CRITICAL_TEMP_C", 70.0).unwrap(),
            68.5
        );
    }

    #[test]
    fn env_f32_rejects_a_non_numeric_value() {
        let lookup = lookup_fn(HashMap::from([("SMART_METER_CRITICAL_TEMP_C", "hot")]));

        assert!(env_f32(&lookup, "SMART_METER_CRITICAL_TEMP_C", 70.0).is_err());
    }

    fn required_vars() -> HashMap<&'static str, &'static str> {
        HashMap::from([("DATABASE_URL", "postgres://localhost/ambagrid")])
    }

    #[test]
    fn from_lookup_fails_when_database_url_is_missing() {
        let err = Config::from_lookup(lookup_fn(HashMap::new())).unwrap_err();
        assert_eq!(err, ConfigError::Missing("DATABASE_URL"));
    }

    #[test]
    fn from_lookup_fails_when_kafka_brokers_is_explicitly_empty() {
        let mut vars = required_vars();
        vars.insert("KAFKA_BROKERS", " , ");

        let err = Config::from_lookup(lookup_fn(vars)).unwrap_err();
        assert_eq!(
            err,
            ConfigError::Invalid("KAFKA_BROKERS must not be empty".to_string())
        );
    }

    #[test]
    fn from_lookup_applies_defaults_when_optional_vars_are_unset() {
        let cfg = Config::from_lookup(lookup_fn(required_vars())).unwrap();

        assert_eq!(cfg.kafka_brokers, vec!["localhost:9092"]);
        assert_eq!(cfg.telemetry_topic, "telemetry.ingested");
        assert_eq!(cfg.telemetry_group_id, "engine-rust");
        assert_eq!(cfg.alert_opened_topic, "alert.opened");
        assert_eq!(cfg.alert_resolved_topic, "alert.resolved");
        assert_eq!(cfg.threshold_policy, ThresholdPolicy::default());
    }

    #[test]
    fn from_lookup_wires_each_env_var_to_the_matching_threshold_field() {
        let mut vars = required_vars();
        vars.insert("ALERT_OPENED_TOPIC", "custom.alert.opened");
        vars.insert("ALERT_RESOLVED_TOPIC", "custom.alert.resolved");
        vars.insert("SMART_METER_CRITICAL_TEMP_C", "80.0");
        vars.insert("BATTERY_BMS_CRITICAL_TEMP_C", "50.0");
        vars.insert("SOLAR_INVERTER_CRITICAL_TEMP_C", "90.0");
        vars.insert("ALERT_RECOVERY_MARGIN_C", "10.0");

        let cfg = Config::from_lookup(lookup_fn(vars)).unwrap();

        assert_eq!(cfg.alert_opened_topic, "custom.alert.opened");
        assert_eq!(cfg.alert_resolved_topic, "custom.alert.resolved");
        assert_eq!(
            cfg.threshold_policy,
            ThresholdPolicy {
                smart_meter_critical_c: 80.0,
                battery_bms_critical_c: 50.0,
                solar_inverter_critical_c: 90.0,
                recovery_margin_c: 10.0,
            }
        );
    }
}
