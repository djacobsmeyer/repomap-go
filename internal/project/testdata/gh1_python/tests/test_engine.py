"""Tests for the query engine (no inbound links by design)."""

from engine import Engine, run


def test_run_returns_queries():
    queries = run(None, "one\ntwo")
    assert queries == ["one", "two"]


def test_engine_submit_roundtrip():
    engine = Engine()
    task = engine.submit("qwen3.5", "one")
    assert task.model == "qwen3.5"