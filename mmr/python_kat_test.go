package mmr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pinned from capsule-emit tests/checkpoint/test_mmr_core.py at
// d0ff0d725d8683a9e8f63c4980fc5fe3d7c5d443 (Apache-2.0).
func TestPythonSevenLeafRootKAT(t *testing.T) {
	expected := []string{
		"184208f662bb7a6f5cc14a39988f74f2bb05bd3f934311da0aa3f65a950d8e01",
		"f7b19d3f831d2cddfe91865465a8beb649e4bf16c3aa2fde7378e7ee2e215694",
		"e5280e43815bcb82f184d4dbe10741b65a28c34488a9a3029e0e08d1cbed9a17",
		"44588de7a213cb67d681fd0822d22f57248a50b5cab4d5579c1d9162403b6755",
		"a19de527084fc32502d998b4dd0e73f942d3b4ddc55b2c093dfd907e95a93f1a",
		"ade39981df4d01db5a3c3e5d1ff0a6f87ea3a63077820d37599c6a39fb904b01",
		"a0c0e8e7d78bf06dee4c988a228ff034dca8a25964a4af89a3d7d11670f31d10",
	}
	tree, err := New(nil)
	require.NoError(t, err)
	for number, want := range expected {
		bodyDigest := sha256.Sum256([]byte(fmt.Sprintf("asg-ledger-mmr-vector-leaf-%d", number+1)))
		_, err := tree.AppendCapsuleID(hex.EncodeToString(bodyDigest[:]))
		require.NoError(t, err)
		root, err := tree.Root()
		require.NoError(t, err)
		require.Equal(t, want, hex.EncodeToString(root))
	}
}
