# Copyright 2015 The Prometheus Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Ensure GOBIN is not set during build so that promu is installed to the correct path
unexport GOBIN

GO           ?= go
GOFMT        ?= $(GO)fmt
FIRST_GOPATH := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
GOHOSTOS     ?= $(shell $(GO) env GOHOSTOS)
GOHOSTARCH   ?= $(shell $(GO) env GOHOSTARCH)

# Enforce Go modules support just in case the directory is inside GOPATH (and for Travis CI).
GO111MODULE := on
# Always use the local vendor/ directory to satisfy the dependencies.
GOOPTS := $(GOOPTS) -mod=vendor

PROMU        := $(FIRST_GOPATH)/bin/promu
STATICCHECK  := $(FIRST_GOPATH)/bin/staticcheck
GOVERALLS    := $(FIRST_GOPATH)/bin/goveralls
pkgs          = ./...

ifeq (arm, $(GOHOSTARCH))
	GOHOSTARM ?= $(shell GOARM= $(GO) env GOARM)
	GO_BUILD_PLATFORM ?= $(GOHOSTOS)-$(GOHOSTARCH)v$(GOHOSTARM)
else
	GO_BUILD_PLATFORM ?= $(GOHOSTOS)-$(GOHOSTARCH)
endif

PROMU_VERSION ?= 0.5.0
PROMU_URL     := https://github.com/prometheus/promu/releases/download/v$(PROMU_VERSION)/promu-$(PROMU_VERSION).$(GO_BUILD_PLATFORM).tar.gz

PREFIX                  ?= $(shell pwd)
BIN_DIR                 ?= $(shell pwd)
# Private repo.
DOCKER_IMAGE_NAME       ?= gcr.io/stackdriver-prometheus/stackdriver-prometheus-sidecar
DOCKER_IMAGE_TAG        ?= $(subst /,-,$(shell git rev-parse --abbrev-ref HEAD))

ifdef DEBUG
	bindata_flags = -debug
endif

# TODO(jkohen): Reenable staticcheck once it no longer crashes.
all: format build test

style:
	@echo ">> checking code style"
	@! $(GOFMT) -d $(shell find . -path ./vendor -prune -o -name '*.go' -print) | grep '^'

deps:
	@echo ">> getting dependencies"
ifdef GO111MODULE
	GO111MODULE=$(GO111MODULE) $(GO) mod download
else
	$(GO) get $(GOOPTS) -t ./...
endif

test-short:
	@echo ">> running short tests"
	GO111MODULE=$(GO111MODULE) $(GO) test -short $(GOOPTS) $(pkgs)

test:
	@echo ">> running all tests"
	GO111MODULE=$(GO111MODULE) $(GO) test $(GOOPTS) $(pkgs)

cover:
	@echo ">> running all tests with coverage"
	GO111MODULE=$(GO111MODULE) $(GO) test -coverprofile=coverage.out $(GOOPTS) $(pkgs)

format:
	@echo ">> formatting code"
	GO111MODULE=$(GO111MODULE) $(GO) fmt $(pkgs)

vet:
	@echo ">> vetting code"
	GO111MODULE=$(GO111MODULE) $(GO) vet $(GOOPTS) $(pkgs)

staticcheck: $(STATICCHECK)
	@echo ">> running staticcheck"
	$(STATICCHECK) $(pkgs)

goveralls: cover $(GOVERALLS)
ifndef COVERALLS_TOKEN
	$(error COVERALLS_TOKEN is undefined, follow https://docs.coveralls.io/go to create one and go to https://coveralls.io to retrieve existing ones)
endif
	@echo ">> running goveralls"
	$(GOVERALLS) -coverprofile=coverage.out -service=travis-ci -repotoken "${COVERALLS_TOKEN}"

build: promu
	@echo ">> building binaries"
	GO111MODULE=$(GO111MODULE) $(PROMU) build --prefix $(PREFIX)

build-linux-amd64: promu
	@echo ">> building linux amd64 binaries"
	@GO111MODULE=$(GO111MODULE) GOOS=linux GOARCH=amd64 $(PROMU) build --prefix $(PREFIX)

tarball: promu
	@echo ">> building release tarball"
	@$(PROMU) tarball --prefix $(PREFIX) $(BIN_DIR)

docker: build-linux-amd64
	@echo ">> building docker image"
	docker build -t "$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)" .

push: test docker
	@echo ">> pushing docker image"
	docker push "$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)"

assets:
	@echo ">> writing assets"
	$(GO) get -u github.com/jteeuwen/go-bindata/...
	go-bindata $(bindata_flags) -pkg ui -o web/ui/bindata.go -ignore '(.*\.map|bootstrap\.js|bootstrap-theme\.css|bootstrap\.css)'  web/ui/templates/... web/ui/static/...
	$(GO) fmt ./web/ui

promu:
	@echo ">> fetching promu"
	$(eval PROMU_TMP := $(shell mktemp -d))
	curl -s -L $(PROMU_URL) | tar -xvzf - -C $(PROMU_TMP)
	mkdir -p $(FIRST_GOPATH)/bin
	cp $(PROMU_TMP)/promu-$(PROMU_VERSION).$(GO_BUILD_PLATFORM)/promu $(FIRST_GOPATH)/bin/promu
	rm -r $(PROMU_TMP)

$(FIRST_GOPATH)/bin/staticcheck:
	GOOS= GOARCH= $(GO) get -u honnef.co/go/tools/cmd/staticcheck

$(FIRST_GOPATH)/bin/goveralls:
	GOOS= GOARCH= $(GO) get -u github.com/mattn/goveralls

.PHONY: all style deps format build test vet assets tarball docker promu staticcheck $(FIRST_GOPATH)/bin/staticcheck goveralls $(FIRST_GOPATH)/bin/goveralls
