# GRID0+
#
# Run `make help` for the target list.

SHELL := /usr/bin/env bash

GO      ?= go
CONFIG  ?= config/config.yaml
BIN_DIR ?= bin

SERVICES := dauth aauth baas scsi stubs natcheck npln nex dashboard toolbox switchnetctl

# Set these to run the integration tests that need a real database and Redis.
# Without them those tests skip, so `make test` works on any machine.
TEST_DATABASE_URL ?=
TEST_REDIS_URL    ?=

.DEFAULT_GOAL := help

## help: list the targets
.PHONY: help
help:
	@echo "GRID0+"
	@echo
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -E 's/^## ([a-z0-9-]+): (.*)/  \1|\2/' | awk -F'|' '{printf "  %-16s %s\n", $$1, $$2}'
	@echo
	@echo "Configuration:"
	@echo "  CONFIG=$(CONFIG)"
	@echo "  TEST_DATABASE_URL=$(TEST_DATABASE_URL)   (unset: the PostgreSQL tests skip)"
	@echo "  TEST_REDIS_URL=$(TEST_REDIS_URL)   (unset: the Redis tests skip)"

## build: compile every service into bin/
.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	@for svc in $(SERVICES); do \
		echo "building $$svc"; \
		$(GO) build -o $(BIN_DIR)/$$svc ./cmd/$$svc || exit 1; \
	done

## test: run the unit tests
.PHONY: test
test:
	SWITCHNET_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
	SWITCHNET_TEST_REDIS_URL="$(TEST_REDIS_URL)" \
	$(GO) test ./...

## test-race: run the tests with the race detector
.PHONY: test-race
test-race:
	SWITCHNET_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
	SWITCHNET_TEST_REDIS_URL="$(TEST_REDIS_URL)" \
	$(GO) test -race ./...

## test-cover: run the tests and report coverage per package
.PHONY: test-cover
test-cover:
	SWITCHNET_TEST_DATABASE_URL="$(TEST_DATABASE_URL)" \
	SWITCHNET_TEST_REDIS_URL="$(TEST_REDIS_URL)" \
	$(GO) test -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | tail -1

## vet: run go vet
.PHONY: vet
vet:
	$(GO) vet ./...

## fmt: format every Go file
.PHONY: fmt
fmt:
	gofmt -w .

## fmt-check: fail if anything is unformatted
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "these files are not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

## lint: run golangci-lint if it is installed
.PHONY: lint
lint:
	@if command -v golangci-lint >/dev/null; then \
		golangci-lint run; \
	else \
		echo "golangci-lint is not installed; skipping."; \
		echo "install: https://golangci-lint.run/welcome/install/"; \
	fi

## check: fmt-check, vet and test -- what CI runs
.PHONY: check
check: fmt-check vet test

## gen-proto: fetch the upstream NPLN definitions and regenerate proto/
.PHONY: gen-proto
gen-proto:
	scripts/fetch-proto.sh
	scripts/gen-proto.sh
	@echo
	@echo "proto/ is generated and tracked in git, so a plain build needs neither"
	@echo "the network nor a protobuf toolchain. Commit the regenerated files."

## patch-splatoon3: build the game's cert-verification patch from your own dump
.PHONY: patch-splatoon3
patch-splatoon3:
	@test -n "$(NSO)" || { \
	  echo "usage: make patch-splatoon3 NSO=/path/to/main [OUT=<mods dir>] [SD=<sd root>]"; \
	  echo; \
	  echo "NSO is the game's main NSO. Ryujinx dumps one: right-click the title,"; \
	  echo "then Extract Data, ExeFS. There is no patch checked in and there cannot"; \
	  echo "be one -- see the comment at the top of scripts/splatoon3-patch.sh."; \
	  exit 2; \
	}
	scripts/splatoon3-patch.sh "$(NSO)" $(if $(OUT),--out "$(OUT)") $(if $(SD),--sd "$(SD)")

## gen-certs: create the local CA and the edge certificate
.PHONY: gen-certs
gen-certs:
	scripts/gen-certs.sh $(if $(wildcard $(CONFIG)),-c $(CONFIG),)

## gen-hosts: write switch/hosts.txt from the config and the host list
.PHONY: gen-hosts
gen-hosts:
	@test -f $(CONFIG) || { echo "$(CONFIG) does not exist. Copy config/config.example.yaml to it first."; exit 1; }
	$(GO) run ./cmd/switchnetctl -config $(CONFIG) hosts > switch/hosts.txt
	@echo "wrote switch/hosts.txt"
	@echo "Copy it to BOTH /atmosphere/hosts/sysmmc.txt and /atmosphere/hosts/emummc.txt"
	@echo "on the console's SD card -- one without the other works from only one"
	@echo "boot configuration."

