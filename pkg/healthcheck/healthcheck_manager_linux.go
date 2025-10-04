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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/log"

	"github.com/containerd/nerdctl/v2/pkg/config"
	"github.com/containerd/nerdctl/v2/pkg/defaults"
	"github.com/containerd/nerdctl/v2/pkg/labels"
	"github.com/containerd/nerdctl/v2/pkg/rootlessutil"
)

// CreateTimer sets up the transient systemd timer and service for healthchecks.
func CreateTimer(ctx context.Context, container containerd.Container, cfg *config.Config) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc, cfg) {
		return nil
	}

	containerID := container.ID()
	log.G(ctx).Debugf("Creating healthcheck timer unit: %s", containerID)

	cmdOpts := []string{}
	if path := os.Getenv("PATH"); path != "" {
		cmdOpts = append(cmdOpts, "--setenv=PATH="+path)
	}

	// Always use health-interval for timer frequency
	cmdOpts = append(cmdOpts, "--unit", containerID, "--on-unit-inactive="+hc.Interval.String(), "--timer-property=AccuracySec=1s")

	cmdOpts = append(cmdOpts, "nerdctl", "container", "healthcheck", containerID)
	if log.G(ctx).Logger.IsLevelEnabled(log.DebugLevel) {
		cmdOpts = append(cmdOpts, "--debug")
	}

	log.G(ctx).Debugf("creating healthcheck timer with: systemd-run %s", strings.Join(cmdOpts, " "))
	run := exec.Command("systemd-run", cmdOpts...)
	if out, err := run.CombinedOutput(); err != nil {
		return fmt.Errorf("systemd-run failed: %w\noutput: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// StartTimer starts the healthcheck timer unit.
func StartTimer(ctx context.Context, container containerd.Container, cfg *config.Config) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc, cfg) {
		return nil
	}

	containerID := container.ID()
	var conn *dbus.Conn
	var err error
	if rootlessutil.IsRootless() {
		conn, err = dbus.NewUserConnectionContext(ctx)
	} else {
		conn, err = dbus.NewSystemConnectionContext(ctx)
	}
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	startChan := make(chan string)
	unit := containerID + ".service"
	if _, err := conn.RestartUnitContext(context.Background(), unit, "fail", startChan); err != nil {
		return err
	}
	if msg := <-startChan; msg != "done" {
		return fmt.Errorf("unexpected systemd restart result: %s", msg)
	}
	return nil
}

// RemoveTransientHealthCheckFiles stops and cleans up the transient timer and service.
func RemoveTransientHealthCheckFiles(ctx context.Context, container containerd.Container) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}

	return ForceRemoveTransientHealthCheckFiles(ctx, container.ID())
}

// ForceRemoveTransientHealthCheckFiles forcefully stops and cleans up the transient timer and service
// using just the container ID. This function is non-blocking and uses timeouts to prevent hanging
// on systemd operations. It logs errors as warnings but continues cleanup attempts.
// func ForceRemoveTransientHealthCheckFiles(ctx context.Context, containerID string) error {
// 	log.G(ctx).Debugf("Force removing healthcheck timer unit: %s", containerID)

// 	// Create a timeout context for systemd operations
// 	timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
// 	defer cancel()

// 	timer := containerID + ".timer"
// 	service := containerID + ".service"

// 	// Channel to collect any critical errors (though we'll continue cleanup regardless)
// 	errChan := make(chan error, 3)

// 	// Goroutine for DBUS connection and cleanup operations
// 	go func() {
// 		defer close(errChan)

// 		var conn *dbus.Conn
// 		var err error
// 		if rootlessutil.IsRootless() {
// 			conn, err = dbus.NewUserConnectionContext(ctx)
// 		} else {
// 			conn, err = dbus.NewSystemConnectionContext(ctx)
// 		}
// 		if err != nil {
// 			log.G(ctx).Warnf("systemd DBUS connect error during force cleanup: %v", err)
// 			errChan <- fmt.Errorf("systemd DBUS connect error: %w", err)
// 			return
// 		}
// 		defer conn.Close()

