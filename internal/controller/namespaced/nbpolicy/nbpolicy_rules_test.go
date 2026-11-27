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

package nbpolicy

import (
	"strings"
	"testing"

	nbapi "github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

func rule(name string) v1alpha1.PolicyRule {
	return v1alpha1.PolicyRule{
		Name:          name,
		Description:   ptr(""),
		Action:        "accept",
		Protocol:      "all",
		Bidirectional: false,
		Enabled:       true,
		Sources: func() *[]v1alpha1.GroupMinimum {
			s := []v1alpha1.GroupMinimum{{Name: ptr("All")}}
			return &s
		}(),
	}
}

func ptr[T any](v T) *T { return &v }

// TestNbToApiRulesRejectsMoreThanOneRule pins the behaviour that cost a whole
// evening of debugging: NetBird accepts a multi-rule policy and returns it
// intact, but stores only the first rule. Left uncorrected that shows up as an
// NbPolicy that reports SYNCED while silently granting nothing for every rule
// after the first, and the reconciler never stops trying to push the rest.
func TestNbToApiRulesRejectsMoreThanOneRule(t *testing.T) {
	groups := []nbapi.Group{{Id: "g-all", Name: "All"}}

	one := []v1alpha1.PolicyRule{rule("only")}
	rules, err := NbToApiRules(&one, groups)
	if err != nil {
		t.Fatalf("one rule should be accepted, got %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Name != "only" {
		t.Errorf("unexpected rule name %q", rules[0].Name)
	}

	for _, n := range []int{2, 3, 5} {
		many := make([]v1alpha1.PolicyRule, n)
		for i := range many {
			many[i] = rule("rule")
		}
		_, err := NbToApiRules(&many, groups)
		if err == nil {
			t.Fatalf("%d rules should be rejected", n)
		}
		if !strings.Contains(err.Error(), "one NbPolicy per rule") {
			t.Errorf("error should name the fix, got: %v", err)
		}
		if !strings.Contains(err.Error(), "first rule") {
			t.Errorf("error should explain what the API does, got: %v", err)
		}
	}
}

func TestNbToApiRulesRejectsEmptyRuleList(t *testing.T) {
	empty := []v1alpha1.PolicyRule{}
	if _, err := NbToApiRules(&empty, nil); err == nil {
		t.Fatal("a policy with no rules should be rejected")
	}
	if _, err := NbToApiRules(nil, nil); err == nil {
		t.Fatal("a nil rule list should be rejected")
	}
}
