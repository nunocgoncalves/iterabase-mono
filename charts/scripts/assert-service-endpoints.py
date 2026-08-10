#!/usr/bin/env python3
"""Assert data and exporter Services select disjoint component pod sets."""

import argparse
import json
import subprocess


def kubectl_json(namespace: str, resource: str) -> list[dict]:
    output = subprocess.check_output(
        ["kubectl", "get", resource, "-n", namespace, "-o", "json"],
        text=True,
    )
    return json.loads(output)["items"]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--namespace", default="iterabase-system")
    parser.add_argument("--release", default="iterabase")
    args = parser.parse_args()

    slices = kubectl_json(args.namespace, "endpointslice")
    pods = kubectl_json(args.namespace, "pods")

    def selected_pods(name: str, component: str) -> set[str]:
        return {
            pod["metadata"]["name"]
            for pod in pods
            if pod["metadata"].get("labels", {}).get("app.kubernetes.io/name") == name
            and pod["metadata"].get("labels", {}).get("app.kubernetes.io/instance")
            == args.release
            and pod["metadata"].get("labels", {}).get("app.kubernetes.io/component")
            == component
        }

    def endpoint_pods(service: str) -> set[str]:
        return {
            endpoint["targetRef"]["name"]
            for item in slices
            if item["metadata"].get("labels", {}).get("kubernetes.io/service-name")
            == service
            for endpoint in item.get("endpoints", [])
            if endpoint.get("targetRef", {}).get("kind") == "Pod"
        }

    checks = (
        ("postgresql", "database", f"{args.release}-postgresql"),
        ("postgresql", "exporter", f"{args.release}-postgresql-exporter"),
        ("redis", "cache", f"{args.release}-redis"),
        ("redis", "exporter", f"{args.release}-redis-exporter"),
    )
    endpoints: dict[str, set[str]] = {}
    for name, component, service in checks:
        expected = selected_pods(name, component)
        actual = endpoint_pods(service)
        if not expected:
            raise SystemExit(f"{service}: no component={component} pods found")
        if actual != expected:
            raise SystemExit(
                f"{service}: endpoints {sorted(actual)} != expected {sorted(expected)}"
            )
        endpoints[service] = actual
        print(f"OK: {service} endpoints={sorted(actual)} component={component}")

    for data, exporter in (
        (f"{args.release}-postgresql", f"{args.release}-postgresql-exporter"),
        (f"{args.release}-redis", f"{args.release}-redis-exporter"),
    ):
        overlap = endpoints[data] & endpoints[exporter]
        if overlap:
            raise SystemExit(f"{data} and {exporter} overlap: {sorted(overlap)}")
        print(f"OK: {data} and {exporter} EndpointSlices are disjoint")


if __name__ == "__main__":
    main()
