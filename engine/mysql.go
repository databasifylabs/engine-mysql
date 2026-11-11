package mysql

import (
	"context"
	"os"

	"github.com/databasifylabs/engine-sdk/version"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/contract"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/layout"
	"github.com/databasifylabs/engine-sdk/nodefs"
	"github.com/databasifylabs/engine-sdk/requests"
	"github.com/databasifylabs/engine-sdk/units"
)

const (
	root       = "/srv/mysql"
	socketPath = root + "/run/mysqld.sock"
	adminCnf   = root + "/conf/admin.cnf"

	nativePasswordsRemovedIn = "9.0"
)

type Engine struct {
	db         family.DB
	supervisor family.Supervisor
	run        family.Runner
	line       string
	root       nodefs.Root
}

func New() Engine {
	return Engine{
		db:         family.SocketDB{Socket: socketPath, PasswordCnf: adminCnf},
		supervisor: units.Control{Socket: layout.FromEnv().Socket()},
		run:        family.ExecRunner,
		line:       os.Getenv("DATABASIFY_LINE"),
		root:       root,
	}
}

func (Engine) Name() string { return "mysql" }

func nativePasswords(line string) bool {
	return version.NewerThan(nativePasswordsRemovedIn, line)
}

func (e Engine) Capabilities() contract.Capabilities {
	encryption := "caching_sha2_password"
	encryptions := []string{encryption}
	if nativePasswords(e.line) {
		encryptions = append(encryptions, "mysql_native_password")
	}

	return contract.Capabilities{
		PasswordEncryptions:       encryptions,
		DefaultPasswordEncryption: &encryption,
		ReservedUsers:             []string{"root", "mysql.sys", "mysql.session", "mysql.infoschema", "databasify_repl", "databasify_agent"},
		ReservedDatabases:         []string{"mysql", "sys", "performance_schema", "information_schema"},
		Ports:                     dtos.Ports{Client: clientPort, Peers: []int{groupPort}},
	}
}

func (Engine) HostProfile() contract.HostProfile { return family.HostProfile() }

type nodes struct{ Engine }

func (e Engine) Nodes() contract.Nodes { return nodes{e} }

func (e Engine) Accounts() contract.Accounts {
	return family.Accounts{DB: e.db, Capabilities: e.Capabilities(), Identified: func(encryption string) string {
		return "IDENTIFIED WITH " + encryption + " BY ?"
	}}
}

func (Engine) Pools() contract.Pools { return contract.UnsupportedPools{} }

func (e Engine) Config() contract.Config { return e.configs() }

func (e Engine) configs() family.Configs {
	return family.Configs{
		DB: e.db, Root: e.root, Catalog: catalog(), Variables: timeVariables, Apply: e.Nodes().ApplyConfig,
		Configure: func(ctx context.Context, request requests.NodeConfigure) error {
			_, err := e.Nodes().Configure(ctx, request)

			return err
		},
	}
}
