#!/usr/bin/env python3
"""Derive the bounded volume-only OpenEBS LVM chart from its reviewed archive.

The upstream 1.10.0 chart renders the CSI snapshotter unconditionally and
bundles broad snapshot CRDs/RBAC even when its VolumeSnapshot CRD switch is
disabled. HOR-545 exposes only dynamic LVM volume lifecycle. DES-HOR-545-05
retains the inert LVMSnapshot schema and exact driver list/watch needed by the
pinned driver's ordinary deletion guard; this fail-closed transform removes all
CSI/user snapshot surfaces and upstream write authority after archive checksum
verification and before packaging into the Iterabase wrapper.
"""

from __future__ import annotations

import hashlib
import os
from pathlib import Path
import shutil
import sys
import tarfile
import tempfile

SOURCE_SHA256 = "3ad766c56d4a0ab0f3f2baaeb726a4554d1f51bb485f1cef00846cf1d82a179d"
CHART_NAME = "lvm-localpv"


class TransformError(RuntimeError):
    pass


def replace_once(text: str, start: str, end: str, label: str) -> str:
    if text.count(start) != 1 or text.count(end) != 1:
        raise TransformError(f"upstream {label} anchors drifted")
    first = text.index(start)
    last = text.index(end, first)
    return text[:first] + text[last:]


def remove_yaml_block(text: str, key: str, indent: int, label: str) -> str:
    lines = text.splitlines(keepends=True)
    marker = " " * indent + key + ":"
    starts = [index for index, line in enumerate(lines) if line.rstrip("\r\n") == marker]
    if len(starts) != 1:
        raise TransformError(f"upstream {label} key drifted")
    start = starts[0]
    end = len(lines)
    for index in range(start + 1, len(lines)):
        stripped = lines[index].lstrip(" ")
        if not stripped.strip() or stripped.startswith("#"):
            continue
        current_indent = len(lines[index]) - len(stripped)
        if current_indent <= indent:
            end = index
            break
    return "".join(lines[:start] + lines[end:])


def read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise TransformError(f"cannot read upstream chart file {path}: {exc}") from exc


