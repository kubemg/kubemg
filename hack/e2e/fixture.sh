#!/usr/bin/env bash
#
# The end-to-end fixture: `make e2e-up` and `make e2e-down`.
#
# A level (c) pass needs the same set every time — the dev stack, a minikube
# cluster with this tree's agent attached through the tunnel, a Helm release, a
# multi-replica Deployment, a namespace-scoped `view` user, a second agent-mode
# cluster with no agent, and two CRD families. Building that by hand is most of
# what a pass used to cost, so it is built here, and built idempotently: `up`
# against a live fixture changes nothing and ends by printing the same summary.
#
# `down` is the deliberate reset. It removes what `up` made — the objects, the
# two clusters, the user and the two setting overrides — and leaves the dev
# stack and minikube running, since they predate the fixture (`make down` and
# `minikube stop` are theirs).
#
# Runs on the host: minikube, kubectl and helm talk to a local cluster, which a
# throwaway container cannot reach without the host's network and kubeconfig.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$ROOT/hack/e2e"

API="${E2E_API:-https://localhost:8443}"
# The address the agent dials. It is written as the runtime `public_url`
# override, because it is the one value the operator's own `.env` usually gets
# wrong for minikube (a LAN or VPN address a pod cannot route to).
PUBLIC_URL="${E2E_PUBLIC_URL:-https://host.docker.internal:8443}"
ADMIN_USER="${E2E_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${E2E_ADMIN_PASSWORD:-admin}"
PROFILE="${E2E_MINIKUBE_PROFILE:-minikube}"
AGENT_VERSION="${E2E_AGENT_VERSION:-0.0.0-e2e}"
STACK_TIMEOUT="${E2E_STACK_TIMEOUT:-300}"
ATTACH_TIMEOUT="${E2E_ATTACH_TIMEOUT:-180}"

CLUSTER="e2e-minikube"
DETACHED="e2e-detached"
NAMESPACE="e2e-apps"
DEPLOYMENT="e2e-web"
RELEASE="e2e-release"
VIEWER="e2e-viewer"
VIEWER_PASSWORD="e2e-viewer-pass"
AGENT_NAMESPACE="kubemg-system"

TOKEN=""

say() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!!\033[0m  %s\n' "$*" >&2; }
die() { printf '\033[31mxx\033[0m  %s\n' "$*" >&2; exit 1; }

kc() { kubectl --context "$PROFILE" "$@"; }

need() {
  local missing=""
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
  done
  [ -z "$missing" ] || die "missing on the host:$missing"
}

# api METHOD PATH [JSON] — prints the body, dies on a non-2xx with the answer.
api() {
  local method=$1 path=$2 data=${3:-}
  local args=(-sSk -X "$method" -H "Content-Type: application/json" -w $'\n%{http_code}')
  [ -n "$TOKEN" ] && args+=(-H "Authorization: Bearer $TOKEN")
  [ -n "$data" ] && args+=(--data "$data")
  local out code
  out=$(curl "${args[@]}" "$API$path") || die "$method $path: the server did not answer"
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  [ "$code" -lt 400 ] || die "$method $path answered $code: $out"
  printf '%s' "$out"
}

healthy() { curl -fsSk -o /dev/null "$API/health" 2>/dev/null; }

login() {
  local body
  body=$(jq -n --arg u "$ADMIN_USER" --arg p "$ADMIN_PASSWORD" '{username: $u, password: $p}')
  TOKEN=$(api POST /api/v1/auth/login "$body" | jq -r .token)
  [ -n "$TOKEN" ] && [ "$TOKEN" != null ] || die "login as $ADMIN_USER returned no token"
}

cluster_id() {
  api GET /api/v1/clusters | jq -r --arg n "$1" '.clusters[] | select(.name == $n) | .id' | head -n1
}

user_id() {
  api GET /api/v1/users | jq -r --arg n "$1" '.users[] | select(.username == $n) | .id' | head -n1
}

# The tag names the agent source it was built from, so an unchanged tree reuses
# the loaded image and a changed one cannot be mistaken for it.
agent_image() {
  local tree
  tree=$(git -C "$ROOT" rev-parse --short HEAD:agent)
  if [ -n "$(git -C "$ROOT" status --porcelain -- agent)" ]; then
    tree="$tree-dirty"
  fi
  printf 'kubemg-agent:e2e-%s' "$tree"
}

minikube_running() {
  [ "$(minikube -p "$PROFILE" status --format '{{.Host}}' 2>/dev/null || true)" = "Running" ]
}

# ---------------------------------------------------------------------------

ensure_stack() {
  if healthy; then
    say "dev stack is up"
    return
  fi
  say "starting the dev stack"
  (cd "$ROOT" && docker compose up --build -d)
  local waited=0
  until healthy; do
    [ "$waited" -lt "$STACK_TIMEOUT" ] || die "backend not healthy after ${STACK_TIMEOUT}s — see 'make logs'"
    sleep 5
    waited=$((waited + 5))
  done
  say "dev stack is up"
}

# An agent that cannot verify the bastion's certificate fails its handshake
# with x509 and nothing else to say, so the name it dials is checked first.
check_certificate() {
  local host sans
  host=$(printf '%s' "$PUBLIC_URL" | sed -E 's#^https://([^/:]+).*#\1#')
  sans=$(printf '' | openssl s_client -connect "${API#https://}" -servername localhost 2>/dev/null |
    openssl x509 -noout -text 2>/dev/null | grep -A1 'Subject Alternative Name' | tail -n1 || true)
  case "$sans" in
    *"DNS:$host"* | *"IP Address:$host"*) ;;
    *) die "the bastion's certificate does not cover $host (SANs: ${sans:-none}).
    Add it to KUBEMG_TLS_HOSTS in .env. The certificate is minted once, so an existing
    one has to go first: 'docker volume rm kubemg_tls-certs' (every attached agent
    will need its manifests re-applied), then 'make up'." ;;
  esac
}

