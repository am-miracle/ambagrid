use crate::domain::{actor::ActorId, rules::ThresholdPolicy};

#[derive(Debug, Clone, PartialEq)]
pub struct Config {
    pub database_url: String,
    pub kafka_brokers: Vec<String>,
    pub kafka_security: KafkaSecurity,
    pub telemetry_topic: String,
    pub telemetry_dlq_topic: String,
    pub telemetry_group_id: String,
    pub alert_opened_topic: String,
    pub alert_resolved_topic: String,
    pub alert_resolve_actors: Vec<ActorId>,
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
        let telemetry_dlq_topic = env_string(
            &lookup,
            "TELEMETRY_DLQ_TOPIC",
            Some("telemetry.ingested.dlq"),
        )?;
        let telemetry_group_id = env_string(&lookup, "TELEMETRY_GROUP_ID", Some("engine-rust"))?;
        let alert_opened_topic = env_string(&lookup, "ALERT_OPENED_TOPIC", Some("alert.opened"))?;
        let alert_resolved_topic =
            env_string(&lookup, "ALERT_RESOLVED_TOPIC", Some("alert.resolved"))?;
        let alert_resolve_actors = env_csv(&lookup, "ALERT_RESOLVE_ACTORS", "")
            .into_iter()
            .map(ActorId::new)
            .collect::<Result<Vec<_>, _>>()
            .map_err(|err| ConfigError::Invalid(err.to_string()))?;

        if kafka_brokers.is_empty() {
            return Err(ConfigError::Invalid(
                "KAFKA_BROKERS must not be empty".to_string(),
            ));
        }

        let kafka_security = KafkaSecurity::from_lookup(&lookup)?;

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
            kafka_security,
            telemetry_topic,
            telemetry_dlq_topic,
            telemetry_group_id,
            alert_opened_topic,
            alert_resolved_topic,
            alert_resolve_actors,
            threshold_policy,
        })
    }
}

/// Passed through to librdkafka as `security.protocol`, `sasl.*` and
/// `ssl.ca.location`/`ssl.ca.pem`.
#[derive(Clone, PartialEq, Eq)]
pub struct KafkaSecurity {
    pub protocol: SecurityProtocol,
    pub sasl: Option<SaslCredentials>,
    pub ssl_ca_location: Option<String>,
    pub ssl_ca_pem: Option<String>,
}

#[derive(Clone, PartialEq, Eq)]
pub struct SaslCredentials {
    pub mechanism: SaslMechanism,
    pub username: String,
    pub password: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SecurityProtocol {
    Plaintext,
    Ssl,
    SaslPlaintext,
    SaslSsl,
}

impl SecurityProtocol {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Plaintext => "plaintext",
            Self::Ssl => "ssl",
            Self::SaslPlaintext => "sasl_plaintext",
            Self::SaslSsl => "sasl_ssl",
        }
    }

    fn uses_sasl(self) -> bool {
        matches!(self, Self::SaslPlaintext | Self::SaslSsl)
    }

    fn parse(value: &str) -> Result<Self, ConfigError> {
        match value.to_lowercase().as_str() {
            "plaintext" => Ok(Self::Plaintext),
            "ssl" => Ok(Self::Ssl),
            "sasl_plaintext" => Ok(Self::SaslPlaintext),
            "sasl_ssl" => Ok(Self::SaslSsl),
            _ => Err(ConfigError::Invalid(format!(
                "KAFKA_SECURITY_PROTOCOL must be one of PLAINTEXT, SSL, SASL_PLAINTEXT, SASL_SSL: {value:?}"
            ))),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SaslMechanism {
    Plain,
    ScramSha256,
    ScramSha512,
}

impl SaslMechanism {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Plain => "PLAIN",
            Self::ScramSha256 => "SCRAM-SHA-256",
            Self::ScramSha512 => "SCRAM-SHA-512",
        }
    }

    fn parse(value: &str) -> Result<Self, ConfigError> {
        match value.to_uppercase().as_str() {
            "PLAIN" => Ok(Self::Plain),
            "SCRAM-SHA-256" => Ok(Self::ScramSha256),
            "SCRAM-SHA-512" => Ok(Self::ScramSha512),
            _ => Err(ConfigError::Invalid(format!(
                "KAFKA_SASL_MECHANISM must be one of PLAIN, SCRAM-SHA-256, SCRAM-SHA-512: {value:?}"
            ))),
        }
    }
}

