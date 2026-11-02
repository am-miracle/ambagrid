ALTER TABLE battery_bms_state
    ADD COLUMN voltage real,
    ADD COLUMN current real;

ALTER TABLE battery_bms_readings
    ADD COLUMN voltage real,
    ADD COLUMN current real;
