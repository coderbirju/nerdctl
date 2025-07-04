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

package compose

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/containerd/log"
	"github.com/containerd/nerdctl/mod/tigron/expect"
	"github.com/containerd/nerdctl/mod/tigron/test"

	"github.com/containerd/nerdctl/v2/cmd/nerdctl/helpers"
	"github.com/containerd/nerdctl/v2/pkg/testutil"
	"github.com/containerd/nerdctl/v2/pkg/testutil/nerdtest"
	"github.com/containerd/nerdctl/v2/pkg/testutil/nettestutil"
	"github.com/containerd/nerdctl/v2/pkg/testutil/testregistry"
)

func TestComposeRun(t *testing.T) {
	const expectedOutput = "speed 38400 baud"

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - stty
`, testutil.CommonImage)

	testCase := nerdtest.Setup()

	testCase.SubTests = []*test.Case{
		{
			Description: "pty run",
			Setup: func(data test.Data, helpers test.Helpers) {
				data.Temp().Save(dockerComposeYAML, "compose.yaml")
			},
			Command: func(data test.Data, helpers test.Helpers) test.TestableCommand {
				cmd := helpers.Command(
					"compose",
					"-f",
					data.Temp().Path("compose.yaml"),
					"run",
					"--name",
					data.Identifier(),
					"alpine",
				)
				cmd.WithPseudoTTY()
				return cmd
			},
			Expected: test.Expects(0, nil, expect.Contains(expectedOutput)),
			Cleanup: func(data test.Data, helpers test.Helpers) {
				helpers.Anyhow("rm", "-f", "-v", data.Identifier())
				helpers.Anyhow("compose", "-f", data.Temp().Path("compose.yaml"), "down", "-v")
			},
		},
		{
			Description: "pty run with --rm",
			Setup: func(data test.Data, helpers test.Helpers) {
				data.Temp().Save(dockerComposeYAML, "compose.yaml")
			},
			Command: func(data test.Data, helpers test.Helpers) test.TestableCommand {
				cmd := helpers.Command(
					"compose",
					"-f",
					data.Temp().Path("compose.yaml"),
					"run",
					"--name",
					data.Identifier(),
					"--rm",
					"alpine",
				)
				cmd.WithPseudoTTY()
				return cmd
			},
			Expected: func(data test.Data, helpers test.Helpers) *test.Expected {
				// Ensure the container has been removed
				capt := helpers.Capture("ps", "-a", "--format=\"{{.Names}}\"")
				assert.Assert(t, !strings.Contains(capt, data.Identifier()), capt)

				return &test.Expected{
					Output: expect.Contains(expectedOutput),
				}
			},
			Cleanup: func(data test.Data, helpers test.Helpers) {
				helpers.Anyhow("rm", "-f", "-v", data.Identifier())
				helpers.Anyhow("compose", "-f", data.Temp().Path("compose.yaml"), "down", "-v")
			},
		},
	}

	testCase.Run(t)
}

func TestComposeRunWithServicePorts(t *testing.T) {
	base := testutil.NewBase(t)
	// specify the name of container in order to remove
	// TODO: when `compose rm` is implemented, replace it.
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  web:
    image: %s
    ports:
      - 8080:80
`, testutil.NginxAlpineImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	go func() {
		// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
		// unbuffer(1) can be installed with `apt-get install expect`.
		unbuffer := []string{"unbuffer"}
		base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
			"run", "--service-ports", "--name", containerName, "web").Run()
	}()

	checkNginx := func() error {
		resp, err := nettestutil.HTTPGet("http://127.0.0.1:8080", 5, false)
		if err != nil {
			return err
		}
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if !strings.Contains(string(respBody), testutil.NginxAlpineIndexHTMLSnippet) {
			t.Logf("respBody=%q", respBody)
			return fmt.Errorf("respBody does not contain %q", testutil.NginxAlpineIndexHTMLSnippet)
		}
		return nil
	}
	var nginxWorking bool
	for i := 0; i < 30; i++ {
		t.Logf("(retry %d)", i)
		err := checkNginx()
		if err == nil {
			nginxWorking = true
			break
		}
		t.Log(err)
		time.Sleep(3 * time.Second)
	}
	if !nginxWorking {
		t.Fatal("nginx is not working")
	}
	t.Log("nginx seems functional")
}

