package service

import "testing"

func TestConfigWrapperAutoDownloadDefaultsOnAndCanBeDisabled(t *testing.T) {
	cfg, err := ParseConfigYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.toProduct().WrapperAutoDownload {
		t.Fatal("wrapper auto-download should default to enabled")
	}

	cfg, err = ParseConfigYAML([]byte("wrapper_auto_download: false\nwrapper_cache_dir: /tmp/wrappers\n"))
	if err != nil {
		t.Fatal(err)
	}
	product := cfg.toProduct()
	if product.WrapperAutoDownload {
		t.Fatal("wrapper auto-download opt-out was ignored")
	}
	if product.WrapperCacheDir != "/tmp/wrappers" {
		t.Fatalf("cache dir = %q", product.WrapperCacheDir)
	}
}