impl KafkaSecurity {
    fn from_lookup(lookup: &impl Fn(&str) -> Option<String>) -> Result<Self, ConfigError> {
        let protocol = SecurityProtocol::parse(&env_string(
            lookup,
            "KAFKA_SECURITY_PROTOCOL",
            Some("plaintext"),
        )?)?;
        let ssl_ca_location = env_string(lookup, "KAFKA_SSL_CA_LOCATION", None).ok();
        // Single-line secret stores often hold the PEM with literal "\n" escapes.
        let ssl_ca_pem = env_string(lookup, "KAFKA_SSL_CA_PEM", None)
            .ok()
            .map(|pem| pem.replace("\\n", "\n"));
        if ssl_ca_location.is_some() && ssl_ca_pem.is_some() {
            return Err(ConfigError::Invalid(
                "set only one of KAFKA_SSL_CA_LOCATION and KAFKA_SSL_CA_PEM".to_string(),
            ));
        }
        let sasl = if protocol.uses_sasl() {
            Some(SaslCredentials {
                mechanism: SaslMechanism::parse(&env_string(
                    lookup,
                    "KAFKA_SASL_MECHANISM",
                    None,
                )?)?,
                username: env_string(lookup, "KAFKA_SASL_USERNAME", None)?,
                password: env_string(lookup, "KAFKA_SASL_PASSWORD", None)?,
            })
        } else {
            None
        };

        Ok(Self {
            protocol,
            sasl,
            ssl_ca_location,
            ssl_ca_pem,
        })
    }
}

impl Default for KafkaSecurity {
    fn default() -> Self {
        Self {
            protocol: SecurityProtocol::Plaintext,
            sasl: None,
            ssl_ca_location: None,
            ssl_ca_pem: None,
        }
    }
}

impl std::fmt::Debug for KafkaSecurity {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("KafkaSecurity")
            .field("protocol", &self.protocol)
            .field("sasl", &self.sasl)
            .field("ssl_ca_location", &self.ssl_ca_location)
            .field("ssl_ca_pem", &self.ssl_ca_pem.as_ref().map(|_| "<set>"))
            .finish()
    }
}

impl std::fmt::Debug for SaslCredentials {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SaslCredentials")
            .field("mechanism", &self.mechanism)
            .field("username", &self.username)
            .field("password", &"<redacted>")
            .finish()
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
    fn from_lookup_reads_sasl_ssl_settings() {
        let mut vars = required_vars();
        vars.insert("KAFKA_SECURITY_PROTOCOL", "SASL_SSL");
        vars.insert("KAFKA_SASL_MECHANISM", "scram-sha-256");
        vars.insert("KAFKA_SASL_USERNAME", "avnadmin");
        vars.insert("KAFKA_SASL_PASSWORD", "secret");
        vars.insert("KAFKA_SSL_CA_LOCATION", "/etc/kafka/ca.pem");

        let cfg = Config::from_lookup(lookup_fn(vars)).unwrap();

        assert_eq!(
            cfg.kafka_security,
            KafkaSecurity {
                protocol: SecurityProtocol::SaslSsl,
                sasl: Some(SaslCredentials {
                    mechanism: SaslMechanism::ScramSha256,
                    username: "avnadmin".to_string(),
                    password: "secret".to_string(),
                }),
                ssl_ca_location: Some("/etc/kafka/ca.pem".to_string()),
                ssl_ca_pem: None,
            }
        );
        assert!(!format!("{cfg:?}").contains("secret"));
    }

    #[test]
    fn from_lookup_reads_inline_ca_pem() {
        let mut vars = required_vars();
        vars.insert("KAFKA_SSL_CA_PEM", "-----BEGIN CERTIFICATE-----\\nabc\n");

        let cfg = Config::from_lookup(lookup_fn(vars)).unwrap();

        assert_eq!(
            cfg.kafka_security.ssl_ca_pem.as_deref(),
            Some("-----BEGIN CERTIFICATE-----\nabc")
        );
    }

