---
name: adding-a-version-line
description: "Add a new MySQL version line to this engine repository: the versions.json entry and its pinned tools, any per-line differences in the engine code, and the local build, smoke, conformance and capabilities check. Use when asked to support a new MySQL major or minor release line. Do not use for patch bumps inside an existing line (the weekly refresh opens those)."
---

# Adding a MySQL version line

A MySQL line is `<major>.<minor>` (`8.4`, `9.7`). It takes patches of its release only. The weekly refresh never moves a pin out of its line.

## The versions.json entry

1. Copy the newest `mysql-*` entry under `images` to the key `mysql-<line>`. Set `line`, `release` (the exact official tag), and `base.tag` to the official tag, keeping the suffix the other lines use.
2. Set `base.digest`. Get it from `docker buildx imagetools inspect <image>:<tag> --format '{{json .Manifest}}' | jq -r .digest`.
3. Set every package version to `"0"`, then run `$IMAGES refresh mysql-<line>`. It pins the packages and moves the base, copies, modules and sources to the newest patch of the line. It may also move the shared `go` digest: revert that unless you mean to rebuild every image.
4. Run `$IMAGES check`. It fails when a key isn't `<engine>-<line>`, when a pin left its line, or when `sdk` differs from `go.mod`.

## What differs per line in this engine

The image sets `DATABASIFY_LINE` from the line, and the engine reads it where releases differ:

- `engine/mysql.go` reads `DATABASIFY_LINE`. `nativePasswords(line)` offers `mysql_native_password` only on lines older than `nativePasswordsRemovedIn` (so 8.4); later lines offer `caching_sha2_password` only.
- `engine/node.go` writes `my.cnf` per line (`myCnfOf`). Check the release notes for removed or renamed server variables.
- XtraBackup comes from Percona's apt repository (`images/mysql/repositories.sh`) as a per-line package, such as `percona-xtrabackup-97` for 9.7. Rename it in the new entry's `packages`, and check that Percona publishes it for the release before adding the line.
- rclone (`modules.rclone`) doesn't depend on the line.

Add or extend a Go test for each per-line branch you add.

## Check the new line

`$IMAGES` stands for `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the SDK version this repository's `go.mod` requires. Run it from this repository's root:

```bash
go vet ./... && go test -race -count=1 ./...
$IMAGES check
$IMAGES build mysql-9.8 --load --tag smoke:mysql-9.8
$IMAGES smoke smoke:mysql-9.8 mysql
echo '{}' | docker run --rm -i --network none --entrypoint databasify smoke:mysql-9.8 engine capabilities | jq -S .
echo '{}' | docker run --rm -i --network none --entrypoint databasify smoke:mysql-9.8 config fields | jq -S .
```

- `smoke` runs `images/mysql/smoke/check.sh` against the new line, then the SDK's conformance suite.
- Compare `engine capabilities` and `config fields` with the closest existing line. Every difference should be one you meant, such as a password encryption or setting the new release adds.
- Also run the CI scan (`TRIVY_IMAGE` in the SDK's `.github/workflows/engine-images.yml`) on `smoke:mysql-9.8`.
- Put the results in the pull request with the line and OS you ran them on.

## The pull request

Open an issue first, branch from `main`, and open one pull request against `main` with the `versions.json` entry, any per-line code and tests, and the README's Lines section. The app registers the new line in its own repository ([databasifylabs/databasify](https://github.com/databasifylabs/databasify)), after this one merges.
