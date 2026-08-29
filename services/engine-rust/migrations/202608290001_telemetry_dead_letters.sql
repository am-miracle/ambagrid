CREATE TABLE telemetry_dead_letters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kafka_topic text NOT NULL,
    kafka_partition int NOT NULL,
    kafka_offset bigint NOT NULL,
    payload bytea NOT NULL,
    error text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT telemetry_dead_letters_offset_unique
        UNIQUE (kafka_topic, kafka_partition, kafka_offset)
);
