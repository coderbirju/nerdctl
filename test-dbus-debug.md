# DBUS Debugging Test Plan

## What We Added

### 1. GitHub Workflow Debugging (.github/workflows/job-test-in-container.yml)
- Added a new step "Debug: DBUS availability in container" before running integration tests
- Checks DBUS tool availability, binary locations, systemd tools, environment variables, and package info
- Runs in the same container that will execute the tests

### 2. Rootless Test Script Debugging (Dockerfile.d/test-integration-rootless.sh)
- Added debugging as ROOT user before switching to rootless
- Added debugging as ROOTLESS user after SSH switch
- Added debugging AFTER containerd-rootless-setuptool.sh setup
- Checks DBUS tools, environment variables, systemd status, and DBUS connectivity

## Expected Output

When the CI runs, we should see:

### Container Level (from GitHub workflow):
```
=== DBUS Debugging in Container ===
--- DBUS Tools Availability ---
/usr/bin/dbus-launch
/usr/bin/dbus-daemon
/usr/bin/dbus-send
/usr/bin/dbus-monitor
/usr/bin/systemd-run
--- DBUS Binaries Location ---
-rwxr-xr-x 1 root root ... /usr/bin/dbus-launch
-rwxr-xr-x 1 root root ... /usr/bin/dbus-daemon
...
--- Package Info ---
dbus 1.14.10-4ubuntu4.1
systemd 255.4-1ubuntu8.4
...
```

### Root User Level:
```
=== DBUS Debugging as ROOT ===
--- DBUS Tools Availability (root) ---
/usr/bin/dbus-launch
/usr/bin/systemd-run
...
--- Environment (root) ---
PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
USER: root
UID: 0
DBUS_SESSION_BUS_ADDRESS: unset
XDG_RUNTIME_DIR: unset
```

### Rootless User Level:
```
=== DBUS Debugging as ROOTLESS USER ===
--- DBUS Tools Availability (rootless) ---
/usr/bin/dbus-launch
/usr/bin/systemd-run
...
--- Environment (rootless) ---
PATH: /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
USER: rootless
UID: 1000
DBUS_SESSION_BUS_ADDRESS: unset
XDG_RUNTIME_DIR: /run/user/1000
```

## What This Will Tell Us

1. **Are DBUS tools installed?** - We'll see if dbus-launch, dbus-daemon, etc. are available
2. **Are they in PATH?** - We'll see the exact paths and verify accessibility
3. **Environment differences** - Compare root vs rootless environment setup
4. **Systemd user session status** - See if systemd --user works properly
5. **DBUS connectivity** - Test if dbus-launch can establish connections
6. **Setup impact** - See how containerd-rootless-setuptool.sh affects DBUS environment

## Next Steps After Getting Debug Output

Based on the debug output, we can:
1. **If DBUS tools are missing**: Add them to the Dockerfile
2. **If DBUS tools exist but fail**: Implement graceful fallback in healthcheck code
3. **If environment is wrong**: Fix environment setup in test script
4. **If systemd user session fails**: Implement proper session initialization

This debugging will give us the exact information needed to solve the healthcheck timer failures in CI.