// 		// Stop timer with timeout
// 		go func() {
// 			select {
// 			case <-timeoutCtx.Done():
// 				log.G(ctx).Warnf("timeout stopping timer %s during force cleanup", timer)
// 				return
// 			default:
// 				tChan := make(chan string, 1)
// 				if _, err := conn.StopUnitContext(timeoutCtx, timer, "ignore-dependencies", tChan); err == nil {
// 					select {
// 					case msg := <-tChan:
// 						if msg != "done" {
// 							log.G(ctx).Warnf("timer stop message during force cleanup: %s", msg)
// 						}
// 					case <-timeoutCtx.Done():
// 						log.G(ctx).Warnf("timeout waiting for timer stop confirmation: %s", timer)
// 					}
// 				} else {
// 					log.G(ctx).Warnf("failed to stop timer %s during force cleanup: %v", timer, err)
// 				}
// 			}
// 		}()

// 		// Stop service with timeout
// 		go func() {
// 			select {
// 			case <-timeoutCtx.Done():
// 				log.G(ctx).Warnf("timeout stopping service %s during force cleanup", service)
// 				return
// 			default:
// 				sChan := make(chan string, 1)
// 				if _, err := conn.StopUnitContext(timeoutCtx, service, "ignore-dependencies", sChan); err == nil {
// 					select {
// 					case msg := <-sChan:
// 						if msg != "done" {
// 							log.G(ctx).Warnf("service stop message during force cleanup: %s", msg)
// 						}
// 					case <-timeoutCtx.Done():
// 						log.G(ctx).Warnf("timeout waiting for service stop confirmation: %s", service)
// 					}
// 				} else {
// 					log.G(ctx).Warnf("failed to stop service %s during force cleanup: %v", service, err)
// 				}
// 			}
// 		}()

// 		// Reset failed units (best effort, non-blocking)
// 		go func() {
// 			select {
// 			case <-timeoutCtx.Done():
// 				log.G(ctx).Warnf("timeout resetting failed unit %s during force cleanup", service)
// 				return
// 			default:
// 				if err := conn.ResetFailedUnitContext(timeoutCtx, service); err != nil {
// 					log.G(ctx).Warnf("failed to reset failed unit %s during force cleanup: %v", service, err)
// 				}
// 			}
// 		}()

// 		// Wait a short time for operations to complete, but don't block indefinitely
// 		select {
// 		case <-time.After(3 * time.Second):
// 			log.G(ctx).Debugf("force cleanup operations completed for container %s", containerID)
// 		case <-timeoutCtx.Done():
// 			log.G(ctx).Warnf("force cleanup timed out for container %s", containerID)
// 		}
// 	}()

// 	// Wait for the cleanup goroutine to finish or timeout
// 	select {
// 	case err := <-errChan:
// 		if err != nil {
// 			log.G(ctx).Warnf("force cleanup encountered errors but continuing: %v", err)
// 		}
// 	case <-timeoutCtx.Done():
// 		log.G(ctx).Warnf("force cleanup timed out for container %s, but cleanup may continue in background", containerID)
// 	}

// 	// Always return nil - this function should never block the caller
// 	// even if systemd operations fail or timeout
// 	log.G(ctx).Debugf("force cleanup completed (non-blocking) for container %s", containerID)
// 	return nil
// }

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
func shouldSkipHealthCheckSystemd(hc *Healthcheck, cfg *config.Config) bool {
	// Don't proceed if systemd is unavailable or disabled
	if !defaults.IsSystemdAvailable() || cfg.DisableHCSystemd {
		return true
	}

	// Don't proceed if health check is nil, empty, explicitly NONE or interval is 0.
	if hc == nil || len(hc.Test) == 0 || hc.Test[0] == "NONE" || hc.Interval == 0 {
		return true
	}
	return false
}

