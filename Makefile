# Operational entry points (context-m2.md §8.2). Targets only delegate to scripts, which
# print every command they run; every target also has an entry in scripts/m2/actions.json.
# Variables: OVERLAY=dev|cluster, REGISTRY=docker.io/<user>, SKIP_BUILD=1, YES=1.

# Recipes call bash explicitly so they work from PowerShell/cmd (Windows) as well as from
# a Unix shell. Inside Git Bash, BASH is already set by the shell.
ifeq ($(OS),Windows_NT)
BASH ?= C:/Program Files/Git/bin/bash.exe
else
BASH ?= bash
endif
SH = "$(BASH)"

.DEFAULT_GOAL := help
.PHONY: help dev-up dev-down deploy status logs tester-list tester open reset

help: ## Daftar target
	@$(SH) scripts/k8s/help.sh Makefile

dev-up: ## Buat/nyalakan cluster k3d lokal (3 node) untuk development
	@$(SH) scripts/k8s/dev-cluster.sh

dev-down: ## Hapus cluster k3d lokal beserta datanya (YES=1 tanpa konfirmasi)
	@$(SH) scripts/k8s/dev-cluster.sh delete

deploy: ## Deploy seluruh sistem ke cluster aktif (OVERLAY=dev|cluster, REGISTRY=...)
	@$(SH) scripts/k8s/deploy.sh

status: ## Node, pod per node (role), Tester Agent
	@$(SH) scripts/k8s/ops.sh status

logs: ## Ikuti log satu service: make logs S=aggregator
	@$(SH) scripts/k8s/ops.sh logs $(S)

tester-list: ## Daftar skenario Tester Agent
	@$(SH) scripts/k8s/ops.sh tester-list

tester: ## Jalankan skenario latihan Tester Agent + log: make tester S=kill-store
	@$(SH) scripts/k8s/ops.sh tester $(S)

open: ## Port-forward RabbitMQ UI dan tampilkan URL layanan
	@$(SH) scripts/k8s/ops.sh open

reset: ## Kembalikan manifest (replika, image) dan mode mock PVMBG ke normal
	@$(SH) scripts/k8s/ops.sh reset
