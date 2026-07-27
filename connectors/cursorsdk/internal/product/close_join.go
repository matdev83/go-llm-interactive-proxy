package product

import "errors"

func closePoolThenBridge(closePool, closeBridge func() error) error {
	var errs []error
	if closePool != nil {
		if err := closePool(); err != nil {
			errs = append(errs, err)
		}
	}
	if closeBridge != nil {
		if err := closeBridge(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
