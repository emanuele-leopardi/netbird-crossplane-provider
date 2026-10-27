/*
Copyright 2020 The Crossplane Authors.

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

// Package namespaced contains the namespace-scoped controllers of the NetBird
// provider.
package namespaced

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/config"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbaccesstoken"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbaccount"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbdnssetting"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbgroup"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbnameserver"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbnetwork"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbnetworkresource"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbnetworkrouter"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbpolicy"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbsetupkey"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nbuser"
)

// setupFuncs are the setup functions of every controller this provider runs.
var setupFuncs = []func(ctrl.Manager, controller.Options) error{
	config.Setup,
	nbgroup.Setup,
	nbaccount.Setup,
	nbdnssetting.Setup,
	nbnameserver.Setup,
	nbuser.Setup,
	nbsetupkey.Setup,
	nbaccesstoken.Setup,
	nbnetwork.Setup,
	nbnetworkresource.Setup,
	nbnetworkrouter.Setup,
	nbpolicy.Setup,
}

// gatedSetupFuncs are the gated variants of the same setup functions. Each is
// only invoked once the CRD it serves has been established, so the provider
// does not spin on resources the API server does not know about yet.
var gatedSetupFuncs = []func(ctrl.Manager, controller.Options) error{
	config.SetupGated,
	nbgroup.SetupGated,
	nbaccount.SetupGated,
	nbdnssetting.SetupGated,
	nbnameserver.SetupGated,
	nbuser.SetupGated,
	nbsetupkey.SetupGated,
	nbaccesstoken.SetupGated,
	nbnetwork.SetupGated,
	nbnetworkresource.SetupGated,
	nbnetworkrouter.SetupGated,
	nbpolicy.SetupGated,
}

// Setup creates all NetBird controllers with the supplied logger and adds them
// to the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	for _, setup := range setupFuncs {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}

// SetupGated creates all NetBird controllers gated on the establishment of the
// CRDs they serve.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	for _, setup := range gatedSetupFuncs {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}
