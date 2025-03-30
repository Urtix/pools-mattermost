.PHONY: run

# Golang Flags
GOFLAGS ?= $(GOFLAGS:)
GO = go
SRC_DIR = src

run:
	cd $(SRC_DIR) && $(GO) run $(GOFLAGS) $(GO_LINKER_FLAGS) .