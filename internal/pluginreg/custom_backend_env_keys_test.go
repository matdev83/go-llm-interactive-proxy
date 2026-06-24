package pluginreg

import (
	"fmt"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func clearCustomEnvRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv(root, "")
	for i := 2; i <= maxNumberedAPIKeysEnv; i++ {
		t.Setenv(fmt.Sprintf("%s_%d", root, i), "")
	}
}

func TestCollectNumberedEnvKeys_numberedFromRoot(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "k1")
	t.Setenv(root+"_2", "k2")
	t.Setenv(root+"_3", "k3")
	got := collectNumberedEnvKeys(root)
	want := []string{"k1", "k2", "k3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectNumberedEnvKeys(%q) = %#v, want %#v", root, got, want)
	}
}

func TestCollectNumberedEnvKeys_ignoresRoot1Suffix(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "")
	t.Setenv(root+"_1", "ignored")
	t.Setenv(root+"_2", "k2")
	got := collectNumberedEnvKeys(root)
	want := []string{"k2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectNumberedEnvKeys(%q) = %#v, want %#v (ROOT_1 is not part of the convention)", root, got, want)
	}
}

func TestCollectNumberedEnvKeys_stopsAtGap(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "a")
	t.Setenv(root+"_2", "")
	t.Setenv(root+"_3", "c")
	got := collectNumberedEnvKeys(root)
	want := []string{"a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectNumberedEnvKeys(%q) = %#v, want %#v", root, got, want)
	}
}

func TestResolveCustomCompatibleAPIKeys_yamlOverridesEnvRoot(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	t.Setenv(root+"_2", "env-two")
	raw := `api_key: yaml-key
api_key_env_var_root: MY_PROVIDER_API_KEY
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	y, err := decodeCustomCompatibleBackendYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveCustomCompatibleAPIKeys(y)
	want := []string{"yaml-key"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCustomCompatibleAPIKeys(...) = %#v, want %#v", got, want)
	}
}

func TestResolveCustomCompatibleAPIKeys_envRootFallbackWhenYAMLMissing(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	t.Setenv(root+"_2", "env-two")
	raw := `api_key_env_var_root: MY_PROVIDER_API_KEY
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	y, err := decodeCustomCompatibleBackendYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveCustomCompatibleAPIKeys(y)
	want := []string{"env-one", "env-two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCustomCompatibleAPIKeys(...) = %#v, want %#v", got, want)
	}
}

func TestResolveCustomCompatibleAPIKeys_mergesYAMLAPIKeys(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	raw := `api_key: yaml-primary
api_keys:
  - yaml-secondary
  - yaml-primary
api_key_env_var_root: MY_PROVIDER_API_KEY
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	y, err := decodeCustomCompatibleBackendYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveCustomCompatibleAPIKeys(y)
	want := []string{"yaml-primary", "yaml-secondary"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCustomCompatibleAPIKeys(...) = %#v, want %#v", got, want)
	}
}

func TestResolveCustomCompatibleAPIKeys_credentialsPrecedeEnvRoot(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	raw := `api_key_env_var_root: MY_PROVIDER_API_KEY
credentials:
  - id: primary
    api_key: cred-key
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	y, err := decodeCustomCompatibleBackendYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveCustomCompatibleAPIKeys(y)
	want := []string{"cred-key"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCustomCompatibleAPIKeys(...) = %#v, want %#v (credentials must override env root fallback)", got, want)
	}
}

func TestResolveCustomCompatibleAPIKeys_credentialsPrecedeYAMLAndEnvRoot(t *testing.T) {
	root := "MY_PROVIDER_API_KEY"
	clearCustomEnvRoot(t, root)
	t.Setenv(root, "env-one")
	raw := `api_key: yaml-primary
api_keys:
  - yaml-secondary
api_key_env_var_root: MY_PROVIDER_API_KEY
credentials:
  - id: primary
    api_key: cred-key
`
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	y, err := decodeCustomCompatibleBackendYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	got := resolveCustomCompatibleAPIKeys(y)
	want := []string{"cred-key"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolveCustomCompatibleAPIKeys(...) = %#v, want %#v (credentials must override YAML and env root)", got, want)
	}
}
