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

	containerd "github.com/containerd/containerd/v2/client"
)

// CreateHealthCheckTimers sets up the transient systemd timer and service for healthchecks.
func CreateHealthCheckTimers(ctx context.Context, container containerd.Container) error {
	return nil
}

// StartHealthCheckTimers starts the healthcheck timer unit.
func StartHealthCheckTimers(ctx context.Context, container containerd.Container) error {
	return nil
}

// RemoveTransientHealthCheckFiles stops and cleans up the transient timer and service.
func RemoveTransientHealthCheckFiles(ctx context.Context, container containerd.Container) error {
	return nil
}

// RemoveTransientHealthCheckFilesByID stops and cleans up the transient timer and service using just the container ID.
func RemoveTransientHealthCheckFilesByID(ctx context.Context, containerID string) error {
	return nil
}

// RemoveAllHealthCheckTimers removes both health-interval and start-period timers for a container.
func RemoveAllHealthCheckTimers(ctx context.Context, containerID string) error {
	return nil
}

// RemoveHealthIntervalTimer removes the health-interval timer for a container.
func RemoveHealthIntervalTimer(ctx context.Context, containerID string) error {
	return nil
}

// RemoveStartPeriodTimer removes the start-period timer for a container.
func RemoveStartPeriodTimer(ctx context.Context, containerID string) error {
	return nil
}
