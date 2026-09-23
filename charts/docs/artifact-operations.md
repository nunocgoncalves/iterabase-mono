# Immutable artifact operations (HOR-399)

The installation is the tenant boundary. Artifact metadata is in Postgres and
immutable bytes are in the `iterabase-artifacts` MinIO bucket. Only the
control-plane API/gateway receive the dedicated bucket credential. Do not copy
MinIO credentials into AgentPool, supervisor, runner, or sandbox configuration.

## Install validation

```sh
kubectl -n <namespace> rollout status statefulset/<release>-minio
kubectl -n <namespace> wait --for=condition=complete job \
  -l app.kubernetes.io/component=artifact-provisioner --timeout=5m
kubectl -n <namespace> get secret <release>-minio-artifacts
kubectl -n <namespace> rollout status deployment/<release>-control-plane-api
kubectl -n <namespace> rollout status deployment/<release>-control-plane-gateway
kubectl -n <namespace> rollout status daemonset/cert-manager-csi-driver
kubectl get csidriver csi.cert-manager.io
kubectl wait --for=condition=Ready clusterissuer/platform-spiffe-ca --timeout=2m
```

The versioned artifact-provisioner is an ordinary Helm-owned Job, not a hook.
Its completed Job/Pod is deliberately retained without a TTL for the active
MinIO subchart revision so a later `helm upgrade --wait` or exact reapply can
continue to observe the same readiness resource. Treat its `Complete` condition,
Helm ownership labels, and UID as lifecycle evidence; do not delete or recreate
it while the release is active.

The workload ArtifactService is available only through the mandatory-mTLS
`<release>-control-plane-gateway:8090` Service. AgentPools use that Service as
their `toolGateway`, trust the chart-generated
`<release>-control-plane-gateway-ca`, and receive leaves from the
`platform-spiffe-ca` ClusterIssuer through the umbrella's pinned
`cert-manager-csi-driver`. The gateway and API mount the bucket-scoped artifact
Secret; supervisors mount only their workload leaf and CA chain.

Validate through the artifact API rather than MinIO:

```sh
printf 'artifact round trip' > /tmp/artifact.txt
curl -fsS -H "Authorization: Bearer $WORK_API_KEY" \
  -H 'Content-Type: text/plain' \
  --data-binary @/tmp/artifact.txt \
  https://<control-plane>/v1/artifacts > /tmp/artifact.json
ARTIFACT_ID=$(jq -r .artifactId /tmp/artifact.json)
curl -fsS -H "Authorization: Bearer $WORK_API_KEY" \
  "https://<control-plane>/v1/artifacts/$ARTIFACT_ID" | cmp - /tmp/artifact.txt
```

Record the chart revision, artifact ID, canonical SHA-256 digest, and command
results as deployment evidence. Never record API keys or MinIO credentials.

## Explicit deletion

Explicit deletion is admin-only. It immediately makes reads unavailable,
removes MinIO bytes, and retains a metadata tombstone and historical work links.

```sh
curl -fsS -X DELETE -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://<control-plane>/v1/artifacts/$ARTIFACT_ID"
```

A subsequent work-scope `GET` must return `410 Gone`. Retention expiry uses the
same lifecycle automatically. `retention_until = NULL` means indefinite.