    #[test]
    fn from_lookup_rejects_both_ca_location_and_pem() {
        let mut vars = required_vars();
        vars.insert("KAFKA_SSL_CA_LOCATION", "/etc/kafka/ca.pem");
        vars.insert("KAFKA_SSL_CA_PEM", "-----BEGIN CERTIFICATE-----");

        assert!(Config::from_lookup(lookup_fn(vars)).is_err());
    }

    #[test]
    fn from_lookup_requires_sasl_credentials_for_sasl_protocols() {
        let mut vars = required_vars();
        vars.insert("KAFKA_SECURITY_PROTOCOL", "SASL_SSL");
        vars.insert("KAFKA_SASL_MECHANISM", "SCRAM-SHA-256");

        let err = Config::from_lookup(lookup_fn(vars)).unwrap_err();
        assert_eq!(err, ConfigError::Missing("KAFKA_SASL_USERNAME"));
    }

    #[test]
    fn from_lookup_rejects_unknown_protocol_and_mechanism() {
        let mut vars = required_vars();
        vars.insert("KAFKA_SECURITY_PROTOCOL", "tls");
        assert!(Config::from_lookup(lookup_fn(vars)).is_err());

        let mut vars = required_vars();
        vars.insert("KAFKA_SECURITY_PROTOCOL", "SASL_SSL");
        vars.insert("KAFKA_SASL_MECHANISM", "GSSAPI");
        vars.insert("KAFKA_SASL_USERNAME", "u");
        vars.insert("KAFKA_SASL_PASSWORD", "p");
        assert!(Config::from_lookup(lookup_fn(vars)).is_err());
    }

    #[test]
    fn from_lookup_rejects_reserved_system_actor() {
        let mut vars = required_vars();
        vars.insert("ALERT_RESOLVE_ACTORS", "system");

        let err = Config::from_lookup(lookup_fn(vars)).unwrap_err();

        assert_eq!(
            err,
            ConfigError::Invalid("actor_id must not be the reserved system actor".to_string())
        );
    }

    #[test]
    fn from_lookup_applies_defaults_when_optional_vars_are_unset() {
        let cfg = Config::from_lookup(lookup_fn(required_vars())).unwrap();

        assert_eq!(cfg.kafka_brokers, vec!["localhost:9092"]);
        assert_eq!(cfg.kafka_security, KafkaSecurity::default());
        assert_eq!(cfg.telemetry_topic, "telemetry.ingested");
        assert_eq!(cfg.telemetry_dlq_topic, "telemetry.ingested.dlq");
        assert_eq!(cfg.telemetry_group_id, "engine-rust");
        assert_eq!(cfg.alert_opened_topic, "alert.opened");
        assert_eq!(cfg.alert_resolved_topic, "alert.resolved");
        assert!(cfg.alert_resolve_actors.is_empty());
        assert_eq!(cfg.threshold_policy, ThresholdPolicy::default());
    }

    #[test]
    fn from_lookup_wires_each_env_var_to_the_matching_threshold_field() {
        let mut vars = required_vars();
        vars.insert("TELEMETRY_DLQ_TOPIC", "telemetry.custom.dlq");
        vars.insert("ALERT_OPENED_TOPIC", "custom.alert.opened");
        vars.insert("ALERT_RESOLVED_TOPIC", "custom.alert.resolved");
        vars.insert("ALERT_RESOLVE_ACTORS", " actor-0101,actor-0102 ");
        vars.insert("SMART_METER_CRITICAL_TEMP_C", "80.0");
        vars.insert("BATTERY_BMS_CRITICAL_TEMP_C", "50.0");
        vars.insert("SOLAR_INVERTER_CRITICAL_TEMP_C", "90.0");
        vars.insert("ALERT_RECOVERY_MARGIN_C", "10.0");

        let cfg = Config::from_lookup(lookup_fn(vars)).unwrap();

        assert_eq!(cfg.telemetry_dlq_topic, "telemetry.custom.dlq");
        assert_eq!(cfg.alert_opened_topic, "custom.alert.opened");
        assert_eq!(cfg.alert_resolved_topic, "custom.alert.resolved");
        assert_eq!(
            cfg.alert_resolve_actors
                .iter()
                .map(ActorId::as_str)
                .collect::<Vec<_>>(),
            vec!["actor-0101", "actor-0102"]
        );
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
