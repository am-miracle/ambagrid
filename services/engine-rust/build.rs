fn main() -> Result<(), Box<dyn std::error::Error>> {
    println!("cargo:rerun-if-changed=migrations");
    println!("cargo:rerun-if-changed=../../proto/telemetry.proto");
    println!("cargo:rerun-if-changed=../../proto/alerts.proto");
    println!("cargo:rerun-if-changed=../../proto/meter_commands.proto");
    println!("cargo:rerun-if-changed=../../proto/revenue.proto");

    prost_build::Config::new().compile_protos(
        &[
            "../../proto/telemetry.proto",
            "../../proto/alerts.proto",
            "../../proto/meter_commands.proto",
            "../../proto/revenue.proto",
        ],
        &["../../proto"],
    )?;

    Ok(())
}
