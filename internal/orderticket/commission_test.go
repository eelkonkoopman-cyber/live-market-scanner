package orderticket

import "testing"

// the older sizing tests state the plain risk / distance arithmetic
func init() { commissionPerShare = 0 }

func TestRiskSizeCountsCommission(t *testing.T) {
	commissionPerShare = 0.006
	defer func() { commissionPerShare = 0 }()
	if q, _ := riskSize("BUY", 30, 0.2031, 0.1929, 1e6); q != 1851 { // 30 / (0.0102 + 0.006)
		t.Fatalf("qty %d", q)
	}
}
