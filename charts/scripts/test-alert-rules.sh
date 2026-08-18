#!/usr/bin/env bash
set -euo pipefail
container_tool=${CONTAINER_TOOL:-docker}
image=${PROMETHEUS_TEST_IMAGE:-quay.io/prometheus/prometheus@sha256:508729e0e2d18e11fd742a5a5ca70e557b940a93948c3c95fd0123a6fd538b69}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
helm template alert-contract charts/observability --show-only templates/iterabase-alerts.yaml | yq '.spec' > "$tmp/rules.yaml"
cp charts/observability/tests/iterabase-alerts.test.yaml "$tmp/tests.yaml"
python3 - "$tmp/dashboard-rules.json" <<'PY'
import json
import pathlib
import sys
rules = []
for path in sorted(pathlib.Path("charts/observability/dashboards").rglob("iterabase-*.json")):
    dashboard = json.loads(path.read_text())
    for panel in dashboard["panels"]:
        for target in panel.get("targets", []):
            expression = target.get("expr")
            if expression:
                rules.append({
                    "record": f"iterabase_dashboard_query_{len(rules) + 1:03d}",
                    "expr": expression.replace("$namespace", ".*").replace("$__rate_interval", "5m"),
                })
pathlib.Path(sys.argv[1]).write_text(json.dumps({"groups": [{"name": "iterabase-dashboard-queries", "rules": rules}]}))
PY
"$container_tool" run --rm --entrypoint /bin/promtool -v "$tmp:/work:ro" "$image" check rules /work/rules.yaml /work/dashboard-rules.json
"$container_tool" run --rm --entrypoint /bin/promtool -v "$tmp:/work:ro" "$image" test rules /work/tests.yaml
