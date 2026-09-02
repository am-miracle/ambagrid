pub mod decode;

pub mod proto {
    include!(concat!(env!("OUT_DIR"), "/ambagrid.telemetry.rs"));
}