## gen-nginx: write deploy/nginx.generated.conf from the config and the host list
.PHONY: gen-nginx
gen-nginx:
	@test -f $(CONFIG) || { echo "$(CONFIG) does not exist. Copy config/config.example.yaml to it first."; exit 1; }
	@mkdir -p deploy
	$(GO) run ./cmd/switchnetctl -config $(CONFIG) nginx > deploy/nginx.generated.conf
	@echo "wrote deploy/nginx.generated.conf"

## gen-dns: write dnsmasq and unbound configuration for an existing resolver
.PHONY: gen-dns
gen-dns:
	@test -f $(CONFIG) || { echo "$(CONFIG) does not exist. Copy config/config.example.yaml to it first."; exit 1; }
	@mkdir -p deploy
	$(GO) run ./cmd/switchnetctl -config $(CONFIG) dnsmasq > deploy/switchnet.dnsmasq.conf
	$(GO) run ./cmd/switchnetctl -config $(CONFIG) unbound > deploy/switchnet.unbound.conf
	@echo "wrote deploy/switchnet.dnsmasq.conf and deploy/switchnet.unbound.conf"
	@echo
	@echo "Add this to a resolver you already run (router, Pi-hole, Unbound)."
	@echo "GRID0+ does not run its own DNS server; nothing goes on the console"
	@echo "for redirection either way."

## gen-switch: gen-nginx, gen-dns and gen-hosts together
.PHONY: gen-switch
gen-switch: gen-nginx gen-dns gen-hosts

## switchnet-mode: point a console's SD card back at GRID0+ after Nextendo (or any other) mode. SD=/path/to/sd/card
.PHONY: switchnet-mode
switchnet-mode:
	scripts/switchnet-mode.sh $(SD)

## config-check: validate the config and warn about risky settings
.PHONY: config-check
config-check:
	$(GO) run ./cmd/switchnetctl $(if $(wildcard $(CONFIG)),-config $(CONFIG),) check

## up: start Postgres, Redis and nginx (the Go services run natively -- see 'run' and 'install-systemd')
.PHONY: up
up:
	docker compose up -d
	@echo
	@docker compose ps

## down: stop Postgres, Redis and nginx, keeping the volumes
.PHONY: down
down:
	docker compose down

## run: build and run the Go services natively (foreground, Ctrl-C to stop). Needs postgres/redis already up -- see 'make up'. To run a subset: bin/switchnetctl run baas dauth
.PHONY: run
run: $(BIN_DIR)/switchnetctl
	$(BIN_DIR)/switchnetctl -config $(CONFIG) run

$(BIN_DIR)/switchnetctl:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/switchnetctl ./cmd/switchnetctl

## install-systemd: install and enable the systemd units for production (see scripts/install-systemd.sh). Run from /opt/switchnet
.PHONY: install-systemd
install-systemd:
	scripts/install-systemd.sh

## clean-data: stop everything and DELETE the database and uploaded images
.PHONY: clean-data
clean-data:
	docker compose down -v
	rm -rf data/images

## logs: follow Postgres/Redis/nginx logs -- for the Go services: journalctl -u 'switchnet-*' -f (or their own output under 'make run')
.PHONY: logs
logs:
	docker compose logs -f

## fest-build: build a Splatfest's packs, seed folder and schedule in one step
.PHONY: fest-build
fest-build:
	@test -n "$(ASSETS)" || { echo "usage: make fest-build ASSETS=./fest-assets OUT=./dev/seed"; exit 2; }
	go run ./cmd/switchnetctl fest build -assets "$(ASSETS)" -out "$(OUT:=./dev/seed)"

## seed: create dev accounts, friendships and presence through the real API
.PHONY: seed
seed:
	scripts/seed-dev-data.sh

## seed-phase2: drive the phase 2 game server end to end (needs `make up` first)
.PHONY: seed-phase2
seed-phase2:
	scripts/seed-phase2.sh

## capture: capture the real BAAS friends API from a PC (see docs/PACKET_CAPTURE.md)
.PHONY: capture
capture:
	@echo "This needs prod.keys, PRODINFO and your BAAS device account credentials."
	@echo "Read docs/PACKET_CAPTURE.md first, then:"
	@echo
	@echo "  python3 scripts/capture-baas.py --parse-save <save>/su/baas/xxxxx.dat"
	@echo "  python3 scripts/capture-baas.py --keys prod.keys --prodinfo PRODINFO \\"
	@echo "      --device-account-id <16 hex> --password '<40 chars>' --country NL"
	@echo
	@python3 scripts/capture-baas.py --help

## clean: remove build output and coverage
.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out
