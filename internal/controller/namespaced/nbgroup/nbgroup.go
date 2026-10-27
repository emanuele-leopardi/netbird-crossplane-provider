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

package nbgroup

import (
	"context"

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
	nbapi "github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbGroup   = "managed resource is not a NbGroup custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCreds     = "cannot get credentials"
)

// Setup adds a controller that reconciles NbGroup managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbGroupGroupKind)

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
		resource.ManagedKind(v1alpha1.NbGroupGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbGroup{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbGroup managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbGroupGroupVersionKind.String())
		}
	}, v1alpha1.NbGroupGroupVersionKind)
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
	cr, ok := mg.(*v1alpha1.NbGroup)
	if !ok {
		return nil, errors.New(errNotNbGroup)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbgroup"),
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {

	cr, ok := mg.(*v1alpha1.NbGroup)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbGroup)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Observing", "cr", cr)
	externalName := meta.GetExternalName(cr)

	// Adoption pattern: handle each case separately
	if externalName == "" || externalName == cr.Name {
		c.log.Info("external name blank or matches resource name, attempting adoption by Name", "name", cr.Spec.ForProvider.Name)
		groups, err := client.Groups.List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("failed to list groups")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		for _, apigroup := range groups {
			if apigroup.Name == cr.Spec.ForProvider.Name {
				c.log.Info("found existing group for adoption", "groupid", apigroup.Id)
				meta.SetExternalName(cr, apigroup.Id)
				cr.Status.SetConditions(xpv2.Available())
				cr.Status.AtProvider = v1alpha1.NbGroupObservation{
					Id:             apigroup.Id,
					Issued:         apigroup.Issued,
					Peers:          apigroup.Peers,
					PeersCount:     apigroup.PeersCount,
					Resources:      apigroup.Resources,
					ResourcesCount: apigroup.ResourcesCount,
				}
				c.log.Info("set atprovider id", "id", cr.Status.AtProvider.Id)
				return managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true, // since we don't update groups
				}, nil
			}
		}
		// Not found, resource does not exist
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	if externalName == cr.Spec.ForProvider.Name {
		// Only check for existence, do not set external name
		c.log.Info("external name matches ForProvider.Name, checking existence only", "name", cr.Spec.ForProvider.Name)
		groups, err := client.Groups.List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("failed to list groups")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		for _, apigroup := range groups {
			if apigroup.Name == externalName {
				c.log.Info("found existing group by ForProvider.Name", "groupid", apigroup.Id)
				cr.Status.SetConditions(xpv2.Available())
				cr.Status.AtProvider = v1alpha1.NbGroupObservation{
					Id:             apigroup.Id,
					Issued:         apigroup.Issued,
					Peers:          apigroup.Peers,
					PeersCount:     apigroup.PeersCount,
					Resources:      apigroup.Resources,
					ResourcesCount: apigroup.ResourcesCount,
				}
				c.log.Info("set atprovider id", "id", cr.Status.AtProvider.Id)
				return managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true, // since we don't update groups
				}, nil
			}
		}
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	c.log.Info("external name set, fetching by ID", "externalName", externalName)
	apigroup, err := client.Groups.Get(ctx, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("failed to get group")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}
	group := *apigroup

	cr.Status.SetConditions(xpv2.Available())
	c.log.Info("setting atprovider")
	cr.Status.AtProvider = v1alpha1.NbGroupObservation{
		Id:             group.Id,
		Issued:         group.Issued,
		Peers:          group.Peers,
		PeersCount:     group.PeersCount,
		Resources:      group.Resources,
		ResourcesCount: group.ResourcesCount,
	}
	c.log.Info("set atprovider id", "id", cr.Status.AtProvider.Id)
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true, // since we don't update groups
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbGroup)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbGroup)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Creating", "cr", cr)
	group, err := client.Groups.Create(ctx, nbapi.GroupRequest{
		Name: cr.Spec.ForProvider.Name,
	})

	if err != nil {
		return managed.ExternalCreation{
			// Optionally return any details that may be required to connect to the
			// external resource. These will be stored as the connection secret.
			ConnectionDetails: managed.ConnectionDetails{},
		}, err
	}
	c.log.Info("group created", "group", group)
	meta.SetExternalName(cr, group.Id)
	return managed.ExternalCreation{}, nil
}

// we don't update group
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbGroup)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbGroup)
	}
	c.log.Info("Updating", "cr", cr) // no fields update on group
	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbGroup)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbGroup)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Deleting", "cr", cr)
	return managed.ExternalDelete{}, client.Groups.Delete(ctx, meta.GetExternalName(cr))
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
