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
.PHONY: help dev-up dev-down deploy status logs tester-list tester open reset p1-checksum p1-distribution p1-load p1-surge p1-wipe-node p1-dashboard p1-amnesia p1-audit p1-netpol obs-open system-status

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

system-status: ## Status sistem dari Aggregator (storage, sumber, leader, DLQ)
	@$(SH) scripts/k8s/ops.sh system-status

obs-open: ## Port-forward Grafana (metrik) dan Jaeger (trace)
	@$(SH) scripts/k8s/ops.sh obs-open

p1-checksum: ## Checksum Audit API per sumber dan semua sumber
	@$(SH) scripts/k8s/ops.sh p1-checksum

p1-distribution: ## Sebaran data (rows, partisi) per unit store/node
	@$(SH) scripts/k8s/ops.sh p1-distribution

p1-dashboard: ## Port-forward dan buka dashboard analitik P1
	@$(SH) scripts/k8s/ops.sh p1-dashboard

p1-wipe-node: ## Checksum awal lalu Tester Agent wipe-node (P1.4)
	@$(SH) scripts/k8s/ops.sh p1-wipe-node

p1-amnesia: ## Simulasi disk kosong pada satu unit (P1.5): make p1-amnesia S=store-1
	@$(SH) scripts/k8s/ops.sh p1-amnesia $(S)

p1-netpol: ## Bukti hanya Aggregator yang bisa konek ke store (NetworkPolicy)
	@$(SH) scripts/k8s/ops.sh p1-netpol

p1-audit: ## Audit API events (cursor) dan location satu event
	@$(SH) scripts/k8s/ops.sh p1-audit

p1-load: ## Muat katalog gempa + ukur tulis per unit: make p1-load FILE=katalog.ndjson
	@$(SH) scripts/k8s/ops.sh p1-load "$(FILE)"

p1-surge: ## Lonjakan gempa satu wilayah + ukur tulis per unit: make p1-surge N=3000 REGION="Sulawesi Tengah"
	@$(SH) scripts/k8s/ops.sh p1-surge "$(or $(N),3000)" "$(or $(REGION),Sulawesi Tengah)"

reset: ## Kembalikan manifest (replika, image) dan mode mock PVMBG ke normal
	@$(SH) scripts/k8s/ops.sh reset

.PHONY: p2-status p2-load p2-failover
p2-status: ## Leader lease and Aggregator pod placement
	kubectl -n sistem-informasi-kebencanaan-bnpb get lease aggregator-leader -o yaml
	kubectl -n sistem-informasi-kebencanaan-bnpb get pods -l app=aggregator -o wide

p2-load: ## 10k VU + scale 3 to 5 to 2; requires TARGET_URL, optional ROLLOUT_IMAGE
	@$(SH) scripts/m2/p2-load.sh

p2-failover: ## Tester Agent kill-leader (asks for confirmation)
	@$(SH) scripts/k8s/ops.sh tester kill-leader

.PHONY: p4-sample
p4-sample: ## Capture 60 seconds of live SSE; set SATELLITE_TOKEN in the local environment
	python3 scripts/m2/satellite-sample.py

.PHONY: p4-check
p4-check: ## Offline satellite/recovery tests; TEST_DATABASE_URL enables real PostgreSQL checks
	@$(SH) scripts/m2/p4-check.sh
