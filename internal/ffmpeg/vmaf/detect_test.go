package vmaf

import "testing"

func TestSelectModelFallback(t *testing.T) {
	// Save and restore global state
	oldModels := vmafModels
	defer func() { vmafModels = oldModels }()

	tests := []struct {
		name      string
		models    []string
		height    int
		wantModel string
	}{
		{
			name:      "use default for 1080p",
			models:    []string{"vmaf_v0.6.1", "vmaf_4k_v0.6.1"},
			height:    1080,
			wantModel: "vmaf_v0.6.1",
		},
		{
			name:      "use 4k for 2160p",
			models:    []string{"vmaf_v0.6.1", "vmaf_4k_v0.6.1"},
			height:    2160,
			wantModel: "vmaf_4k_v0.6.1",
		},
		{
			name:      "fallback when 4k missing",
			models:    []string{"vmaf_v0.6.1"},
			height:    2160,
			wantModel: "vmaf_v0.6.1",
		},
		{
			name:      "fallback when no models",
			models:    []string{},
			height:    1080,
			wantModel: "vmaf_v0.6.1", // Default fallback
		},
		{
			name:      "use 720p with default model",
			models:    []string{"vmaf_v0.6.1", "vmaf_4k_v0.6.1"},
			height:    720,
			wantModel: "vmaf_v0.6.1",
		},
		{
			name:      "use first available when no standard model",
			models:    []string{"vmaf_custom_model"},
			height:    1080,
			wantModel: "vmaf_custom_model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vmafModels = tt.models
			got := SelectModel(tt.height)
			if got != tt.wantModel {
				t.Errorf("SelectModel(%d) = %q, want %q", tt.height, got, tt.wantModel)
			}
		})
	}
}

func TestIsAvailable(t *testing.T) {
	// Save and restore global state
	oldAvailable := vmafAvailable
	defer func() { vmafAvailable = oldAvailable }()

	vmafAvailable = true
	if !IsAvailable() {
		t.Error("IsAvailable() = false, want true")
	}

	vmafAvailable = false
	if IsAvailable() {
		t.Error("IsAvailable() = true, want false")
	}
}

func TestGetModels(t *testing.T) {
	// Save and restore global state
	oldModels := vmafModels
	defer func() { vmafModels = oldModels }()

	vmafModels = []string{"model1", "model2"}
	got := GetModels()
	if len(got) != 2 || got[0] != "model1" || got[1] != "model2" {
		t.Errorf("GetModels() = %v, want [model1, model2]", got)
	}
}
