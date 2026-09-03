CHANGELOG_FILE := CHANGELOG.md
CHANGELOG_CHECK_FILE := /tmp/ambagrid_CHANGELOG.md
DATABASE_URL ?= postgres://ambagrid_admin:ambagrid_secure_pass@localhost:5432/ambagrid_operational

KAFKA_BROKERS ?= localhost:9092
TELEMETRY_PARTITIONS ?= 12

.PHONY: changelog changelog-check db-migrate engine-serve kafka-topics proto-gen-go

changelog:
	git cliff -o $(CHANGELOG_FILE)
	perl -0pi -e 's/(Dates use `YYYY-MM-DD`\.)\n##/$$1\n\n##/g; s/\n{3,}/\n\n/g' $(CHANGELOG_FILE)

changelog-check:
	git cliff -o $(CHANGELOG_CHECK_FILE)
	perl -0pi -e 's/(Dates use `YYYY-MM-DD`\.)\n##/$$1\n\n##/g; s/\n{3,}/\n\n/g' $(CHANGELOG_CHECK_FILE)
	diff -u $(CHANGELOG_FILE) $(CHANGELOG_CHECK_FILE)

db-migrate:
	cd services/engine-rust && DATABASE_URL="$(DATABASE_URL)" cargo run -- migrate

engine-serve:
	cd services/engine-rust && DATABASE_URL="$(DATABASE_URL)" cargo run -- serve

# Same topics docker compose provisions, for a Redpanda you are running yourself.
# Existing topics are left alone: raising a live topic's partition count
# rehashes keys and needs a planned cutover, not a make target.
kafka-topics:
	rpk topic create telemetry.ingested -p $(TELEMETRY_PARTITIONS) --brokers $(KAFKA_BROKERS) || true
	rpk topic create telemetry.ingested.dlq -p 1 --brokers $(KAFKA_BROKERS) || true
	rpk topic create alert.opened -p 1 --brokers $(KAFKA_BROKERS) || true
	rpk topic create alert.resolved -p 1 --brokers $(KAFKA_BROKERS) || true
	rpk topic list --brokers $(KAFKA_BROKERS)

proto-gen-go:
	protoc --go_out=services/ingestion-go --go_opt=module=ingestion-go -I proto proto/telemetry.proto
