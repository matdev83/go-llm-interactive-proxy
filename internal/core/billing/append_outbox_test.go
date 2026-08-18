package billing

import "testing"

func TestUsageAppendWorkIsMigrationOnly(t *testing.T) {
	if UsageAppendCall == UsageAppendLeg {
		t.Fatal("call and leg migration kinds must remain distinct")
	}
	work := UsageAppendWork{Key: "legacy", Kind: UsageAppendCall}
	if work.Key == "" || work.Kind != UsageAppendCall {
		t.Fatalf("invalid migration work: %+v", work)
	}
}
