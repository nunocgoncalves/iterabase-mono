#!/usr/bin/env python3

from __future__ import annotations

import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from urllib.error import URLError

import remote_content

ROOT = Path(__file__).resolve().parents[2]


class Response(io.BytesIO):
    def __enter__(self):
        return self

    def __exit__(self, *_args):
        self.close()


class RemoteContentTests(unittest.TestCase):
    def test_repository_inventory_is_complete_and_exact(self) -> None:
        remote_content.validate_manifest(remote_content.load_manifest(), ROOT)

    def test_same_url_byte_substitution_fails_before_materialization(self) -> None:
        expected = hashlib.sha256(b"reviewed").hexdigest()
        with tempfile.TemporaryDirectory() as value:
            output = Path(value) / "artifact"
            calls = 0

            def substitute(_url: str, **_kwargs):
                nonlocal calls
                calls += 1
                return Response(b"substituted")

            with self.assertRaisesRegex(remote_content.ContentError, "checksum mismatch"):
                remote_content.verified_download(
                    "https://example.invalid/tool",
                    expected,
                    output,
                    attempts=4,
                    opener=substitute,
                    sleeper=lambda _delay: None,
                )
            self.assertEqual(1, calls, "content mismatch must not be retried")
            self.assertFalse(output.exists(), "unverified bytes must not materialize")

    def test_retry_recovers_transport_only_and_still_verifies_bytes(self) -> None:
        content = b"reviewed"
        expected = hashlib.sha256(content).hexdigest()
        responses = [URLError("reset"), Response(content)]

        def transport_then_content(_url: str, **_kwargs):
            response = responses.pop(0)
            if isinstance(response, Exception):
                raise response
            return response

        with tempfile.TemporaryDirectory() as value:
            output = Path(value) / "artifact"
            remote_content.verified_download(
                "https://example.invalid/tool",
                expected,
                output,
                attempts=2,
                opener=transport_then_content,
                sleeper=lambda _delay: None,
            )
            self.assertEqual(content, output.read_bytes())
            self.assertEqual([], responses)

    def test_manifest_rejects_duplicate_content_identity(self) -> None:
        manifest = remote_content.load_manifest()
        manifest["helm_charts"].append(dict(manifest["helm_charts"][0]))
        with self.assertRaisesRegex(remote_content.ContentError, "duplicates"):
            remote_content.validate_manifest(manifest, ROOT)

    def test_manifest_rejects_version_only_container_input(self) -> None:
        manifest = remote_content.load_manifest()
        manifest["container_images"][0]["digest"] = "latest"
        with self.assertRaisesRegex(remote_content.ContentError, "non-canonical"):
            remote_content.validate_manifest(manifest, ROOT)

    def test_remote_action_requires_full_commit_pin(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            action = root / "action.yml"
            action.write_text("runs:\n  using: composite\n  steps:\n    - uses: actions/cache@v4\n", encoding="utf-8")
            with self.assertRaisesRegex(remote_content.ContentError, "non-commit Action pin"):
                remote_content.validate_action_pins([action], root)


if __name__ == "__main__":
    unittest.main()
