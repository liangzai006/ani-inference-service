#!/usr/bin/env python3
"""Offline checks using the downloaded KServe v0.16 LLMI chart.

Run ``./scripts/fetch-dependencies.sh`` first, then
``python3 scripts/post_renderer_test.py`` (PyYAML is only needed by this test).
"""

import os
from pathlib import Path
import subprocess
import sys
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
RENDERER = ROOT / "scripts/drop-kserve-configmap-post-renderer.py"
DEFAULT_ARTIFACTS = ROOT / "charts/vendor"
if not DEFAULT_ARTIFACTS.is_dir():
    DEFAULT_ARTIFACTS = ROOT / ".build/platform-charts"
ARTIFACTS = Path(os.environ.get("PLATFORM_CHART_DIR", DEFAULT_ARTIFACTS))
CHART = ARTIFACTS / "llmisvc-v0.16.0.tgz"
CORE_CHART = ARTIFACTS / "kserve-v0.16.0.tgz"
CONFIG_CHART = ROOT / "charts/ani-inference-config"
CONFIG_NAMES = {
    "kserve-config-llm-template",
    "kserve-config-llm-decode-template",
    "kserve-config-llm-prefill-template",
    "kserve-config-llm-worker-data-parallel",
    "kserve-config-llm-decode-worker-data-parallel",
    "kserve-config-llm-prefill-worker-data-parallel",
    "kserve-config-llm-scheduler",
    "kserve-config-llm-router-route",
}
PIPELINE_NAME = "kserve-config-llm-worker-pipeline-parallel"


def run(*args, payload=None):
    return subprocess.run(
        args, input=payload, text=True, check=True, capture_output=True,
        env={**os.environ, "KUBECONFIG": "/dev/null"},
    ).stdout


def documents(payload):
    return [item for item in yaml.safe_load_all(payload) if item is not None]


class PostRendererTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not CHART.is_file():
            raise RuntimeError(f"Download the dependency charts first: {CHART}")
        cls.original = run("helm", "template", "llmisvc", str(CHART), "--namespace", "kserve")
        cls.rendered = run(sys.executable, str(RENDERER), payload=cls.original)
        cls.configs = run("helm", "template", "ani-inference-config", str(CONFIG_CHART), "--namespace", "kserve")
        core_original = run("helm", "template", "kserve", str(CORE_CHART), "--namespace", "kserve")
        cls.core = run(sys.executable, str(RENDERER), "--mode=core", payload=core_original)

    def test_required_configs_are_retained_without_engine_defaults(self):
        configs = {item["metadata"]["name"]: item for item in documents(self.configs)
                   if item["kind"] == "LLMInferenceServiceConfig"}
        self.assertEqual(set(configs), CONFIG_NAMES | {PIPELINE_NAME})
        for name, config in configs.items():
            with self.subTest(name=name):
                self.assertEqual(config["spec"], {})
        self.assertNotIn("namespace", configs[PIPELINE_NAME]["metadata"])

    def test_only_duplicate_configmap_and_llm_specs_change(self):
        before = documents(self.original)
        after = documents(self.rendered)
        duplicate = lambda item: item["kind"] == "ConfigMap" and item["metadata"]["name"] == "inferenceservice-config"
        self.assertEqual(sum(duplicate(item) for item in before), 1)
        self.assertFalse(any(duplicate(item) for item in after))
        self.assertEqual(
            [item for item in before if not duplicate(item) and item["kind"] != "LLMInferenceServiceConfig"],
            [item for item in after if item["kind"] != "LLMInferenceServiceConfig"],
        )
        self.assertFalse(any(item["kind"] == "LLMInferenceServiceConfig" for item in after))

    def test_helm_can_execute_the_post_renderer(self):
        output = run("helm", "template", "llmisvc", str(CHART), "--namespace", "kserve", "--post-renderer", str(RENDERER))
        self.assertCountEqual(documents(output), documents(self.rendered))

    def test_combined_install_has_nine_empty_configs_and_no_duplicate_configmap(self):
        combined = documents(self.core + "\n---\n" + self.rendered + "\n---\n" + self.configs)
        configs = [item for item in combined if item["kind"] == "LLMInferenceServiceConfig"]
        self.assertEqual(len(configs), len(CONFIG_NAMES | {PIPELINE_NAME}))
        self.assertTrue(all(item["spec"] == {} for item in configs))
        self.assertEqual(sum(item["kind"] == "ConfigMap" and item["metadata"]["name"] == "inferenceservice-config"
                             for item in combined), 1)

    def test_core_mode_drops_sample_runtime_objects_only(self):
        original = run("helm", "template", "kserve", str(CORE_CHART), "--namespace", "kserve")
        output = run("helm", "template", "kserve", str(CORE_CHART), "--namespace", "kserve",
                     "--post-renderer", str(RENDERER), "--post-renderer-args", "--mode=core")
        self.assertTrue(any(item["kind"] == "ConfigMap" for item in documents(output)))
        self.assertFalse(any(item["kind"] in {"ClusterServingRuntime", "ClusterStorageContainer"}
                             for item in documents(output)))
        self.assertFalse(any(
            item["kind"] == "ValidatingWebhookConfiguration"
            and item["metadata"]["name"] in {
                "llminferenceservice.serving.kserve.io",
                "llminferenceserviceconfig.serving.kserve.io",
            }
            for item in documents(output)
        ))
        before = [item for item in documents(original)
                  if item["kind"] not in {"ClusterServingRuntime", "ClusterStorageContainer"}
                  and not (
                      item["kind"] == "ValidatingWebhookConfiguration"
                      and item["metadata"]["name"] in {
                          "llminferenceservice.serving.kserve.io",
                          "llminferenceserviceconfig.serving.kserve.io",
                      }
                  )]
        self.assertCountEqual(before, documents(output))

    def test_reapplying_does_not_duplicate_pipeline_config(self):
        output = run(sys.executable, str(RENDERER), payload=self.rendered)
        self.assertEqual(output, self.rendered)


if __name__ == "__main__":
    unittest.main(verbosity=2)
