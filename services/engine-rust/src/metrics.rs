use std::sync::Arc;

use prometheus::{IntCounter, Registry, proto::MetricFamily};

#[derive(Clone)]
pub struct Metrics {
    inner: Arc<MetricsInner>,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct MetricsSnapshot {
    pub outbox_claimed: u64,
    pub outbox_published: u64,
    pub outbox_publish_failed: u64,
    pub outbox_mark_failed: u64,
    pub outbox_claim_lost: u64,
    pub dlq_parked: u64,
}

struct MetricsInner {
    registry: Registry,
    outbox_claimed_total: IntCounter,
    outbox_published_total: IntCounter,
    outbox_publish_failed_total: IntCounter,
    outbox_mark_failed_total: IntCounter,
    outbox_claim_lost_total: IntCounter,
    dlq_parked_total: IntCounter,
}

impl Metrics {
    pub fn new() -> Result<Self, prometheus::Error> {
        let registry = Registry::new();
        let outbox_claimed_total =
            IntCounter::new("outbox_claimed_total", "Outbox events claimed")?;
        let outbox_published_total =
            IntCounter::new("outbox_published_total", "Outbox events published")?;
        let outbox_publish_failed_total =
            IntCounter::new("outbox_publish_failed_total", "Outbox publish failures")?;
        let outbox_mark_failed_total =
            IntCounter::new("outbox_mark_failed_total", "Outbox mark_published failures")?;
        let outbox_claim_lost_total = IntCounter::new(
            "outbox_claim_lost_total",
            "Outbox claim lost (duplicate-safe)",
        )?;
        let dlq_parked_total = IntCounter::new("dlq_parked_total", "Messages parked to DLQ")?;

        for counter in [
            &outbox_claimed_total,
            &outbox_published_total,
            &outbox_publish_failed_total,
            &outbox_mark_failed_total,
            &outbox_claim_lost_total,
            &dlq_parked_total,
        ] {
            registry.register(Box::new(counter.clone()))?;
        }

        Ok(Self {
            inner: Arc::new(MetricsInner {
                registry,
                outbox_claimed_total,
                outbox_published_total,
                outbox_publish_failed_total,
                outbox_mark_failed_total,
                outbox_claim_lost_total,
                dlq_parked_total,
            }),
        })
    }

    pub fn record_outbox_claimed(&self, count: usize) {
        self.inner.outbox_claimed_total.inc_by(count as u64);
    }

    pub fn record_outbox_published(&self) {
        self.inner.outbox_published_total.inc();
    }

    pub fn record_outbox_publish_failed(&self) {
        self.inner.outbox_publish_failed_total.inc();
    }

    pub fn record_outbox_mark_failed(&self) {
        self.inner.outbox_mark_failed_total.inc();
    }

    pub fn record_outbox_claim_lost(&self) {
        self.inner.outbox_claim_lost_total.inc();
    }

    pub fn record_dlq_parked(&self) {
        self.inner.dlq_parked_total.inc();
    }

    pub fn gather(&self) -> Vec<MetricFamily> {
        self.inner.registry.gather()
    }

    pub fn snapshot(&self) -> MetricsSnapshot {
        MetricsSnapshot {
            outbox_claimed: self.inner.outbox_claimed_total.get(),
            outbox_published: self.inner.outbox_published_total.get(),
            outbox_publish_failed: self.inner.outbox_publish_failed_total.get(),
            outbox_mark_failed: self.inner.outbox_mark_failed_total.get(),
            outbox_claim_lost: self.inner.outbox_claim_lost_total.get(),
            dlq_parked: self.inner.dlq_parked_total.get(),
        }
    }
}

impl Default for Metrics {
    fn default() -> Self {
        Self::new().expect("static metric names and descriptions must be valid")
    }
}
