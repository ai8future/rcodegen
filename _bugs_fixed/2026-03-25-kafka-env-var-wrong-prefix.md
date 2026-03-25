# Kafka env var wrong prefix in rserve

**Date:** 2026-03-25
**Severity:** Low (config only matters when Kafka is enabled)

## Problem

`cmd/rserve/main.go` used `KAFKA_BOOTSTRAP_SERVERS` and `KAFKA_TENANT_ID` environment variable names, but chassis v10's kafkakit convention expects the `KAFKAKIT_*` prefix. This meant rserve would never detect the Kafka config when set with the correct chassis v10 variable names.

## Fix

Renamed all three occurrences in `cmd/rserve/main.go`:
- `KAFKA_BOOTSTRAP_SERVERS` -> `KAFKAKIT_BOOTSTRAP_SERVERS`
- `KAFKA_TENANT_ID` -> `KAFKAKIT_TENANT_ID`
- Updated the inline comment accordingly

Also removed stale Syncthing conflict `.go` files in `cmd/rserve/` and `cmd/rcodegen/` that referenced chassis v9 and blocked compilation.
