/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package healthcheck

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strings"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/sirupsen/logrus"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/log"

	"github.com/containerd/nerdctl/v2/pkg/defaults"
	"github.com/containerd/nerdctl/v2/pkg/labels"
	"github.com/containerd/nerdctl/v2/pkg/rootlessutil"
)

// CreateHealthCheckTimers sets up both health-interval and start-period systemd timers for healthchecks.
func CreateHealthCheckTimers(ctx context.Context, container containerd.Container) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc) {
		return nil
	}

	// Create health-interval timer (always created if healthcheck is configured)
	if err := createHealthIntervalTimer(ctx, container); err != nil {
		return fmt.Errorf("failed to create health-interval timer: %w", err)
	}

	// Create start-period timer (only if start period is configured)
	if err := createStartPeriodTimer(ctx, container); err != nil {
		return fmt.Errorf("failed to create start-period timer: %w", err)
	}

	return nil
}

// StartHealthCheckTimers starts both health-interval and start-period timer units.
func StartHealthCheckTimers(ctx context.Context, container containerd.Container) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc) {
		return nil
	}

	containerID := container.ID()

	// Start health-interval timer
	if err := startTimerForWorkflow(ctx, containerID, "health-interval"); err != nil {
		return fmt.Errorf("failed to start health-interval timer: %w", err)
	}

	// Start start-period timer (only if start period is configured)
	if hc.StartPeriod > 0 {
		if err := startTimerForWorkflow(ctx, containerID, "start-period"); err != nil {
			return fmt.Errorf("failed to start start-period timer: %w", err)
		}
	}

	return nil
}

// startTimerForWorkflow starts a specific timer workflow.
func startTimerForWorkflow(ctx context.Context, containerID string, workflowType string) error {
	conn, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	unitName := hcUnitNameWithType(containerID, workflowType, true)
	startChan := make(chan string)
	unit := unitName + ".service"
	if _, err := conn.RestartUnitContext(context.Background(), unit, "fail", startChan); err != nil {
		return err
	}
	if msg := <-startChan; msg != "done" {
		return fmt.Errorf("unexpected systemd restart result for %s: %s", workflowType, msg)
	}
	return nil
}

// RemoveTransientHealthCheckFiles stops and cleans up both health-interval and start-period timers.
func RemoveTransientHealthCheckFiles(ctx context.Context, container containerd.Container) error {
	return RemoveAllHealthCheckTimers(ctx, container.ID())
}

// RemoveTransientHealthCheckFilesByID stops and cleans up both health-interval and start-period timers using just the container ID.
func RemoveTransientHealthCheckFilesByID(ctx context.Context, containerID string) error {
	return RemoveAllHealthCheckTimers(ctx, containerID)
}

// hcUnitNameWithType returns a systemd unit name for a specific workflow type.
func hcUnitNameWithType(containerID string, workflowType string, bare bool) string {
	unit := containerID
	if workflowType == "start-period" {
		unit += "-start"
	}
	// For health-interval, we use the base name (no suffix)

	if !bare {
		unit += fmt.Sprintf("-%x", rand.Int())
	}
	return unit
}

func extractHealthcheck(ctx context.Context, container containerd.Container) *Healthcheck {
	l, err := container.Labels(ctx)
	if err != nil {
		log.G(ctx).WithError(err).Debugf("could not get labels for container %s", container.ID())
		return nil
	}
	hcStr, ok := l[labels.HealthCheck]
	if !ok || hcStr == "" {
		return nil
	}
	hc, err := HealthCheckFromJSON(hcStr)
	if err != nil {
		log.G(ctx).WithError(err).Debugf("invalid healthcheck config on container %s", container.ID())
		return nil
	}
	return hc
}

// shouldSkipHealthCheckSystemd determines if healthcheck timers should be skipped.
func shouldSkipHealthCheckSystemd(hc *Healthcheck) bool {
	// Don't proceed if systemd is unavailable or disabled
	if !defaults.IsSystemdAvailable() || os.Getenv("DISABLE_HC_SYSTEMD") == "true" {
		return true
	}

	// Don't proceed if health check is nil, empty, explicitly NONE or interval is 0.
	if hc == nil || len(hc.Test) == 0 || hc.Test[0] == "NONE" || hc.Interval == 0 {
		return true
	}
	return false
}

