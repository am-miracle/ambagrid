use std::sync::LazyLock;

use prometheus::{Counter, IntCounter, Registry};

static REGISTRY: LazyLock<Registry> = LazyLock::new(Registry::new);

pub static OUTBOX_CLAIMED_TOTAL: LazyLock<IntCounter> = LazyLock::new(|| {
    let c = IntCounter::new("outbox_claimed_total", "Outbox events claimed").unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
pub static OUTBOX_PUBLISHED_TOTAL: LazyLock<IntCounter> = LazyLock::new(|| {
    let c = IntCounter::new("outbox_published_total", "Outbox events published").unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
pub static OUTBOX_PUBLISH_FAILED_TOTAL: LazyLock<IntCounter> = LazyLock::new(|| {
    let c = IntCounter::new("outbox_publish_failed_total", "Outbox publish failures").unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
pub static OUTBOX_MARK_FAILED_TOTAL: LazyLock<IntCounter> = LazyLock::new(|| {
    let c = IntCounter::new("outbox_mark_failed_total", "Outbox mark_published failures").unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
pub static OUTBOX_CLAIM_LOST_TOTAL: LazyLock<IntCounter> = LazyLock::new(|| {
    let c = IntCounter::new(
        "outbox_claim_lost_total",
        "Outbox claim lost (duplicate-safe)",
    )
    .unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
pub static DLQ_PARKED_TOTAL: LazyLock<Counter> = LazyLock::new(|| {
    let c = Counter::new("dlq_parked_total", "Messages parked to DLQ").unwrap();
    REGISTRY.register(Box::new(c.clone())).unwrap();
    c
});
