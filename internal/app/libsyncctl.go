package app

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/libsync"
)

// djSyncState is the SYNC-DJ run guard + last-result snapshot for LIBSYNC-STATUS.
type djSyncState struct {
	mu         sync.Mutex
	running    bool
	jobID      string
	dry        bool
	startedAt  time.Time
	finishedAt time.Time
	last       *libsync.Result
	err        string
}

// findSyncJob returns the configured sync job by ID, ok=false if absent.
func (c *appControl) findSyncJob(id string) (config.SyncJob, int, bool) {
	for i, j := range c.cfg.Features.LibrarySync.Jobs {
		if j.ID == id {
			return j, i, true
		}
	}
	return config.SyncJob{}, -1, false
}

// SyncDJ starts a cross-DJ-software sync job (ctl SYNC-DJ <id> [DRY]). Async - one run at a time.
func (c *appControl) SyncDJ(id string, dry bool) string {
	if c.lib == nil {
		return "unavailable"
	}
	job, idx, ok := c.findSyncJob(id)
	if !ok {
		return "unknown job"
	}
	c.djSync.mu.Lock()
	if c.djSync.running {
		c.djSync.mu.Unlock()
		return "already running"
	}
	c.djSync.running, c.djSync.jobID, c.djSync.dry = true, id, dry
	c.djSync.startedAt, c.djSync.finishedAt = time.Now(), time.Time{}
	c.djSync.last, c.djSync.err = nil, ""
	c.djSync.mu.Unlock()

	debuglog.Go(c.log, "dj-sync", func() {
		res, err := libsync.Run(c.lib, job, dry)
		c.djSync.mu.Lock()
		c.djSync.running, c.djSync.finishedAt, c.djSync.last = false, time.Now(), &res
		if err != nil {
			c.djSync.err = err.Error()
		}
		c.djSync.mu.Unlock()
		if err != nil {
			c.log.Warn("dj-sync", "job failed", map[string]any{"job": id, "err": err.Error()})
			return
		}
		c.log.Info("dj-sync", "job done", map[string]any{"job": id, "summary": res.Summary(), "dry": dry})
		if !dry && idx >= 0 {
			c.cfg.Features.LibrarySync.Jobs[idx].LastRunAt = time.Now().UTC().Format(time.RFC3339)
			c.cfg.Features.LibrarySync.Jobs[idx].LastSummary = res.Summary()
			if err := c.cfg.Save(); err != nil {
				c.log.Warn("dj-sync", "save run result failed", map[string]any{"err": err.Error()})
			}
		}
	})
	return "started"
}

// DJSyncStatus returns the cross-DJ sync state as one-line JSON (ctl LIBSYNC-STATUS).
func (c *appControl) DJSyncStatus() string {
	c.djSync.mu.Lock()
	defer c.djSync.mu.Unlock()
	out := map[string]any{"running": c.djSync.running, "job": c.djSync.jobID, "dry": c.djSync.dry}
	if !c.djSync.startedAt.IsZero() {
		out["started_at"] = c.djSync.startedAt.UTC().Format(time.RFC3339)
	}
	if !c.djSync.finishedAt.IsZero() {
		out["finished_at"] = c.djSync.finishedAt.UTC().Format(time.RFC3339)
	}
	if r := c.djSync.last; r != nil {
		out["scanned"], out["canonical"], out["tagged"] = r.Scanned, r.Canonical, r.Tagged
		out["targets"], out["summary"] = r.Targets, r.Summary()
		if len(r.Errors) > 0 {
			out["target_errors"] = r.Errors
		}
	}
	if c.djSync.err != "" {
		out["error"] = c.djSync.err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"running":false}`
	}
	return string(b)
}

// DJSyncList returns the configured sync jobs as JSON (ctl LIBSYNC-LIST).
func (c *appControl) DJSyncList() string {
	type jobOut struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Enabled     bool   `json:"enabled"`
		Scope       string `json:"scope"`
		Targets     int    `json:"targets"`
		LastRunAt   string `json:"lastRunAt,omitempty"`
		LastSummary string `json:"lastSummary,omitempty"`
	}
	var out []jobOut
	for _, j := range c.cfg.Features.LibrarySync.Jobs {
		out = append(out, jobOut{
			ID: j.ID, Label: j.Label, Enabled: j.Enabled,
			Scope: j.Scope.Kind, Targets: len(j.Targets),
			LastRunAt: j.LastRunAt, LastSummary: j.LastSummary,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// parseSyncDJCmd extracts the job ID + dry flag from a "SYNC-DJ <id> [DRY]" ctl line.
func parseSyncDJCmd(cmd string) (id string, dry bool) {
	fields := strings.Fields(cmd)
	if len(fields) >= 2 {
		id = fields[1]
	}
	if len(fields) >= 3 && strings.EqualFold(fields[2], "DRY") {
		dry = true
	}
	return id, dry
}
