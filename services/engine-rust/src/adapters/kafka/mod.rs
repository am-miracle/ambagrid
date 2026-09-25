use rdkafka::ClientConfig;

use crate::config::KafkaSecurity;

pub mod consumer;
pub mod producer;

fn client_config(brokers: &[String], security: &KafkaSecurity) -> ClientConfig {
    let mut config = ClientConfig::new();
    config
        .set("bootstrap.servers", brokers.join(","))
        .set("security.protocol", security.protocol.as_str());
    if let Some(sasl) = &security.sasl {
        config
            .set("sasl.mechanism", sasl.mechanism.as_str())
            .set("sasl.username", &sasl.username)
            .set("sasl.password", &sasl.password);
    }
    if let Some(ca_location) = &security.ssl_ca_location {
        config.set("ssl.ca.location", ca_location);
    }
    if let Some(ca_pem) = &security.ssl_ca_pem {
        config.set("ssl.ca.pem", ca_pem);
    }
    config
}

mod proto {
    include!(concat!(env!("OUT_DIR"), "/ambagrid.alerts.rs"));
}

#[cfg(test)]
mod tests {
    use rdkafka::producer::FutureProducer;

    use super::*;
    use crate::config::{SaslCredentials, SaslMechanism, SecurityProtocol};

    // librdkafka rejects sasl_ssl/SCRAM at client creation when built without SSL.
    #[test]
    fn client_config_supports_sasl_ssl_scram() {
        let security = KafkaSecurity {
            protocol: SecurityProtocol::SaslSsl,
            sasl: Some(SaslCredentials {
                mechanism: SaslMechanism::ScramSha256,
                username: "user".to_string(),
                password: "pass".to_string(),
            }),
            ssl_ca_location: None,
            ssl_ca_pem: None,
        };

        let producer: Result<FutureProducer, _> =
            client_config(&["localhost:9092".to_string()], &security).create();

        assert!(producer.is_ok(), "{:?}", producer.err());
    }
}
