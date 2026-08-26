#!/usr/bin/env bash
set -Eeuo pipefail

readonly TOPOLOGY="${ITERABASE_MANAGED_TOPOLOGY:?ITERABASE_MANAGED_TOPOLOGY is required}"
readonly LONGHORN_NAMESPACE="${LONGHORN_NAMESPACE:?LONGHORN_NAMESPACE is required}"
readonly INTERNAL_TLS_ENABLED="${ITERABASE_INTERNAL_TLS_ENABLED:-false}"
readonly DEADLINE=$((SECONDS + 900))
readonly TLS_DIR=/etc/longhorn-grpc-tls
readonly RELEASE_NAME="${ITERABASE_RELEASE_NAME:-rwx-storage}"

pod_started_before() {
  local selector="$1"
  local not_before_epoch="$2"
  kubectl -n "$LONGHORN_NAMESPACE" get pods -l "$selector" -o json | jq -e --argjson notBefore "$not_before_epoch" '
    any(.items[]?; ((.status.startTime // "1970-01-01T00:00:00Z") | fromdateiso8601) < $notBefore)
  ' >/dev/null
}

wait_for_current_mtls_components() {
  local not_before_epoch="$1"
  local last=""
  while ((SECONDS < DEADLINE)); do
    local pods managers instance_managers stale ready_managers running_instance_managers
    pods="$(kubectl -n "$LONGHORN_NAMESPACE" get pods -o json)"
    managers="$(jq '[.items[] | select(.metadata.labels.app == "longhorn-manager")] | length' <<<"$pods")"
    instance_managers="$(jq '[.items[] | select(.metadata.labels["longhorn.io/component"] == "instance-manager")] | length' <<<"$pods")"
    stale="$(jq --argjson notBefore "$not_before_epoch" '[.items[] | select(.metadata.labels.app == "longhorn-manager" or .metadata.labels["longhorn.io/component"] == "instance-manager") | select(((.status.startTime // "1970-01-01T00:00:00Z") | fromdateiso8601) < $notBefore)] | length' <<<"$pods")"
    ready_managers="$(jq '[.items[] | select(.metadata.labels.app == "longhorn-manager") | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$pods")"
    running_instance_managers="$(kubectl -n "$LONGHORN_NAMESPACE" get instancemanagers.longhorn.io -o json | jq '[.items[] | select(.status.currentState == "running") | select(.status.ip != null and .status.ip != "")] | length')"
    last="managerPods=${managers}/${ready_managers} instanceManagerPods=${instance_managers} runningInstanceManagers=${running_instance_managers} stalePods=${stale}"
    if ((managers > 0 && managers == ready_managers && instance_managers > 0 && instance_managers == running_instance_managers && stale == 0)); then
      echo "longhorn-grpc-mtls-components=pass $last"
      return
    fi
    sleep 5
  done
  echo "Longhorn components did not converge on the current gRPC mTLS leaf: $last" >&2
  kubectl -n "$LONGHORN_NAMESPACE" get certificates,instancemanagers.longhorn.io,pods -o wide >&2 || true
  exit 1
}

validate_grpc_mtls() {
  kubectl -n "$LONGHORN_NAMESPACE" wait --for=condition=Ready certificate/longhorn-grpc-tls --timeout=5m
  for file in ca.crt tls.crt tls.key; do
    [[ -s "$TLS_DIR/$file" ]] || { echo "mounted longhorn-grpc-tls is missing $file" >&2; exit 1; }
  done

  local not_before not_before_epoch
  not_before="$(kubectl -n "$LONGHORN_NAMESPACE" get certificate longhorn-grpc-tls -o jsonpath='{.status.notBefore}')"
  [[ -n "$not_before" ]] || { echo "longhorn-grpc-tls Certificate has no status.notBefore" >&2; exit 1; }
  not_before_epoch="$(jq -nr --arg value "$not_before" '$value | fromdateiso8601')"

  local restart_required=false
  if pod_started_before 'longhorn.io/component=instance-manager' "$not_before_epoch"; then
    restart_required=true
    kubectl -n "$LONGHORN_NAMESPACE" delete pods -l longhorn.io/component=instance-manager --wait=false
  fi
  if pod_started_before 'app=longhorn-manager' "$not_before_epoch"; then
    restart_required=true
    kubectl -n "$LONGHORN_NAMESPACE" rollout restart daemonset/longhorn-manager
  fi
  echo "longhorn-grpc-mtls-restart-required=$restart_required certificateNotBefore=$not_before"
  wait_for_current_mtls_components "$not_before_epoch"

  local tls_host=longhorn-backend.longhorn-system
  local health_request=/tmp/grpc-health-request
  local health_response=/tmp/grpc-health-response
  printf '\0\0\0\0\0' >"$health_request"
  local tested=0
  local name ip pod log auth_count port health_hex
  while IFS=$'\t' read -r name ip; do
    [[ -n "$name" && -n "$ip" ]] || continue
    pod="$(kubectl -n "$LONGHORN_NAMESPACE" get pod -l "longhorn.io/component=instance-manager,longhorn.io/instance-manager-name=$name" -o json | jq -r '.items[0].metadata.name // empty')"
    if [[ -z "$pod" ]]; then
      pod="$(kubectl -n "$LONGHORN_NAMESPACE" get pod -l longhorn.io/component=instance-manager -o json | jq -r --arg name "$name" '.items[] | select(.metadata.name == $name) | .metadata.name' | head -n1)"
    fi
    [[ -n "$pod" ]] || { echo "cannot resolve pod for InstanceManager $name" >&2; exit 1; }
    log="$(kubectl -n "$LONGHORN_NAMESPACE" logs "$pod")"
    auth_count="$(grep -c 'Creating gRPC server with mtls auth' <<<"$log" || true)"
    if ((auth_count < 1)); then
      echo "InstanceManager $name did not start its current V1 gRPC services with mTLS" >&2
      exit 1
    fi
    if grep -Eq 'Creating gRPC server with no auth|starting without TLS' <<<"$log"; then
      echo "InstanceManager $name logged a plaintext gRPC startup" >&2
      exit 1
    fi

    for port in 8500 8501 8502 8503; do
      if ! curl --noproxy '*' --silent --show-error --http2 --connect-timeout 5 --max-time 10 \
        --resolve "${tls_host}:${port}:${ip}" \
        --cacert "$TLS_DIR/ca.crt" --cert "$TLS_DIR/tls.crt" --key "$TLS_DIR/tls.key" \
        --request POST --header 'content-type: application/grpc' --header 'te: trailers' \
        --data-binary "@$health_request" --output "$health_response" \
        "https://${tls_host}:${port}/grpc.health.v1.Health/Check"; then
        echo "authenticated gRPC mTLS health request failed for $name at $ip:$port" >&2
        exit 1
      fi
      health_hex="$(od -An -v -tx1 "$health_response" | tr -d ' \n')"
      if [[ "$health_hex" != "00000000020801" ]]; then
        echo "authenticated gRPC mTLS health response was not SERVING for $name at $ip:$port (hex=${health_hex:-<empty>})" >&2
        exit 1
      fi
      if curl --noproxy '*' --silent --show-error --http2 --connect-timeout 5 --max-time 10 \
        --resolve "${tls_host}:${port}:${ip}" --cacert "$TLS_DIR/ca.crt" \
        --request POST --header 'content-type: application/grpc' --header 'te: trailers' \
        --data-binary "@$health_request" --output /dev/null \
        "https://${tls_host}:${port}/grpc.health.v1.Health/Check" >/dev/null 2>&1; then
        echo "instance-manager $name accepted TLS without a client certificate on $ip:$port" >&2
        exit 1
      fi
      if curl --noproxy '*' --silent --show-error --http2-prior-knowledge --connect-timeout 5 --max-time 10 \
        --request POST --header 'content-type: application/grpc' --header 'te: trailers' \
        --data-binary "@$health_request" --output /dev/null \
        "http://${ip}:${port}/grpc.health.v1.Health/Check" >/dev/null 2>&1; then
        echo "instance-manager $name accepted plaintext HTTP/2 gRPC transport on $ip:$port" >&2
        exit 1
      fi
      tested=$((tested + 1))
    done
  done < <(kubectl -n "$LONGHORN_NAMESPACE" get instancemanagers.longhorn.io -o json | jq -r '.items[] | select(.status.currentState == "running") | [.metadata.name, .status.ip] | @tsv')

  ((tested > 0)) || { echo "no running instance-manager gRPC services were tested" >&2; exit 1; }
  local attestation certificate_uid
  attestation="$(printf '%s-longhorn-grpc-mtls' "$RELEASE_NAME" | cut -c1-63 | sed 's/-$//')"
  certificate_uid="$(kubectl -n "$LONGHORN_NAMESPACE" get certificate longhorn-grpc-tls -o jsonpath='{.metadata.uid}')"
  kubectl -n "$LONGHORN_NAMESPACE" delete configmap "$attestation" --ignore-not-found --wait=true >/dev/null
  kubectl -n "$LONGHORN_NAMESPACE" create configmap "$attestation" \
    --from-literal=result=pass \
    --from-literal=certificateUID="$certificate_uid" \
    --from-literal=certificateNotBefore="$not_before" \
    --from-literal=authenticatedServices="$tested" \
    --from-literal=unauthenticatedTLSRejected="$tested" \
    --from-literal=plaintextRejected="$tested" >/dev/null
  kubectl -n "$LONGHORN_NAMESPACE" label configmap "$attestation" \
    platform.iterabase.com/evidence=longhorn-grpc-mtls \
    platform.iterabase.com/storage-contract-version=HOR-469-v1 >/dev/null
  echo "longhorn-grpc-mtls=pass attestation=$attestation authenticatedServices=$tested unauthenticatedTLSRejected=$tested plaintextRejected=$tested"
}

if [[ "$INTERNAL_TLS_ENABLED" == "true" ]]; then
  validate_grpc_mtls
elif [[ "$INTERNAL_TLS_ENABLED" != "false" ]]; then
  echo "ITERABASE_INTERNAL_TLS_ENABLED must be true or false" >&2
  exit 2
fi

required_nodes=1
comparison="eq"
if [[ "$TOPOLOGY" == "three-node" ]]; then
  required_nodes=3
  comparison="ge"
elif [[ "$TOPOLOGY" != "single-node" ]]; then
  echo "unsupported managed topology: $TOPOLOGY" >&2
  exit 2
fi

last=""
while ((SECONDS < DEADLINE)); do
  kubernetes="$(kubectl get nodes -o json)"
  longhorn="$(kubectl -n "$LONGHORN_NAMESPACE" get nodes.longhorn.io -o json 2>/dev/null || printf '{"items":[]}')"
  ready_nodes="$(jq '[.items[] | select(.spec.unschedulable != true) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))] | length' <<<"$kubernetes")"
  storage_nodes="$(jq '[.items[] | select(.spec.allowScheduling != false) | select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) | select(any(.status.conditions[]?; .type == "Schedulable" and .status == "True"))] | length' <<<"$longhorn")"
  last="topology=${TOPOLOGY} readyNodes=${ready_nodes} storageNodes=${storage_nodes} required=${required_nodes}"
  if [[ "$comparison" == eq && "$ready_nodes" -eq "$required_nodes" && "$storage_nodes" -eq "$required_nodes" ]]; then
    echo "managed-profile=pass $last"
    exec /bin/bash /contract/hor-424-rwx-conformance.sh
  fi
  if [[ "$comparison" == ge && "$ready_nodes" -ge "$required_nodes" && "$storage_nodes" -ge "$required_nodes" ]]; then
    echo "managed-profile=pass $last replicas=3 distinct-node-anti-affinity=required"
    exec /bin/bash /contract/hor-424-rwx-conformance.sh
  fi
  sleep 5
done

echo "managed profile did not become eligible before timeout: $last" >&2
kubectl get nodes -o wide >&2 || true
kubectl -n "$LONGHORN_NAMESPACE" get nodes.longhorn.io -o yaml >&2 || true
exit 1
