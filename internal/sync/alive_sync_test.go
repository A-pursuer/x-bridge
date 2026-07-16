package sync

import (
	"reflect"
	"testing"

	"github.com/xboard-bridge/xboard-xui-bridge/internal/xui"
)

func TestExtractBatchIPsMergesAllGuidSubtrees(t *testing.T) {
	tree := map[string]map[string][]xui.ClientIpEntry{
		"guid-a": {
			"u@example.com": {
				{IP: "1.1.1.1", Timestamp: 10},
				{IP: "2.2.2.2", Timestamp: 30},
			},
		},
		"guid-b": {
			"u@example.com": {
				{IP: "1.1.1.1", Timestamp: 40},
				{IP: "", Timestamp: 50},
				{IP: "3.3.3.3", Timestamp: 30},
			},
			"other@example.com": {
				{IP: "9.9.9.9", Timestamp: 99},
			},
		},
	}

	got := extractBatchIPs(tree, "u@example.com")
	want := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("extractBatchIPs = %#v, want %#v", got, want)
	}
}

func TestExtractBatchIPsMissingEmail(t *testing.T) {
	tree := map[string]map[string][]xui.ClientIpEntry{
		"guid-a": {"u@example.com": {{IP: "1.1.1.1", Timestamp: 10}}},
	}

	got := extractBatchIPs(tree, "missing@example.com")
	if len(got) != 0 {
		t.Fatalf("extractBatchIPs = %#v, want empty", got)
	}
}
