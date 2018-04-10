TARGET := gh2mg-gw

NAMESPACE	:=github.com/erasche/github-issue-mailgun-gateway
WORKSPACE	:=$(GOPATH)/src/$(NAMESPACE)
GO_SOURCES	:=$(wildcard *.go)
GO_PACKAGES	:=$(dir $(GO_SOURCES))
VERSION	:=$(shell git describe --tags --always)
GO_FLAGS	:=-ldflags="-X main.version=$(VERSION) -X main.builddate=$(shell date --iso-8601=seconds --utc)"
DEP_ARGS	:=-v

all: $(TARGET)
	echo $(WORKSPACE)

test: setup.lock
	@cd $(WORKSPACE)\
		&& go test $(addprefix $(NAMESPACE)/,$(GO_PACKAGES))

setup.lock: $(WORKSPACE) vendor
	@echo $(VERSION) > setup.lock

fmt: $(GO_SOURCES)
	gofmt -w $(GO_SOURCES)
	goimports -w $(GO_SOURCES)

check: vet lint

vet: $(GO_SOURCES)
	go vet $(NAMESPACE)/

lint: $(GO_SOURCES)
	golint $(NAMESPACE)/

dep: $(WORKSPACE)
	@cd $(WORKSPACE) && dep $(ARGS)

vendor: Gopkg.toml Gopkg.lock
	@cd $(WORKSPACE) && dep ensure $(DEP_ARGS)
	@touch $@


$(TARGET): $(SRC) setup.lock
	go build $(GO_FLAGS) -o $@

clean:
	$(RM) $(TARGET)

release:
	rm -rf dist/
	mkdir dist
	CGO_ENABLED=0 gox $(GO_FLAGS) -os="linux" -output "dist/{{.Dir}}_{{.OS}}_{{.Arch}}"
	ghr -u erasche -replace $(VERSION) dist/

.PHONY: clean lint gofmt vet complexity qc qc_deps test clean release
