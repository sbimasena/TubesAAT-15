#!/usr/bin/env bash
# Single deploy command (D-DEPLOY), idempotent:
#   namespace -> Secrets from .env -> build + distribute images tagged with the git SHA
#   -> kubectl apply -k -> Tester Agent -> wait for every rollout.
#
#   OVERLAY=dev      (default) full k3d cluster, images loaded with `k3d image import`
#   OVERLAY=dev-practice
#                    local k3d practice without Sensor Pantai; not P5 acceptance
#   OVERLAY=cluster  demo cluster, images pushed to $REGISTRY (e.g. docker.io/<user>)
#   SKIP_BUILD=1     reuse the images of the current tag (no build, import or push)
source "$(dirname "$0")/lib.sh"

OVERLAY="${OVERLAY:-dev}"
case "$OVERLAY" in
  dev | dev-practice) IMAGE_MODE="${IMAGE_MODE:-import}" ;;
  cluster) IMAGE_MODE="${IMAGE_MODE:-push}" ;;
  *) die "OVERLAY must be dev or cluster" ;;
esac
SERVICES=(aggregator auth bmkg client-api dashboard-consumer notification-consumer pvmbg)

need docker kubectl git
CONTEXT="$(kubectl config current-context)"
echo "deploying overlay '$OVERLAY' to context '$CONTEXT'" >&2
if [[ "$IMAGE_MODE" == import ]]; then
  need k3d
  [[ "$CONTEXT" == k3d-* ]] || die "IMAGE_MODE=import needs a k3d context, got '$CONTEXT'"
  CLUSTER="${CONTEXT#k3d-}"
else
  [[ -n "${REGISTRY:-}" ]] || die "IMAGE_MODE=push needs REGISTRY, e.g. REGISTRY=docker.io/<user>"
fi

# Immutable tags (D-IMG): the commit SHA, plus a content suffix for uncommitted work.
TAG="$(git -C "$ROOT" rev-parse --short=12 HEAD)"
if [[ -n "$(git -C "$ROOT" status --porcelain -- services)" ]]; then
  [[ "$IMAGE_MODE" == import ]] || die "services/ has uncommitted changes; commit before pushing images"
  TAG="$TAG-dirty-$( (cd "$ROOT" && git diff HEAD -- services &&
    git ls-files -o --exclude-standard -z services | xargs -0 cat) | sha256 | cut -c1-8)"
fi

load_env
REQUIRED=(BMKG_API_KEY PVMBG_TOKEN POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD
  RABBITMQ_DEFAULT_USER RABBITMQ_DEFAULT_PASS AUTH_INTERNAL_SECRET JWT_SIGNING_SECRET
  MEDIA_CLIENT_PASSWORD FIELD_TEAM_CLIENT_PASSWORD INTERNAL_OPS_CLIENT_PASSWORD AUDIT_TOKEN)
if [[ "$OVERLAY" == dev-practice ]]; then
  echo "PANTAI disabled by dev-practice overlay; this deployment cannot prove P5." >&2
else
  REQUIRED+=(PANTAI_AUTH_TOKEN)
fi
for key in "${REQUIRED[@]}"; do
  [[ -n "${!key:-}" ]] || die "$key is empty in .env"
done
# These are spliced into DATABASE_URL/BROKER_URL by the manifests.
for key in POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD RABBITMQ_DEFAULT_USER RABBITMQ_DEFAULT_PASS; do
  [[ "${!key}" =~ ^[A-Za-z0-9._~-]+$ ]] || die "$key may only contain A-Z a-z 0-9 . _ ~ - (it is used inside a URL)"
done

run kubectl apply -f "$ROOT/deploy/k8s/base/namespace.yaml"

