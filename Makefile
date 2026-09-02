CHANGELOG_FILE := CHANGELOG.md
CHANGELOG_CHECK_FILE := /tmp/ambagrid_CHANGELOG.md
DATABASE_URL ?= postgres://ambagrid_admin:ambagrid_secure_pass@localhost:5432/ambagrid_operational

.PHONY: changelog changelog-check db-migrate engine-serve proto-gen-go

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

proto-gen-go:
	protoc --go_out=services/ingestion-go --go_opt=module=ingestion-go -I proto proto/telemetry.proto
