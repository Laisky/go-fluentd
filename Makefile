test:
	@tox --recreate
	@tox

lint:
	go mod tidy
	# goimports -local oogway,library,common -w .
	goimports -w .
	gofmt -s -w .
	golangci-lint run --timeout 3m -E golint,depguard,gocognit,goconst,gofmt,misspell,exportloopref,nilerr #,gosec,lll


changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh
