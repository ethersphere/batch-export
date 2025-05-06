GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= v1.64.5

.PHONY: binary

binary: dist FORCE
	$(GO) version
ifeq ($(OS),Windows_NT)
	$(GO) build  -o dist/batch-export.exe .
else
	$(GO) build -o dist/batch-export .
endif

dist:
	mkdir $@

.PHONY: lint
lint: linter
	$(GOLANGCI_LINT) run

.PHONY: linter
linter:
	which $(GOLANGCI_LINT) || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$($(GO) env GOPATH)/bin $(GOLANGCI_LINT_VERSION)

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: build
build: export CGO_ENABLED=0
build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" ./...

.PHONY: clean
clean:
	$(GO) clean
	rm -rf dist/


.PHONY: test
test:
	$(GO) test -v ./pkg/...

FORCE:
