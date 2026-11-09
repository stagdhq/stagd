package cli

import (
	"context"
	"strings"
	"testing"

	apiv1 "github.com/stagdhq/stagd/internal/gen/stagd/api/v1"
)

// azureKind is a kind with a choice of how to sign in, each with its own fields.
var azureKind = &apiv1.IntegrationKind{Id: "azure-blob", Fields: []*apiv1.IntegrationField{
	{Key: "account", Required: true},
	{Key: "sign_in", Options: []*apiv1.IntegrationFieldOption{{Value: "sas"}, {Value: "oidc"}}},
	{Key: "sas", Required: true, Secret: true, ShownWith: "sign_in=sas"},
	{Key: "tenant_id", Required: true, ShownWith: "sign_in=oidc"},
	{Key: "restrict", Checkbox: true},
	{Key: "prefix", ShownWith: "restrict"},
}}

func field(key string) *apiv1.IntegrationField {
	for _, f := range azureKind.GetFields() {
		if f.GetKey() == key {
			return f
		}
	}

	return nil
}

func TestFieldShownFollowsChoicesAndCheckboxes(t *testing.T) {
	// No choice given: the first option's fields.
	if !fieldShown(azureKind, field("sas"), map[string]string{}) || fieldShown(azureKind, field("tenant_id"), map[string]string{}) {
		t.Error("with no choice, the first option's fields apply")
	}

	oidc := map[string]string{"sign_in": "oidc"}

	if fieldShown(azureKind, field("sas"), oidc) || !fieldShown(azureKind, field("tenant_id"), oidc) {
		t.Error("with oidc chosen, its fields apply and the SAS doesn't")
	}

	if fieldShown(azureKind, field("prefix"), map[string]string{}) || !fieldShown(azureKind, field("prefix"), map[string]string{"restrict": "true"}) {
		t.Error("a checkbox's field applies only while it's ticked")
	}
}

func TestAskIntegrationFieldsNamesOnlyThoseThatApply(t *testing.T) {
	err := askIntegrationFields(context.Background(), azureKind, map[string]string{"sign_in": "oidc"})
	if err == nil {
		t.Fatal("missing required values: want an error")
	}

	if !strings.Contains(err.Error(), "account, tenant_id") || strings.Contains(err.Error(), "sas") {
		t.Errorf("named %q", err.Error())
	}

	if err := askIntegrationFields(context.Background(), azureKind, map[string]string{"account": "acme", "sas": "sv=…"}); err != nil {
		t.Errorf("the default choice's fields given: %v", err)
	}
}
