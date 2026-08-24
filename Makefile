CHANGELOG_FILE := CHANGELOG.md
CHANGELOG_CHECK_FILE := /tmp/ambagrid_CHANGELOG.md

.PHONY: changelog changelog-check

changelog:
	git cliff -o $(CHANGELOG_FILE)
	perl -0pi -e 's/(Dates use `YYYY-MM-DD`\.)\n##/$$1\n\n##/g; s/\n{3,}/\n\n/g' $(CHANGELOG_FILE)

changelog-check:
	git cliff -o $(CHANGELOG_CHECK_FILE)
	perl -0pi -e 's/(Dates use `YYYY-MM-DD`\.)\n##/$$1\n\n##/g; s/\n{3,}/\n\n/g' $(CHANGELOG_CHECK_FILE)
	diff -u $(CHANGELOG_FILE) $(CHANGELOG_CHECK_FILE)
