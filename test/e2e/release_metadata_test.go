package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseWorkflowPublishesValidatedMultiArchMetadata(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	required := []string{
		"Build linux/amd64 image",
		"platforms: linux/amd64",
		"name: amd64-digest",
		"Build linux/arm64 image",
		"runs-on: ubuntu-24.04-arm",
		"platforms: linux/arm64",
		"name: arm64-digest",
		"needs:\n      - prepare\n      - build-amd64\n      - build-arm64\n      - validate",
		"--annotation \"index:org.opencontainers.image.source=https://github.com/thorion3006/foip-operator\"",
		"--annotation \"index:org.opencontainers.image.version=${{ needs.prepare.outputs.version }}\"",
		"--annotation \"index:org.opencontainers.image.revision=${{ github.sha }}\"",
		"echo \"digest=$manifest_digest\" >> \"$GITHUB_OUTPUT\"",
		"image: ${{ env.IMAGE }}@${{ steps.manifest.outputs.digest }}",
		"subject-digest: ${{ steps.manifest.outputs.digest }}",
		"Verify published image architectures",
		"docker buildx imagetools inspect \"${IMAGE}@${DIGEST}\" --raw",
		".manifests[] | select(.platform.architecture == \"amd64\" and .platform.os == \"linux\")",
		".manifests[] | select(.platform.architecture == \"arm64\" and .platform.os == \"linux\")",
		"cosign sign --yes \"${IMAGE}@${DIGEST}\"",
		"helm push dist/foip-operator-${{ needs.prepare.outputs.version }}.tgz",
	}
	for _, want := range required {
		if !strings.Contains(workflow, want) {
			t.Errorf("release workflow is missing %q", want)
		}
	}
}

func readRepoFile(t *testing.T, name string) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	content, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(content)
}
