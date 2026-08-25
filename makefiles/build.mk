BASE_REGISTRY ?= kubeovn
# Makefile for building and pushing Docker images

COMMIT = git-$(shell git rev-parse --short HEAD)
DATE = $(shell date +"%Y-%m-%d_%H:%M:%S")
IMAGE_BUILD_TARGETS = build-fabric build-fabric-dpdk build-dev build-debug base-amd64 base-amd64-dpdk base-arm64 build-kit image-fabric image-fabric-arm64 image-fabric-debug image-fabric-dpdk image-test release release-arm release-arm-debug push-release local-dev
ifneq ($(filter $(IMAGE_BUILD_TARGETS),$(MAKECMDGOALS)),)
IMAGE_REVISION ?= $(if $(GITHUB_SHA),$(GITHUB_SHA),$(shell git rev-parse HEAD))
IMAGE_REF_NAME ?= $(if $(GITHUB_HEAD_REF),$(GITHUB_HEAD_REF),$(if $(GITHUB_REF_NAME),$(GITHUB_REF_NAME),$(shell git symbolic-ref -q --short HEAD || git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD)))
IMAGE_REVISION := $(IMAGE_REVISION)
IMAGE_REF_NAME := $(IMAGE_REF_NAME)
endif
IMAGE_LABELS = --label "org.opencontainers.image.source=https://github.com/cloudyfolks-labs/fabric" --label "org.opencontainers.image.revision=$(IMAGE_REVISION)" --label "org.opencontainers.image.ref.name=$(IMAGE_REF_NAME)"

GOLDFLAGS = -extldflags '-z now' -X github.com/cloudyfolks-labs/fabric/versions.COMMIT=$(COMMIT) -X github.com/cloudyfolks-labs/fabric/versions.VERSION=$(RELEASE_TAG) -X github.com/cloudyfolks-labs/fabric/versions.BUILDDATE=$(DATE)
ifdef DEBUG
GO_BUILD_FLAGS = -ldflags "$(GOLDFLAGS)"
else
GO_BUILD_FLAGS = -trimpath -ldflags "-w -s $(GOLDFLAGS)"
endif

GO_MOD_VERSION := $(shell awk '/^go[[:space:]]+/ { print $$2; exit }' go.mod)
ifeq ($(strip $(GO_MOD_VERSION)),)
$(error failed to determine Go version from go.mod)
endif
GOTOOLCHAIN_VERSION := go$(GO_MOD_VERSION)
MODERNIZE_EXCLUDE := github.com/cloudyfolks-labs/fabric/mocks|github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn|github.com/cloudyfolks-labs/fabric/pkg/client

.PHONY: gen-crd
gen-crd:
	hack/gen-crd.sh

.PHONY: pull-base
pull-base:
	docker pull $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)
	docker pull $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-debug
	docker pull $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-amd64-legacy
	docker tag $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG) $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)
	docker tag $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-debug $(BASE_REGISTRY)/kube-ovn-base:$(DEBUG_TAG)
	docker tag $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-amd64-legacy $(BASE_REGISTRY)/kube-ovn-base:$(LEGACY_TAG)

.PHONY: pull-base-dpdk
pull-base-dpdk:
	docker pull $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-dpdk
	docker tag $(BASE_REGISTRY)/kube-ovn-base:$(BASE_VERSION_TAG)-dpdk $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-dpdk

.PHONY: sync-version
sync-version:
	hack/sync-version.sh

.PHONY: verify-version
verify-version: sync-version
	@if ! git diff --exit-code charts/fabric/Chart.yaml charts/fabric/charts/fabric-crds/Chart.yaml charts/fabric/values.yaml dist/images/install.sh >/dev/null; then echo "Error: the chart version is out of sync with VERSION. Please run 'make sync-version' and commit the changes."; exit 1; fi

.PHONY: verify-crd
verify-crd: gen-crd
	@if ! git diff --exit-code charts/fabric/charts/fabric-crds/templates/crds.yaml dist/images/install.sh >/dev/null; then echo "Error: CRDs are out of sync. Please run 'make gen-crd' and commit the changes."; exit 1; fi
	@echo "CRDs are up to date."

