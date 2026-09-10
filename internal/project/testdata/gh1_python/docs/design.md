# Engine Design

The engine splits extraction into a registry of per-model query extractors.

## Pipeline

1. `schedule` submits a job to an `Engine` controller.
2. `run` executes the extractor synchronously.

## Cache Layout

The persistence header carries `_CACHE_PERSIST_VERSION`.