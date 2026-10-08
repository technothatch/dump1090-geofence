# dump1090-geofence — build/test targets. Every target below is also
# documented as a plain command in README.md (make is not required).

GO ?= go

.PHONY: build test cover race fmt vet check clean

build:
	$(GO) build ./...

test:
	$(GO) test ./...

# Coverage total (atomic mode, same as CI): go tool cover -func coverage.out
cover:
	$(GO) test -count=1 -coverprofile coverage.out -covermode atomic ./...
	$(GO) tool cover -func coverage.out | tail -1

# CI runs tests with the race detector.
race:
	$(GO) test -race ./...

# Fails if any file needs formatting (mirrors the CI gofmt step).
fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

check: fmt vet test build

clean:
	rm -f dump1090-geofence coverage.out coverage.html
