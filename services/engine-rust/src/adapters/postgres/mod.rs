pub mod ingest_repo;
pub mod outbox_repo;

pub use ingest_repo::PostgresIngestRepository;
pub use outbox_repo::PostgresOutboxRepository;
