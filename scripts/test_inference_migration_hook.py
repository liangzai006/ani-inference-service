#!/usr/bin/env python3
"""Run the rendered migration hook against an isolated, disposable PostgreSQL container."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import unittest
import uuid

from test_inference_chart import CHART, render, resource


class MigrationHookIntegration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="inference-migration-test-")
        cls.path = Path(cls.temp.name)
        cls.path.chmod(0o755)
        cls.migrations = cls.path / "migrations"
        shutil.copytree(CHART / "migrations", cls.migrations)
        cls.name = "inference-migration-test-" + uuid.uuid4().hex[:12]
        image = os.environ.get("INFERENCE_TEST_POSTGRES_IMAGE", "postgres:16-alpine")
        result = subprocess.run(
            ["docker", "run", "--rm", "-d", "--network", "none", "--name", cls.name,
             "-e", "POSTGRES_PASSWORD=temporary-test-only", "-v", f"{cls.migrations}:/migrations:ro", image],
            text=True, capture_output=True,
        )
        if result.returncode:
            cls.temp.cleanup()
            raise RuntimeError("Could not start disposable PostgreSQL: " + result.stderr)
        try:
            for _ in range(60):
                if subprocess.run(["docker", "exec", cls.name, "pg_isready", "-U", "postgres"],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
                    break
                time.sleep(0.5)
            else:
                raise RuntimeError("Disposable PostgreSQL did not become ready")
            job = resource(render(), "Job", "ani-inference-migration-1")
            cls.script = job["spec"]["template"]["spec"]["containers"][0]["args"][0]
        except BaseException:
            subprocess.run(["docker", "stop", "-t", "1", cls.name], capture_output=True)
            cls.temp.cleanup()
            raise

    @classmethod
    def tearDownClass(cls):
        subprocess.run(["docker", "stop", "-t", "1", cls.name], check=True, capture_output=True)
        cls.temp.cleanup()

    def database(self):
        name = "test_" + uuid.uuid4().hex
        self.docker("createdb", "-U", "postgres", name)
        return name

    def docker(self, *args, **kwargs):
        return subprocess.run(["docker", "exec", self.name, *args], check=True,
                              text=True, capture_output=True, **kwargs)

    def sql(self, database, query):
        return self.docker("psql", "-U", "postgres", "-d", database,
                           "-v", "ON_ERROR_STOP=1", "-Atqc", query).stdout.strip()

    def hook(self, database):
        dsn = f"postgresql:///{database}?host=/var/run/postgresql&user=postgres"
        return subprocess.run(["docker", "exec", "-i", "-e", f"DATABASE_DSN={dsn}", self.name, "sh", "-s"],
                              input=self.script, text=True, capture_output=True)

    def test_fresh_install_and_retry(self):
        database = self.database()
        first = self.hook(database)
        self.assertEqual(first.returncode, 0, first.stdout + first.stderr)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "26")
        self.assertEqual(self.sql(database, "SELECT to_regclass('public.inference_access_policies') IS NOT NULL"), "t")
        again = self.hook(database)
        self.assertEqual(again.returncode, 0, again.stdout + again.stderr)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "26")

    def test_sql_failure_rolls_back_schema_and_ledger(self):
        database = self.database()
        path = self.migrations / "000026_inference_access_policies.sql"
        original = path.read_text()
        try:
            path.write_text(original.replace("COMMIT;", "SELECT 1/0;\nCOMMIT;"))
            failed = self.hook(database)
            self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
            self.assertEqual(self.sql(database, "SELECT to_regclass('public.inference_access_policies') IS NULL"), "t")
            self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations WHERE filename LIKE '000026_%'"), "0")
        finally:
            path.write_text(original)
        retry = self.hook(database)
        self.assertEqual(retry.returncode, 0, retry.stdout + retry.stderr)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "26")

    def test_legacy_schema_requires_verified_baseline(self):
        database = self.database()
        later = sorted(self.migrations.glob("00002[5-6]_*.sql"))
        saved = {path: path.read_text() for path in later}
        try:
            for path in later:
                path.unlink()
            initial = self.hook(database)
            self.assertEqual(initial.returncode, 0, initial.stdout + initial.stderr)
            verified = self.sql(database, "SELECT filename FROM inference_schema_migrations ORDER BY filename").splitlines()
            self.assertEqual(len(verified), 24)
            self.sql(database, "DROP TABLE inference_schema_migrations")
        finally:
            for path, contents in saved.items():
                path.write_text(contents)
        result = self.hook(database)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("existing inference schema has no migration ledger entries", result.stdout)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "0")
        self.assertEqual(self.sql(database, "SELECT to_regclass('public.inference_access_policies') IS NULL"), "t")
        # Here the baseline is backed by this test's own successful migration run.
        for filename in verified:
            self.sql(database, f"INSERT INTO inference_schema_migrations(filename) VALUES ('{filename}')")
        resumed = self.hook(database)
        self.assertEqual(resumed.returncode, 0, resumed.stdout + resumed.stderr)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "26")
        self.assertEqual(self.sql(database, "SELECT to_regclass('public.inference_access_policies') IS NOT NULL"), "t")

    def test_ledger_insert_failure_rolls_back_schema(self):
        database = self.database()
        path = self.migrations / "000026_inference_access_policies.sql"
        original = path.read_text()
        try:
            path.unlink()
            initial = self.hook(database)
            self.assertEqual(initial.returncode, 0, initial.stdout + initial.stderr)
        finally:
            path.write_text(original)
        self.sql(database, "ALTER TABLE inference_schema_migrations ADD CONSTRAINT reject_26 CHECK (filename NOT LIKE '000026_%')")
        failed = self.hook(database)
        self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
        self.assertEqual(self.sql(database, "SELECT to_regclass('public.inference_access_policies') IS NULL"), "t")
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations WHERE filename LIKE '000026_%'"), "0")
        self.sql(database, "ALTER TABLE inference_schema_migrations DROP CONSTRAINT reject_26")
        retry = self.hook(database)
        self.assertEqual(retry.returncode, 0, retry.stdout + retry.stderr)
        self.assertEqual(self.sql(database, "SELECT count(*) FROM inference_schema_migrations"), "26")


if __name__ == "__main__":
    unittest.main()
