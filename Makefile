.PHONY: fmt test build run help ansible-syntax ansible-lint verify deploy deploy-service deploy-adapter deploy-status rollback-service rollback-adapter check-version check-inventory check-host check-rollback-confirm

ANSIBLE ?= ansible-playbook
ANSIBLE_LINT ?= ansible-lint
ANSIBLE_CFG ?= ansible/ansible.cfg
INVENTORY ?= ansible/hosts.ini
HOST ?=
VERSION ?=
ANSIBLE_FLAGS ?=
EXTRA ?=
BACKUP_DIR ?=
ROLLBACK_CONFIRM ?= 0
VFF_FISCAL_ALLOW_DIRTY_LOCAL_CONTROLLER ?= 0
PLAYBOOK_DIR := ansible/playbooks

export ANSIBLE_CONFIG := $(ANSIBLE_CFG)

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

build:
	go build ./cmd/vff-fiscal

run:
	go run ./cmd/vff-fiscal

help:
	@echo "Targets:"
	@echo "  fmt build test run"
	@echo "  verify ansible-syntax ansible-lint"
	@echo "  deploy deploy-service deploy-adapter deploy-status"
	@echo "  rollback-service rollback-adapter"
	@echo ""
	@echo "Examples:"
	@echo "  make deploy HOST=vff-fiscal VERSION=<40-char-sha>"
	@echo "  make deploy-status HOST=vff-fiscal"
	@echo "  make rollback-adapter HOST=vff-fiscal BACKUP_DIR=/opt/vff-fiscal/backups/releases/<release>/adapter ROLLBACK_CONFIRM=1"

check-version:
	@test -n "$(VERSION)" || { echo "VERSION is required and must be an exact 40-character lowercase commit SHA"; exit 1; }
	@printf '%s' "$(VERSION)" | grep -Eq '^[0-9a-f]{40}$$' || { echo "VERSION must be an exact 40-character lowercase commit SHA"; exit 1; }

check-host:
	@test -n "$(HOST)" || { echo "HOST is required for deployment and rollback commands"; exit 1; }

check-inventory:
	@test -f "$(INVENTORY)" || { echo "$(INVENTORY) is missing. Copy ansible/hosts.ini.example to ansible/hosts.ini and edit it locally."; exit 1; }

check-rollback-confirm:
	@test "$(ROLLBACK_CONFIRM)" = "1" || { echo "Set ROLLBACK_CONFIRM=1 to run rollback targets"; exit 1; }

check-controller-clean:
ifeq ($(VFF_FISCAL_ALLOW_DIRTY_LOCAL_CONTROLLER),0)
	@test -z "$$(git status --porcelain)" || { echo "Local controller working tree is not clean. Commit/stash changes or set VFF_FISCAL_ALLOW_DIRTY_LOCAL_CONTROLLER=1 (exceptional; does not bypass server-side SHA checks)."; exit 1; }
endif

SYNTAX_INVENTORY := $(if $(wildcard $(INVENTORY)),$(INVENTORY),ansible/hosts.ini.example)

ansible-syntax:
	@for playbook in $(PLAYBOOK_DIR)/*.yml; do \
		$(ANSIBLE) -i $(SYNTAX_INVENTORY) --syntax-check "$$playbook"; \
	done

ansible-lint:
	ANSIBLE_CONFIG=$(ANSIBLE_CFG) $(ANSIBLE_LINT) ansible/

verify: ansible-syntax ansible-lint fmt
	go vet ./...
	go test -race ./...
	go build ./cmd/vff-fiscal

deploy: check-host check-inventory check-version check-controller-clean
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/deploy.yml -l $(HOST) \
		-e vff_fiscal_version=$(VERSION) $(ANSIBLE_FLAGS) $(EXTRA)

deploy-service: check-host check-inventory check-version check-controller-clean
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/deploy-service.yml -l $(HOST) \
		-e vff_fiscal_version=$(VERSION) $(ANSIBLE_FLAGS) $(EXTRA)

deploy-adapter: check-host check-inventory check-version check-controller-clean
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/deploy-adapter.yml -l $(HOST) \
		-e vff_fiscal_version=$(VERSION) $(ANSIBLE_FLAGS) $(EXTRA)

deploy-status: check-host check-inventory
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/deploy-status.yml -l $(HOST) $(ANSIBLE_FLAGS) $(EXTRA)

rollback-service: check-host check-inventory check-rollback-confirm
	@test -n "$(BACKUP_DIR)" || { echo "BACKUP_DIR is required"; exit 1; }
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/rollback-service.yml -l $(HOST) \
		-e vff_fiscal_rollback_backup_dir=$(BACKUP_DIR) \
		-e vff_fiscal_rollback_confirm=true $(ANSIBLE_FLAGS) $(EXTRA)

rollback-adapter: check-host check-inventory check-rollback-confirm
	@test -n "$(BACKUP_DIR)" || { echo "BACKUP_DIR is required"; exit 1; }
	$(ANSIBLE) -i $(INVENTORY) $(PLAYBOOK_DIR)/rollback-adapter.yml -l $(HOST) \
		-e vff_fiscal_rollback_backup_dir=$(BACKUP_DIR) \
		-e vff_fiscal_rollback_confirm=true $(ANSIBLE_FLAGS) $(EXTRA)