// createHealthIntervalTimer creates a systemd timer for health-interval workflow.
func createHealthIntervalTimer(ctx context.Context, container containerd.Container) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc) {
		return nil
	}

	containerID := container.ID()
	return createTimerForWorkflow(ctx, containerID, hc, "health-interval", hc.Interval.String())
}

// createStartPeriodTimer creates a systemd timer for start-period workflow.
func createStartPeriodTimer(ctx context.Context, container containerd.Container) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc) {
		return nil
	}

	// Skip if no start period is configured
	if hc.StartPeriod == 0 {
		return nil
	}

	containerID := container.ID()
	interval := hc.StartInterval.String()
	if hc.StartInterval == 0 {
		interval = DefaultStartInterval.String()
	}

	return createTimerForWorkflow(ctx, containerID, hc, "start-period", interval)
}

// createTimerForWorkflow creates a systemd timer for a specific workflow type.
func createTimerForWorkflow(ctx context.Context, containerID string, hc *Healthcheck, workflowType string, interval string) error {
	hcName := hcUnitNameWithType(containerID, workflowType, true)
	fmt.Printf("⏱️  Creating %s timer unit: %s\n", workflowType, hcName+".timer")

	cmd := []string{}
	if rootlessutil.IsRootless() {
		cmd = append(cmd, "--user")
	}
	if path := os.Getenv("PATH"); path != "" {
		cmd = append(cmd, "--setenv=PATH="+path)
	}

	cmd = append(cmd, "--unit", hcName+".timer", "--on-unit-inactive="+interval, "--timer-property=AccuracySec=1s")

	// Add the appropriate workflow flag
	cmd = append(cmd, "nerdctl", "container", "healthcheck")
	if workflowType == "health-interval" {
		cmd = append(cmd, "--health")
	} else if workflowType == "start-period" {
		cmd = append(cmd, "--start-period")
	}
	cmd = append(cmd, containerID)

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		cmd = append(cmd, "--debug")
	}

	conn, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	logrus.Debugf("creating %s timer with: systemd-run %s", workflowType, strings.Join(cmd, " "))
	run := exec.Command("systemd-run", cmd...)
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run failed for %s: %w\noutput: %s", workflowType, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// RemoveHealthIntervalTimer removes the health-interval timer for a container.
func RemoveHealthIntervalTimer(ctx context.Context, containerID string) error {
	return removeTimerForWorkflow(ctx, containerID, "health-interval")
}

// RemoveStartPeriodTimer removes the start-period timer for a container.
func RemoveStartPeriodTimer(ctx context.Context, containerID string) error {
	return removeTimerForWorkflow(ctx, containerID, "start-period")
}

// removeTimerForWorkflow removes a systemd timer for a specific workflow type.
func removeTimerForWorkflow(ctx context.Context, containerID string, workflowType string) error {
	// Don't proceed if systemd is unavailable or disabled
	if !defaults.IsSystemdAvailable() || os.Getenv("DISABLE_HC_SYSTEMD") == "true" {
		return nil
	}

	fmt.Printf("⏱️  Removing %s timer unit: %s\n", workflowType, containerID)

	conn, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	unitName := hcUnitNameWithType(containerID, workflowType, true)
	timer := unitName + ".timer"
	service := unitName + ".service"

	// Stop timer
	tChan := make(chan string)
	if _, err := conn.StopUnitContext(context.Background(), timer, "ignore-dependencies", tChan); err == nil {
		if msg := <-tChan; msg != "done" {
			logrus.Warnf("%s timer stop message: %s", workflowType, msg)
		}
	}

	// Stop service
	sChan := make(chan string)
	if _, err := conn.StopUnitContext(context.Background(), service, "ignore-dependencies", sChan); err == nil {
		if msg := <-sChan; msg != "done" {
			logrus.Warnf("%s service stop message: %s", workflowType, msg)
		}
	}

	// Reset failed units
	_ = conn.ResetFailedUnitContext(context.Background(), service)
	return nil
}

// RemoveAllHealthCheckTimers removes both health-interval and start-period timers for a container.
func RemoveAllHealthCheckTimers(ctx context.Context, containerID string) error {
	// Remove health-interval timer
	if err := RemoveHealthIntervalTimer(ctx, containerID); err != nil {
		logrus.Warnf("Failed to remove health-interval timer for %s: %v", containerID, err)
	}

	// Remove start-period timer
	if err := RemoveStartPeriodTimer(ctx, containerID); err != nil {
		logrus.Warnf("Failed to remove start-period timer for %s: %v", containerID, err)
	}

	return nil
}
