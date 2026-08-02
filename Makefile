.PHONY: site serve clean tools generate fmt vet lint build test test-unit check demo-up demo demo-down

# The roadmap site, generated from roadmaps/ into site/. Standard library only, so there is nothing
# to install first.
site:
	python3 scripts/build_roadmap_site.py

# Build, then serve site/ on http://127.0.0.1:8000 for a local look.
serve: site
	python3 -m http.server 8000 --directory site

clean:
	rm -rf site gen .tools coverage.out

# Build the protobuf codegen plugins pinned in go.mod's tool block into .tools/bin, so buf generate
# does not depend on protoc or buf being installed on the host.
tools:
	mkdir -p .tools/bin
	go build -o .tools/bin/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go
	go build -o .tools/bin/protoc-gen-connect-go connectrpc.com/connect/cmd/protoc-gen-connect-go

# Regenerate gen/ from proto/. Run this after editing any .proto file and commit the result; CI
# checks that gen/ matches proto/ rather than generating on every build.
generate: tools
	go run github.com/bufbuild/buf/cmd/buf generate

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt: files need formatting:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

# golangci-lint is not vendored as a tool dependency; install it separately
# (https://golangci-lint.run/welcome/install/) to run this target.
lint:
	golangci-lint run

build:
	go build ./...

test:
	go test ./...

# The unit suite under the two conditions plain `test` does not apply: the race detector, and a
# randomized test order that catches a test only passing because a sibling ran first. This is what
# .github/workflows/unit-tests.yml runs, kept here so the same command reproduces a CI failure
# locally. It writes coverage.out, which that workflow turns into its job summary.
test-unit:
	go test ./... -race -shuffle=on -coverprofile=coverage.out

# The mechanical gate every change must pass before it ships (.agent-workflows/implement/workflow.md
# step 7). golangci-lint runs only when it is on PATH, since it is a separate install from `go get`.
check: fmt vet build test
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; else echo "golangci-lint not installed, skipping"; fi

# Start the disposable PostgreSQL and Redis cmd/demo (see docs/demo.md) runs against, and wait for
# both to report healthy before returning.
demo-up:
	docker compose -f deploy/docker-compose.yml up -d --wait

# Run the demo walkthrough against whatever demo-up started. Requires demo-up to have run first.
demo:
	go run ./cmd/demo

# Tear down the containers demo-up started, including their data.
demo-down:
	docker compose -f deploy/docker-compose.yml down -v
