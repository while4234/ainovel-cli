package artwork

import (
	"context"
	"errors"
)

type GatewayConfigResolver func() (ImageGatewayConfig, error)

type ImageJobRunner struct {
	ResolveConfig GatewayConfigResolver
	HTTPClient    HTTPDoer
}

func (r ImageJobRunner) Run(ctx context.Context, store *WorkspaceStore, jobID string) error {
	job, err := store.BeginJob(jobID)
	if err != nil {
		return err
	}
	if job.Request.CatalogVersion != CapabilityRegistryVersion {
		return r.fail(store, job.ID, JobFailed, "capability_catalog_changed", DeliveryNotSent, nil)
	}
	_, prompt, _, err := store.ExecutionSnapshot(job.ID)
	if err != nil {
		return r.fail(store, job.ID, JobFailed, "job_snapshot_invalid", DeliveryNotSent, err)
	}
	if r.ResolveConfig == nil {
		return r.fail(store, job.ID, JobFailed, "gateway_not_configured", DeliveryNotSent, errors.New("gateway configuration resolver is unavailable"))
	}
	config, err := r.ResolveConfig()
	if err != nil {
		return r.fail(store, job.ID, JobFailed, "gateway_not_configured", DeliveryNotSent, err)
	}
	// The endpoint/model/size catalog are immutable submission snapshots. Only
	// the current credential and timeout are resolved at execution time.
	config.BaseURL = job.Internal.GatewayEndpoint
	config.DefaultModel = job.Request.ModelID
	client, err := NewGatewayClient(config, r.HTTPClient)
	if err != nil {
		return r.fail(store, job.ID, JobFailed, gatewayErrorCode(err, "gateway_not_configured"), DeliveryNotSent, err)
	}
	generated, err := client.Generate(ctx, GenerateRequest{
		Model: job.Request.ModelID, Prompt: prompt.Prompt, Size: job.Request.Size,
	})
	if err != nil {
		delivery := gatewayErrorDelivery(err)
		status := JobFailed
		if delivery == DeliveryUncertain {
			status = JobInterruptedUnknown
		}
		return r.fail(store, job.ID, status, gatewayErrorCode(err, "image_generation_failed"), delivery, err)
	}
	if _, err := ValidateImage(generated.Content); err != nil {
		return r.fail(store, job.ID, JobFailed, "gateway_image_invalid", DeliveryResponded, err)
	}
	if _, err := store.FinalizeJob(job.ID, generated.Content); err != nil {
		// Reconciliation may be able to prove and complete an image/metadata
		// commit after a local fault. It never submits another gateway request.
		if _, reconcileErr := store.Reconcile(); reconcileErr == nil {
			if current, getErr := store.GetJob(job.ID); getErr == nil && current.Status == JobSucceeded {
				return nil
			}
		}
		return r.fail(store, job.ID, JobFailed, "asset_persist_failed", DeliveryResponded, err)
	}
	return nil
}

func (r ImageJobRunner) fail(store *WorkspaceStore, jobID string, status JobStatus, code string, delivery DeliveryState, cause error) error {
	if err := store.CompleteJobFailure(jobID, status, code, delivery); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func gatewayErrorCode(err error, fallback string) string {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		return safeFailureCode(gatewayErr.Code)
	}
	return fallback
}

func gatewayErrorDelivery(err error) DeliveryState {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) {
		return gatewayErr.Delivery
	}
	return DeliveryNotSent
}
