CHARTS_DIR := charts
CHARTS := $(notdir $(wildcard $(CHARTS_DIR)/*))
UMBRELLA := iterabase-platform
CERT_MANAGER_SUBSTRATE := cert-manager-substrate
CONTROLPLANE := control-plane
OBSERVABILITY := observability
RENDER := /tmp/$(UMBRELLA).rendered.yaml
RENDER_CP := /tmp/$(CONTROLPLANE).rendered.yaml
RENDER_TLS := /tmp/$(UMBRELLA).tls.rendered.yaml
RENDER_OBS := /tmp/$(UMBRELLA).observability.rendered.yaml

.PHONY: build-deps lint template kubeconform template-controlplane kubeconform-controlplane template-tls kubeconform-tls template-observability kubeconform-observability check-certificate-substrate check-service-selectors check-redis-exporter-auth check-artifact-config check-tool-runner check-manager-contract check check-tls check-observability clean

# control-plane has its own file:// dep (postgresql) and observability has its
# own upstream deps (kube-prometheus-stack + loki); build them first so the
# umbrella vendors each with its nested dependencies baked in.
build-deps:
	helm dependency build $(CHARTS_DIR)/$(CONTROLPLANE)
	helm dependency build $(CHARTS_DIR)/$(OBSERVABILITY)
	helm dependency build $(CHARTS_DIR)/$(CERT_MANAGER_SUBSTRATE)
	helm dependency build $(CHARTS_DIR)/$(UMBRELLA)

lint: build-deps
	@for c in $(CHARTS); do echo ":: helm lint $$c"; helm lint $(CHARTS_DIR)/$$c || exit 1; done

template: build-deps
	helm template $(UMBRELLA) $(CHARTS_DIR)/$(UMBRELLA) > $(RENDER)
	@echo "rendered $(RENDER)"

kubeconform: template
	# The umbrella now renders the control-plane's CRDs (enabled by default);
	# kubeconform's bundled schema set doesn't resolve apiextensions CRDs.
	kubeconform -strict -kubernetes-version 1.31.0 -ignore-missing-schemas $(RENDER)

# The umbrella keeps control-plane disabled by default, so validate the
# control-plane chart standalone (renders with its own enabled=true default).
template-controlplane: build-deps
	helm template $(CONTROLPLANE) $(CHARTS_DIR)/$(CONTROLPLANE) > $(RENDER_CP)
	@echo "rendered $(RENDER_CP)"

kubeconform-controlplane: template-controlplane
	# The chart renders a CRD (kubebuilder-generated, sourced verbatim from the
	# control-plane repo); kubeconform's bundled schema set does not resolve the
	# apiextensions CRD schema, so ignore missing schemas rather than failing.
	kubeconform -strict -kubernetes-version 1.31.0 -ignore-missing-schemas $(RENDER_CP)

# Static check with internal TLS on (values-tls.yaml flips global.internalTLS).
# Catches conditional/render errors in the TLS-on path that the default
# (plaintext) `make check` doesn't exercise. cert-manager Certificate/
# ClusterIssuer CRs are ignored (no bundled schema), like the CRDs above.
template-tls: build-deps
	helm template $(UMBRELLA) $(CHARTS_DIR)/$(UMBRELLA) -f values-tls.yaml > $(RENDER_TLS)
	@echo "rendered $(RENDER_TLS)"

kubeconform-tls: template-tls
	kubeconform -strict -kubernetes-version 1.31.0 -ignore-missing-schemas $(RENDER_TLS)

check-certificate-substrate: build-deps
	./scripts/check-certificate-substrate.sh

check-service-selectors:
	./scripts/check-service-selectors.sh

check-redis-exporter-auth:
	./scripts/check-redis-exporter-auth.sh

check-artifact-config:
	./scripts/check-artifact-config.sh

check-tool-runner:
	./scripts/check-tool-runner.sh

check-manager-contract:
	./scripts/check-manager-contract.sh

check: lint kubeconform kubeconform-controlplane kubeconform-observability check-certificate-substrate check-service-selectors check-redis-exporter-auth check-artifact-config check-tool-runner check-manager-contract

check-tls: kubeconform-tls check-redis-exporter-auth

# Static check with the observability stack + every component's metrics on
# (values-observability.yaml). Catches conditional/render errors in the
# ServiceMonitor / PodMonitor / PrometheusRule / exporter paths that the
# default (stack off) `make check` doesn't exercise. The Prometheus Operator
# CRDs (ServiceMonitor / PodMonitor / PrometheusRule) have no bundled schema, so
# ignore missing schemas like the platform CRDs above.
template-observability: build-deps
	helm template $(UMBRELLA) $(CHARTS_DIR)/$(UMBRELLA) -f values-observability.yaml > $(RENDER_OBS)
	@echo "rendered $(RENDER_OBS)"

kubeconform-observability: template-observability
	kubeconform -strict -kubernetes-version 1.31.0 -ignore-missing-schemas $(RENDER_OBS)

check-observability: kubeconform-observability

clean:
	rm -f $(RENDER) $(RENDER_CP) $(RENDER_OBS)
	rm -rf $(CHARTS_DIR)/$(UMBRELLA)/charts $(CHARTS_DIR)/$(CERT_MANAGER_SUBSTRATE)/charts $(CHARTS_DIR)/$(CONTROLPLANE)/charts $(CHARTS_DIR)/$(OBSERVABILITY)/charts
