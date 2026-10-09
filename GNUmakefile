default: test

build:
	go build ./...

# All tests, against the in-memory fake API.
test:
	go test -count=1 ./...

# Provider tests against the real API. Needs NANELO_API_KEY (and NANELO_TEST_ZONE for team keys).
testacc:
	TF_ACC=1 go test -count=1 -v -timeout 30m ./internal/provider/

generate:
	go generate ./...

.PHONY: default build test testacc generate