func TestComposeRunWithPublish(t *testing.T) {
	base := testutil.NewBase(t)
	// specify the name of container in order to remove
	// TODO: when `compose rm` is implemented, replace it.
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  web:
    image: %s
`, testutil.NginxAlpineImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	go func() {
		// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
		// unbuffer(1) can be installed with `apt-get install expect`.
		unbuffer := []string{"unbuffer"}
		base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
			"run", "--publish", "8080:80", "--name", containerName, "web").Run()
	}()

	checkNginx := func() error {
		resp, err := nettestutil.HTTPGet("http://127.0.0.1:8080", 5, false)
		if err != nil {
			return err
		}
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if !strings.Contains(string(respBody), testutil.NginxAlpineIndexHTMLSnippet) {
			t.Logf("respBody=%q", respBody)
			return fmt.Errorf("respBody does not contain %q", testutil.NginxAlpineIndexHTMLSnippet)
		}
		return nil
	}
	var nginxWorking bool
	for i := 0; i < 30; i++ {
		t.Logf("(retry %d)", i)
		err := checkNginx()
		if err == nil {
			nginxWorking = true
			break
		}
		t.Log(err)
		time.Sleep(3 * time.Second)
	}
	if !nginxWorking {
		t.Fatal("nginx is not working")
	}
	t.Log("nginx seems functional")
}

func TestComposeRunWithEnv(t *testing.T) {
	base := testutil.NewBase(t)
	// specify the name of container in order to remove
	// TODO: when `compose rm` is implemented, replace it.
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - sh
      - -c
      - "echo $$FOO"
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	const partialOutput = "bar"
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "-e", "FOO=bar", "--name", containerName, "alpine").AssertOutContains(partialOutput)
}

func TestComposeRunWithUser(t *testing.T) {
	base := testutil.NewBase(t)
	// specify the name of container in order to remove
	// TODO: when `compose rm` is implemented, replace it.
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - id
      - -u
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	const partialOutput = "5000"
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "--user", "5000", "--name", containerName, "alpine").AssertOutContains(partialOutput)
}

func TestComposeRunWithLabel(t *testing.T) {
	base := testutil.NewBase(t)
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - echo
      - "dummy log"
    labels:
      - "foo=bar"
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "--label", "foo=rab", "--label", "x=y", "--name", containerName, "alpine").AssertOK()

	container := base.InspectContainer(containerName)
	if container.Config == nil {
		log.L.Errorf("test failed, cannot fetch container config")
		t.Fail()
	}
	assert.Equal(t, container.Config.Labels["foo"], "rab")
	assert.Equal(t, container.Config.Labels["x"], "y")
}

func TestComposeRunWithArgs(t *testing.T) {
	base := testutil.NewBase(t)
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - echo
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	const partialOutput = "hello world"
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "--name", containerName, "alpine", partialOutput).AssertOutContains(partialOutput)
}

func TestComposeRunWithEntrypoint(t *testing.T) {
	base := testutil.NewBase(t)
	// specify the name of container in order to remove
	// TODO: when `compose rm` is implemented, replace it.
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
      - stty # should be changed
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	const partialOutput = "hello world"
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "--entrypoint", "echo", "--name", containerName, "alpine", partialOutput).AssertOutContains(partialOutput)
}

func TestComposeRunWithVolume(t *testing.T) {
	base := testutil.NewBase(t)
	containerName := testutil.Identifier(t)

	dockerComposeYAML := fmt.Sprintf(`
services:
  alpine:
    image: %s
    entrypoint:
    - stty # no meaning, just put any command
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	// The directory is automatically removed by Cleanup
	tmpDir := t.TempDir()
	destinationDir := "/data"
	volumeFlagStr := fmt.Sprintf("%s:%s", tmpDir, destinationDir)

	defer base.Cmd("rm", "-f", "-v", containerName).Run()
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(),
		"run", "--volume", volumeFlagStr, "--name", containerName, "alpine").AssertOK()

	container := base.InspectContainer(containerName)
	errMsg := fmt.Sprintf("test failed, cannot find volume: %v", container.Mounts)
	assert.Assert(t, container.Mounts != nil, errMsg)
	assert.Assert(t, len(container.Mounts) == 1, errMsg)
	assert.Assert(t, container.Mounts[0].Source == tmpDir, errMsg)
	assert.Assert(t, container.Mounts[0].Destination == destinationDir, errMsg)
}

