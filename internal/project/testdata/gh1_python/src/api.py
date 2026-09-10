"""Public API surface for the query engine."""

from engine import cache_header, run, schedule


def handle_request(model, text):
    """Handle one extraction request from the HTTP layer."""
    header = cache_header()
    queries = run(model, text)
    return {"header": header, "queries": queries}


def handle_async(model, text):
    """Submit an extraction job without blocking the caller."""
    return schedule(model, text)