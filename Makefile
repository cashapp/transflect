# Run `make help` to display help

# --- Global -------------------------------------------------------------------
O = out
COVERAGE = 62
VERSION ?= $(shell git describe --tags --dirty  --always)

all: build test check-coverage lint shellcheck ## Build, test, check coverage and lint
	@if [ -e .git/rebase-merge ]; then git --no-pager log -1 --pretty='%h %s'; fi
	@echo '$(COLOUR_GREEN)Success$(COLOUR_NORMAL)'

ci: clean all envoy-config-test istio-config-test cluster-test ## Run full clean build and cluster tests as on CI

setup: add-registry-host ## First time setup

clean::  ## Remove generated files
	-rm -rf $(O)

.PHONY: all ci clean

# --- Build --------------------------------------------------------------------
GO_CMDS = $(if $(wildcard ./cmd/*),./cmd/...,.)
GO_LDFLAGS = -X main.version=$(VERSION)
RUN_TESTSERVER = docker run --rm -p 9090:9090 --name echo julia/echo:v0.0.4
KILL_TESTSERVER = docker kill echo

build: | $(O)  ## Build binaries of directories in ./cmd to out/
	go build -o $(O) -ldflags='$(GO_LDFLAGS)' $(GO_CMDS)

install:  ## Build and install binaries to hermit environment
	go install -ldflags='$(GO_LDFLAGS)' $(GO_CMDS)

run-testserver:  ## Run testserver
	$(RUN_TESTSERVER)

.PHONY: build install run-testserver

# --- Envoy --------------------------------------------------------------------
ENVOY_OUT = envoy/envoy.yaml

run-envoy:
	envoy -c $(ENVOY_OUT)

GEN_ENVOY_CONFIG =  $(O)/transflect --plaintext --format envoy localhost:9090 $(ENVOY_OUT)
envoy-config: build
	$(GEN_ENVOY_CONFIG)

envoy-config-test: build
	-$(KILL_TESTSERVER)
	$(RUN_TESTSERVER) &
	sleep 1
	$(GEN_ENVOY_CONFIG)
	test -z "$$(git status --porcelain $(ENVOY_OUT))"
	@rv=$$?; $(KILL_TESTSERVER); exit $$rv

# --- Istio --------------------------------------------------------------------
ISTIO_OUT = istio/envoy-filter.yaml
GEN_ISTIO_CONFIG = $(O)/transflect \
                        --plaintext \
                        --format istio \
                        --app echo \
                        --namespace echo \
                        localhost:9090 $(ISTIO_OUT)
LOCAL_OPERATOR_CMD = $(O)/transflect-operator --plaintext --use-ingress --lease-namespace default --address localhost:80

istio: istio-config istio-apply

istio-config: build
	$(GEN_ISTIO_CONFIG)

istio-config-test: build
	-$(KILL_TESTSERVER)
	$(RUN_TESTSERVER) &
	sleep 1
	$(GEN_ISTIO_CONFIG)
	test -z "$$(git status --porcelain $(ISTIO_OUT))"
	@rv=$$?; $(KILL_TESTSERVER); exit $$rv

run-local-operator: build ## Run transflect on host machine connecting to cluster
	$(LOCAL_OPERATOR_CMD)

.PHONY: istio-apply istio-config istio-delete istio-get istio-show-filters istio-config-test

# --- Test ---------------------------------------------------------------------
COVERFILE = $(O)/coverage.txt

test: ## Test go code, fast
	go test -coverprofile=$(COVERFILE) ./...

check-coverage: test
	@go tool cover -func=$(COVERFILE) | $(CHECK_COVERAGE) || $(FAIL_COVERAGE)

cover: test
	go tool cover -html=$(COVERFILE)


CHECK_COVERAGE = awk -F '[ \t%]+' '/^total:/ {print; if ($$3 < $(COVERAGE)) exit 1}'
FAIL_COVERAGE = { echo '$(COLOUR_RED)FAIL - Coverage below $(COVERAGE)%$(COLOUR_NORMAL)'; exit 1; }

.PHONY: check-coverage cover test

# --- Local cluster ------------------------------------------------------------
CLUSTER ?= transflect
REGISTRY_NAME = registry.$(CLUSTER).local
REGISTRY = k3d-$(REGISTRY_NAME):420
DELETE_CLUSTER_CMD = k3d cluster delete $(CLUSTER); k3d registry delete $(REGISTRY_NAME)

cluster-test: build cluster-create  ## Test integration on local cluster, slower
	./cluster-test.sh

cluster-clean:  ## Cleanup all deployments and resources in local k3d cluster, but keep cluster
	-kubectl delete -f deployment/transflect.yaml
	-kubectl delete -f deployment/routeguide.yaml
	-kubectl delete -f deployment/guppyecho.yaml
	-kubectl delete all,ingress,envoyfilter -n routeguide --all
	-kubectl delete all,ingress,envoyfilter -n guppyecho --all

registry-create: ci-add-registry-host
	k3d registry create $(REGISTRY_NAME) --port 420
	$(DOCKER_PUSH_CMD)

ci-add-registry-host:
	[ -z "$(CI)" ] || sudo sh -c "echo 127.0.0.1 k3d-$(REGISTRY_NAME) >> /etc/hosts"

add-registry-host: CI=true # Force modification of /etc/hosts
add-registry-host: ci-add-registry-host

cluster-create: registry-create
	k3d cluster create $(CLUSTER) \
	    --servers-memory 16GiB \
	    --port 80:80@loadbalancer \
	    --port 443:443@loadbalancer \
	    --api-port 6443 \
	    --k3s-server-arg '--no-deploy=traefik' \
	    --registry-use $(REGISTRY)
	kubectl config use-context k3d-$(CLUSTER)
	istioctl install --set profile=default --skip-confirmation

cluster-delete:  ## Delete the entire local k3d cluster
	$(DELETE_CLUSTER_CMD)

docker-purge: # WARNING: will delete dangling images and stopped containers!
	docker system prune --all
	docker network prune

clean::
	-$(DELETE_CLUSTER_CMD) 2> /dev/null

.PHONY: add-registry-host ci-add-registry-host cluster-clean cluster-create cluster-delete cluster-test docker-purge

# --- Lint ---------------------------------------------------------------------

lint:  ## Lint go source code
	golangci-lint run

shellcheck:
	shellcheck *.sh
	shfmt -i 4 -d *.sh

.PHONY: lint shellcheck

# --- Docker --------------------------------------------------------------------
DOCKER_PUSH_CMD = docker buildx build \
                -f Dockerfile \
                --build-arg VERSION=$(VERSION) \
                --push \
                --tag $(REGISTRY)/transflect:$(VERSION) \
                --tag $(REGISTRY)/transflect:latest \
                .

docker-build-push:
	$(DOCKER_PUSH_CMD)

.PHONY: docker-build-push

# --- Release -------------------------------------------------------------------
NEXTTAG := $(shell { git tag --list --merged HEAD --sort=-v:refname; echo v0.0.0; } | grep -E "^v?[0-9]+.[0-9]+.[0-9]+$$" | head -n1 | awk -F . '{ print $$1 "." $$2 "." $$3 + 1 }')

DOCKER_LOGIN = printenv DOCKER_PASSWORD | docker login --username "$(DOCKER_USERNAME)" --password-stdin

release: REGISTRY = cashapp
release: VERSION = $(NEXTTAG)
release:
	git tag $(NEXTTAG)
	git push origin $(NEXTTAG)
	goreleaser release --rm-dist
	[ -z "$(DOCKER_PASSWORD)" ] || $(DOCKER_LOGIN)
	$(DOCKER_PUSH_CMD)

.PHONY: release

# --- Utilities ----------------------------------------------------------------
COLOUR_NORMAL = $(shell tput sgr0 2>/dev/null)
COLOUR_RED    = $(shell tput setaf 1 2>/dev/null)
COLOUR_GREEN  = $(shell tput setaf 2 2>/dev/null)
COLOUR_WHITE  = $(shell tput setaf 7 2>/dev/null)

XARGS = xargs $(if $(filter Linux,$(shell uname)),--no-run-if-empty)

help:
	@awk -F ':.*## ' 'NF == 2 && $$1 ~ /^[A-Za-z0-9%_-]+$$/ { printf "$(COLOUR_WHITE)%-25s$(COLOUR_NORMAL)%s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

$(O):
	@mkdir -p $@

define nl


endef

# Ensure make version is gnu make 3.82 or higher
ifeq ($(filter undefine,$(value .FEATURES)),)
$(error Unsupported Make version. \
	$(nl)Use GNU Make 3.82 or higher (current: $(MAKE_VERSION)). \
	$(nl)Activate 🐚 hermit with `. bin/activate-hermit` and run again)
endif

.PHONY: help
