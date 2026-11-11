package mysql

import (
	"testing"

	"github.com/databasifylabs/engine-sdk/dtos"
)

func TestMysqlTuningFollowsTheNodeSize(t *testing.T) {
	values := tuned(dtos.Member{Cpus: 4, MemoryMb: 8192, DiskGb: 80})

	want := map[string]string{"innodb_buffer_pool_size": "5248M", "max_connections": "608", "innodb_redo_log_capacity": "1312M", "innodb_read_io_threads": "4", "innodb_log_buffer_size": "64M"}
	for key, value := range want {
		if values[key] != value {
			t.Errorf("%s = %s, want %s", key, values[key], value)
		}
	}
}

func TestOnlyMysqlLimitsAreEnforced(t *testing.T) {
	if err := catalog().Check(map[string]string{"wait_timeout": "31536000", "max_connections": "100000", "innodb_buffer_pool_size": "64G", "sql_mode": "STRICT_ALL_TABLES,NO_ZERO_DATE"}); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"max_connections": "100001", "binlog_expire_logs_seconds": "3600", "sql_require_primary_key": "OFF", "innodb_buffer_pool_size": "1M"} {
		if catalog().Check(map[string]string{key: value}) == nil {
			t.Errorf("%s=%s was accepted", key, value)
		}
	}
}
