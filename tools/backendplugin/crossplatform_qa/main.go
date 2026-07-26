package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const matrixSchema = "golip.crossplatform.matrix/v1"

type releaseMeta struct {
	Schema              string   `yaml:"schema"`
	PluginID            string   `yaml:"plugin_id"`
	FactoryKind         string   `yaml:"factory_kind"`
	Module              string   `yaml:"module"`
	Command             string   `yaml:"command"`
	ManifestTmpl        string   `yaml:"manifest_template"`
	Version             string   `yaml:"version"`
	BuildID             string   `yaml:"build_id"`
	Tag                 string   `yaml:"tag"`
	Profiles            []string `yaml:"profiles"`
	PublishedRootModule string   `yaml:"published_root_module"`
	ReplacePolicy       string   `yaml:"replace_policy"`
	PrivateCompanions   []string `yaml:"private_companions"`
}

type discoveredRelease struct {
	DirName string
	Root    string
	Meta    releaseMeta
}

type platformClaim struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type hostProfileJSON struct {
	Platform             string `json:"platform"`
	Verification         string `json:"verification"`
	LaunchBinding        string `json:"launch_binding"`
	LocalChannel         string `json:"local_channel"`
	RuntimeChannelOK     bool   `json:"runtime_channel_ok"`
	RuntimeChannelReason string `json:"runtime_channel_reason,omitempty"`
}

