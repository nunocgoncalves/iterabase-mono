package cli

import (
	"fmt"
	"io"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// hostInotifySummary is the compact observed-versus-required inotify evidence
// surfaced by the apply plan, the apply/upgrade result, and status. It is a
// thin wrapper over the shared evidence fragments so the status and
// fail-closed surfaces cannot drift.
func hostInotifySummary(state *provisioner.HostInotifyState) string {
	if state == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%s (%s drop-in %s)",
		boolLabel(state.Ready(), "ready", "drift"), state.CapacityEvidence(), state.DropInEvidence())
}

func printHostInotifyStatus(out io.Writer, state *provisioner.HostInotifyState) {
	if state == nil {
		return
	}
	fmt.Fprintf(out, "inotify:    %s\n", hostInotifySummary(state))
}
