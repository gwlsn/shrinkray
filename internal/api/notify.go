package api

import (
	"fmt"

	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/logger"
	"github.com/gwlsn/shrinkray/internal/util"
)

// startNotificationWorker subscribes to queue events and sends Pushover
// notifications when all jobs finish. Runs for the lifetime of the handler,
// independent of any browser SSE connection. This ensures notifications fire
// even when no browser tab is open.
func (h *Handler) startNotificationWorker() {
	eventCh := h.queue.Subscribe()
	go func() {
		for event := range eventCh {
			if event.Type == "complete" || event.Type == "failed" ||
				event.Type == "cancelled" || event.Type == "skipped" {
				h.sendNotificationIfReady()
			}
		}
	}()
}

// sendNotificationIfReady sends a Pushover notification if all jobs are done
// and notifications are enabled. Safe to call concurrently - the notifyMu
// mutex ensures only one notification is sent per batch.
func (h *Handler) sendNotificationIfReady() {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()

	if !h.getNotifyOnComplete() || !h.pushover.IsConfigured() {
		return
	}

	stats := h.queue.Stats()
	if stats.Pending > 0 || stats.Running > 0 {
		return
	}

	message := fmt.Sprintf("%d jobs complete, %d failed\nSaved %s",
		stats.Complete, stats.Failed, util.FormatBytes(stats.TotalSaved))

	if err := h.pushover.Send("Shrinkray Complete", message); err != nil {
		logger.Warn("Failed to send Pushover notification", "error", err)
		return
	}

	// Disable the checkbox until the user re-enables it for the next batch
	h.setNotifyOnComplete(false)

	// Broadcast notify_sent to all connected SSE clients so they uncheck the
	// notification checkbox. Works even if multiple tabs are open.
	h.queue.BroadcastEvent(jobs.JobEvent{Type: "notify_sent"})
}
