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
.PHONY: help dev-up dev-down deploy status prepull logs tester-list tester open reset p1-checksum p1-distribution p1-load p1-surge p1-wipe-node p1-dashboard p1-amnesia p1-audit p1-netpol obs-open system-status broker-status broker-recover

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

prepull: ## Tarik semua image workload di setiap node sebelum demo: make prepull [NODES="node-b node-c"]
	@$(SH) scripts/k8s/ops.sh prepull "$(NODES)"

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

broker-status: ## Anggota cluster RabbitMQ dan replika tiap quorum queue
	@$(SH) scripts/k8s/ops.sh broker-status

broker-recover: ## Kosongkan disk member RabbitMQ yang crash-loop agar bergabung ulang: make broker-recover S=rabbitmq-2
	@$(SH) scripts/k8s/ops.sh broker-recover $(S)

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

.PHONY: p2-status p2-load p2-throughput p2-failover p2-slo
p2-status: ## Leader lease and Aggregator pod placement
	kubectl -n sistem-informasi-kebencanaan-bnpb get lease aggregator-leader -o yaml
	kubectl -n sistem-informasi-kebencanaan-bnpb get pods -l app=aggregator -o wide

p2-load: ## 10k VU + scale 3 to 5 to 2; requires TARGET_URL, optional ROLLOUT_IMAGE
	@$(SH) scripts/m2/p2-load.sh

p2-throughput: ## Measure successful request throughput at 1 and 3 replicas; requires TARGET_URL
	@$(SH) scripts/m2/p2-load.sh throughput

p2-failover: ## Tester Agent kill-leader (asks for confirmation)
	@$(SH) scripts/k8s/ops.sh tester kill-leader

p2-slo: ## Event-to-durable-ack SLI from a notification journal: make p2-slo JOURNAL=path [EXPECTED=n]
	@$(SH) scripts/m2/slo-check.sh $(if $(JOURNAL),--journal "$(JOURNAL)",) $(if $(EXPECTED),--expected "$(EXPECTED)",)

.PHONY: p3-check
p3-check: ## Isolated P3 checks with real PostgreSQL/RabbitMQ; no existing stack is changed
	@$(SH) scripts/m2/p3-check.sh

.PHONY: p5-sample
p5-sample: ## Sample the official Sensor Pantai over HTTP on an isolated practice container
	@$(SH) scripts/m2/pantai-sample.sh

.PHONY: p5-resilience
p5-resilience: ## Run isolated Sensor Pantai adapter and resilience fault tests with the race detector
	@$(SH) scripts/m2/pantai-resilience.sh

.PHONY: p5-ingestion
p5-ingestion: ## Verify coastal ingestion, restart and sensor outage on an isolated Compose stack
	@$(SH) scripts/m2/pantai-ingestion.sh

.PHONY: p4-sample
p4-sample: ## Capture 60 seconds of live SSE using SATELLITE_TOKEN from the environment or .env
	bash -c 'source scripts/k8s/lib.sh; if [[ -z "$${SATELLITE_TOKEN:-}" ]]; then load_env; fi; exec python3 scripts/m2/satellite-sample.py'

.PHONY: p4-check p4-compare p4-export
p4-check: ## Offline satellite/recovery tests; TEST_DATABASE_URL enables real PostgreSQL checks
	@$(SH) scripts/m2/p4-check.sh

p4-compare: ## Compare complete satellite exports: make p4-compare ACTUAL=actual.json REFERENCE=reference.json
	python3 scripts/m2/compare-satellite.py "$(ACTUAL)" "$(REFERENCE)"

p4-export: ## Export all SATELIT Audit events as JSON on stdout
	@$(SH) scripts/k8s/ops.sh p4-export
