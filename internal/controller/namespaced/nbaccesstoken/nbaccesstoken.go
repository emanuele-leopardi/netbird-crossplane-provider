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

package nbaccesstoken

import (
	"context"
	"time"

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
	netbird "github.com/netbirdio/netbird/management/client/rest"
	nbapi "github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbAccessToken  = "managed resource is not a NbAccessToken custom resource"
	accessTokenSecretKey = "NB_API_KEY"
)

// Setup adds a controller that reconciles NbAccessToken managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbAccessTokenGroupKind)

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
		resource.ManagedKind(v1alpha1.NbAccessTokenGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbAccessToken{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbAccessToken managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbAccessTokenGroupVersionKind.String())
		}
	}, v1alpha1.NbAccessTokenGroupVersionKind)
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
	cr, ok := mg.(*v1alpha1.NbAccessToken)
	if !ok {
		return nil, errors.New(errNotNbAccessToken)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbaccesstoken"),
	}, nil
}

type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.NbAccessToken)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbAccessToken)
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

	// If externalName is set, fetch by ID as usual
	var userid string
	switch {
	case cr.Spec.ForProvider.UserName != nil:
		users, err := client.Users.List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				c.log.Error(err, "Token invalid error detected during get, forcing token refresh")
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("accesskey not found")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		var apiuser *nbapi.User
		for _, user := range users {
			if *user.IsServiceUser && (user.Name == *cr.Spec.ForProvider.UserName) {
				apiuser = &user
				break
			}
		}
		if apiuser == nil {
			return managed.ExternalObservation{ResourceExists: false}, nil
		}
		userid = apiuser.Id
	case cr.Spec.ForProvider.UserId != nil:
		userid = *cr.Spec.ForProvider.UserId
	default:
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	accesstoken, err := client.Tokens.Get(ctx, userid, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			c.log.Error(err, "Token invalid error detected during get, forcing token refresh")
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("accesskey not found")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}
	var lastused *string
	if accesstoken.LastUsed != nil {
		lastusedstring := accesstoken.LastUsed.Local().String()
		lastused = &lastusedstring
	}
	if accesstoken.ExpirationDate.Before(time.Now()) {
		// Token exists but is expired: trigger Update, not Create
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}
	cr.Status.AtProvider = v1alpha1.NbAccessTokenObservation{
		Id:             accesstoken.Id,
		Name:           accesstoken.Name,
		ExpirationDate: accesstoken.ExpirationDate.String(),
		LastUsed:       lastused,
	}
	cr.Status.SetConditions(xpv2.Available())
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true, // update logic can be added if needed
	}, nil
}

func (c *external) getUserID(ctx context.Context, cr *v1alpha1.NbAccessToken, client *netbird.Client) (string, error) {
	if cr.Spec.ForProvider.UserName != nil {

		users, err := client.Users.List(ctx)
		if err != nil {
			return "", errors.Wrap(err, "failed to list users")
		}
		for _, user := range users {
			if user.Name == *cr.Spec.ForProvider.UserName {
				return user.Id, nil
			}
		}
		return "", errors.New("user not found")
	}
	return *cr.Spec.ForProvider.UserId, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbAccessToken)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbAccessToken)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Creating", "cr", cr)
	var userid string
	if cr.Spec.ForProvider.UserName != nil {
		users, err := client.Users.List(ctx)
		if err != nil {
			return managed.ExternalCreation{}, err
		}
		var apiuser nbapi.User
		for _, user := range users {
			if user.Name == *cr.Spec.ForProvider.UserName {
				apiuser = user
			}
		}
		userid = apiuser.Id
	} else {
		userid = *cr.Spec.ForProvider.UserId
	}
	accesstoken, err := client.Tokens.Create(ctx, userid, nbapi.PersonalAccessTokenRequest{
		ExpiresIn: cr.Spec.ForProvider.ExpiresIn,
		Name:      cr.Spec.ForProvider.Name,
	})
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	meta.SetExternalName(cr, accesstoken.PersonalAccessToken.Id)

	cd := managed.ConnectionDetails{
		accessTokenSecretKey: []byte(accesstoken.PlainToken),
	}
	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: cd,
	}, nil
}

// for now only called when token expired
func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbAccessToken)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbAccessToken)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to get authenticated client")
	}

	c.log.Info("Updating", "cr", cr)
	externalName := meta.GetExternalName(cr)
	if externalName == "" {
		return managed.ExternalUpdate{}, errors.New("no externalname found")
	}
	userid, err := c.getUserID(ctx, cr, client)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if err := client.Tokens.Delete(ctx, userid, externalName); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to delete expired token")
	}
	meta.SetExternalName(cr, "")
	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbAccessToken)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbAccessToken)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}

	externalName := meta.GetExternalName(cr)
	if externalName == "" {
		return managed.ExternalDelete{}, errors.New("no externalname found")
	}
	c.log.Info("Deleting", "cr", cr)
	var userid string
	if cr.Spec.ForProvider.UserName != nil {
		users, err := client.Users.List(ctx)
		if err != nil {
			return managed.ExternalDelete{}, err
		}
		var apiuser nbapi.User
		for _, user := range users {
			if user.Name == *cr.Spec.ForProvider.UserName {
				apiuser = user
			}
		}
		userid = apiuser.Id
	} else {
		userid = *cr.Spec.ForProvider.UserId
	}
	return managed.ExternalDelete{}, client.Tokens.Delete(ctx, userid, externalName)
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
