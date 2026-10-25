"""Read-only mock source shared by the two MCP services."""

import json
from pathlib import Path

FIXTURES = Path(__file__).resolve().parent.parent / "fixtures"


def lookup(collection: str, key: str) -> dict:
    with (FIXTURES / f"{collection}.json").open(encoding="utf-8") as source:
        record = json.load(source).get(key)
    if record is None:
        raise ValueError(f"Unknown {collection} key: {key}")
    return record
