#!/usr/bin/env python3
"""Regression checks for smoke-test database isolation."""
import os
import unittest
from unittest import mock

import smoke


class SmokeEnvironmentTests(unittest.TestCase):
    def test_ordinary_smoke_never_inherits_operator_database(self):
        with mock.patch.dict(os.environ, {'DATABASE_URL': 'postgres://production/private',
                                         'RSS_TEST_POSTGRES_URL': 'postgres://test/disposable'}):
            for compose in (False, True):
                env = smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, False, compose)
                self.assertEqual(env['DATABASE_URL'], '')

    def test_postgres_requires_separate_explicit_test_database(self):
        with mock.patch.dict(os.environ, {'DATABASE_URL': 'postgres://production/private'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'RSS_TEST_POSTGRES_URL'):
                smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, True, False)
            # Compose gets its own generated credentials after environment setup.
            env = smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, True, True)
            self.assertEqual(env['DATABASE_URL'], '')

    def test_host_storage_paths_are_owned_by_each_test(self):
        with mock.patch.dict(os.environ, {'RSS_DATA_DIR': '/operator/sqlite',
                                         'POSTGRES_DATA_DIR': '/operator/postgres'}):
            for postgres in (False, True):
                for compose in (False, True):
                    with mock.patch.dict(os.environ, {'RSS_TEST_POSTGRES_URL': 'postgres://test/disposable'}):
                        env = smoke.smoke_environment('/tmp/private-fixture', 'fixture-password', 'http://localhost:1234', 1234, postgres, compose)
                    self.assertEqual(env['RSS_DATA_DIR'], '/tmp/private-fixture/data')
                    self.assertEqual(env['POSTGRES_DATA_DIR'], '/tmp/private-fixture/postgres-data')

    def test_notifications_never_reach_an_operator_service(self):
        with mock.patch.dict(os.environ, {'NOTIFY_URL': 'https://ntfy.sh/operator-private-topic',
                                         'NOTIFY_TOKEN': 'operator-token'}):
            for compose in (False, True):
                env = smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, False, compose)
                self.assertEqual(env['NOTIFY_URL'], '')
                self.assertEqual(env['NOTIFY_TOKEN'], '')

    def test_log_checks_never_follow_an_operator_log_setting(self):
        # The run checks for info lines in the text format.
        with mock.patch.dict(os.environ, {'LOG_LEVEL': 'warn', 'LOG_FORMAT': 'json'}):
            for compose in (False, True):
                env = smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, False, compose)
                self.assertEqual(env['LOG_LEVEL'], 'info')
                self.assertEqual(env['LOG_FORMAT'], 'text')

    def test_only_explicit_native_postgres_mode_uses_test_database(self):
        with mock.patch.dict(os.environ, {'DATABASE_URL': 'postgres://production/private',
                                         'RSS_TEST_POSTGRES_URL': 'postgres://test/disposable'}):
            env = smoke.smoke_environment('/tmp/fixture', 'fixture-password', 'http://localhost:1234', 1234, True, False)
            self.assertEqual(env['DATABASE_URL'], 'postgres://test/disposable')


if __name__ == '__main__':
    unittest.main()