func TestComposePushAndPullWithCosignVerify(t *testing.T) {
	testutil.RequireExecutable(t, "cosign")
	testutil.DockerIncompatible(t)
	testutil.RequiresBuild(t)
	testutil.RegisterBuildCacheCleanup(t)
	t.Parallel()

	base := testutil.NewBase(t)
	base.Env = append(base.Env, "COSIGN_PASSWORD=1")

	keyPair := helpers.NewCosignKeyPair(t, "cosign-key-pair", "1")
	reg := testregistry.NewWithNoAuth(base, 0, false)
	t.Cleanup(func() {
		keyPair.Cleanup()
		reg.Cleanup(nil)
	})

	tID := testutil.Identifier(t)
	testImageRefPrefix := fmt.Sprintf("127.0.0.1:%d/%s/", reg.Port, tID)

	var (
		imageSvc0 = testImageRefPrefix + "composebuild_svc0"
		imageSvc1 = testImageRefPrefix + "composebuild_svc1"
		imageSvc2 = testImageRefPrefix + "composebuild_svc2"
	)

	dockerComposeYAML := fmt.Sprintf(`
services:
  svc0:
    build: .
    image: %s
    x-nerdctl-verify: cosign
    x-nerdctl-cosign-public-key: %s
    x-nerdctl-sign: cosign
    x-nerdctl-cosign-private-key: %s
    entrypoint:
      - stty
  svc1:
    build: .
    image: %s
    x-nerdctl-verify: cosign
    x-nerdctl-cosign-public-key: dummy_pub_key
    x-nerdctl-sign: cosign
    x-nerdctl-cosign-private-key: %s
    entrypoint:
      - stty
  svc2:
    build: .
    image: %s
    x-nerdctl-verify: none
    x-nerdctl-sign: none
    entrypoint:
      - stty
`, imageSvc0, keyPair.PublicKey, keyPair.PrivateKey,
		imageSvc1, keyPair.PrivateKey, imageSvc2)

	dockerfile := fmt.Sprintf(`FROM %s`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	comp.WriteFile("Dockerfile", dockerfile)

	projectName := comp.ProjectName()
	t.Logf("projectName=%q", projectName)
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").Run()

	// 1. build both services/images
	base.ComposeCmd("-f", comp.YAMLFullPath(), "build").AssertOK()
	// 2. compose push with cosign for svc0/svc1, (and none for svc2)
	base.ComposeCmd("-f", comp.YAMLFullPath(), "push").AssertOK()
	// 3. compose pull with cosign
	base.ComposeCmd("-f", comp.YAMLFullPath(), "pull", "svc0").AssertOK()   // key match
	base.ComposeCmd("-f", comp.YAMLFullPath(), "pull", "svc1").AssertFail() // key mismatch
	base.ComposeCmd("-f", comp.YAMLFullPath(), "pull", "svc2").AssertOK()   // verify passed
	// 4. compose run
	const sttyPartialOutput = "speed 38400 baud"
	// unbuffer(1) emulates tty, which is required by `nerdctl run -t`.
	// unbuffer(1) can be installed with `apt-get install expect`.
	unbuffer := []string{"unbuffer"}
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(), "run", "svc0").AssertOutContains(sttyPartialOutput) // key match
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(), "run", "svc1").AssertFail()                         // key mismatch
	base.ComposeCmdWithHelper(unbuffer, "-f", comp.YAMLFullPath(), "run", "svc2").AssertOutContains(sttyPartialOutput) // verify passed
	// 5. compose up
	base.ComposeCmd("-f", comp.YAMLFullPath(), "up", "svc0").AssertOK()   // key match
	base.ComposeCmd("-f", comp.YAMLFullPath(), "up", "svc1").AssertFail() // key mismatch
	base.ComposeCmd("-f", comp.YAMLFullPath(), "up", "svc2").AssertOK()   // verify passed
}