ensure_minikube() {
  if minikube_running; then
    say "minikube ($PROFILE) is running"
  else
    say "starting minikube ($PROFILE)"
    minikube -p "$PROFILE" start
  fi
}

ensure_agent_image() {
  local image=$1
  if [[ "$image" != *-dirty ]] && minikube -p "$PROFILE" image ls 2>/dev/null | grep -q "$image\$"; then
    say "agent image $image already loaded"
    return
  fi
  say "building $image from agent/"
  docker buildx build --load -t "$image" --build-arg VERSION="$AGENT_VERSION" "$ROOT/agent"
  say "loading $image into minikube"
  minikube -p "$PROFILE" image load --overwrite=true "$image"
}

ensure_settings() {
  local image=$1 current want
  current=$(api GET /api/v1/settings)
  if [ "$(jq -r .effective.public_url <<<"$current")" = "$PUBLIC_URL" ] &&
    [ "$(jq -r .effective.agent_image <<<"$current")" = "$image" ]; then
    say "settings already point agents at $PUBLIC_URL and $image"
    return
  fi
  want=$(jq -n --arg u "$PUBLIC_URL" --arg i "$image" '{public_url: $u, agent_image: $i}')
  api PUT /api/v1/settings "$want" >/dev/null
  say "set public_url=$PUBLIC_URL and agent_image=$image (runtime overrides; 'make e2e-down' clears them)"
}

ensure_cluster() {
  local name=$1 environment=$2 id body
  id=$(cluster_id "$name")
  if [ -n "$id" ]; then
    printf '%s' "$id"
    return
  fi
  body=$(jq -n --arg n "$name" --arg e "$environment" \
    '{name: $n, environment: $e, connection_mode: "agent", description: "e2e fixture"}')
  api POST /api/v1/clusters "$body" | jq -r .id
}

# One agent namespace holds one cluster's agent. If it already holds another
# KubeMG cluster's (a hand-registered one, or a fixture from before a reset),
# applying over it silently detaches that cluster, so it is refused by name
# unless asked for.
attach_agent() {
  local id=$1 manifest installed wanted
  manifest=$(api GET "/api/v1/clusters/$id/kustomize?format=yaml")
  wanted=$(printf '%s\n' "$manifest" | sed -n 's/^ *cluster-token: *//p' | head -n1 | tr -d '"')
  installed=$(kc -n "$AGENT_NAMESPACE" get secret kubemg-agent \
    -o jsonpath='{.data.cluster-token}' 2>/dev/null | base64 --decode 2>/dev/null || true)
  if [ -n "$installed" ] && [ "$installed" != "$wanted" ] && [ "${E2E_REPLACE_AGENT:-}" != 1 ]; then
    die "$AGENT_NAMESPACE on $PROFILE already runs the agent of another KubeMG cluster.
    Applying $CLUSTER's package would detach that cluster. Re-run with
    E2E_REPLACE_AGENT=1 to take the namespace over, or E2E_MINIKUBE_PROFILE=<other>."
  fi
  say "applying the agent package for $CLUSTER (#$id)"
  printf '%s\n' "$manifest" | kc apply -f - >/dev/null
  local waited=0
  until [ "$(api GET "/api/v1/clusters/$id" | jq -r .agent_attached)" = "true" ]; do
    if [ "$waited" -ge "$ATTACH_TIMEOUT" ]; then
      kc -n "$AGENT_NAMESPACE" get pods >&2 || true
      die "agent did not attach within ${ATTACH_TIMEOUT}s — 'kubectl --context $PROFILE -n $AGENT_NAMESPACE logs deploy/kubemg-agent'"
    fi
    sleep 3
    waited=$((waited + 3))
  done
  say "agent attached"
}

ensure_objects() {
  say "applying the fixture's namespace, Deployment and CRDs"
  kc apply -f "$HERE/manifests/workloads.yaml" -f "$HERE/manifests/crds.yaml" >/dev/null
  kc wait --for condition=Established --timeout=60s \
    crd/widgets.stable.e2emulti.example crd/gadgets.stable.e2emulti.example \
    crd/solos.things.e2esingle.example >/dev/null
  kc apply -f "$HERE/manifests/custom-objects.yaml" >/dev/null
  kc -n "$NAMESPACE" rollout status "deploy/$DEPLOYMENT" --timeout=180s >/dev/null
}

