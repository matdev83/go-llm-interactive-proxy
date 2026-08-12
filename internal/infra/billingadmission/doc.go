// Package billingadmission adapts the runtime pre-provider BillingAdmission seam
// to the billing AdmissionService. It builds MaxChargeInput from the
// side-effect-free route plan and request-size estimate without calling
// providers or mutating streams.
package billingadmission
