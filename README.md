# Databasify MySQL Engine

- [Introduction](#introduction)
- [Lines](#lines)
- [Features](#features)
- [Ports](#ports)
- [Notes](#notes)
- [Running Locally](#running-locally)
- [Adding a Version Line](#adding-a-version-line)

## Introduction

The MySQL engine for [Databasify](https://github.com/databasifylabs/databasify), built on the [engine SDK](https://github.com/databasifylabs/engine-sdk).

Each image is the official `mysql` image plus its tools and `databasify`, the [engine SDK](https://github.com/databasifylabs/engine-sdk) 1.0 runtime that runs as PID 1. Images are published as `ghcr.io/databasifylabs/databasify-mysql`.

Engine behaviour lives in the Databasify app: its PHP driver for this engine runs Bash scripts inside the container through the runtime. This repository changes only when the database or one of its tools moves to a new version.

## Lines

MySQL 8.4 and 9.7. Each line takes patches of its release only and never leaves it. The weekly refresh moves the pins in `versions.json`.

## Features

| Feature | MySQL |
| --- | --- |
| Node lifecycle, status, metrics, TLS | ✓ |
| Databases and users with read/write grants | ✓ |
| Password encryptions | `caching_sha2_password`; 8.4 also `mysql_native_password` |
| High availability | Group Replication |
| Automatic and manual failover, force quorum | ✓ |
| Standbys (up to 2) and read-only nodes (any number) | ✓ |
| Primary key required in a cluster | ✓ |
| Connection pools | — |
| Backups and point-in-time restore | XtraBackup and binary logs |
| Continuous migration | MySQL Shell and a GTID channel |
| Fork (one-shot copy) | ✓ |
| Query insights | statement digests |
| Host tuning | ✓ |

## Ports

| Port | Use |
| --- | --- |
| 25060 | `client`, for applications |
| 33061 | `peers`: group replication |

## Notes

- XtraBackup comes from Percona's apt repository, set up by `images/mysql/repositories.sh`.

## Running Locally

`$IMAGES` is `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the tag the `sdk` pin in `versions.json` names (`v1.0.0`). Run it from this repository's root:

```bash
$IMAGES check
$IMAGES build mysql-9.7 --load --tag smoke:mysql-9.7
$IMAGES smoke smoke:mysql-9.7 mysql
```

The smoke test starts the image, runs `images/mysql/smoke/check.sh` and then the SDK's runtime conformance suite.

## Adding a Version Line

Follow `.claude/skills/adding-a-version-line/SKILL.md`. It covers the `versions.json` entry, what differs per line, and the checks to run.
