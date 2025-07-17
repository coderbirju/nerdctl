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

package container

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	containerd "github.com/containerd/containerd/v2/client"

	"github.com/containerd/nerdctl/v2/cmd/nerdctl/completion"
	"github.com/containerd/nerdctl/v2/cmd/nerdctl/helpers"
	"github.com/containerd/nerdctl/v2/pkg/clientutil"
	"github.com/containerd/nerdctl/v2/pkg/cmd/container"
	"github.com/containerd/nerdctl/v2/pkg/idutil/containerwalker"
)

// HealthCheckCommand returns a cobra command for `nerdctl container healthcheck`
func HealthCheckCommand() *cobra.Command {
	var healthCheckCommand = &cobra.Command{
		Use:               "healthcheck [flags] CONTAINER",
		Short:             "Execute the health check command in a container",
		Args:              cobra.ExactArgs(1),
		RunE:              healthCheckAction,
		ValidArgsFunction: healthCheckShellComplete,
		SilenceUsage:      true,
		SilenceErrors:     true,
	}

	// Internal flags for workflow differentiation
	healthCheckCommand.Flags().Bool("health", false, "Execute health interval workflow (internal use)")
	healthCheckCommand.Flags().Bool("start-period", false, "Execute start period workflow (internal use)")

	// Mark these flags as hidden since they're for internal use
	healthCheckCommand.Flags().MarkHidden("health")
	healthCheckCommand.Flags().MarkHidden("start-period")

	return healthCheckCommand
}

func healthCheckAction(cmd *cobra.Command, args []string) error {
	globalOptions, err := helpers.ProcessRootCmdFlags(cmd)
	if err != nil {
		return err
	}

	client, ctx, cancel, err := clientutil.NewClient(cmd.Context(), globalOptions.Namespace, globalOptions.Address)
	if err != nil {
		return err
	}
	defer cancel()

	// Parse workflow flags
	healthFlag, err := cmd.Flags().GetBool("health")
	if err != nil {
		return err
	}
	startPeriodFlag, err := cmd.Flags().GetBool("start-period")
	if err != nil {
		return err
	}

	// Ensure flags are mutually exclusive
	if healthFlag && startPeriodFlag {
		return fmt.Errorf("--health and --start-period flags are mutually exclusive")
	}

	containerID := args[0]
	walker := &containerwalker.ContainerWalker{
		Client: client,
		OnFound: func(ctx context.Context, found containerwalker.Found) error {
			if found.MatchCount > 1 {
				return fmt.Errorf("multiple IDs found with provided prefix: %s", found.Req)
			}

			// Route to appropriate workflow based on flags
			if healthFlag {
				return container.HealthCheck(ctx, client, found.Container, container.WorkflowHealthInterval)
			} else if startPeriodFlag {
				return container.HealthCheck(ctx, client, found.Container, container.WorkflowStartPeriod)
			} else {
				// No workflow specified - this should not happen in normal operation
				return fmt.Errorf("workflow type must be specified (--health or --start-period)")
			}
		},
	}

	n, err := walker.Walk(ctx, containerID)
	if err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("no such container %s", containerID)
	}
	return nil
}

func healthCheckShellComplete(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return completion.ContainerNames(cmd, func(status containerd.ProcessStatus) bool {
		return status == containerd.Running
	})
}
