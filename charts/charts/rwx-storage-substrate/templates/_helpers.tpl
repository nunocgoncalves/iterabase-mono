{{- define "rwx-storage-substrate.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rwx-storage-substrate.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "rwx-storage-substrate.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "rwx-storage-substrate.labels" -}}
app.kubernetes.io/name: {{ include "rwx-storage-substrate.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
platform.iterabase.com/storage-mode: managed-longhorn
{{- end -}}

{{- define "rwx-storage-substrate.validate" -}}
{{- $rwx := required "storage.rwx is required" .Values.storage.rwx -}}
{{- if ne $rwx.mode "managed-longhorn" -}}
{{- fail "rwx-storage-substrate may be installed only with storage.rwx.mode=managed-longhorn" -}}
{{- end -}}
{{- if ne $rwx.storageClassName "iterabase-rwx" -}}
{{- fail "managed-longhorn requires storage.rwx.storageClassName=iterabase-rwx" -}}
{{- end -}}
{{- $managed := required "storage.rwx.managedLonghorn is required in managed-longhorn mode" $rwx.managedLonghorn -}}
{{- if not (has $managed.topology (list "single-node" "three-node")) -}}
{{- fail "storage.rwx.managedLonghorn.topology must be single-node or three-node" -}}
{{- end -}}
{{- if not .Values.longhorn.enabled -}}
{{- fail "the managed companion requires its pinned longhorn dependency" -}}
{{- end -}}
{{- if .Values.longhorn.persistence.createStorageClass -}}
{{- fail "the upstream longhorn StorageClass must remain disabled; iterabase-rwx is chart-owned" -}}
{{- end -}}
{{- if or .Values.longhorn.persistence.defaultClass .Values.longhorn.ingress.enabled .Values.longhorn.httproute.enabled -}}
{{- fail "Longhorn default-class and UI ingress/HTTPRoute exposure are unsupported" -}}
{{- end -}}
{{- if ne .Values.longhorn.global.imageRegistry "" -}}{{- fail "Longhorn global image registry is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.engine.repository "longhornio/longhorn-engine" -}}{{- fail "Longhorn engine repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.manager.repository "longhornio/longhorn-manager" -}}{{- fail "Longhorn manager repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.ui.repository "longhornio/longhorn-ui" -}}{{- fail "Longhorn UI repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.instanceManager.repository "longhornio/longhorn-instance-manager" -}}{{- fail "Longhorn instance-manager repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.shareManager.repository "longhornio/longhorn-share-manager" -}}{{- fail "Longhorn share-manager repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.backingImageManager.repository "longhornio/backing-image-manager" -}}{{- fail "Longhorn backing-image-manager repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.supportBundleKit.repository "longhornio/support-bundle-kit" -}}{{- fail "Longhorn support-bundle repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.engine.tag "v1.12.1@sha256:9b1b720b56df6612c9589cbc156acbca6419fa61de818d05db7226b0722f2868" -}}{{- fail "Longhorn engine image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.manager.tag "v1.12.1@sha256:83b79f57043fe1405e68bc0d4c7987accbc6bb512def3e0db12b31966c070801" -}}{{- fail "Longhorn manager image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.ui.tag "v1.12.1@sha256:03a3ce6673df6e948c261fe978a695adaa8fb190d68bfe5c358af8ee3d3fbef5" -}}{{- fail "Longhorn UI image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.instanceManager.tag "v1.12.1@sha256:b255f3279dd9d830ea153e9369928646dee519fd853036388926dddb5c66094b" -}}{{- fail "Longhorn instance-manager image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.shareManager.tag "v1.12.1@sha256:efaf47aeb4e8615e312f0880df860bf2e5b9fa53006fe075f057c6dd4089f47d" -}}{{- fail "Longhorn share-manager image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.backingImageManager.tag "v1.12.1@sha256:dfb9452e4190fb80e39c7976a0036ac0ca314c05328b67952f8c165cbb4dabf3" -}}{{- fail "Longhorn backing-image-manager identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.longhorn.supportBundleKit.tag "v0.0.92@sha256:02baa824d9a4174747ab9db2635ae000b1198d2d5ed3a4c69caf28724224e783" -}}{{- fail "Longhorn support-bundle image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.attacher.repository "longhornio/csi-attacher" -}}{{- fail "Longhorn CSI attacher repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.provisioner.repository "longhornio/csi-provisioner" -}}{{- fail "Longhorn CSI provisioner repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.nodeDriverRegistrar.repository "longhornio/csi-node-driver-registrar" -}}{{- fail "Longhorn CSI registrar repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.resizer.repository "longhornio/csi-resizer" -}}{{- fail "Longhorn CSI resizer repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.snapshotter.repository "longhornio/csi-snapshotter" -}}{{- fail "Longhorn CSI snapshotter repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.livenessProbe.repository "longhornio/livenessprobe" -}}{{- fail "Longhorn CSI liveness repository is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.attacher.tag "v4.12.0@sha256:a814aa4784197116983ea13e376fc691e000a390de9d0b9fca2bc4a2fb7c4a1f" -}}{{- fail "Longhorn CSI attacher image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.provisioner.tag "v5.3.0@sha256:1bbb7b11d8087130e722e3249f364d0ab49ee3545e847c2f299e87b7e1ce5c4f" -}}{{- fail "Longhorn CSI provisioner image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.nodeDriverRegistrar.tag "v2.17.0@sha256:29f7cfd519008fe8f8dff5e79db43f70d65c43a89c08f1bafbb199ca90df79f0" -}}{{- fail "Longhorn CSI registrar image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.resizer.tag "v2.2.1@sha256:63d0aef25114d4a682b25afa6d9623a3cfcc19aca910269124408476bbe2c6fd" -}}{{- fail "Longhorn CSI resizer image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.snapshotter.tag "v8.6.0@sha256:2bca9ac55170efa61dc50e5cc8d9550373db2e3e5161d82d3fdaac5c25150360" -}}{{- fail "Longhorn CSI snapshotter image identity is chart-owned" -}}{{- end -}}
{{- if ne .Values.longhorn.image.csi.livenessProbe.tag "v2.19.0@sha256:d0cb76b565ba9d36da0dc2b38e2b6a49a0ae4fe067b03086110682f32c600318" -}}{{- fail "Longhorn CSI liveness image identity is chart-owned" -}}{{- end -}}
{{- if not .Values.validation.enabled -}}
{{- fail "managed RWX conformance and uninstall validation cannot be disabled" -}}
{{- end -}}
{{- if or (ne .Values.validation.image.repository "docker.io/alpine/k8s") (ne (toString .Values.validation.image.tag) "1.34.1") (ne .Values.validation.image.digest "sha256:ec714df3813b5405292860f8a1c55c5727bf8c33c88992f1e981efad8065547f") -}}
{{- fail "managed RWX validation image identity is chart-owned" -}}
{{- end -}}
{{- if not .Values.longhorn.networkPolicies.enabled -}}
{{- fail "Longhorn internal NetworkPolicies must remain enabled" -}}
{{- end -}}
{{- if not .Values.longhorn.networkPolicies.restrictInternalTraffic -}}
{{- fail "Longhorn internal traffic restriction must remain enabled" -}}
{{- end -}}
{{- if ne .Values.longhorn.networkPolicies.type "k3s" -}}
{{- fail "managed Longhorn NetworkPolicies must use type=k3s" -}}
{{- end -}}
{{- if not .Values.longhorn.defaultSettings.createDefaultDiskLabeledNodes -}}
{{- fail "managed Longhorn disks must be explicitly selected by node label" -}}
{{- end -}}
{{- if ne .Values.longhorn.defaultSettings.defaultDataPath "/var/lib/longhorn" -}}
{{- fail "managed Longhorn defaultDataPath must remain /var/lib/longhorn" -}}
{{- end -}}
{{- if ne (toString .Values.longhorn.defaultSettings.replicaSoftAntiAffinity) "false" -}}
{{- fail "replicaSoftAntiAffinity must remain false" -}}
{{- end -}}
{{- if ne (int .Values.longhorn.defaultSettings.storageOverProvisioningPercentage) 100 -}}
{{- fail "storageOverProvisioningPercentage must remain 100" -}}
{{- end -}}
{{- if lt (int .Values.longhorn.defaultSettings.storageMinimalAvailablePercentage) 25 -}}
{{- fail "storageMinimalAvailablePercentage must remain at least 25" -}}
{{- end -}}
{{- if ne (toString .Values.longhorn.defaultSettings.allowVolumeCreationWithDegradedAvailability) "false" -}}
{{- fail "degraded volume creation must remain disabled" -}}
{{- end -}}
{{- if or .Values.longhorn.defaultSettings.v2DataEngine .Values.longhorn.defaultSettings.rwxVolumeFastFailover -}}
{{- fail "Longhorn V2 data engine and experimental RWX fast failover must remain disabled" -}}
{{- end -}}
{{- if not .Values.longhorn.defaultSettings.v1DataEngine -}}
{{- fail "Longhorn V1 data engine must remain enabled" -}}
{{- end -}}
{{- if .Values.longhorn.defaultSettings.deletingConfirmationFlag -}}
{{- fail "deletingConfirmationFlag must remain false during normal reconciliation" -}}
{{- end -}}
{{- if ne .Values.longhorn.persistence.nfsOptions "" -}}
{{- fail "nfsOptions must remain unset so Longhorn 1.12.1 owns the tested defaults" -}}
{{- end -}}
{{- end -}}
