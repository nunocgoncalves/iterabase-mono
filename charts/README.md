# Iterabase charts

> Canonical source: [`iterabase-mono/charts`](https://github.com/nunocgoncalves/iterabase-mono/tree/master/charts). The former standalone source repository is historical and read-only; the existing `ghcr.io/nunocgoncalves/iterabase-charts` package namespace remains the stable artifact identity.

Helm charts for the [iterabase](https://iterabase.com) platform. The `cert-manager-substrate` release establishes certificate CRDs, webhook, controller, and CSI driver before the `iterabase-platform` umbrella deploys application workloads. [Forge](https://github.com/nunocgoncalves/iterabase-mono/tree/master/forge) enforces this release ordering automatically; direct Helm users install the two same-version artifacts in order.

## Charts

| Chart | Description | Released individually |
|---|---|---|
| `cert-manager-substrate` | Ordered certificate operator, CRDs, webhook, and CSI substrate | ✅, alongside platform |
| `iterabase-platform` | Application umbrella — composes all platform components | ✅ |
| `inference-gateway` | Model-access service | ✅ |
| `control-plane` | Durable workflow/control APIs, operator, and immutable artifact service | ✅ |
| `postgresql` | Self-contained Postgres on the official image | bundled only |
| `redis` | Self-contained Redis (hot-path cache) | bundled only |
| `minio` | Self-contained MinIO object storage | bundled only |
| `cert-issuers` | cert-manager ClusterIssuers (Let's Encrypt DNS-01/Cloudflare + self-signed) | bundled only |
| `metallb-config` | MetalLB IPAddressPool + L2Advertisement (L2 edge for bare-metal/kind/OPO1) | bundled only |
| `observability` | Prometheus + Grafana + Loki + Alertmanager (kube-prometheus-stack + loki) + default alert rules + GPU (DCGM) scraping | bundled only |

control-plane ships standalone and is enabled in the umbrella by default (it provides the shared pgvector Postgres + the schemas the gateway reads).

## Install

Install the same-version certificate substrate first and wait for its webhook;
then install the platform. The gateway is the only public endpoint, served over
HTTPS by the platform edge (ingress-nginx + cert-manager-issued leaves). The
edge is always a **LoadBalancer** Service —
no hostNetwork. The LB implementation is pluggable:

- **kind/dev** — MetalLB L2 with a pool in the kind docker-bridge subnet. Clone
  `iterabase-mono`, change into its `charts/` directory, and use the
  `values-kind.yaml` preset:
  ```sh
  helm install iterabase-cert-manager charts/cert-manager-substrate \
    -n iterabase-system --create-namespace --wait
  helm install iterabase charts/iterabase-platform -n iterabase-system \
    -f values-kind.yaml --set control-plane.toolRunner.enabled=false --wait
  ```
  then verify the self-signed edge against its generated certificate and DNS identity:
  ```sh
  LB_IP=$(kubectl get svc -n iterabase-system -l app.kubernetes.io/name=ingress-nginx \
    -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}')
  kubectl get secret iterabase-gateway-tls -n iterabase-system \
    -o jsonpath='{.data.tls\.crt}' | base64 -d >/tmp/iterabase-gateway.crt
  curl --cacert /tmp/iterabase-gateway.crt \
    --resolve gateway.iterabase.local:443:"$LB_IP" https://gateway.iterabase.local/health
  ```
- **bare-metal/OPO1** — MetalLB L2 with a real pool (e.g. a VLAN range); see the
  prod overlay below.
- **cloud** — leave MetalLB disabled and set provider annotations on
  `ingress-nginx.controller.service` so the cloud LB provisions the Service.

Get the generated gateway admin API key:

```sh
kubectl get secret iterabase-gateway-admin -n iterabase-system \
  -o jsonpath='{.data.adminApiKey}' | base64 -d
```

Production (OPO1, IPv6-only origin + Cloudflare-proxied dual-stack) — override in
your values/overlay:

```sh
helm install iterabase-cert-manager \
  oci://ghcr.io/nunocgoncalves/iterabase-charts/cert-manager-substrate \
  --version 0.3.0 -n iterabase-system --create-namespace --wait
helm install iterabase oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform \
  --version 0.3.0 -n iterabase-system \
  --set inference-gateway.ingress.host=gateway.opo1.example.com \
  --set inference-gateway.ingress.tls.clusterIssuer=letsencrypt-prod \
  --set ingress-nginx.controller.service.ipFamilyPolicy=SingleStack \
  --set ingress-nginx.controller.service.ipFamilies[0]=IPv6 \
  --set metallb.enabled=true \
  --set metallb-config.enabled=true \
  --set metallb-config.addresses[0]=2001:db8:30::/64 \
  --set cert-issuers.letsencrypt.enabled=true \
  --set cert-issuers.letsencrypt.email=you@example.com \
  --set external-dns.enabled=true \
  --set external-dns.domainFilters[0]=opo1.example.com.
```

(The Cloudflare API-token Secret shared by cert-issuers + external-dns must be
provisioned out-of-band — see the umbrella `values.yaml` comments. For IPv4-first
clients, set `ipFamilies[0]=IPv4` and an IPv4 `metallb-config.addresses` pool.)

### Private observability ingress

A deployment can expose Grafana, Prometheus, Alertmanager, and Loki through a
dedicated ingress plane without publishing those routes through the public
ingress plane. Reachability, DNS, and authorization are customer/deployment
policy owned by the overlay; the reusable chart does not prescribe a universal
exposure model. This private-network example enables the aliased
`internal-ingress-nginx` dependency, adds a named MetalLB pool, and binds the
private controller's IPv4 LoadBalancer Service to that pool explicitly:

```yaml
metallb-config:
  additionalPools:
    - name: internal
      addresses: ["10.0.20.200-10.0.20.215"]
      autoAssign: false
      interfaces: [eth0]
internal-ingress-nginx:
  enabled: true
  controller:
    service:
      annotations:
        metallb.io/address-pool: <release>-internal
        metallb.io/loadBalancerIPs: 10.0.20.200
```

Set each upstream observability ingress to `ingressClassName: nginx-internal`.
Use cert-manager's DNS-01 issuer for publicly trusted leaves and annotate each
Ingress with `external-dns.alpha.kubernetes.io/cloudflare-proxied: "false"` so
the resulting `A` record points directly at the private address. Prometheus,
Alertmanager, and Grafana serve internal-CA HTTPS; their Ingresses must use the
`backend-protocol`, `proxy-ssl-secret`, `proxy-ssl-verify`, and
`proxy-ssl-name` annotations shown in [`values-internal-observability.yaml`](values-internal-observability.yaml).
Loki's Ingress targets its HTTP gateway, which independently verifies the
internal-CA HTTPS hop to Loki.

For this example policy, the trusted private network/VPN is the authorization
boundary for the direct Prometheus, Alertmanager, and Loki APIs; Grafana keeps
its own login. A customer deployment must choose and approve its own network
reachability and authentication policy in the overlay. Do not use this recipe
on an untrusted LAN, and do not place these routes on the public `nginx` class.
The complete documentation-address fixture is rendered and validated by
`make check-internal-observability`.

MetalLB configuration CRs remain post-install/post-upgrade hooks so a fresh
umbrella install can establish the operator CRDs first. If an additional pool
is removed from values or the platform is rolled back to a version without it,
delete its inert hook resources explicitly after the LoadBalancer Service is
gone: `kubectl delete ipaddresspool,l2advertisement <release>-internal -n
<namespace>`. With `autoAssign: false`, an unselected leftover pool cannot be
consumed by another Service.

### Upgrade from platform 0.2.2 or earlier

Platform 0.2.2 bundled cert-manager and the CSI driver in the platform Helm
release. **Do not install the companion substrate first** on an existing
installation: its resources intentionally have the same names, and Helm will
reject the second release's ownership metadata. Use this staged hand-off, with
the deployment's normal values arguments in both platform upgrades:

```sh
platform_values=(-f /path/to/current-values.yaml) # add the deployment's usual --set options too

# 1. Upgrade the old owner first. Existing certificate Secrets remain valid;
#    defer the newly introduced runner until the substrate is restored.
helm upgrade iterabase \
  oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform \
  --version 0.3.0 -n iterabase-system "${platform_values[@]}" \
  --set control-plane.toolRunner.enabled=false --wait

# 2. The old chart keeps its CRDs. Transfer those six retained objects to the
#    companion release, then install the new owner.
cert_crds=$(kubectl get crd -l app.kubernetes.io/name=cert-manager -o name)
kubectl annotate --overwrite $cert_crds \
  meta.helm.sh/release-name=iterabase-cert-manager \
  meta.helm.sh/release-namespace=iterabase-system
helm install iterabase-cert-manager \
  oci://ghcr.io/nunocgoncalves/iterabase-charts/cert-manager-substrate \
  --version 0.3.0 -n iterabase-system --wait

# 3. Reconcile the intended values now that the operator and CSI driver are Ready.
helm upgrade iterabase \
  oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform \
  --version 0.3.0 -n iterabase-system "${platform_values[@]}" --wait
```

Forge detects a pre-0.3 platform release and performs this same platform-first
hand-off before returning to the normal substrate-first order. The companion
release name must be `<platform-release>-cert-manager`; the examples therefore
pair `iterabase` with `iterabase-cert-manager`.

Rollback uses the inverse hand-off. First uninstall the companion release, then
rollback the platform to its pre-0.3 revision; a direct rollback collides with
the still-owned companion resources:

```sh
helm uninstall iterabase-cert-manager -n iterabase-system --wait
cert_crds=$(kubectl get crd -l app.kubernetes.io/name=cert-manager -o name)
kubectl annotate --overwrite $cert_crds \
  meta.helm.sh/release-name=iterabase \
  meta.helm.sh/release-namespace=iterabase-system
helm rollback iterabase <pre-0.3-revision> -n iterabase-system --wait
```

The chart-owned compiled Kind scenario exercises both directions against the
released 0.2.2 chart via `make test-e2e-certificate-migration`.

### Enabling an operator-backed dependency during upgrade

Helm does not install CRDs from a dependency that was disabled on the original
release. Before enabling observability—or another operator-backed dependency—
extract CRDs from the **exact target chart archive**, select the authoritative
schema for duplicate CRDs using its operator-owned version annotation, apply
those CRDs server-side, and wait for every CRD to become `Established` before
running `helm upgrade`. Forge performs this sequence automatically. Direct Helm
operators must perform the same ordered operation; applying the regular custom
resources first can fail during REST mapping before any chart hook executes.

The chart-owned `test/e2e/transition-baselines.json` currently declares platform
and substrate `0.3.12` as the checksum-pinned supported predecessor for current
`0.3.15`. The supported inverse boundary is current → that declared predecessor
within the post-0.3 companion-ownership model, followed by a current forward
recovery. Roll back the platform release before the companion substrate. CRDs,
generated Secrets, and PVCs are retained. The separate pre-0.3 ownership
handoff above remains mandatory; arbitrary-version rollback safety is not
claimed.

Run `make test-e2e-feature-enable` for the absent-CRD path and
`make test-e2e-reapply-rollback` for idempotent reapply plus inverse/forward
recovery evidence.

## Flux-backed gateway tool runner

The control-plane chart can deploy the HOR-397 Node 24 runner as a two-container
pod. Its materializer receives a projected, read-only Kubernetes token limited
to `get` on the configured Flux `GitRepository`; the runner receives the mTLS
SPIFFE leaf but no Kubernetes or Git credential. The materializer verifies the
exact Flux artifact digest and writes immutable generation directories. The
runner mounts those files read-only, validates all manifests/bundles atomically,
and connects outbound to the tool gateway. It exposes no inbound execution API;
when `control-plane.metrics.enabled=true` (or the narrower
`control-plane.toolRunner.metrics.enabled=true`), a metrics-only ServiceMonitor
scrapes separate materializer and runner `/metrics` endpoints, matching the inference
gateway observability pattern.

Configure exact dotted tool-name namespaces through
`control-plane.toolRunner.allowedToolNamespaces`; wildcards are not supported.
Defaults retain at most eight generations / 512 MiB and drain old pins for up to
24 hours. Invalid revisions leave the last valid generation serving. Product and
client bundle authoring is documented in the overlay's `tools/README.md`.

A valid, Ready Flux `GitRepository` named `overlay` must exist before installing
with the runner enabled because readiness intentionally requires a validated
first generation. Forge establishes and gates on the exact source revision and
digest before Helm. Generic chart-only CI disables the runner because it has no
cross-repository runner image or Flux source; the dedicated kind+Flux contract
covers the enabled runtime path.

**Dispatch (Work server).** The control-plane chart deploys the durable Work
bidi-stream gRPC server (HOR-249) at `control-plane.dispatch.*`, enabled by
default in the umbrella. AgentPool/warm-agent workers connect over mTLS, receive
durable graph-node assignments and return completion events. Its serving leaf is
signed by the shared platform workload CA (`platform-spiffe-ca`) with the
Dispatch Service name as a SAN, so a worker's `controlPlane.serverName` matches
it and its client CA verifies the worker's SPIFFE leafs. The default model
permission (`control-plane.dispatch.defaultModel.id/api`) is
**customer/overlay-specific** and is set by the overlay — a dispatch enabled
without one fails closed at startup (it never emits an empty model permission),
so the shared umbrella does not hardcode a model.

## Immutable artifacts

The MinIO chart provisions `iterabase-artifacts` plus a dedicated bucket-scoped
credential consumed only by the control-plane API/gateway. Sandboxes and tool
runners have no object-store credential or direct route. Retention is indefinite
unless `control-plane.artifact.defaultRetention` is configured. See
[`docs/artifact-operations.md`](docs/artifact-operations.md) for round-trip and
explicit deletion validation.

## Develop

```sh
make check                  # Helm lint/template + kubeconform + static contracts
make check-tls              # TLS presets, including observability + TLS together
make test-e2e-unit          # compiled suite + intentional break fixtures (no cluster)
make test-e2e-install       # one fresh Kind cluster
make test-e2e-upgrade       # checksum-pinned N-1 -> exact current transition
make test-e2e-feature-enable # disabled operator dependency -> CRDs -> current
make test-e2e-reapply-rollback # idempotent reapply + inverse/forward recovery
make test-e2e-observability
make test-e2e-observability-tls
make test-e2e-internal-tls
make test-e2e-certificate-migration
```

The runtime targets are chart-owned typed Go scenarios built on `testkit/e2e`.
Each target creates exactly one isolated Kind cluster, runs once without retries,
and collects shared redacted diagnostics on failure. See
[`test/e2e/README.md`](test/e2e/README.md) for scenario and fixture contracts.

Requires `helm` and `kubeconform`. Add the external repos first:

```sh
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo add metallb https://metallb.github.io/metallb
helm repo add jetstack https://charts.jetstack.io
helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/
```

`make build-deps` resolves the platform's local and upstream dependencies plus the companion substrate's pinned `cert-manager` and `cert-manager-csi-driver` charts. The substrate contains no cert-manager custom resources, so Helm can establish and wait for the operator before the platform release submits issuers and Certificates.

## Observability (HOR-408)

The optional `observability` subchart (disabled by default) deploys Prometheus +
Grafana + Loki + Alertmanager via `kube-prometheus-stack` + `loki`, with the
Prometheus Operator CRDs (`ServiceMonitor` / `PodMonitor` / `PrometheusRule`),
PV-backed storage, default alert rules, and a Loki Grafana datasource. Enable it
with the preset:

```sh
helm install iterabase charts/iterabase-platform -n iterabase-system --create-namespace \
  -f values-kind.yaml -f values-observability.yaml --wait
```

The preset flips the stack on **and** every component's `metrics.enabled` knob,
so Prometheus scrapes inference-gateway; control-plane API, manager, gateway,
dispatch, harness workers, tool runner, and materializer; vLLM model-backend
pods; Postgres/Redis exporters; MinIO; and enabled upstream substrate targets.
Dedicated metrics listeners keep customer ingress and mandatory-mTLS workload
listeners isolated.
GPU metrics: set `observability.dcgmExporter.enabled=true` (gpu-operator must be
installed out-of-band). Alertmanager **email routing is overlay-owned** — the
chart ships a null-receiver default; set
`observability.kube-prometheus-stack.alertmanager.config` in the prod overlay
(HOR-408: the OPO1 overlay carries the email receiver).

Grafana provisions the immutable seven-dashboard Iterabase production suite
under `Iterabase`, focused component dashboards under `Infrastructure` and
`Observability`, and bundled kube-prometheus-stack dashboards under `Kubernetes`.
Dashboard source, stable UIDs, ordering, and provenance live
under `charts/observability/dashboards`; runtime Internet imports are forbidden.
The chart also ships bounded recording rules, invariant production alerts, and
runbook links. `up == 0` coverage is paired with explicit per-component absence
checks under `observability.alerts.expectedTargets`, so a vanished Pod, Service
endpoint, or monitor still alerts. Disable expectations for intentionally omitted
core components, and enable the `harness` / `modelBackend` expectations when an
overlay creates those operator-managed workloads. Workload-specific performance
alerts remain off until an overlay sets accepted thresholds under
`observability.alerts.performance`.

The vLLM `PodMonitor` selects operator-created model-backend pods by their stable
label and named serving port. GPU panels require the optional DCGM target.

## Release

Raw tags do not publish. `Chart.yaml` is the chart version authority. Dispatch
the root **Release candidate** workflow with an explicit affected-target set
and an exact master SHA. It packages the selected chart archives once, builds
any selected component/Forge artifacts, and validates the coherent bundle
before retaining it as Actions artifacts. Then dispatch **Promote release**
with that successful run ID.
After founder approval in the protected `release` environment, the unchanged
archive is published. See [`../docs/release.md`](../docs/release.md).
