#!/usr/bin/env python3
"""Select monorepo CI owners from changed paths.

The selector is deliberately repository-owned and fixture-tested so changes to
fan-out semantics are reviewed alongside the workflows they control.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import sys

OUTPUTS = (
    "control_plane",
    "ui",
    "harness",
    "tool_runner",
    "proto",
    "inference_gateway",
    "forge",
    "forge_e2e",
    "forge_real_e2e",
    "charts",
    "images",
)

DOC_NAMES = {
    "AGENTS.md",
    "CHANGELOG.md",
    "CODE_OF_CONDUCT.md",
    "CONTRIBUTING.md",
    "LICENSE",
    "LICENSE.md",
    "README.md",
    "SECURITY.md",
}


def is_documentation(path: str) -> bool:
    parts = Path(path).parts
    return (
        path.endswith(".md")
        or (parts and parts[0] == "docs")
        or Path(path).name in DOC_NAMES
    )


def selection(paths: list[str], select_all: bool = False) -> dict[str, object]:
    selected = {name: select_all for name in OUTPUTS}
    selected_images: set[str] = set()

    if select_all:
        selected_images.update(
            {
                "control-plane",
                "control-plane-harness",
                "control-plane-harness-isolation",
                "control-plane-tool-runner",
                "inference-gateway",
            }
        )
    else:
        for raw_path in paths:
            path = raw_path.strip().removeprefix("./")
            if not path or is_documentation(path):
                continue

            # Root build/workspace contracts and CI implementation fan out to
            # every owner. Component-local Makefiles remain component-scoped.
            if path.startswith((".github/", "release/")) or path in {
                "Makefile",
                "go.work",
                "go.work.sum",
            }:
                for name in OUTPUTS:
                    selected[name] = True
                selected_images.update(
                    {
                        "control-plane",
                        "control-plane-harness",
                        "control-plane-harness-isolation",
                        "control-plane-tool-runner",
                        "inference-gateway",
                    }
                )
                continue

            if path.startswith("control-plane/"):
                relative = path.removeprefix("control-plane/")
                is_ui = relative.startswith("ui/")
                is_harness = relative.startswith("harness/")
                is_tool_runner = relative.startswith("tool-runner/")
                is_proto = relative.startswith("proto/") or relative in {
                    "buf.gen.yaml",
                    "buf.gateway.gen.yaml",
                    "buf.yaml",
                } or relative.startswith("internal/harnessrpc/") or relative.startswith(
                    "internal/gatewayrpc/"
                )

                selected["ui"] |= is_ui
                selected["harness"] |= is_harness or is_proto
                selected["tool_runner"] |= is_tool_runner or is_proto
                selected["proto"] |= is_proto
                selected["control_plane"] |= not (is_harness or is_tool_runner)

                if is_ui or not (is_harness or is_tool_runner):
                    selected_images.add("control-plane")
                if is_harness:
                    selected_images.add("control-plane-harness")
                    if relative.startswith("harness/isolation/"):
                        selected_images.add("control-plane-harness-isolation")
                if is_tool_runner:
                    selected_images.add("control-plane-tool-runner")
                if is_proto:
                    selected_images.update(
                        {"control-plane-harness", "control-plane-tool-runner"}
                    )
                selected["images"] = bool(selected_images)
                continue

            if path.startswith("inference-gateway/"):
                selected["inference_gateway"] = True
                selected["images"] = True
                selected_images.add("inference-gateway")
                continue

            if path.startswith("forge/"):
                selected["forge"] = True
                relative = path.removeprefix("forge/")
                affects_e2e = (
                    relative.startswith("cmd/")
                    or relative.startswith("internal/")
                    or relative.startswith("test/e2e/")
                    or relative in {"go.mod", "go.sum", "Makefile"}
                )
                selected["forge_e2e"] |= affects_e2e
                selected["forge_real_e2e"] |= affects_e2e
                continue

            if path.startswith("charts/"):
                selected["charts"] = True
                # Current deterministic Kind scenarios compose local charts.
                selected["forge_e2e"] = True
                continue

    selected["image_matrix"] = [
        {"name": name, **IMAGE_CONFIG[name]} for name in sorted(selected_images)
    ]
    selected["any"] = any(bool(selected[name]) for name in OUTPUTS)
    return selected


IMAGE_CONFIG = {
    "control-plane": {
        "context": "control-plane",
        "dockerfile": "control-plane/Dockerfile",
    },
    "control-plane-harness": {
        "context": "control-plane/harness",
        "dockerfile": "control-plane/harness/Dockerfile",
    },
    "control-plane-harness-isolation": {
        "context": "control-plane/harness",
        "dockerfile": "control-plane/harness/isolation/Dockerfile",
    },
    "control-plane-tool-runner": {
        "context": "control-plane/tool-runner",
        "dockerfile": "control-plane/tool-runner/Dockerfile",
    },
    "inference-gateway": {
        "context": "inference-gateway",
        "dockerfile": "inference-gateway/Dockerfile",
    },
}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("paths", nargs="*")
    parser.add_argument("--paths-file", type=Path)
    parser.add_argument("--all", action="store_true", dest="select_all")
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()

    paths = list(args.paths)
    if args.paths_file:
        paths.extend(args.paths_file.read_text().splitlines())

    result = selection(paths, args.select_all)
    rendered = json.dumps(result, sort_keys=True)
    print(rendered)

    output_path = args.github_output
    if output_path is None and os.environ.get("GITHUB_OUTPUT"):
        output_path = Path(os.environ["GITHUB_OUTPUT"])
    if output_path:
        with output_path.open("a", encoding="utf-8") as output:
            for name, value in result.items():
                if isinstance(value, bool):
                    value = str(value).lower()
                elif not isinstance(value, str):
                    value = json.dumps(value, separators=(",", ":"))
                output.write(f"{name}={value}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
