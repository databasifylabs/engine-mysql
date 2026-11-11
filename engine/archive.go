package mysql

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/enums"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/responses"
)

const (
	agentCnf         = "conf/agent.cnf"
	backupTemp       = "backup/tmp"
	backupMeta       = "backup/meta"
	restoreDir       = "restore"
	baseSuffix       = ".xbstream"
	xtrabackupBinary = "/usr/bin/xtrabackup"
	xbstreamBinary   = "/usr/bin/xbstream"
	mysqlbinlog      = "/usr/bin/mysqlbinlog"
	mysqlClient      = "/usr/bin/mysql"
)

var archiveUnits = family.ArchiveUnits{Stream: "mysql-binlog-stream", Ship: "mysql-binlog-ship", Backup: "mysql-backup"}

type archive struct{ Engine }

func (e Engine) Archive() contract.Archive { return archive{e} }

func (e Engine) archiver() family.Archiver {
	return family.Archiver{Root: e.root, DB: e.db, Supervisor: e.supervisor, Units: archiveUnits, BaseSuffix: baseSuffix}
}

func agentCnfOf(root string, password string) string {
	return "[client]\nuser=databasify_agent\npassword=" + password + "\nsocket=" + root + "/run/mysqld.sock\n\n[xtrabackup]\ndatadir=" + root + "/data\n"
}

func (e Engine) isPrimary(ctx context.Context) bool {
	role, err := e.role(ctx)

	return err == nil && role == enums.NodeRolePrimary
}

func (a archive) Backup(ctx context.Context) error {
	settings, err := family.Storage(a.root)
	if err != nil || !a.isPrimary(ctx) {
		return err
	}
	for _, dir := range []string{backupTemp, backupMeta} {
		_ = os.RemoveAll(a.root.Path(dir))
		if err := os.MkdirAll(a.root.Path(dir), 0o700); err != nil {
			return err
		}
	}
	backup := exec.CommandContext(ctx, xtrabackupBinary, "--defaults-file="+a.root.Path(agentCnf), "--backup", "--stream=xbstream",
		"--target-dir="+a.root.Path(backupTemp), "--extra-lsndir="+a.root.Path(backupMeta))

	return a.archiver().Upload(ctx, settings, backup, a.root.Path(backupMeta+"/xtrabackup_info"))
}

func (a archive) Stream(ctx context.Context) error {
	return family.StreamWhile(ctx, a.isPrimary, a.streamOnce)
}

func (a archive) streamOnce(ctx context.Context) error {
	if err := os.MkdirAll(a.root.Path(family.BinlogDir), 0o700); err != nil {
		return err
	}
	first, err := a.archiver().FirstBinlog(ctx, a.db)
	if err != nil {
		return err
	}
	request, err := family.Saved(a.root)
	if err != nil {
		return err
	}
	output, err := a.run(ctx, mysqlbinlog, "--defaults-extra-file="+a.root.Path(agentCnf), "--read-from-remote-server", "--raw", "--stop-never",
		"--connection-server-id="+strconv.FormatInt(streamServerIDOf(request), 10), "--result-file="+a.root.Path(family.BinlogDir)+"/", first)
	if err != nil {
		return fmt.Errorf("%w: %s", err, family.LastLines(output, 3))
	}

	return nil
}

func (a archive) Ship(ctx context.Context) error {
	settings, err := family.Storage(a.root)
	if err != nil {
		return err
	}
	uuid, _, err := a.db.Value(ctx, "SELECT @@server_uuid")
	if err != nil {
		return err
	}

	return a.archiver().Ship(ctx, settings, uuid)
}

func (a archive) Status(ctx context.Context, request requests.ArchiveStatus) (responses.ArchiveStatus, error) {
	return a.archiver().Status(ctx, request)
}

func (a archive) Restore(ctx context.Context, request requests.RestoreStart, report contract.Report) error {
	report("downloading")
	info, err := a.archiver().Download(ctx, request.Storage, request.Target, restoreDir, func(dir string) *exec.Cmd {
		return exec.CommandContext(ctx, xbstreamBinary, "-x", "-C", dir)
	})
	if err != nil {
		return err
	}
	baseGtids, err := family.BackupGtids(info)
	if err != nil {
		return err
	}
	report("preparing")
	if _, err := a.run(ctx, xtrabackupBinary, "--prepare", "--target-dir="+a.root.Path(restoreDir+"/data")); err != nil {
		return err
	}
	if err := a.replaceData(ctx); err != nil {
		return err
	}
	report("replaying")
	if err := a.replay(ctx, baseGtids, request.Target); err != nil {
		return err
	}

	return os.RemoveAll(a.root.Path(restoreDir))
}
func (a archive) replaceData(ctx context.Context) error {
	if err := a.supervisor.Do("stop", serverUnit); err != nil {
		return err
	}
	if err := os.RemoveAll(a.root.Path("data")); err != nil {
		return err
	}
	if err := a.root.Dir("data"); err != nil {
		return err
	}
	if _, err := a.run(ctx, xtrabackupBinary, "--defaults-file="+a.root.Path(agentCnf), "--move-back", "--target-dir="+a.root.Path(restoreDir+"/data")); err != nil {
		return err
	}
	if _, err := a.run(ctx, "chown", "-R", "64999:64999", a.root.Path("data")); err != nil {
		return err
	}
	if err := a.supervisor.Do("start", serverUnit); err != nil {
		return err
	}

	return family.Until(ctx, readyTimeout, func() (bool, error) {
		_, _, err := a.db.Value(ctx, "SELECT 1")

		return err == nil, nil
	})
}

func (a archive) replay(ctx context.Context, baseGtids string, target time.Time) error {
	hosts, _ := os.ReadDir(a.root.Path(restoreDir + "/binlogs"))
	for _, host := range hosts {
		files, _ := filepath.Glob(a.root.Path(restoreDir + "/binlogs/" + host.Name() + "/*"))
		slices.SortFunc(files, cmp.Compare[string])
		if len(files) == 0 {
			continue
		}
		args := append([]string{"--exclude-gtids=" + baseGtids, "--stop-datetime=" + target.UTC().Format(family.ReplayLayout)}, files...)
		decode := exec.CommandContext(ctx, mysqlbinlog, args...)
		decode.Env = append(os.Environ(), "TZ=UTC")
		apply := exec.CommandContext(ctx, mysqlClient, "--defaults-extra-file="+a.root.Path("conf/admin.cnf"))
		if err := family.PipeInto(decode, func(stream io.Reader) error {
			apply.Stdin = stream
			output, err := apply.CombinedOutput()
			if err != nil {
				return fmt.Errorf("replaying %s: %w: %s", host.Name(), err, family.LastLines(string(output), 3))
			}

			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}
