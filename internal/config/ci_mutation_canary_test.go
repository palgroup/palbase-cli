package config

import "testing"

// Temporary mutation-test canary: proves the ci.yml unit gate can go red.
// Reverted immediately after the red run is observed.
func TestCIMutationCanary(t *testing.T) {
	t.Fatal("intentional failure: ci gate mutation test")
}
