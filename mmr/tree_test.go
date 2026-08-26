package mmr

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBaggedInclusionRoundTrip(t *testing.T) {
	tree, err := New(nil)
	require.NoError(t, err)
	ids := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	for _, id := range ids {
		_, err := tree.AppendCapsuleID(id)
		require.NoError(t, err)
	}
	root, err := tree.Root()
	require.NoError(t, err)
	for index, id := range ids {
		proof, err := tree.InclusionProof(uint64(index))
		require.NoError(t, err)
		require.True(t, VerifyInclusion(root, tree.Size(), uint64(index), id, proof))
		proof[0][0] ^= 0xff
		require.False(t, VerifyInclusion(root, tree.Size(), uint64(index), id, proof))
	}
}

func TestBaggedConsistencyProofAcrossMultiPeakTrees(t *testing.T) {
	tree, err := New(nil)
	require.NoError(t, err)
	for index := 1; index <= 3; index++ {
		_, err = tree.AppendCapsuleID(fmt.Sprintf("%064x", index))
		require.NoError(t, err)
	}
	oldSize := tree.Size()
	oldRoot, err := tree.Root()
	require.NoError(t, err)
	for index := 4; index <= 7; index++ {
		_, err = tree.AppendCapsuleID(fmt.Sprintf("%064x", index))
		require.NoError(t, err)
	}
	newRoot, err := tree.Root()
	require.NoError(t, err)
	proof, err := tree.ConsistencyProof(oldSize)
	require.NoError(t, err)
	require.True(t, VerifyConsistency(oldRoot, newRoot, proof))
	if len(proof.Path) > 0 {
		proof.Path[0][0] ^= 1
		require.False(t, VerifyConsistency(oldRoot, newRoot, proof))
	}
}

func TestEmptyRootAndAdversarialInputs(t *testing.T) {
	tree, err := New(nil)
	require.NoError(t, err)
	root, err := tree.Root()
	require.NoError(t, err)
	require.Equal(t, make([]byte, 32), root)
	require.False(t, VerifyInclusion(root, 0, 0, "bad", nil))
	_, err = New([][]byte{{1}})
	require.Error(t, err)
	_, err = New([][]byte{make([]byte, 32), make([]byte, 32)})
	require.Error(t, err)
	corrupt := [][]byte{make([]byte, 32), make([]byte, 32), make([]byte, 32)}
	_, err = New(corrupt)
	require.Error(t, err)
	require.False(t, VerifyInclusion(make([]byte, 32), 2, 0, fmt.Sprintf("%064x", 1), nil))
}
