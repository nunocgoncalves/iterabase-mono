#!/usr/bin/env python3

import json
from pathlib import Path
import unittest

from select_ci import OUTPUTS, selection


ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / ".github/ci/path-selection-fixtures.json"


class PathSelectionFixtures(unittest.TestCase):
    def test_fixture_matrix(self) -> None:
        fixtures = json.loads(FIXTURES.read_text())
        for fixture in fixtures:
            with self.subTest(fixture["name"]):
                result = selection(fixture["paths"])
                actual_true = {name for name in OUTPUTS if result[name]}
                self.assertEqual(set(fixture["true"]), actual_true)
                self.assertEqual(
                    fixture["images"],
                    [image["name"] for image in result["image_matrix"]],
                )
                self.assertEqual(bool(fixture["true"]), result["any"])

    def test_select_all_is_explicit_and_complete(self) -> None:
        result = selection([], select_all=True)
        self.assertTrue(all(result[name] for name in OUTPUTS))
        self.assertEqual(5, len(result["image_matrix"]))


if __name__ == "__main__":
    unittest.main()
