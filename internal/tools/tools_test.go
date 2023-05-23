package tools

import (
	"testing"

	"github.com/kubescape/k8s-interface/instanceidhandler/v1"
	"github.com/stretchr/testify/assert"
)

func TestEnsureSetup(t *testing.T) {
	EnsureSetup(t, true)
}

func TestPackageVersion(t *testing.T) {
	assert.True(t, PackageVersion("github.com/anchore/syft") == "unknown") // only works on compiled binaries
}

func TestLabelsFromImageID(t *testing.T) {
	tests := []struct {
		imageID string
		want    map[string]string
	}{
		{
			imageID: "myapp",
			want:    map[string]string{instanceidhandler.ImageIDMetadataKey: "myapp", instanceidhandler.ImageNameMetadataKey: "myapp"},
		},
		{
			imageID: "registry.com:8080/myapp",
			want:    map[string]string{instanceidhandler.ImageIDMetadataKey: "registry-com-8080-myapp", instanceidhandler.ImageNameMetadataKey: "registry-com-8080-myapp"},
		},
		{
			imageID: "registry.com:8080/myapp:tag",
			want:    map[string]string{instanceidhandler.ImageIDMetadataKey: "registry-com-8080-myapp-tag", instanceidhandler.ImageNameMetadataKey: "registry-com-8080-myapp", instanceidhandler.ImageTagMetadataKey: "tag"},
		},
		{
			imageID: "registry.com:8080/myapp@sha256:be178c0543eb17f5f3043021c9e5fcf30285e557a4fc309cce97ff9ca6182912",
			want:    map[string]string{instanceidhandler.ImageIDMetadataKey: "registry-com-8080-myapp-sha256-be178c0543eb17f5f3043021c9e5fcf3", instanceidhandler.ImageNameMetadataKey: "registry-com-8080-myapp"},
		},
		{
			imageID: "registry.com:8080/myapp:tag2@sha256:be178c0543eb17f5f3043021c9e5fcf30285e557a4fc309cce97ff9ca6182912",
			want:    map[string]string{instanceidhandler.ImageIDMetadataKey: "registry-com-8080-myapp-tag2-sha256-be178c0543eb17f5f3043021c9e", instanceidhandler.ImageNameMetadataKey: "registry-com-8080-myapp", instanceidhandler.ImageTagMetadataKey: "tag2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.imageID, func(t *testing.T) {
			got := LabelsFromImageID(tt.imageID)
			assert.Equal(t, got, tt.want)
		})
	}
}
