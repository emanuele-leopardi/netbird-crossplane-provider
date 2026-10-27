/*
Copyright 2022 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nbsetupkey

import (
	"context"
	"slices"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbSetupKey = "managed resource is not a NbSetupKey custom resource"
	errTrackPCUsage  = "cannot track ProviderConfig usage"
	errGetPC         = "cannot get ProviderConfig"
	errGetCreds      = "cannot get credentials"

	errGetSetupKey    = "failed to get setupkey"
	errDeleteSetupKey = "failed to delete expired setupkey"
	errUpdateSetupKey = "failed to update setupkey"
	errNoSetupKeyID   = "can't find setupKeyId"
)

// NetBird reports a setup key's state as one of these strings.
const (
	setupKeyStateExpired = "expired"
	setupKeyStateRevoked = "revoked"
)

// isDead reports whether a setup key can no longer be used to register a peer,
// and therefore should be replaced rather than updated.
//
// We deliberately key this off the state NetBird computes rather than
// re-deriving it from Expires: for a key created with expires_in 0 the API
// returns the zero time, and calling Expires.Before(time.Now()) on it would
// report every non-expiring key as expired. A "overused" key is also dead, but
// we do not replace it, so that a key with a finite usage_limit is not
// reissued on every reconcile.
func isDead(setupkey *api.SetupKey) bool {
	return setupkey.State == setupKeyStateExpired || setupkey.State == setupKeyStateRevoked
}

// isUpToDate reports whether the auto-assigned groups of a setup key match the
// desired ones. auto_groups is the only field the NetBird API lets us update.
func isUpToDate(nbSetupKeyParameters *v1alpha1.NbSetupKeyParameters, setupkey *api.SetupKey, log logr.Logger) bool {
	if !equalStringSets(nbSetupKeyParameters.AutoGroups, setupkey.AutoGroups) {
		log.Info("setupkey auto_groups is not up to date",
			"desired", nbSetupKeyParameters.AutoGroups, "observed", setupkey.AutoGroups)
		return false
	}
	return true
}

// equalStringSets compares two string slices as sets, so that the order in
// which NetBird returns group IDs cannot cause spurious updates. A nil slice
// and an empty slice are considered equal.
func equalStringSets(a, b []string) bool {
	as := slices.Clone(a)
	bs := slices.Clone(b)
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// Setup adds a controller that reconciles NbSetupKey managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbSetupKeyGroupKind)

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnector(&connector{
			SharedConnector: nb.NewSharedConnector(
				mgr.GetClient(),
				resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{}),
			),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(features.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.NbSetupKeyGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbSetupKey{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbSetupKey managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbSetupKeyGroupVersionKind.String())
		}
	}, v1alpha1.NbSetupKeyGroupVersionKind)
	return nil
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	*nb.SharedConnector
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.NbSetupKey)
	if !ok {
		return nil, errors.New(errNotNbSetupKey)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbsetupkey"),
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.NbSetupKey)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbSetupKey)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Observing", "cr", cr)
	externalName := meta.GetExternalName(cr)

	if externalName == "" {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// If we have an external name, fetch by ID
	setupkey, err := client.SetupKeys.Get(ctx, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("setupkey not found")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}

	// The reconciler republishes whatever Observe returns as the connection
	// secret, so the key NetBird holds is always what gets published. Returning
	// no details here would blank the secret on every reconcile of a resource
	// that is already up to date, and the routing peer reads its setup key from
	// that secret.
	details := connectionDetails(setupkey)

	if isDead(setupkey) {
		// The key exists but can no longer be used. Return ResourceUpToDate
		// false so that Update replaces it, and the next reconcile issues a
		// fresh key and a fresh connection secret.
		c.log.Info("setupkey is expired or revoked, it will be replaced",
			"state", setupkey.State, "id", setupkey.Id)
		return managed.ExternalObservation{
			ResourceExists:    true,
			ResourceUpToDate:  false,
			ConnectionDetails: details,
		}, nil
	}
	cr.Status.AtProvider = v1alpha1.NbSetupKeyObservation{
		Id:                  setupkey.Id,
		AllowExtraDnsLabels: setupkey.AllowExtraDnsLabels,
		AutoGroups:          &setupkey.AutoGroups,
		Ephemeral:           setupkey.Ephemeral,
		Expires:             setupkey.Expires.String(),
		LastUsed:            setupkey.LastUsed.String(),
		Name:                setupkey.Name,
		Revoked:             setupkey.Revoked,
		State:               setupkey.State,
		Type:                setupkey.Type,
		UpdatedAt:           setupkey.UpdatedAt.String(),
		UsageLimit:          setupkey.UsageLimit,
		UsedTimes:           setupkey.UsedTimes,
		Valid:               setupkey.Valid,
	}

	cr.Status.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  isUpToDate(&cr.Spec.ForProvider, setupkey, c.log),
		ConnectionDetails: details,
	}, nil
}

// connectionDetails returns the connection secret that reflects the setup key
// NetBird holds. The key name is part of this provider's public interface: it
// is what consumers read the key from, so it must not be renamed.
func connectionDetails(setupkey *api.SetupKey) managed.ConnectionDetails {
	if setupkey == nil {
		return managed.ConnectionDetails{}
	}
	return managed.ConnectionDetails{
		xpv2.CredentialsSecretPasswordKey: []byte(setupkey.Key),
	}
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbSetupKey)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbSetupKey)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Creating", "cr", cr)
	setupkey, err := client.SetupKeys.Create(ctx, api.PostApiSetupKeysJSONRequestBody{
		Name:                cr.Spec.ForProvider.Name,
		AllowExtraDnsLabels: &cr.Spec.ForProvider.AllowExtraDnsLabels,
		AutoGroups:          cr.Spec.ForProvider.AutoGroups,
		Ephemeral:           &cr.Spec.ForProvider.Ephemeral,
		ExpiresIn:           cr.Spec.ForProvider.ExpiresIn,
		Type:                cr.Spec.ForProvider.Type,
		UsageLimit:          cr.Spec.ForProvider.UsageLimit,
	})

	if err != nil {
		return managed.ExternalCreation{
			// Optionally return any details that may be required to connect to the
			// external resource. These will be stored as the connection secret.
			ConnectionDetails: managed.ConnectionDetails{},
		}, err
	}
	meta.SetExternalName(cr, setupkey.Id)
	cd := managed.ConnectionDetails{xpv2.CredentialsSecretPasswordKey: []byte(setupkey.Key)}
	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: cd,
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbSetupKey)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbSetupKey)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Updating", "cr", cr)
	setupKeyId := meta.GetExternalName(cr)
	if setupKeyId == "" {
		return managed.ExternalUpdate{}, errors.New(errNoSetupKeyID)
	}

	// Observe does not tell us why the key is out of date, so re-read it to
	// decide between replacing a dead key and correcting drift in place.
	existing, err := client.SetupKeys.Get(ctx, setupKeyId)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalUpdate{}, err
		}
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetSetupKey)
	}

	if isDead(existing) {
		// The key can no longer be used and the API will not let us revive it.
		// Delete it and clear the external name so that the next reconcile
		// creates a brand new key, which also refreshes the connection secret.
		// Until that happens there is no key to publish, so the connection
		// secret is left empty rather than holding a key NetBird rejects.
		if err := client.SetupKeys.Delete(ctx, setupKeyId); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errDeleteSetupKey)
		}
		meta.SetExternalName(cr, "")
		return managed.ExternalUpdate{
			ConnectionDetails: managed.ConnectionDetails{},
		}, nil
	}

	// The key is still valid, so the only thing that can have drifted is
	// auto_groups: it is the only field the NetBird API allows us to update.
	// We never ask to un-revoke, which the API rejects.
	if _, err := client.SetupKeys.Update(ctx, setupKeyId, api.PutApiSetupKeysKeyIdJSONRequestBody{
		AutoGroups: cr.Spec.ForProvider.AutoGroups,
		Revoked:    false,
	}); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateSetupKey)
	}

	return managed.ExternalUpdate{
		ConnectionDetails: connectionDetails(existing),
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbSetupKey)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbSetupKey)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Deleting", "cr", cr)

	err = client.SetupKeys.Delete(ctx, meta.GetExternalName(cr))
	if err != nil {
		return managed.ExternalDelete{}, err
	}

	return managed.ExternalDelete{}, nil
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