.PHONY: build-go
build-go:
	go mod tidy
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o $(CURDIR)/dist/images/fabric -v ./cmd/cni
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-cmd -v ./cmd
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-bfdd-supervisor -v ./cmd/bfdd_supervisor
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-daemon -v ./cmd/daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-controller -v ./cmd/controller
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GO_BUILD_FLAGS) -o $(CURDIR)/dist/images/test-server -v ./test/server

.PHONY: build-go-arm
build-go-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -o $(CURDIR)/dist/images/fabric -v ./cmd/cni
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-cmd -v ./cmd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-bfdd-supervisor -v ./cmd/bfdd_supervisor
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-daemon -v ./cmd/daemon
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GO_BUILD_FLAGS) -buildmode=pie -o $(CURDIR)/dist/images/fabric-controller -v ./cmd/controller

.PHONY: build-fabric
build-fabric: gen-crd build-debug build-go
	docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(RELEASE_TAG) --build-arg VERSION=$(RELEASE_TAG) -f dist/images/Dockerfile dist/images/
	docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(LEGACY_TAG) --build-arg VERSION=$(LEGACY_TAG) -f dist/images/Dockerfile dist/images/

.PHONY: build-fabric-dpdk
build-fabric-dpdk: gen-crd build-go
	docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(RELEASE_TAG)-dpdk --build-arg BASE_TAG=$(RELEASE_TAG)-dpdk -f dist/images/Dockerfile dist/images/

.PHONY: build-dev
build-dev: gen-crd build-go
	docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(DEV_TAG) --build-arg VERSION=$(RELEASE_TAG) -f dist/images/Dockerfile dist/images/

.PHONY: build-debug
build-debug: gen-crd
	@DEBUG=1 $(MAKE) build-go
	docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(DEBUG_TAG) --build-arg BASE_TAG=$(DEBUG_TAG) -f dist/images/Dockerfile dist/images/

.PHONY: base-amd64
base-amd64:
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 --build-arg ARCH=amd64 --build-arg GO_VERSION --build-arg TRIVY_DB_REPOSITORY -t $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-amd64 -o type=docker -f dist/images/Dockerfile.base dist/images/
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 --build-arg ARCH=amd64 --build-arg GO_VERSION --build-arg TRIVY_DB_REPOSITORY --build-arg LEGACY=true -t $(BASE_REGISTRY)/kube-ovn-base:$(LEGACY_TAG) -o type=docker -f dist/images/Dockerfile.base dist/images/
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 --build-arg ARCH=amd64 --build-arg GO_VERSION --build-arg TRIVY_DB_REPOSITORY --build-arg DEBUG=true -t $(BASE_REGISTRY)/kube-ovn-base:$(DEBUG_TAG)-amd64 -o type=docker -f dist/images/Dockerfile.base dist/images/

.PHONY: base-amd64-dpdk
base-amd64-dpdk:
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 --build-arg ARCH=amd64 -t $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-amd64-dpdk -o type=docker -f dist/images/Dockerfile.base-dpdk dist/images/

.PHONY: base-arm64
base-arm64:
	docker buildx build $(IMAGE_LABELS) --platform linux/arm64 --build-arg ARCH=arm64 --build-arg GO_VERSION --build-arg TRIVY_DB_REPOSITORY -t $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-arm64 -o type=docker -f dist/images/Dockerfile.base dist/images/
	docker buildx build $(IMAGE_LABELS) --platform linux/arm64 --build-arg ARCH=arm64 --build-arg GO_VERSION --build-arg TRIVY_DB_REPOSITORY --build-arg DEBUG=true -t $(BASE_REGISTRY)/kube-ovn-base:$(DEBUG_TAG)-arm64 -o type=docker -f dist/images/Dockerfile.base dist/images/

.PHONY: build-kit
build-kit: gen-crd build-go
	DOCKER_BUILDKIT=1 docker build $(IMAGE_LABELS) -t $(REGISTRY)/fabric:$(RELEASE_TAG) --build-arg VERSION=$(RELEASE_TAG) -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: image-fabric
