.PHONY: build test vet lint accuracy rules-doc demo install clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o narc ./cmd/narc

test:
	go test ./...

vet:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

accuracy:
	NARC_WRITE_ACCURACY=1 go test ./internal/rules/all -run TestAccuracy -count=1 -v | tail -n 40

rules-doc: build
	./narc rules --format markdown > docs/rules.md

demo: build
	./examples/demo.sh

install:
	go install -trimpath ./cmd/narc

clean:
	rm -f narc narc.sarif
