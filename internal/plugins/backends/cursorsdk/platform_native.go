package cursorsdk

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	minNodeMajor = 22
	minNodeMinor = 13
)

// NativeBridgeProbeResult reports whether the native Node bridge lane can run.
type NativeBridgeProbeResult struct {
	Status string `json:"status"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	Reason string `json:"reason,omitempty"`
	Node   string `json:"node,omitempty"`
	Bridge string `json:"bridge,omitempty"`
	SDKPin string `json:"sdkPin,omitempty"`
}

// ProbeNativeBridgeOpts configures native lane probing (injectable for tests).
type ProbeNativeBridgeOpts struct {
	LookPath    func(name string) (string, error)
	NodeVersion func(nodeExe string) (string, error)
	RunNode     func(ctx context.Context, nodeExe, bridgeBin string) (string, error)
	BridgeBin   string
	BridgeRoot  string
}

// ProbeNativeBridgeLane checks Node >=22.13 and bridge --version on the current OS.
func ProbeNativeBridgeLane(opts ProbeNativeBridgeOpts) NativeBridgeProbeResult {
	out := NativeBridgeProbeResult{
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
		Status: "blocked",
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	nodeExe, err := lookPath("node")
	if err != nil {
		out.Reason = "node runtime not found on PATH"
		return out
	}
	readVersion := opts.NodeVersion
	if readVersion == nil {
		readVersion = nodeVersion
	}
	ver, err := readVersion(nodeExe)
	if err != nil {
		out.Reason = sanitizeProbeErr(err)
		return out
	}
	if !nodeVersionOK(ver) {
		out.Reason = fmt.Sprintf("node %s below required >=22.13", ver)
		return out
	}
	out.Node = ver

	runNode := opts.RunNode
	bridgeBin := opts.BridgeBin
	if bridgeBin == "" {
		bridgeBin = defaultBridgeBinPath(opts.BridgeRoot)
	}
	if runNode == nil {
		runNode = func(ctx context.Context, node, bin string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, bin, "--version")
			raw, err := cmd.CombinedOutput()
			if err != nil {
				return "", fmt.Errorf("bridge --version failed: %s", strings.TrimSpace(string(raw)))
			}
			return strings.TrimSpace(string(raw)), nil
		}
	}
	versionLine, err := runNode(context.Background(), nodeExe, bridgeBin)
	if err != nil {
		out.Reason = sanitizeProbeErr(err)
		return out
	}
	if !strings.Contains(versionLine, "1.0.23") {
		out.Reason = "bridge package not pinned to @cursor/sdk 1.0.23"
		return out
	}
	out.Bridge = "lip-cursor-sdk-bridge"
	out.SDKPin = "1.0.23"
	out.Status = "ready"
	return out
}

func defaultBridgeBinPath(root string) string {
	if root == "" {
		_, file, _, ok := runtime.Caller(0)
		if ok {
			root = filepath.Dir(file)
		}
	}
	return filepath.Join(root, "bridge", "bin", "lip-cursor-sdk-bridge.js")
}

func nodeVersion(nodeExe string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodeExe, "-p", "process.version")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("node -p process.version failed")
	}
	return strings.TrimSpace(string(raw)), nil
}

var nodeVerRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)`)

func nodeVersionOK(version string) bool {
	m := nodeVerRe.FindStringSubmatch(version)
	if len(m) != 4 {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > minNodeMajor {
		return true
	}
	if major < minNodeMajor {
		return false
	}
	return minor >= minNodeMinor
}

func sanitizeProbeErr(err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, `\`, "[path]")
	re := regexp.MustCompile(`[A-Za-z]:\\[^\s]+`)
	msg = re.ReplaceAllString(msg, "[path]")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