type unsupportedPair struct {
	Connector string `json:"connector"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Reason    string `json:"reason"`
}

type compileResult struct {
	Connector string `json:"connector"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

type matrixReport struct {
	Schema              string            `json:"schema"`
	HostProfiles        []hostProfileJSON `json:"host_profiles"`
	Modules             []string          `json:"modules"`
	Connectors          []string          `json:"connectors"`
	Unsupported         []unsupportedPair `json:"unsupported"`
	ClaimedCompile      []compileResult   `json:"claimed_compile"`
	RootIndependent     bool              `json:"root_independent"`
	PackageMatrixMatch  bool              `json:"package_matrix_match"`
	NativeHost          string            `json:"native_host"`
	NativeTestsRan      bool              `json:"native_tests_ran"`
	FalseClaimsRejected []string          `json:"false_claims_rejected,omitempty"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	outPath := flag.String("out", "", "write matrix JSON (required)")
	selectCSV := flag.String("select", "", "optional comma-separated connector dirs")
	skipNative := flag.Bool("skip-native", false, "skip native lifecycle test subprocesses")
	flag.Parse()
	if strings.TrimSpace(*outPath) == "" {
		fmt.Fprintln(os.Stderr, "crossplatform_qa: -out is required")
		os.Exit(2)
	}
	var selectSet map[string]struct{}
	if s := strings.TrimSpace(*selectCSV); s != "" {
		selectSet = map[string]struct{}{}
		for p := range strings.SplitSeq(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				selectSet[p] = struct{}{}
			}
		}
	}
	report, err := runQA(*root, selectSet, !*skipNative)
	if err != nil {
		if report != nil {
			_ = writeReport(*outPath, report)
		}
		fmt.Fprintf(os.Stderr, "crossplatform_qa: %v\n", err)
		os.Exit(1)
	}
	if err := writeReport(*outPath, report); err != nil {
		fmt.Fprintf(os.Stderr, "crossplatform_qa: write report: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

func writeReport(path string, report *matrixReport) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(abs, b, 0o644)
}

func runQA(root string, selectSet map[string]struct{}, runNative bool) (*matrixReport, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	mods, err := discoverModules(absRoot)
	if err != nil {
		return nil, err
	}
	rels, err := discoverReleases(absRoot)
	if err != nil {
		return nil, err
	}
	selected := make([]discoveredRelease, 0, len(rels))
	for _, r := range rels {
		if selectSet != nil {
			if _, ok := selectSet[r.DirName]; !ok {
				continue
			}
		}
		selected = append(selected, r)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no connectors discovered (release.yaml)")
	}

	hostProfiles := hostSecureInventory()
	report := &matrixReport{
		Schema:       matrixSchema,
		HostProfiles: hostProfiles,
		Modules:      mods,
		NativeHost:   runtime.GOOS + "/" + runtime.GOARCH,
	}
	connNames := make([]string, 0, len(selected))
	for _, r := range selected {
		connNames = append(connNames, r.DirName)
	}
	sort.Strings(connNames)
	report.Connectors = connNames

	var falseClaims []string
	var compileResults []compileResult
	var unsupported []unsupportedPair

	for _, r := range selected {
		claims, err := readManifestPlatforms(filepath.Join(r.Root, r.Meta.ManifestTmpl))
		if err != nil {
			return report, fmt.Errorf("%s: %w", r.DirName, err)
		}
		for _, osName := range []string{"linux", "darwin", "windows"} {
			for _, arch := range []string{"amd64", "arm64"} {
				if !runtimeChannelOK(osName) {
					unsupported = append(unsupported, unsupportedPair{
						Connector: r.DirName,
						OS:        osName,
						Arch:      arch,
						Reason:    "host_channel_fail_closed:unsupported_channel",
					})
				}
			}
		}
		for _, c := range claims {
			if !runtimeChannelOK(c.OS) {
				msg := fmt.Sprintf("%s claims %s/%s but host channel unsupported", r.DirName, c.OS, c.Arch)
				falseClaims = append(falseClaims, msg)
				continue
			}
			cr := crossCompile(r, c.OS, c.Arch)
			compileResults = append(compileResults, cr)
			if !cr.OK {
				return report, fmt.Errorf("compile %s %s/%s: %s", r.DirName, c.OS, c.Arch, cr.Error)
			}
		}
	}
	report.Unsupported = unsupported
	report.ClaimedCompile = compileResults
	report.FalseClaimsRejected = falseClaims
	if len(falseClaims) > 0 {
		return report, fmt.Errorf("false manifest platform claims: %s", strings.Join(falseClaims, "; "))
	}

	rootOK, err := rootIndependent(absRoot)
	if err != nil {
		return report, err
	}
	report.RootIndependent = rootOK
	if !rootOK {
		return report, fmt.Errorf("root go.mod must not require connectors/ or connector-support/")
	}

	pkgOK, err := packageMatrixMatches(absRoot, selected, selectSet)
	if err != nil {
		return report, err
	}
	report.PackageMatrixMatch = pkgOK
	if !pkgOK {
		return report, fmt.Errorf("package matrix does not match discovered full-profile releases")
	}

	if runNative {
		if err := runNativeGates(absRoot); err != nil {
			return report, err
		}
		report.NativeTestsRan = true
	}
	return report, nil
}

func hostSecureInventory() []hostProfileJSON {
	// Keep inventory aligned with processhost.HostSecureProfiles without importing
	// internal packages from this tool module path (tools run via go run on root).
	type row struct {
		platform, ver, launch, channel string
		ok                             bool
		reason                         string
	}
	rows := []row{
		{"linux", "design_source_evidenced", "sealed_or_immutable_descriptor_execveat_empty_path", "private_af_unix_so_peercred_expected_generation", true, ""},
		{"darwin", "compile_unverified", "protected_private_digest_staging_path_launch", "fail_closed_unsupported_channel_until_peercred_profile", false, "host_channel_fail_closed:unsupported_channel"},
		{"windows", "design_source_evidenced", "protected_private_digest_staging_path_launch", "named_pipe_dacl_token_pid_job_expected_generation", true, ""},
	}
	out := make([]hostProfileJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, hostProfileJSON{
			Platform:             r.platform,
			Verification:         r.ver,
			LaunchBinding:        r.launch,
			LocalChannel:         r.channel,
			RuntimeChannelOK:     r.ok,
			RuntimeChannelReason: r.reason,
		})
	}
	return out
}

func runtimeChannelOK(goos string) bool {
	switch goos {
	case "linux", "windows":
		return true
	default:
		return false
	}
}

func discoverModules(root string) ([]string, error) {
	var out []string
	for _, base := range []string{"connectors", "connector-support"} {
		dir := filepath.Join(root, base)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "go.mod")); err != nil {
				continue
			}
			out = append(out, filepath.ToSlash(filepath.Join(base, e.Name())))
		}
	}
	sort.Strings(out)
	return out, nil
}

func discoverReleases(root string) ([]discoveredRelease, error) {
	base := filepath.Join(root, "connectors")
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []discoveredRelease
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := e.Name()
		connRoot := filepath.Join(base, dirName)
		raw, err := os.ReadFile(filepath.Join(connRoot, "release.yaml"))
		if err != nil {
			continue
		}
		meta, err := parseRelease(raw)
		if err != nil {
			return nil, fmt.Errorf("%s/release.yaml: %w", dirName, err)
		}
		out = append(out, discoveredRelease{DirName: dirName, Root: connRoot, Meta: meta})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName < out[j].DirName })
	return out, nil
}

func parseRelease(raw []byte) (releaseMeta, error) {
	var meta releaseMeta
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return releaseMeta{}, err
	}
	if meta.Schema != "golip.connector.release/v1" {
		return releaseMeta{}, fmt.Errorf("unsupported schema %q", meta.Schema)
	}
	return meta, nil
}

func readManifestPlatforms(path string) ([]platformClaim, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var man struct {
		Platforms []platformClaim `json:"platforms"`
	}
	if err := json.Unmarshal(b, &man); err != nil {
		return nil, err
	}
	if len(man.Platforms) == 0 {
		return nil, fmt.Errorf("manifest %s has empty platforms", path)
	}
	return man.Platforms, nil
}

func crossCompile(r discoveredRelease, goos, goarch string) compileResult {
	tmp, err := os.MkdirTemp("", "golip-xplat-*")
	if err != nil {
		return compileResult{Connector: r.DirName, OS: goos, Arch: goarch, Error: err.Error()}
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	outBin := filepath.Join(tmp, "plugin")
	if goos == "windows" {
		outBin += ".exe"
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-buildid=", "-o", outBin, r.Meta.Command)
	cmd.Dir = r.Root
	cmd.Env = append(
		os.Environ(),
		"GOWORK=off",
		"CGO_ENABLED=0",
		"GOOS="+goos,
		"GOARCH="+goarch,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return compileResult{
			Connector: r.DirName,
			OS:        goos,
			Arch:      goarch,
			Error:     strings.TrimSpace(string(out) + " " + err.Error()),
		}
	}
	return compileResult{Connector: r.DirName, OS: goos, Arch: goarch, OK: true}
}

func rootIndependent(root string) (bool, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return false, err
	}
	body := string(b)
	for _, needle := range []string{
		"/connectors/",
		"/connector-support/",
		"\nconnectors/",
		"\nconnector-support/",
	} {
		if strings.Contains(body, needle) {
			return false, nil
		}
	}
	// Also reject require lines that end with connectors/<name>
	for line := range strings.SplitSeq(body, "\n") {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "github.com/matdev83/go-llm-interactive-proxy/connectors/") &&
			!strings.HasPrefix(trim, "github.com/matdev83/go-llm-interactive-proxy/connector-support/") {
			continue
		}
		return false, nil
	}
	return true, nil
}

func packageMatrixMatches(root string, selected []discoveredRelease, selectSet map[string]struct{}) (bool, error) {
	want := map[string]struct{}{}
	for _, r := range selected {
		if hasProfile(r.Meta.Profiles, "full") {
			want[r.DirName] = struct{}{}
		}
	}
	if len(want) == 0 {
		return true, nil
	}
	dest := filepath.Join(root, ".golip-crossplatform-package-check")
	_ = os.RemoveAll(dest)
	defer func() { _ = os.RemoveAll(dest) }()
	args := []string{
		"run", "./tools/backendplugin/package_plugins",
		"-root", root, "-profile", "full", "-dest", dest,
	}
	if selectSet != nil {
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		args = append(args, "-select", strings.Join(names, ","))
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("package_plugins: %w\n%s", err, out)
	}
	idxRaw, err := os.ReadFile(filepath.Join(dest, "package-index.json"))
	if err != nil {
		return false, err
	}
	var idx struct {
		Plugins []struct {
			Path string `json:"path"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(idxRaw, &idx); err != nil {
		return false, err
	}
	got := map[string]struct{}{}
	for _, p := range idx.Plugins {
		got[p.Path] = struct{}{}
	}
	if len(got) != len(want) {
		return false, nil
	}
	for n := range want {
		if _, ok := got[n]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func hasProfile(profiles []string, want string) bool {
	return slices.Contains(profiles, want)
}

func runNativeGates(root string) error {
	type step struct {
		dir  string
		args []string
		env  []string
	}
	runFilter := "TestAdversarial_|TestActivate_|TestStream_|TestDigest|TestManifest|TestDiscover|TestShutdown|TestReap|TestPeer|TestChannel|TestExact|TestUpgrade|TestRollback|TestUninstall|TestConfig|TestSecrecy|TestUnauthorized|TestProtected|TestLaunch|TestKill|TestCancel"
	steps := []step{
		{dir: root, args: []string{"test", "-count=1", "-timeout=15m", "./internal/infra/backendplugins/...", "-run", runFilter}},
		{dir: root, args: []string{"test", "-count=1", "-timeout=10m", "./pkg/lipsdk/backendplugin/...", "-run", "Test|FuzzManifest"}},
		{dir: filepath.Join(root, "connector-support", "acp"), args: []string{"test", "-count=1", "-timeout=10m", "./...", "-run", "KillProcessTree_|ProcessTree_CrossCompile|Cancel"}},
	}
	for _, s := range steps {
		if _, err := os.Stat(s.dir); err != nil {
			continue
		}
		cmd := exec.Command("go", s.args...)
		cmd.Dir = s.dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Env = append(cmd.Env, s.env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("native %s %v: %w\n%s", s.dir, s.args, err, out)
		}
	}
	return nil
}
