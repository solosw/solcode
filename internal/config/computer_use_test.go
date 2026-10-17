package config

import "testing"

func TestComputerUseRequiresNativeOptIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  ComputerUseConfig
		want bool
	}{
		{"default", ComputerUseConfig{}, false},
		{"legacy enabled only", ComputerUseConfig{Enabled: true}, false},
		{"legacy robotgo", ComputerUseConfig{Enabled: true, Backend: "robotgo"}, false},
		{"native disabled", ComputerUseConfig{Backend: ComputerUseBackendNative}, false},
		{"native enabled", ComputerUseConfig{Enabled: true, Backend: ComputerUseBackendNative}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.ComputerUse = tc.cfg
			if got := cfg.ComputerUseEnabled(); got != tc.want {
				t.Fatalf("ComputerUseEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
