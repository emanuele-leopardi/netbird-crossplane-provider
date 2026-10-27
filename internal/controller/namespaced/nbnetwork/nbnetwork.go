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

package nbnetwork

import (
	"context"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	nbapi "github.com/netbirdio/netbird/management/server/http/api"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbNetwork = "managed resource is not a NbNetwork custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errGetCreds     = "cannot get credentials"
)

// Setup adds a controller that reconciles NbNetwork managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbNetworkGroupKind)

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
		resource.ManagedKind(v1alpha1.NbNetworkGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbNetwork{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbNetwork managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbNetworkGroupVersionKind.String())
		}
	}, v1alpha1.NbNetworkGroupVersionKind)
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
	cr, ok := mg.(*v1alpha1.NbNetwork)
	if !ok {
		return nil, errors.New(errNotNbNetwork)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbnetwork"),
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.NbNetwork)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbNetwork)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("observing", "cr", cr)
	externalName := meta.GetExternalName(cr)

	// Adoption pattern: if externalName is blank or matches resource name, try to find by Name
	if externalName == "" || externalName == cr.Name {
		networks, err := client.Networks.List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("failed to list networks")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		for _, net := range networks {
			if net.Name == cr.Spec.ForProvider.Name {
				meta.SetExternalName(cr, net.Id)
				cr.Status.AtProvider = v1alpha1.NbNetworkObservation{
					Id:                net.Id,
					Resources:         &net.Resources,
					Description:       net.Description,
					Name:              net.Name,
					Policies:          &net.Policies,
					Routers:           &net.Routers,
					RoutingPeersCount: net.RoutingPeersCount,
				}
				cr.Status.SetConditions(xpv2.Available())
				return managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false, // force requeue to persist external name
				}, nil
			}
		}
		// Not found by name, treat as not existing
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// If we have an external name (and it's not just the resource name), fetch by ID
	network, err := client.Networks.Get(ctx, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("failed to get network")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}

	cr.Status.AtProvider = v1alpha1.NbNetworkObservation{
		Id:                network.Id,
		Resources:         &network.Resources,
		Description:       network.Description,
		Name:              network.Name,
		Policies:          &network.Policies,
		Routers:           &network.Routers,
		RoutingPeersCount: network.RoutingPeersCount,
	}

	cr.Status.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: isnetworkuptodate(network, cr.Spec.ForProvider),
	}, nil
}

func isnetworkuptodate(network *nbapi.Network, nbNetworkParameters v1alpha1.NbNetworkParameters) bool {
	if !cmp.Equal(*network.Description, nbNetworkParameters.Description) {
		return false
	}
	if !cmp.Equal(network.Name, nbNetworkParameters.Name) {
		return false
	}
	return true
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbNetwork)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbNetwork)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("creating", "cr", cr)
	network, err := client.Networks.Create(ctx, nbapi.NetworkRequest{
		Name:        cr.Spec.ForProvider.Name,
		Description: &cr.Spec.ForProvider.Description,
	})

	if err != nil {
		return managed.ExternalCreation{
			// Optionally return any details that may be required to connect to the
			// external resource. These will be stored as the connection secret.
			ConnectionDetails: managed.ConnectionDetails{},
		}, err
	}
	meta.SetExternalName(cr, network.Id)
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbNetwork)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbNetwork)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to get authenticated client")
	}
	networkid := meta.GetExternalName(cr)
	c.log.Info("Updating", "cr", cr)
	_, err = client.Networks.Update(ctx, networkid, nbapi.PutApiNetworksNetworkIdJSONRequestBody{
		Name:        cr.Spec.ForProvider.Name,
		Description: &cr.Spec.ForProvider.Description,
	})
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbNetwork)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbNetwork)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Deleting", "cr", cr)
	networkid := meta.GetExternalName(cr)
	return managed.ExternalDelete{}, client.Networks.Delete(ctx, networkid)
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
