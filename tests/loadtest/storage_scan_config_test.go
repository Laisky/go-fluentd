package main

import "testing"

func TestStorageScanConfigIsExplicitForCompatibleBinaries(t *testing.T) {
	for _, timeout := range []string{"", "1s"} {
		tr := trial{root: t.TempDir(), opts: options{Destinations: 1, StorageScanTimeout: timeout}}
		config := tr.config()["settings"].(map[string]any)["otlp"].(map[string]any)
		got, exists := config["storage_scan_timeout"]
		if exists != (timeout != "") {
			t.Fatalf("timeout=%q key existence=%v", timeout, exists)
		}
		if exists && got != timeout {
			t.Fatalf("timeout=%q value=%v", timeout, got)
		}
	}
}
