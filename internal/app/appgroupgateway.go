package app

import (
	"fmt"

	"rave.page/mate/internal/appgroups"
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/studio"
)

// appGroupGateway adapts appgroups.Service to studio.AppGroupGateway so the web
// Local Studio can list/launch this desktop's app groups (crash-recovery sets) and
// poll their readiness - the launch step of quickAction.streamReady.
type appGroupGateway struct {
	svc     *appgroups.Service
	enabled func() bool
	groups  func() []config.AppGroup
}

func newAppGroupGateway(svc *appgroups.Service, enabled func() bool, groups func() []config.AppGroup) *appGroupGateway {
	return &appGroupGateway{svc: svc, enabled: enabled, groups: groups}
}

func (g *appGroupGateway) Configured() bool { return g.enabled() && len(g.groups()) > 0 }

func (g *appGroupGateway) List() []studio.AppGroupInfo {
	gs := g.groups()
	out := make([]studio.AppGroupInfo, 0, len(gs))
	for _, grp := range gs {
		running, total := g.svc.RunningCount(grp)
		out = append(out, studio.AppGroupInfo{ID: grp.ID, Name: grp.Name, Apps: total, Running: running})
	}
	return out
}

func (g *appGroupGateway) Launch(id string) (started, skipped []string, err error) {
	return g.svc.LaunchGroup(id)
}

func (g *appGroupGateway) Readiness(id string) (running, total int, err error) {
	grp, ok := g.svc.Group(id)
	if !ok {
		return 0, 0, fmt.Errorf("appgroups: no group %q", id)
	}
	running, total = g.svc.RunningCount(grp)
	return running, total, nil
}

var _ studio.AppGroupGateway = (*appGroupGateway)(nil)
