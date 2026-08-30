pub mod consumer;
pub mod producer;

mod proto {
    include!(concat!(env!("OUT_DIR"), "/ambagrid.alerts.rs"));
}