def write(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")


def extract_reviewed_chart(archive_path: Path, destination: Path) -> Path:
    digest = hashlib.sha256(archive_path.read_bytes()).hexdigest()
    if digest != SOURCE_SHA256:
        raise TransformError(
            f"OpenEBS LVM chart checksum mismatch: expected {SOURCE_SHA256}, got {digest}"
        )
    try:
        archive = tarfile.open(archive_path, "r:gz")
    except tarfile.TarError as exc:
        raise TransformError(f"invalid OpenEBS LVM chart archive: {exc}") from exc
    with archive:
        for member in archive.getmembers():
            parts = Path(member.name).parts
            if not parts or parts[0] != CHART_NAME or any(part in {"", ".", ".."} for part in parts):
                raise TransformError(f"unsafe or unexpected chart archive member {member.name!r}")
            target = destination.joinpath(*parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            if not member.isfile():
                raise TransformError(f"unsupported chart archive member {member.name!r}")
            source = archive.extractfile(member)
            if source is None:
                raise TransformError(f"unreadable chart archive member {member.name!r}")
            target.parent.mkdir(parents=True, exist_ok=True)
            with source, target.open("wb") as output:
                shutil.copyfileobj(source, output)
            os.chmod(target, member.mode & 0o777)
    chart = destination / CHART_NAME
    if not (chart / "Chart.yaml").is_file():
        raise TransformError("reviewed archive did not contain the expected lvm-localpv chart")
    return chart


def transform(chart: Path) -> None:
    controller = chart / "templates/lvm-controller.yaml"
    controller_text = replace_once(
        read(controller),
        "        - name: {{ .Values.lvmController.snapshotter.name }}\n",
        "        - name: {{ .Values.lvmController.provisioner.name }}\n",
        "controller snapshot sidecars",
    )
    write(controller, controller_text)

    rbac = chart / "templates/rbac.yaml"
    rbac_text = replace_once(
        read(rbac),
        "---\nkind: ClusterRole\napiVersion: rbac.authorization.k8s.io/v1\nmetadata:\n  name: openebs-lvm-snapshotter-role\n",
        "{{- end }}\n\n{{- if .Values.serviceAccount.lvmNode.create }}\n",
        "snapshot RBAC",
    )
    controller_resources = '''    resources: ["lvmvolumes", "lvmsnapshots", "lvmnodes"]
    verbs: ["*"]'''
    controller_volume_only = '''    resources: ["lvmvolumes", "lvmnodes"]
    verbs: ["*"]'''
    if rbac_text.count(controller_resources) != 1:
        raise TransformError("upstream controller LVMSnapshot RBAC references drifted")
    rbac_text = rbac_text.replace(controller_resources, controller_volume_only)

    node_resources = '''    resources: ["lvmvolumes", "lvmsnapshots", "lvmnodes"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]'''
    node_volume_only = '''    resources: ["lvmvolumes", "lvmnodes"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  # DES-HOR-545-05: the pinned node driver unconditionally starts an
  # LVMSnapshot informer. It may only observe the inert schema.
  - apiGroups: ["local.openebs.io"]
    resources: ["lvmsnapshots"]
    verbs: ["list", "watch"]'''
    if rbac_text.count(node_resources) != 1:
        raise TransformError("upstream node LVMSnapshot RBAC references drifted")
    rbac_text = rbac_text.replace(node_resources, node_volume_only)
    write(rbac, rbac_text)

    values = chart / "values.yaml"
    values_text = remove_yaml_block(read(values), "snapshotter", 2, "snapshotter values")
    values_text = remove_yaml_block(values_text, "snapshotController", 2, "snapshot-controller values")
    values_text = remove_yaml_block(values_text, "csi", 2, "snapshot CRD values")
    write(values, values_text)

    crd_values = chart / "charts/crds/values.yaml"
    write(crd_values, remove_yaml_block(read(crd_values), "csi", 0, "nested snapshot CRD values"))

    for relative in (
        "charts/crds/templates/csi-volume-snapshot-class.yaml",
        "charts/crds/templates/csi-volume-snapshot-content.yaml",
        "charts/crds/templates/csi-volume-snapshot.yaml",
    ):
        path = chart / relative
        if not path.is_file():
            raise TransformError(f"upstream CSI snapshot CRD path drifted: {relative}")
        path.unlink()

    inert_snapshot_crd = chart / "charts/crds/templates/lvmsnapshot.yaml"
    if not inert_snapshot_crd.is_file():
        raise TransformError("upstream inert LVMSnapshot CRD path drifted")

    # Do not package upstream instructions for surfaces removed by this derived
    # volume-only dependency. The parent chart documents the supported contract.
    readme = chart / "README.md"
    if not readme.is_file():
        raise TransformError("upstream chart README path drifted")
    readme.unlink()


def main() -> int:
    if len(sys.argv) != 3:
        print(f"usage: {Path(sys.argv[0]).name} ARCHIVE DESTINATION", file=sys.stderr)
        return 2
    archive = Path(sys.argv[1]).resolve()
    destination = Path(sys.argv[2]).resolve()
    destination.mkdir(parents=True, exist_ok=True)
    target = destination / CHART_NAME
    try:
        with tempfile.TemporaryDirectory(prefix=".lvm-volume-only-", dir=destination) as temporary:
            chart = extract_reviewed_chart(archive, Path(temporary))
            transform(chart)
            if target.exists():
                shutil.rmtree(target)
            shutil.move(str(chart), target)
    except (OSError, TransformError) as exc:
        print(f"volume-only OpenEBS chart transform failed: {exc}", file=sys.stderr)
        return 1
    print(f"prepared volume-only {CHART_NAME} from reviewed archive {SOURCE_SHA256}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
