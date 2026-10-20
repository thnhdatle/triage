// SPDX-License-Identifier: Apache-2.0

// Package calibrate implements threshold calibration to a target strong-call ratio.
//
// Calibrate scores a sample of requests with a difficulty strategy and picks the route threshold so
// that the share of requests with p >= threshold is as close as possible to the target ratio. Result
// renders the YAML routes snippet to paste into the configuration.
//
//	res, err := calibrate.Calibrate(ctx, strategy, sample, 0.3)
//	if err != nil {
//		return err
//	}
//	fmt.Print(res.Snippet("smart")) // routes:\n  - name: smart\n    threshold: 0.6234
package calibrate
