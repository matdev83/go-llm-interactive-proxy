package economics

import "fmt"

// SupportAdvisoryVersionV1 identifies the first frozen support-advisory contract.
const SupportAdvisoryVersionV1 = "component-support-advisory-v1"

func validateSupportAdvisoryVersion(version string) error {
	if version == "" || version == SupportAdvisoryVersionV1 {
		return nil
	}
	return fmt.Errorf("%w: unsupported support advisory version %q", ErrInvalidTariffSnapshot, version)
}
