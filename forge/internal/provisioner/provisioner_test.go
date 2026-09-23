package provisioner

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHostInotifyEvidenceFragments pins the shared evidence vocabulary used by
// both the human status surface (cli hostInotifySummary) and the fail-closed
// evidence string (HostInotifyState.String), so a wording change cannot
// silently diverge the two operator-facing forms.
func TestHostInotifyEvidenceFragments(t *testing.T) {
	ready := HostInotifyState{
		Effective:       InotifyMaxUserInstancesRequired,
		Persisted:       InotifyMaxUserInstancesRequired,
		DropInPresent:   true,
		DropInRegular:   true,
		DropInOwner:     "0:0",
		DropInMode:      "644",
		DropInCanonical: true,
	}
	assert.Equal(t, "effective=8192 required=8192 persisted=8192", ready.CapacityEvidence())
	assert.Equal(t, "present regular=true owner=0:0 mode=644 canonical=true", ready.DropInEvidence())
	assert.Equal(t, "effective=8192 required=8192 persisted=8192 drop-in=present regular=true owner=0:0 mode=644 canonical=true ready=true", ready.String())

	missing := HostInotifyState{Effective: 128}
	assert.Equal(t, "effective=128 required=8192 persisted=0", missing.CapacityEvidence())
	assert.Equal(t, "missing", missing.DropInEvidence())
	assert.Equal(t, "effective=128 required=8192 persisted=0 drop-in=missing ready=false", missing.String())
}

func TestHostInotifyStateReadyAcceptsSufficientLiveCapacity(t *testing.T) {
	canonical := HostInotifyState{
		Effective:       InotifyMaxUserInstancesRequired * 2,
		Persisted:       InotifyMaxUserInstancesRequired,
		DropInPresent:   true,
		DropInRegular:   true,
		DropInOwner:     "0:0",
		DropInMode:      "644",
		DropInCanonical: true,
	}
	assert.True(t, canonical.Ready(), "a live value above the requirement must stay ready and never be lowered")

	drifted := canonical
	drifted.DropInCanonical = false
	assert.False(t, drifted.Ready())
}
