"""SQLite cache for pr-sync, keyed on (repo, number).

The cache lets re-runs skip the heavy detail query for PRs whose `updatedAt`
has not changed since the last sync. State lives under state/ (per-user,
gitignored).
"""

from __future__ import annotations

import json
import sqlite3
from typing import Any


class Cache:
    def __init__(self, path: str):
        self.conn = sqlite3.connect(path)
        self.conn.execute(
            """
            CREATE TABLE IF NOT EXISTS pr_cache (
                repo        TEXT NOT NULL,
                number      INTEGER NOT NULL,
                updated_at  TEXT NOT NULL,
                head_oid    TEXT NOT NULL,
                record_json TEXT NOT NULL,
                PRIMARY KEY (repo, number)
            )
            """
        )
        self.conn.execute("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)")
        self.conn.commit()

    def ensure_fingerprint(self, fingerprint: str) -> None:
        """Drop all cached records if the derivation inputs (config + logic
        version) changed — `updatedAt` alone can't detect that."""
        row = self.conn.execute(
            "SELECT value FROM meta WHERE key = 'fingerprint'"
        ).fetchone()
        if row is None or row[0] != fingerprint:
            self.conn.execute("DELETE FROM pr_cache")
            self.conn.execute(
                "INSERT INTO meta (key, value) VALUES ('fingerprint', ?) "
                "ON CONFLICT(key) DO UPDATE SET value = excluded.value",
                (fingerprint,),
            )
            self.conn.commit()

    def get(self, repo: str, number: int) -> dict[str, Any] | None:
        row = self.conn.execute(
            "SELECT updated_at, record_json FROM pr_cache WHERE repo = ? AND number = ?",
            (repo, number),
        ).fetchone()
        if row is None:
            return None
        return {"updated_at": row[0], "record": json.loads(row[1])}

    def put(self, repo: str, number: int, updated_at: str, head_oid: str,
            record: dict[str, Any]) -> None:
        self.conn.execute(
            """
            INSERT INTO pr_cache (repo, number, updated_at, head_oid, record_json)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(repo, number) DO UPDATE SET
                updated_at = excluded.updated_at,
                head_oid = excluded.head_oid,
                record_json = excluded.record_json
            """,
            (repo, number, updated_at, head_oid, json.dumps(record)),
        )
        self.conn.commit()

    def close(self) -> None:
        self.conn.close()
