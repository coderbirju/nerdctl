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
	"github.com/containerd/nerdctl/mod/tigron/expect"
	"github.com/containerd/nerdctl/mod/tigron/require"
	"github.com/containerd/nerdctl/mod/tigron/test"
	"github.com/containerd/nerdctl/v2/pkg/testutil"
	"github.com/containerd/nerdctl/v2/pkg/testutil/nerdtest"
	"gotest.tools/v3/assert"
	"testing"
	"time"
)

func TestHealthCheck_SystemdIntegration_Basic(t *testing.T) {
	testCase := nerdtest.Setup()
	testCase.Require = require.Not(nerdtest.Docker)

	testCase.SubTests = []*test.Case{
		{
			Description: "Basic healthy container with systemd-triggered healthcheck",
			Setup: func(data test.Data, helpers test.Helpers) {
				helpers.Ensure("run", "-d", "--name", data.Identifier(),
					"--health-cmd", "echo healthy",
					"--health-interval", "2s",
					testutil.CommonImage, "sleep", "30")
				// Wait for a couple of healthchecks to execute
				time.Sleep(10 * time.Second)
			},
			Cleanup: func(data test.Data, helpers test.Helpers) {
				helpers.Anyhow("rm", "-f", data.Identifier())
			},
			Command: func(data test.Data, helpers test.Helpers) test.TestableCommand {
				return helpers.Command("inspect", data.Identifier())
			},
			Expected: func(data test.Data, helpers test.Helpers) *test.Expected {
				return &test.Expected{
					ExitCode: 0,
					Output: expect.All(func(stdout, _ string, t *testing.T) {
						inspect := nerdtest.InspectContainer(helpers, data.Identifier())
						h := inspect.State.Health
						assert.Assert(t, h != nil, "expected health state to be present")
						assert.Equal(t, h.Status, "healthy")
						assert.Assert(t, len(h.Log) > 0, "expected at least one health check log entry")
					}),
				}
			},
		},
	}

	testCase.Run(t)
}
