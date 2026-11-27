"""Publication boundary regression tests; no deployment state is read."""
import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location('export_source', Path(__file__).with_name('export-source.py'))
export = importlib.util.module_from_spec(spec)
spec.loader.exec_module(export)

class SourceBoundaryTest(unittest.TestCase):
    def test_private_files_are_excluded_even_if_git_tracked(self):
        for path in (
            'infra/terraform/envs/prod/secret.tfvars',
            'infra/terraform/envs/prod/secret.tfvars.json',
            'infra/terraform/envs/prod/terraform.tfstate.backup',
            'infra/terraform/envs/prod/backend.hcl',
            'apps/api/.data/runtime.json', 'apps/api/.aws/credentials',
            'apps/api/.env.production', 'apps/api/.ssh/id_rsa',
            'apps/web/node_modules/example/index.js', 'apps/api/private.pem',
            'apps/api/.git/config', 'research/private.md',
            'infra/terraform/envs/prod/override.tf',
            'infra/terraform/envs/prod/private_override.tf.json',
            'apps/api/.netrc', 'apps/api/secrets.kdbx',
            'apps/api/secrets.p8', 'apps/api/config.backup',
            'apps/api/runtime.tar.gz', 'apps/web/.DS_Store',
        ):
            with self.subTest(path=path): self.assertFalse(export.allowed(path))
    def test_source_and_public_examples_remain_exportable(self):
        for path in ('apps/api/internal/accept/accept.go', 'LICENSE',
                     'infra/terraform/envs/staging/terraform.tfvars.example',
                     'apps/api/.env.example', 'docs/open-source/security-review.md'):
            with self.subTest(path=path): self.assertTrue(export.allowed(path))

if __name__ == '__main__': unittest.main()
