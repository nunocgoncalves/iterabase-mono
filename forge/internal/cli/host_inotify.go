package cli

import (
	"fmt"
	"io"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// hostInotifySummary is the compact observed-versus-required inotify evidence
// surfaced by the apply plan, the apply/upgrade result, and status.
func hostInotifySummary(state *provisioner.HostInotifyState) string {
	if state == nil {
		return "unavailable"
	}
	dropIn := "missing"
	if state.DropInPresent {
		dropIn = fmt.Sprintf("present regular=%t owner=%s mode=%s canonical=%t", state.DropInRegular, state.DropInOwner, state.DropInMode, state.DropInCanonical)
	}
	return fmt.Sprintf("%s (effective=%d required=%d persisted=%d drop-in %s)",
		boolLabel(state.Ready(), "ready", "drift"), state.Effective,
		provisioner.InotifyMaxUserInstancesRequired, state.Persisted, dropIn)
}

func printHostInotifyStatus(out io.Writer, state *provisioner.HostInotifyState) {
	if state == nil {
		return
	}
	fmt.Fprintf(out, "inotify:    %s\n", hostInotifySummary(state))
}
