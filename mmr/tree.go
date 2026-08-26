package mmr

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"

	dtmmr "github.com/datatrails/go-datatrails-merklelog/mmr"
)

const hashSize = sha256.Size

// Tree is an in-memory DataTrails-compatible bagged MMR.
type Tree struct {
	mu    sync.RWMutex
	nodes [][]byte
}

// ConsistencyProof proves that NewSize append-only extends OldSize.
type ConsistencyProof struct {
	OldSize  uint64
	NewSize  uint64
	OldPeaks [][]byte
	Path     [][]byte
}

// New restores a Tree from its complete ordered node sequence.
func New(nodes [][]byte) (*Tree, error) {
	tree := &Tree{nodes: cloneProof(nodes)}
	if !validMMRSize(uint64(len(tree.nodes))) {
		return nil, fmt.Errorf("invalid complete MMR size %d", len(tree.nodes))
	}
	for index, node := range tree.nodes {
		if len(node) != hashSize {
			return nil, fmt.Errorf("node %d has length %d", index, len(node))
		}
		height := dtmmr.IndexHeight(uint64(index))
		if height == 0 {
			continue
		}
		left := uint64(index) - (2 << (height - 1))
		right := uint64(index) - 1
		hasher := sha256.New()
		var position [8]byte
		binary.BigEndian.PutUint64(position[:], uint64(index)+1)
		hasher.Write(position[:])
		hasher.Write(tree.nodes[left])
		hasher.Write(tree.nodes[right])
		if !bytes.Equal(node, hasher.Sum(nil)) {
			return nil, fmt.Errorf("interior node %d does not match its children", index)
		}
	}
	return tree, nil
}

func (t *Tree) AppendCapsuleID(capsuleID string) (uint64, error) {
	id, err := hex.DecodeString(capsuleID)
	if err != nil || len(id) != hashSize || hex.EncodeToString(id) != capsuleID {
		return 0, fmt.Errorf("capsule id must be 64 lowercase hexadecimal characters")
	}
	leafInput := append([]byte{0}, id...)
	leaf := sha256.Sum256(leafInput)

	t.mu.Lock()
	defer t.mu.Unlock()
	size, err := dtmmr.AddHashedLeaf((*nodeStore)(t), sha256.New(), leaf[:])
	if err != nil {
		return 0, fmt.Errorf("append MMR leaf: %w", err)
	}
	return size, nil
}

func (t *Tree) Size() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return uint64(len(t.nodes))
}

func (t *Tree) Nodes() [][]byte {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return cloneProof(t.nodes)
}

func (t *Tree) Root() ([]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.nodes) == 0 {
		return make([]byte, hashSize), nil
	}
	root, err := dtmmr.GetRoot(uint64(len(t.nodes)), (*nodeStore)(t), sha256.New())
	if err != nil {
		return nil, fmt.Errorf("compute bagged MMR root: %w", err)
	}
	return append([]byte(nil), root...), nil
}

func (t *Tree) InclusionProof(leafIndex uint64) ([][]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if leafIndex >= dtmmr.LeafCount(uint64(len(t.nodes))) {
		return nil, fmt.Errorf("leaf index %d out of range", leafIndex)
	}
	proof, err := dtmmr.InclusionProofBagged(
		uint64(len(t.nodes)), (*nodeStore)(t), sha256.New(), dtmmr.MMRIndex(leafIndex),
	)
	if err != nil {
		return nil, fmt.Errorf("create bagged inclusion proof: %w", err)
	}
	return cloneProof(proof), nil
}

func (t *Tree) ConsistencyProof(oldSize uint64) (ConsistencyProof, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	newSize := uint64(len(t.nodes))
	if oldSize == 0 || oldSize > newSize || !validMMRSize(oldSize) || !validMMRSize(newSize) {
		return ConsistencyProof{}, fmt.Errorf("invalid MMR consistency sizes")
	}
	proof, err := dtmmr.IndexConsistencyProofBagged(oldSize, newSize, (*nodeStore)(t), sha256.New())
	if err != nil {
		return ConsistencyProof{}, fmt.Errorf("create bagged consistency proof: %w", err)
	}
	peaks, err := dtmmr.PeakHashes((*nodeStore)(t), oldSize-1)
	if err != nil {
		return ConsistencyProof{}, fmt.Errorf("read old MMR peaks: %w", err)
	}
	return ConsistencyProof{OldSize: oldSize, NewSize: newSize, OldPeaks: cloneProof(peaks), Path: cloneProof(proof.PathBagged)}, nil
}

// VerifyConsistency validates a bagged append-only consistency proof.
func VerifyConsistency(oldRoot, newRoot []byte, proof ConsistencyProof) bool {
	if len(oldRoot) != hashSize || len(newRoot) != hashSize || proof.OldSize == 0 || proof.OldSize > proof.NewSize || !validMMRSize(proof.OldSize) || !validMMRSize(proof.NewSize) || len(dtmmr.PosPeaks(proof.OldSize)) != len(proof.OldPeaks) {
		return false
	}
	for _, collection := range [][][]byte{proof.OldPeaks, proof.Path} {
		for _, node := range collection {
			if len(node) != hashSize {
				return false
			}
		}
	}
	return dtmmr.VerifyConsistencyBagged(sha256.New(), proof.OldPeaks, dtmmr.ConsistencyProof{MMRSizeA: proof.OldSize, MMRSizeB: proof.NewSize, PathBagged: proof.Path}, oldRoot, newRoot)
}

// VerifyInclusion validates a Capsule ID leaf against a bagged MMR root.
func VerifyInclusion(root []byte, mmrSize, leafIndex uint64, capsuleID string, proof [][]byte) bool {
	if len(root) != hashSize || !validMMRSize(mmrSize) || leafIndex >= dtmmr.LeafCount(mmrSize) {
		return false
	}
	id, err := hex.DecodeString(capsuleID)
	if err != nil || len(id) != hashSize || hex.EncodeToString(id) != capsuleID {
		return false
	}
	for _, node := range proof {
		if len(node) != hashSize {
			return false
		}
	}
	leaf := sha256.Sum256(append([]byte{0}, id...))
	return dtmmr.VerifyInclusionBagged(
		mmrSize, sha256.New(), leaf[:], dtmmr.MMRIndex(leafIndex), proof, root,
	)
}

func validMMRSize(size uint64) bool {
	if size == 0 {
		return true
	}
	// Leave enough arithmetic headroom for FirstMMRSize's peak completion.
	if size > (1<<63)-1 {
		return false
	}
	return dtmmr.FirstMMRSize(size-1) == size
}

// LeafCount returns the committed leaf count for a complete MMR size.
func LeafCount(size uint64) (uint64, bool) {
	if !validMMRSize(size) {
		return 0, false
	}
	return dtmmr.LeafCount(size), true
}

type nodeStore Tree

func (s *nodeStore) Get(index uint64) ([]byte, error) {
	if index >= uint64(len(s.nodes)) {
		return nil, dtmmr.ErrNotFound
	}
	return s.nodes[index], nil
}

func (s *nodeStore) Append(value []byte) (uint64, error) {
	s.nodes = append(s.nodes, append([]byte(nil), value...))
	return uint64(len(s.nodes)), nil
}

func cloneProof(input [][]byte) [][]byte {
	result := make([][]byte, len(input))
	for index, value := range input {
		result[index] = append([]byte(nil), value...)
	}
	return result
}
