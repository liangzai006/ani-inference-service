#!/usr/bin/env python3
"""Render contract checks for the offline profile; no cluster access."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/ani-inference"
HELM = os.environ.get("HELM", "helm")


def render(settings=None, profile=False, success=True):
    with tempfile.TemporaryDirectory(prefix="inference-chart-") as directory:
        values = Path(directory) / "values.json"
        values.write_text(json.dumps(settings or {}))
        argv = [HELM, "template", "ani-inference", str(CHART), "--namespace", "ani-system",
                "--kube-version", "1.32.0", "--set-string", "higress.publicBaseURL=http://192.0.2.11:30080"]
        if profile:
            argv += ["-f", str(CHART / "values-offline.example.yaml")]
        result = subprocess.run(argv + ["-f", str(values)], capture_output=True, text=True)
    if success and result.returncode:
        raise AssertionError(result.stderr)
    if not success:
        return result
    return [item for item in yaml.safe_load_all(result.stdout) if item]


def resource(objects, kind, name):
    return next(item for item in objects if item["kind"] == kind and item["metadata"]["name"] == name)


def containers(objects):
    return resource(objects, "Deployment", "ani-inference-service")["spec"]["template"]["spec"]["containers"]


def variables(container):
    return {item["name"]: item for item in container["env"]}


class OfflineChartContract(unittest.TestCase):
    def test_legacy_tag_and_database_fallback(self):
        objects = render()
        self.assertTrue(containers(objects)[0]["image"].endswith(":main-ff24af7-20261016"))
        migration = resource(objects, "Job", "ani-inference-migration-1")
        reference = variables(migration["spec"]["template"]["spec"]["containers"][0])["DATABASE_DSN"]["valueFrom"]["secretKeyRef"]
        self.assertEqual(reference, {"name": "inference-database", "key": "dsn"})
        self.assertNotIn("ANI_MODEL_DOWNLOAD_CA_CONFIGMAP", variables(containers(objects)[0]))

    def test_digest_wins_over_tag_for_all_three_images(self):
        image = {"repository": "192.0.2.11:30003/project/service", "tag": "", "digest": "sha256:" + "a" * 64}
        objects = render({"image": image, "migration": {"image": image}, "gatewayAuthz": {"enabled": True, "image": image}})
        expected = image["repository"] + "@" + image["digest"]
        self.assertEqual([item["image"] for item in containers(objects)], [expected, expected])
        job = resource(objects, "Job", "ani-inference-migration-1")
        self.assertEqual(job["spec"]["template"]["spec"]["containers"][0]["image"], expected)

    def test_malformed_digest_fails_instead_of_using_tag(self):
        for field in ("image", "migration", "gatewayAuthz"):
            with self.subTest(field=field):
                settings = {field: {"digest": "sha256:bad"}} if field == "image" else {field: {"image": {"digest": "sha256:bad"}}}
                if field == "gatewayAuthz":
                    settings[field]["enabled"] = True
                self.assertNotEqual(render(settings, success=False).returncode, 0)

    def test_migration_owner_does_not_enter_runtime(self):
        settings = {"database": {"existingSecret": "runtime", "secretKey": "runtime-dsn", "migrationSecret": "owner", "migrationSecretKey": "owner-dsn"}}
        objects = render(settings)
        self.assertEqual(variables(containers(objects)[0])["ANI_DATABASE_DSN"]["valueFrom"]["secretKeyRef"], {"name": "runtime", "key": "runtime-dsn"})
        job = resource(objects, "Job", "ani-inference-migration-1")
        self.assertEqual(variables(job["spec"]["template"]["spec"]["containers"][0])["DATABASE_DSN"]["valueFrom"]["secretKeyRef"], {"name": "owner", "key": "owner-dsn"})
        self.assertNotIn('"owner"', json.dumps(containers(objects)))

    def test_migration_reference_falls_back_per_field(self):
        for override, expected in [({"migrationSecret": "owner"}, {"name": "owner", "key": "dsn"}), ({"migrationSecretKey": "owner-dsn"}, {"name": "inference-database", "key": "owner-dsn"})]:
            objects = render({"database": override})
            job = resource(objects, "Job", "ani-inference-migration-1")
            self.assertEqual(variables(job["spec"]["template"]["spec"]["containers"][0])["DATABASE_DSN"]["valueFrom"]["secretKeyRef"], expected)

    def test_offline_profile_shared_storage_ca_and_pull_identity(self):
        objects = render(profile=True)
        env = variables(containers(objects)[0])
        self.assertEqual(env["ANI_MODEL_STORAGE_CLASS"]["value"], "ani-cephfs")
        self.assertEqual(env["ANI_MODEL_DOWNLOAD_CA_CONFIGMAP"]["value"], "ani-model-public-ca")
        self.assertEqual(env["ANI_MODEL_DOWNLOAD_CA_KEY"]["value"], "ca-certificates.crt")
        runtime = resource(objects, "ServiceAccount", env["ANI_KUBERNETES_SERVICE_ACCOUNT"]["value"])
        self.assertEqual(runtime["imagePullSecrets"], [{"name": "ani-inference-registry"}])
        self.assertFalse(runtime["automountServiceAccountToken"])
        self.assertFalse(any(item["kind"] == "RoleBinding" and item["subjects"][0]["name"] == runtime["metadata"]["name"] for item in objects))
        self.assertFalse(any(item["kind"] == "Certificate" for item in objects))
        self.assertEqual(variables(containers(objects)[1])["ANI_INFERENCE_CLIENT_IDENTITY"]["value"], "ani-inference-gateway.ani.internal")

    def test_ca_reference_requires_key(self):
        result = render({"model": {"downloadCA": {"configMap": "trust", "key": ""}}}, success=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("model.downloadCA.key is required", result.stderr)


if __name__ == "__main__":
    unittest.main()
