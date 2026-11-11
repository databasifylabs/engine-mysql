package mysql

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	family "github.com/databasifylabs/engine-mysql-family"
	"github.com/databasifylabs/engine-sdk/dtos"
	"github.com/databasifylabs/engine-sdk/manifest"
	"github.com/databasifylabs/engine-sdk/requests"
)

const (
	clientPort   = 25060
	groupPort    = 33061
	serverUnit   = "mysql-server"
	engineUser   = "mysql"
	mysqldBinary = "/usr/sbin/mysqld"
	urlNamespace = "6ba7b811-9dad-11d1-80b4-00c04fd430c8"
	stopTimeout  = 10 * time.Minute
	restartPause = 3 * time.Second
)

func groupNameOf(databaseID string) string {
	namespace := uuidBytes(urlNamespace)
	hash := sha1.Sum(append(namespace, []byte(databaseID)...))
	hash[6] = hash[6]&0x0f | 0x50
	hash[8] = hash[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", hash[0:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
}

func uuidBytes(text string) []byte {
	var bytes []byte
	hex := strings.ReplaceAll(text, "-", "")
	for index := 0; index+1 < len(hex); index += 2 {
		value, _ := strconv.ParseUint(hex[index:index+2], 16, 8)
		bytes = append(bytes, byte(value))
	}

	return bytes
}

func serverIDOf(request requests.NodeConfigure) int64 {
	return family.ServerIDOfAddress(request.Ip)
}

func streamServerIDOf(request requests.NodeConfigure) int64 {
	return serverIDOf(request) ^ 0x80000000
}

func seedsOf(members []dtos.Member) string {
	seeds := []string{}
	for _, member := range members {
		if member.Voting {
			seeds = append(seeds, member.Ip+":"+strconv.Itoa(groupPort))
		}
	}

	return strings.Join(seeds, ",")
}

func myCnfOf(root string, line string, request requests.NodeConfigure) string {
	lines := []string{
		"[mysqld]",
		"datadir=" + root + "/data",
		"socket=" + root + "/run/mysqld.sock",
		"pid-file=" + root + "/run/mysqld.pid",
		"port=" + strconv.Itoa(clientPort),
		"bind-address=*",
		"report_host=" + request.Ip,
		"server_id=" + strconv.FormatInt(serverIDOf(request), 10),
		"gtid_mode=ON",
		"enforce_gtid_consistency=ON",
		"log_bin=binlog",
		"binlog_expire_logs_seconds=604800",
		"ssl_ca=" + root + "/tls/ca.crt",
		"ssl_cert=" + root + "/tls/server.crt",
		"ssl_key=" + root + "/tls/server.key",
		"tls_version=TLSv1.2,TLSv1.3",
		"plugin-load-add=mysql_clone.so",
		"performance_schema=ON",
		"performance-schema-consumer-statements-digest=ON",
	}
	if nativePasswords(line) {
		lines = append(lines, "mysql_native_password=ON")
	}
	if request.Clustered && !request.ReadOnly {
		lines = append(lines,
			"plugin-load-add=group_replication.so",
			"group_replication_group_name="+groupNameOf(request.DatabaseId),
			"group_replication_local_address="+request.Ip+":"+strconv.Itoa(groupPort),
			"group_replication_group_seeds="+seedsOf(request.Members),
			"group_replication_single_primary_mode=ON",
			"group_replication_start_on_boot=OFF",
			"group_replication_ssl_mode=REQUIRED",
			"group_replication_recovery_use_ssl=ON",
			"sql_require_primary_key=ON",
		)
	}
	if request.ReadOnly {
		lines = append(lines, "super_read_only=ON", "replica_parallel_workers=4")
	}
	for _, key := range slices.Sorted(maps.Keys(request.Settings)) {
		lines = append(lines, key+"="+request.Settings[key])
	}
	lines = append(lines, "!includedir "+root+"/conf/conf.d")

	return strings.Join(lines, "\n") + "\n"
}

func manifestOf(root string, request requests.NodeConfigure) ([]byte, error) {
	server := manifest.Process{
		Name:        serverUnit,
		User:        engineUser,
		Argv:        []string{mysqldBinary, "--defaults-file=" + root + "/conf/my.cnf"},
		Restart:     manifest.RestartAlways,
		RestartSec:  manifest.Duration(restartPause),
		StopSignal:  "TERM",
		StopTimeout: manifest.Duration(stopTimeout),
		Enabled:     true,
	}

	streams, schedules := family.ArchiveManifest(request, archiveUnits, serverUnit)
	processes := append([]manifest.Process{server}, streams...)

	return json.MarshalIndent(manifest.Manifest{Processes: processes, Schedules: schedules}, "", "  ")
}
