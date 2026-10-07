# mcp-sandbox build system interface (R07).
#
# CI binds to this interface — it calls `make <target>`, never raw
# language-specific commands. `container-image` is deliberately NOT declared:
# this is a Local stdio-only server that produces no image (B03/R01/N31).

COVERAGE_THRESHOLD := 95

.PHONY: build test lint e2e mutate clean coverage

## build — produce binaries for all architectures via goreleaser (B01/B02).
build:
	goreleaser release --snapshot --clean

## test — unit tests with race detector + coverage ≥ 95% gate (C01/C04GO).
test:
	go test -race -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | awk -v t="$(COVERAGE_THRESHOLD)" \
		'$$1=="total:" { gsub("%","",$$3); if ($$3+0 < t) { \
			print "coverage " $$3 "% is below the " t "% threshold"; exit 1 } }'

## coverage — print the coverage report (helper, not part of the gate).
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

## lint — static analysis gate (C03GO/C05GO/C06GO/C07GO).
lint:
	golangci-lint run ./...
	go vet ./...
	gosec -quiet ./...
	scripts/govulncheck.sh
	go-arch-lint check

## e2e — build-tagged contract suite via go-docker-testsuite (T02/C09GO).
e2e:
	go test -tags e2e ./...

## mutate — gremlins mutation testing hard gate 80/80 (C02/C08GO).
## Run from the module root without a path argument: passing a path makes
## gremlins treat it as the module directory and it finds no source files.
mutate:
	gremlins unleash --threshold-efficacy=80 --threshold-mcover=80

## clean — remove build/test artifacts.
clean:
	rm -rf dist coverage.out
