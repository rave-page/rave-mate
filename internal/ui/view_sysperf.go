package ui

// System-performance dashboard card: app + whole-system CPU/RAM traces off perfmon's
// 1 Hz collector (same LCD chassis as the network graphs), plus an explicit HEADROOM
// readout so newcomers see how much room is left. Every metric carries a ?-tooltip.

import (
	"fmt"
	"math"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

const perfSpan = 120 // 2 min at 1 Hz - one graph column per sample

// Metric tooltips (education-first; see CLAUDE.md standing requirement).
const (
	helpAppCPU = "CPU used by rave-mate, as % of ONE core - 100% = one full core busy (can exceed 100% when work spreads over cores). Sustained >150% while you're idle usually means a runaway loop - run 'ctl perf' to attribute it."
	helpSysCPU = "CPU used by the WHOLE machine, % of all cores combined. Near 100% = everything (OBS, VRChat, browser …) together saturates the box; expect stutter and audio glitches above ~90%."
	helpAppRAM = "RAM held by the rave-mate process (working set / RSS). Steady growth that never comes back down can indicate a leak - 'ctl perf' shows heap vs total."
	helpSysRAM = "Physical RAM used by everything on this machine vs installed total. When it fills up the OS starts swapping to disk and the whole system crawls."
	helpHead   = "How much room is left RIGHT NOW: free physical RAM and unused system CPU. Keep some spare before going live - a set + OBS + VR can eat several GB and full cores in bursts."
)

// buildSysPerfContent is the System-performance module. nil when perfmon is absent.
func (u *UI) buildSysPerfContent() fyne.CanvasObject {
	if u.svc.Perf == nil {
		return nil
	}
	cpuG := newNetGraph(perfSpan)
	ramG := newNetGraph(perfSpan)

	appCPU := lcdText(colBrandMint)
	sysCPU := lcdText(colBrandHot)
	appRAM := lcdText(colBrandViol)
	sysRAM := lcdText(colBrandAmber)
	head := lcdText(withAlpha(colBrandMint, 0xcc))
	cpuLegend := container.NewHBox(appCPU, helpIcon(helpAppCPU), sysCPU, helpIcon(helpSysCPU))
	ramLegend := container.NewHBox(appRAM, helpIcon(helpAppRAM), sysRAM, helpIcon(helpSysRAM))
	headRow := container.NewHBox(head, helpIcon(helpHead))
	well := newBeveledPanel(container.NewVBox(cpuLegend, cpuG, ramLegend, ramG, headRow), colLCD, false, 6)

	update := func() {
		ss := u.svc.Perf.Snapshot()
		if len(ss) > perfSpan {
			ss = ss[len(ss)-perfSpan:]
		}
		appC := make([]float64, len(ss))
		sysC := make([]float64, len(ss))
		appR := make([]float64, len(ss))
		sysR := make([]float64, len(ss))
		for i, s := range ss {
			appC[i], appR[i] = s.CPUPct, s.RSSMB
			if s.SysOK {
				sysC[i], sysR[i] = s.SysCPUPct, s.SysMemUsedMB
			} else {
				sysC[i], sysR[i] = math.NaN(), math.NaN() // gap, not zero
			}
		}
		appCPU.Text, sysCPU.Text = "APP -", "SYS -"
		appRAM.Text, sysRAM.Text = "APP -", "SYS -"
		head.Text = "HEADROOM -"
		if len(ss) > 0 {
			last := ss[len(ss)-1]
			appCPU.Text = fmt.Sprintf("APP %.0f%%", last.CPUPct)
			appRAM.Text = fmt.Sprintf("APP %.0f MB", last.RSSMB)
			if last.SysOK {
				sysCPU.Text = fmt.Sprintf("SYS %.0f%%", last.SysCPUPct)
				sysRAM.Text = fmt.Sprintf("SYS %.1f/%.1f GB", last.SysMemUsedMB/1024, last.SysMemTotalMB/1024)
				head.Text = fmt.Sprintf("HEADROOM %.1f GB RAM · %.0f%% CPU FREE",
					(last.SysMemTotalMB-last.SysMemUsedMB)/1024, math.Max(0, 100-last.SysCPUPct))
			} else {
				head.Text = "HEADROOM - (no system stats on this OS)"
			}
		}
		for _, t := range []*canvas.Text{appCPU, sysCPU, appRAM, sysRAM, head} {
			canvas.Refresh(t)
		}
		cpuG.SetSeries([]graphSeries{
			{vals: appC, col: toNRGBA(colBrandMint), fill: true},
			{vals: sysC, col: toNRGBA(colBrandHot)},
		})
		// RAM traces share the MB scale: SYS used dominates, APP hugs the floor -
		// honest proportions (the legend carries the exact numbers).
		ramG.SetSeries([]graphSeries{
			{vals: sysR, col: toNRGBA(colBrandAmber)},
			{vals: appR, col: toNRGBA(colBrandViol), fill: true},
		})
	}
	update()
	tick := time.NewTicker(time.Second)
	u.closers = append(u.closers, tick.Stop)
	goUI("dashboard-perf", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return well
}