# `helm upgrade --install` would append a revision on every run, which is not a
# no-op; the release is installed once and left alone.
ensure_release() {
  if helm --kube-context "$PROFILE" -n "$NAMESPACE" status "$RELEASE" >/dev/null 2>&1; then
    say "Helm release $RELEASE already installed"
    return
  fi
  say "installing Helm release $RELEASE"
  helm --kube-context "$PROFILE" -n "$NAMESPACE" install "$RELEASE" "$HERE/chart" >/dev/null
}

ensure_viewer() {
  local cluster=$1 id body
  id=$(user_id "$VIEWER")
  if [ -z "$id" ]; then
    body=$(jq -n --arg u "$VIEWER" --arg p "$VIEWER_PASSWORD" \
      '{username: $u, password: $p, system_role: "user"}')
    id=$(api POST /api/v1/users "$body" | jq -r .id)
  fi
  # Assigning replaces the pair's grant, so this is a no-op when it already holds.
  body=$(jq -n --argjson u "$id" --argjson c "$cluster" --arg ns "$NAMESPACE" \
    '{subject_type: "user", subject_id: $u, cluster_id: $c, k8s_role: "view", namespaces: [$ns]}')
  api POST /api/v1/permissions/assign "$body" >/dev/null
  printf '%s' "$id"
}

up() {
  need docker minikube kubectl helm jq curl openssl git
  ensure_stack
  check_certificate
  ensure_minikube

  local image
  image=$(agent_image)
  ensure_agent_image "$image"

  login
  ensure_settings "$image"

  local cluster detached viewer
  cluster=$(ensure_cluster "$CLUSTER" dev)
  detached=$(ensure_cluster "$DETACHED" staging)
  attach_agent "$cluster"
  ensure_objects
  ensure_release
  viewer=$(ensure_viewer "$cluster")

  local setup
  setup=$(api GET /api/v1/setup/state | jq -r .required)

  cat <<EOF

KubeMG e2e fixture — ready
  API                https://localhost:8443   (self-signed: curl -k)
  Console            http://localhost:5173
  Admin              $ADMIN_USER / (E2E_ADMIN_PASSWORD)
  Scoped viewer      $VIEWER / $VIEWER_PASSWORD   (user id $viewer, view on $NAMESPACE of $CLUSTER only)
  Cluster (attached) $CLUSTER   id $cluster   kube context: $PROFILE
  Cluster (no agent) $DETACHED   id $detached   agent mode, never installed
  Agent image        $image   in $AGENT_NAMESPACE, dialling $PUBLIC_URL
  Deployment         $NAMESPACE/$DEPLOYMENT   2 replicas, registry.k8s.io/pause (no shell)
  Helm release       $NAMESPACE/$RELEASE   local chart hack/e2e/chart, one ConfigMap
  CRDs               stable.e2emulti.example: Widget, Gadget (own section)
                     things.e2esingle.example: Solo (Other)
EOF
  if [ "$setup" = "true" ]; then
    warn "first-run setup is not finished, so the console opens on the setup wizard; the API is unaffected"
  fi
}

down() {
  need kubectl helm jq curl
  healthy || die "the dev stack is not up; nothing to remove from it"
  login

  local cluster detached viewer
  cluster=$(cluster_id "$CLUSTER")
  detached=$(cluster_id "$DETACHED")
  viewer=$(user_id "$VIEWER")

  if minikube_running; then
    say "removing the fixture's objects from minikube ($PROFILE)"
    helm --kube-context "$PROFILE" -n "$NAMESPACE" uninstall "$RELEASE" >/dev/null 2>&1 || true
    kc delete --ignore-not-found -f "$HERE/manifests/crds.yaml" >/dev/null
    kc delete --ignore-not-found namespace "$NAMESPACE" >/dev/null
    if [ -n "$cluster" ]; then
      api GET "/api/v1/clusters/$cluster/kustomize?format=yaml" | kc delete --ignore-not-found -f - >/dev/null
    fi
  else
    warn "minikube ($PROFILE) is not running; its objects are left as they are"
  fi

  [ -z "$viewer" ] || api DELETE "/api/v1/users/$viewer" >/dev/null
  [ -z "$cluster" ] || api DELETE "/api/v1/clusters/$cluster" >/dev/null
  [ -z "$detached" ] || api DELETE "/api/v1/clusters/$detached" >/dev/null

  # Only the overrides this fixture wrote are cleared; a value somebody set by
  # hand since is theirs.
  local current clear='{}'
  current=$(api GET /api/v1/settings)
  if [ "$(jq -r .overrides.public_url <<<"$current")" = "$PUBLIC_URL" ]; then
    clear=$(jq '. + {public_url: ""}' <<<"$clear")
  fi
  if [[ "$(jq -r .overrides.agent_image <<<"$current")" == kubemg-agent:e2e-* ]]; then
    clear=$(jq '. + {agent_image: ""}' <<<"$clear")
  fi
  [ "$clear" = '{}' ] || api PUT /api/v1/settings "$clear" >/dev/null

  say "fixture removed; the dev stack and minikube are still running"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  *) die "usage: $0 up|down" ;;
esac
