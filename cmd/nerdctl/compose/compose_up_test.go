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
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/icmd"

	"github.com/containerd/nerdctl/v2/pkg/testutil"
)

// https://github.com/containerd/nerdctl/issues/1942
func TestComposeUpDetailedError(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("FIXME: test does not work on Windows yet (runtime \"io.containerd.runc.v2\" binary not installed \"containerd-shim-runc-v2.exe\": file does not exist)")
	}
	base := testutil.NewBase(t)
	dockerComposeYAML := fmt.Sprintf(`
services:
  foo:
    image: %s
    runtime: invalid
`, testutil.CommonImage)
	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()

	c := base.ComposeCmd("-f", comp.YAMLFullPath(), "up", "-d")
	expected := icmd.Expected{
		ExitCode: 1,
		Err:      `exec: \"invalid\": executable file not found in $PATH`,
	}
	if base.Target == testutil.Docker {
		expected.Err = `unknown or invalid runtime name: invalid`
	}
	c.Assert(expected)
}

// https://github.com/containerd/nerdctl/issues/1652
func TestComposeUpBindCreateHostPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip(`FIXME: no support for Windows path: (error: "volume target must be an absolute path, got \"/mnt\")`)
	}

	base := testutil.NewBase(t)

	var dockerComposeYAML = fmt.Sprintf(`
services:
  test:
    image: %s
    command: sh -euxc "echo hi >/mnt/test"
    volumes:
      # ./foo should be automatically created
      - ./foo:/mnt
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()

	base.ComposeCmd("-f", comp.YAMLFullPath(), "up").AssertOK()
	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down").AssertOK()
	testFile := filepath.Join(comp.Dir(), "foo", "test")
	testB, err := os.ReadFile(testFile)
	assert.NilError(t, err)
	assert.Equal(t, "hi\n", string(testB))
}

func TestComposeUpEnvFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}

	base := testutil.NewBase(t)

	tmpDir := t.TempDir()
	fmt.Printf("Created temporary directory: %s\n", tmpDir)

	envFilePath := filepath.Join(tmpDir, ".env")
	envFileContent := "TEST_VAR1=Hello\nTEST_VAR2=World"
	err := os.WriteFile(envFilePath, []byte(envFileContent), 0644)
	assert.NilError(t, err)
	fmt.Printf("Created .env file at: %s\n", envFilePath)
	fmt.Printf("Env file content:\n%s\n", envFileContent)

	dockerComposeYAML := fmt.Sprintf(`
version: '3.1'
services:
  show-env:
    image: %s
    command: ["sh", "-c", "env"]
`, testutil.CommonImage)

	comp := testutil.NewComposeDir(t, dockerComposeYAML)
	defer comp.CleanUp()
	fmt.Printf("Created docker-compose.yml at: %s\n", comp.YAMLFullPath())
	fmt.Printf("Docker Compose YAML content:\n%s\n", dockerComposeYAML)

	upCmd := base.ComposeCmd("--env-file", envFilePath, "-f", comp.YAMLFullPath(), "up", "-d")
	fmt.Println("Executing up command:", upCmd)
	upCmd.AssertOK()

	defer base.ComposeCmd("-f", comp.YAMLFullPath(), "down", "-v").AssertOK()

	containerID := strings.TrimSpace(base.ComposeCmd("-f", comp.YAMLFullPath(), "ps", "-q").Run().Stdout())
	fmt.Printf("Container ID: %s\n", containerID)
	if containerID == "" {
		t.Fatalf("Failed to get container ID")
	}

	logsCmd := base.ComposeCmd("-f", comp.YAMLFullPath(), "logs", containerID)
	logs := logsCmd.Run().Stdout()
	fmt.Printf("Container logs:\n%s\n", logs)

	// Check for environment variables in the logs
	assert.Assert(t, strings.Contains(logs, "TEST_VAR1=Hello"), "TEST_VAR1 not found in logs")
	assert.Assert(t, strings.Contains(logs, "TEST_VAR2=World"), "TEST_VAR2 not found in logs")

	fmt.Println("Test completed successfully")
}
