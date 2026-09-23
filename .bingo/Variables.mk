# Auto generated binary variables helper managed by https://github.com/bwplotka/bingo v0.9. DO NOT EDIT.
# All tools are designed to be build inside $GOBIN.
BINGO_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
GOPATH ?= $(shell go env GOPATH)
GOBIN  ?= $(firstword $(subst :, ,${GOPATH}))/bin
GO     ?= $(shell which go)

# Below generated variables ensure that every time a tool under each variable is invoked, the correct version
# will be used; reinstalling only if needed.
# For example for crane variable:
#
# In your main Makefile (for non array binaries):
#
#include .bingo/Variables.mk # Assuming -dir was set to .bingo .
#
#command: $(CRANE)
#	@echo "Running crane"
#	@$(CRANE) <flags/args..>
#
CRANE := $(GOBIN)/crane-v0.20.6
$(CRANE): $(BINGO_DIR)/crane.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/crane-v0.20.6"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=crane.mod -o=$(GOBIN)/crane-v0.20.6 "github.com/google/go-containerregistry/cmd/crane"

GO_APIDIFF := $(GOBIN)/go-apidiff-v0.8.3
$(GO_APIDIFF): $(BINGO_DIR)/go-apidiff.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/go-apidiff-v0.8.3"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=go-apidiff.mod -o=$(GOBIN)/go-apidiff-v0.8.3 "github.com/joelanford/go-apidiff"

GOLANGCI_LINT := $(GOBIN)/golangci-lint-v2.8.0
$(GOLANGCI_LINT): $(BINGO_DIR)/golangci-lint.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/golangci-lint-v2.8.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=golangci-lint.mod -o=$(GOBIN)/golangci-lint-v2.8.0 "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"

KIND := $(GOBIN)/kind-v0.33.0
$(KIND): $(BINGO_DIR)/kind.mod
	@# Install binary/ries using Go 1.14+ build command. This is using bwplotka/bingo-controlled, separate go module with pinned dependencies.
	@echo "(re)installing $(GOBIN)/kind-v0.33.0"
	@cd $(BINGO_DIR) && GOWORK=off $(GO) build -mod=mod -modfile=kind.mod -o=$(GOBIN)/kind-v0.33.0 "sigs.k8s.io/kind"