# Keep secret values out of kubectl argv. kubectl reads the values from
# short-lived files in the user's temp directory; the files are removed at exit.
SECRET_DIR="$(mktemp -d "${TMPDIR:-/tmp}/bnpb-deploy-secrets.XXXXXX")"
cleanup_secrets() {
  [[ -n "${SECRET_DIR:-}" && -d "$SECRET_DIR" ]] || return 0
  rm -f -- "$SECRET_DIR"/*
  rmdir -- "$SECRET_DIR"
}
trap cleanup_secrets EXIT
chmod 700 "$SECRET_DIR"

apply_secret() {
  local name="$1"
  shift
  local entry key variable file value
  local -a files=()
  for entry in "$@"; do
    key="${entry%%=*}"
    variable="${entry#*=}"
    [[ "$key" =~ ^[A-Za-z0-9._-]+$ && "$variable" =~ ^[A-Z0-9_]+$ ]] ||
      die "invalid Secret key or environment variable"
    file="$SECRET_DIR/$name-$key"
    if declare -p "$variable" >/dev/null 2>&1; then
      value="${!variable}"
    else
      value=""
    fi
    printf '%s' "$value" >"$file"
    chmod 600 "$file"
    files+=(--from-file="$key=$file")
  done
  echo "+ secret $name (from .env)" >&2
  kubectl -n "$NS" create secret generic "$name" "${files[@]}" \
    --dry-run=client -o yaml | kubectl apply -f -
}
apply_secret source-credentials \
  BMKG_API_KEY=BMKG_API_KEY PVMBG_TOKEN=PVMBG_TOKEN
apply_secret store-credentials \
  POSTGRES_DB=POSTGRES_DB POSTGRES_USER=POSTGRES_USER POSTGRES_PASSWORD=POSTGRES_PASSWORD
apply_secret broker-credentials \
  RABBITMQ_DEFAULT_USER=RABBITMQ_DEFAULT_USER RABBITMQ_DEFAULT_PASS=RABBITMQ_DEFAULT_PASS
apply_secret auth-credentials \
  AUTH_INTERNAL_SECRET=AUTH_INTERNAL_SECRET JWT_SIGNING_SECRET=JWT_SIGNING_SECRET \
  MEDIA_CLIENT_PASSWORD=MEDIA_CLIENT_PASSWORD FIELD_TEAM_CLIENT_PASSWORD=FIELD_TEAM_CLIENT_PASSWORD \
  INTERNAL_OPS_CLIENT_PASSWORD=INTERNAL_OPS_CLIENT_PASSWORD
apply_secret pantai-token token=PANTAI_AUTH_TOKEN
apply_secret audit-token token=AUDIT_TOKEN
apply_secret satellite-token token=SATELLITE_TOKEN
# Pods restart when a credential changes: the hash goes into a pod template annotation.
SECRETS_HASH="$(
  {
    for key in "${REQUIRED[@]}"; do printf '%s=%s\n' "$key" "${!key}"; done
    printf 'SATELLITE_TOKEN=%s\n' "${SATELLITE_TOKEN:-}"
  } | sha256 | cut -c1-16
)"

image_ref() {
  if [[ "$IMAGE_MODE" == push ]]; then echo "$REGISTRY/bnpb-$1:$TAG"; else echo "bnpb/$1:$TAG"; fi
}

if [[ "${SKIP_BUILD:-}" != 1 ]]; then
  refs=()
  for service in "${SERVICES[@]}"; do
    ref="$(image_ref "$service")"
    if [[ "$IMAGE_MODE" == push ]]; then
      # Every demo node is x86_64 (D-IMG).
      run docker build --platform linux/amd64 -t "$ref" "$ROOT/services/$service"
      run docker push "$ref"
    else
      run docker build -t "$ref" "$ROOT/services/$service"
    fi
    refs+=("$ref")
  done
  if [[ "$IMAGE_MODE" == import ]]; then
    run k3d image import -c "$CLUSTER" "${refs[@]}"
  fi
fi

# Generated overlay: the chosen overlay plus image tags and the secrets hash.
mkdir -p "$GENERATED"
{
  echo "# Generated by scripts/k8s/deploy.sh; do not edit or commit."
  echo "apiVersion: kustomize.config.k8s.io/v1beta1"
  echo "kind: Kustomization"
  echo "resources: [../overlays/$OVERLAY]"
  echo "patches:"
  for kind in Deployment StatefulSet; do
    echo "  - target: {kind: $kind, labelSelector: app.kubernetes.io/part-of=bnpb}"
    echo "    patch: '[{\"op\": \"add\", \"path\": \"/spec/template/metadata/annotations\", \"value\": {\"bnpb/secrets-hash\": \"$SECRETS_HASH\"}}]'"
  done
  echo "images:"
  for service in "${SERVICES[@]}"; do
    ref="$(image_ref "$service")"
    echo "  - {name: bnpb/$service, newName: ${ref%:*}, newTag: \"$TAG\"}"
  done
} >"$GENERATED/kustomization.yaml"

# One-time move from the M1 single-node broker (D-BROKER): serviceName is immutable and its
# classic queues clash with the quorum ones, so the old StatefulSet and its disk must go.
if [[ "$(kubectl -n "$NS" get statefulset rabbitmq -o 'jsonpath={.spec.serviceName}' 2>/dev/null)" == rabbitmq ]]; then
  confirm "Replace the single-node broker with the 3-node cluster? Messages still queued in it are lost (the outbox resends unpublished events)."
  # Foreground + wait: a PVC still Terminating would leave the new rabbitmq-0 Pending.
  run kubectl -n "$NS" delete statefulset rabbitmq --cascade=foreground --wait
  run kubectl -n "$NS" delete pvc data-rabbitmq-0 --ignore-not-found --wait
fi

run kubectl apply -k "$GENERATED"
run kubectl apply -f "$TESTER_MANIFEST"
run kubectl apply -k "$ROOT/deploy/k8s/observability"

# 600 s: pulling a new image from Docker Hub on the cluster VMs has taken 5 min.
for workload in $(kubectl -n "$NS" get deployment,statefulset -o name); do
  run kubectl -n "$NS" rollout status "$workload" --timeout=600s
done
run kubectl -n "$TESTER_NS" rollout status ds/tester-agent --timeout=600s
for workload in $(kubectl -n observability get deployment -o name); do
  run kubectl -n observability rollout status "$workload" --timeout=600s
done
echo "deployed $TAG" >&2
