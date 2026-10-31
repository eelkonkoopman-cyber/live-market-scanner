package scan

import (
	"testing"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

func init() { minScanPrice = 0 }

func TestMinScanPrice(t *testing.T) {
	minScanPrice = 0.50
	defer func() { minScanPrice = 0 }()
	s := &Scanner{scans: map[string]*scanState{}}
	s.upsert(core.ScanDef{ID: "x", Enabled: true})
	p := s.scans["x"].pred
	if p(&core.Row{Last: 0.49}) {
		t.Fatal("0.49 listed")
	}
	if !p(&core.Row{Last: 0.50}) {
		t.Fatal("0.50 refused")
	}
}