image-fabric: gen-crd image-fabric-debug build-go
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 -t $(REGISTRY)/fabric:$(RELEASE_TAG) --build-arg VERSION=$(RELEASE_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG) -o type=docker -f dist/images/Dockerfile dist/images/
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 -t $(REGISTRY)/fabric:$(LEGACY_TAG) --build-arg VERSION=$(LEGACY_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG)-amd64-legacy -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: image-fabric-arm64
image-fabric-arm64: gen-crd build-go-arm
	docker buildx build $(IMAGE_LABELS) --platform linux/arm64 -t $(REGISTRY)/fabric:$(RELEASE_TAG) --build-arg VERSION=$(RELEASE_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG) -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: image-fabric-debug
image-fabric-debug: gen-crd
	@DEBUG=1 $(MAKE) build-go
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 -t $(REGISTRY)/fabric:$(DEBUG_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG)-debug -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: image-fabric-dpdk
image-fabric-dpdk: gen-crd build-go
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 -t $(REGISTRY)/fabric:$(RELEASE_TAG)-dpdk --build-arg VERSION=$(RELEASE_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG)-dpdk -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: image-test
image-test: build-go
	docker buildx build $(IMAGE_LABELS) --platform linux/amd64 -t $(REGISTRY)/test:$(RELEASE_TAG) -o type=docker -f dist/images/Dockerfile.test dist/images/

.PHONY: release
release: lint image-fabric

.PHONY: release-arm
release-arm: release-arm-debug image-fabric-arm64

.PHONY: release-arm-debug
release-arm-debug:
	@DEBUG=1 $(MAKE) build-go-arm
	docker buildx build $(IMAGE_LABELS) --platform linux/arm64 -t $(REGISTRY)/fabric:$(DEBUG_TAG) --build-arg BASE_TAG=$(BASE_VERSION_TAG)-debug -o type=docker -f dist/images/Dockerfile dist/images/

.PHONY: push-dev
push-dev:
	docker push $(REGISTRY)/fabric:$(DEV_TAG)

.PHONY: push-release
push-release: release
	docker push $(REGISTRY)/fabric:$(RELEASE_TAG)

.PHONY: tar-fabric
tar-fabric:
	docker save $(REGISTRY)/fabric:$(RELEASE_TAG) $(REGISTRY)/fabric:$(LEGACY_TAG) $(REGISTRY)/fabric:$(DEBUG_TAG) -o fabric.tar

.PHONY: tar-fabric-dpdk
tar-fabric-dpdk:
	docker save $(REGISTRY)/fabric:$(RELEASE_TAG)-dpdk -o fabric-dpdk.tar

.PHONY: tar
tar: tar-fabric

.PHONY: base-tar-amd64
base-tar-amd64:
	docker save $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-amd64 $(BASE_REGISTRY)/kube-ovn-base:$(LEGACY_TAG) $(BASE_REGISTRY)/kube-ovn-base:$(DEBUG_TAG)-amd64 -o image-amd64.tar

.PHONY: base-tar-amd64-dpdk
base-tar-amd64-dpdk:
	docker save $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-amd64-dpdk -o image-amd64-dpdk.tar

.PHONY: base-tar-arm64
base-tar-arm64:
	docker save $(BASE_REGISTRY)/kube-ovn-base:$(RELEASE_TAG)-arm64 $(BASE_REGISTRY)/kube-ovn-base:$(DEBUG_TAG)-arm64 -o image-arm64.tar

.PHONY: lint
lint: verify-crd
ifeq ($(CI),true)
	@echo "Running in GitHub Actions"
	golangci-lint run -v
	go list ./... | grep -vE '$(MODERNIZE_EXCLUDE)' | xargs go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest -test
else
	@echo "Running in local environment"
	golangci-lint run -v --fix
	go list ./... | grep -vE '$(MODERNIZE_EXCLUDE)' | xargs go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest -test -fix
endif

.PHONY: scan
scan:
	trivy image --exit-code=1 --ignore-unfixed --scanners vuln $(REGISTRY)/fabric:$(RELEASE_TAG)
