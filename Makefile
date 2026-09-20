.PHONY: build test cover fmt vet generate smoke check

check: fmt vet build test

build:
	go build ./...

vet:
	go vet ./...
	go vet -tags smoke ./...

test:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

fmt:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "$$unformatted"; exit 1; fi

# Regenerate types.gen.go from the vendored contract. Replace
# api/openapi.yaml with the published document first.
generate:
	go generate ./...

# Needs WALLETD_API and WALLETD_API_KEY pointing at a sandbox deployment.
smoke:
	go test -tags smoke -v -run TestSmoke ./...
