package db

import "testing"

// The upsert names its columns, so the model's columns must be those names.
// GORM's own naming would write MemoryGiBHour as memory_gi_b_hour, and the
// upsert would then fail on every save — which the API's fake store cannot see.
func TestCostRatesColumnsAreTheOnesTheUpsertNames(t *testing.T) {
	parsed, err := schemaOf(&ClusterCostRates{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"cluster_id", "currency", "cpu_core_hour", "memory_gib_hour",
		"storage_gib_month", "load_balancer_hour", "updated_at",
	} {
		if parsed.LookUpField(column) == nil {
			t.Errorf("no column %q on cluster_cost_rates", column)
		}
	}
}

// Same rule for the vulnerability-scanning row: the upsert names its columns.
func TestVulnScanningColumnsAreTheOnesTheUpsertNames(t *testing.T) {
	parsed, err := schemaOf(&ClusterVulnScanning{})
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"cluster_id", "provider", "enabled", "last_checked_at", "last_status",
		"last_message", "detected_version", "updated_at",
	} {
		if parsed.LookUpField(column) == nil {
			t.Errorf("no column %q on cluster_vuln_scanning", column)
		}
	}
}
