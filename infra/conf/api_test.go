package conf_test

import (
	"testing"

	fllckerservice "github.com/xtls/xray-core/app/fllcker/command"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/infra/conf"
)

// TestAPIConfigResolvesFllckerService covers the one failure mode the patch
// marker check cannot see (FLK-005): the switch in APIConfig.Build has no
// default branch, so a misspelled service name is silently dropped. The API
// then starts fine and the method is simply absent, which is a confusing thing
// to debug from the outside.
func TestAPIConfigResolvesFllckerService(t *testing.T) {
	const want = "xray.app.fllcker.command.Config"

	config, err := (&conf.APIConfig{
		Tag:      "api",
		Services: []string{"FllckerService"},
	}).Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(config.Service) != 1 {
		t.Fatalf("%d services built, want 1: the name did not match any branch", len(config.Service))
	}
	if got := config.Service[0].Type; got != want {
		t.Errorf("service type = %q, want %q", got, want)
	}

	// The name is matched case-insensitively, and callers write it in mixed
	// case in JSON.
	lower, err := (&conf.APIConfig{
		Tag:      "api",
		Services: []string{"fllckerservice"},
	}).Build()
	if err != nil {
		t.Fatalf("Build (lower case): %v", err)
	}
	if len(lower.Service) != 1 {
		t.Fatal("lower case spelling did not resolve")
	}

	// Guards the expected type string itself, so a package rename cannot make
	// the assertion above vacuous.
	if got := serial.ToTypedMessage(&fllckerservice.Config{}).Type; got != want {
		t.Errorf("Config type is %q, expected %q", got, want)
	}
}
