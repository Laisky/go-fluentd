test:
	@tox --recreate
	@tox

changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh
