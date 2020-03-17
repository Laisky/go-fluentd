init:
	go get golang.org/x/tools/cmd/goimports
	# go get -u github.com/golang/protobuf/protoc-gen-go

test:
	@tox --recreate
	@tox

changelog: CHANGELOG.md
	sh ./.scripts/generate_changelog.sh

lint:
	# goimports -local oogway -w .
	gofmt -s -w .
	go mod tidy
	golangci-lint run