func ForceRemoveTransientHealthCheckFiles(ctx context.Context, containerID string) error {
	timer := containerID + ".timer"
	service := containerID + ".service"

	// Use a short timeout to avoid hanging
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Connect to the right systemd instance
	var conn *dbus.Conn
	var err error
	if rootlessutil.IsRootless() {
		conn, err = dbus.NewUserConnectionContext(timeoutCtx)
	} else {
		conn, err = dbus.NewSystemConnectionContext(timeoutCtx)
	}
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	// Stop the timer and service units (best effort)
	stopAndWait := func(unit string) {
		ch := make(chan string, 1)
		_, err := conn.StopUnitContext(timeoutCtx, unit, "ignore-dependencies", ch)
		if err == nil {
			select {
			case <-ch:
			case <-timeoutCtx.Done():
			}
		}
	}
	stopAndWait(timer)
	stopAndWait(service)

	// Disable the timer unit (best effort)
	_, _ = conn.DisableUnitFilesContext(timeoutCtx, []string{timer}, false)

	// Reset failed state (best effort)
	_ = conn.ResetFailedUnitContext(timeoutCtx, service)

	// Remove unit files
	var unitDir string
	if rootlessutil.IsRootless() {
		unitDir = filepath.Join(os.Getenv("HOME"), ".config/systemd/user")
	} else {
		unitDir = "/etc/systemd/system"
	}
	timerPath := filepath.Join(unitDir, timer)
	servicePath := filepath.Join(unitDir, service)
	_ = os.Remove(timerPath)
	_ = os.Remove(servicePath)

	// Reload systemd to apply changes
	_ = conn.ReloadContext(timeoutCtx)

	return nil
}

func CreateAndStartTimer(ctx context.Context, container containerd.Container, cfg *config.Config) error {
	hc := extractHealthcheck(ctx, container)
	if hc == nil {
		return nil
	}
	if shouldSkipHealthCheckSystemd(hc, cfg) {
		return nil
	}

	containerID := container.ID()

	// Generate service and timer unit content
	serviceContent := generateServiceContent(containerID, ctx)
	timerContent := generateTimerContent(containerID, hc.Interval)

	// Determine the unit path
	var unitDir string
	var conn *dbus.Conn
	var err error

	if rootlessutil.IsRootless() {
		unitDir = filepath.Join(os.Getenv("HOME"), ".config/systemd/user")
		conn, err = dbus.NewUserConnectionContext(ctx)
	} else {
		unitDir = "/etc/systemd/system"
		conn, err = dbus.NewSystemConnectionContext(ctx)
	}
	if err != nil {
		return fmt.Errorf("systemd DBUS connect error: %w", err)
	}
	defer conn.Close()

	// Write unit files
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return err
	}
	servicePath := filepath.Join(unitDir, containerID+".service")
	timerPath := filepath.Join(unitDir, containerID+".timer")

	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, []byte(timerContent), 0644); err != nil {
		return err
	}

	// Reload systemd and enable/start timer
	if err := conn.ReloadContext(ctx); err != nil {
		return fmt.Errorf("systemd reload failed: %w", err)
	}
	_, _, err = conn.EnableUnitFilesContext(ctx, []string{containerID + ".timer"}, false, true)
	if err != nil {
		return fmt.Errorf("enable timer failed: %w", err)
	}
	_, err = conn.StartUnitContext(ctx, containerID+".timer", "replace", nil)
	if err != nil {
		return fmt.Errorf("start timer failed: %w", err)
	}

	log.G(ctx).Debugf("Created and started healthcheck timer unit: %s", containerID)
	return nil
}

// generateServiceContent creates the systemd service unit content for healthcheck
func generateServiceContent(containerID string, ctx context.Context) string {
	return fmt.Sprintf(`[Unit]
Description=Healthcheck for container %s

[Service]
Type=oneshot
Environment=PATH=%s
ExecStart=%s
`, containerID, os.Getenv("PATH"), buildHealthcheckCommand(containerID, ctx))
}

// generateTimerContent creates the systemd timer unit content for healthcheck
func generateTimerContent(containerID string, interval time.Duration) string {
	return fmt.Sprintf(`[Unit]
Description=Healthcheck timer for container %s

[Timer]
OnUnitInactiveSec=%ds
AccuracySec=1s

[Install]
WantedBy=timers.target
`, containerID, int(interval.Seconds()))
}

// Helper to build the healthcheck exec command
func buildHealthcheckCommand(containerID string, ctx context.Context) string {
	cmd := fmt.Sprintf("nerdctl container healthcheck %s", containerID)
	if log.G(ctx).Logger.IsLevelEnabled(log.DebugLevel) {
		cmd += " --debug"
	}
	return cmd
}
