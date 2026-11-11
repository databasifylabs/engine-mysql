# Databasify MySQL Engine

- [Introduction](#introduction)
- [Lines](#lines)
- [Features](#features)
- [Ports](#ports)
- [Engine Notes](#engine-notes)
- [Running Locally](#running-locally)
- [Adding a Version Line](#adding-a-version-line)

## Introduction

The MySQL engine for [Databasify](https://github.com/databasifylabs/databasify), built on the [engine SDK](https://github.com/databasifylabs/engine-sdk).

Each image is the official `mysql` image plus `databasify`, the engine binary that runs as PID 1 and answers the SDK's typed commands. Images are published as `ghcr.io/databasifylabs/databasify-mysql`.

The commands and the flows Databasify runs are the same for every engine; see the SDK's [Flows](https://github.com/databasifylabs/engine-sdk#flows).

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
| Log forwarding and host tuning | ✓ |

A command the engine doesn't offer answers `unsupported`; `engine capabilities` lists the ones it does.

## Ports

| Port | Use |
| --- | --- |
| 25060 | `client`, for applications |
| 33061 | `peers`: group replication |

## Engine Notes

- The code MySQL shares with [engine-mariadb](https://github.com/databasifylabs/engine-mariadb) and [engine-mongodb](https://github.com/databasifylabs/engine-mongodb) lives in [engine-mysql-family](https://github.com/databasifylabs/engine-mysql-family).
- XtraBackup comes from Percona's apt repository, set up by `images/mysql/repositories.sh`.

## Running Locally

`$IMAGES` is `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the version `go.mod` requires. Run it from this repository's root:

```bash
go vet ./... && go test -race -count=1 ./...
$IMAGES build mysql-9.7 --load --tag smoke:mysql-9.7
$IMAGES smoke smoke:mysql-9.7 mysql
```

The smoke test starts the image, runs `images/mysql/smoke/check.sh` and then the SDK's conformance suite.

## Adding a Version Line

Follow `.claude/skills/adding-a-version-line/SKILL.md`. It covers the `versions.json` entry, the code that differs per line in this engine, and the checks to run.
