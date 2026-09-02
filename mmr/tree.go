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
	Witness  [][][]byte
	NewPeaks [][]byte
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
	return t.Append(id)
}

// Append commits an application-neutral 32-byte record identity as the next
// CLL leaf. Fixed-width identities preserve leaf/interior domain separation.
func (t *Tree) Append(value []byte) (uint64, error) {
	if len(value) != hashSize {
		return 0, fmt.Errorf("CLL leaf value must be exactly 32 bytes")
	}
	leafInput := append([]byte{0}, value...)
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

// PeakHashesAt returns the ordered accumulator peaks at a complete historical
// MMR size. The order is tallest-to-smallest, matching the MMR commitment
// object carried by the CLL checkpoint COSE wire profile.
func (t *Tree) PeakHashesAt(size uint64) ([][]byte, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if size == 0 {
		return [][]byte{}, nil
	}
	if size > uint64(len(t.nodes)) || !validMMRSize(size) {
		return nil, fmt.Errorf("invalid historical MMR size %d", size)
	}
	peaks, err := dtmmr.PeakHashes((*nodeStore)(t), size-1)
	if err != nil {
		return nil, fmt.Errorf("read MMR peaks at size %d: %w", size, err)
	}
	return cloneProof(peaks), nil
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
	oldPositions := dtmmr.Peaks(oldSize - 1)
	newPositions := dtmmr.Peaks(newSize - 1)
	oldPeaks, err := dtmmr.PeakHashes((*nodeStore)(t), oldSize-1)
	if err != nil {
		return ConsistencyProof{}, fmt.Errorf("read old MMR peaks: %w", err)
	}
	newPeaks, err := dtmmr.PeakHashes((*nodeStore)(t), newSize-1)
	if err != nil {
		return ConsistencyProof{}, fmt.Errorf("read new MMR peaks: %w", err)
	}
	witness := make([][][]byte, len(oldPositions))
	for index, oldPeak := range oldPositions {
		containing := containingPeak(oldPeak, newPositions)
		if containing < 0 {
			return ConsistencyProof{}, fmt.Errorf("old peak %d is not contained in new MMR", oldPeak)
		}
		steps, ok := pathToPeak(newPositions[containing], dtmmr.IndexHeight(newPositions[containing]), oldPeak)
		if !ok {
			return ConsistencyProof{}, fmt.Errorf("old peak %d has no path to new peak", oldPeak)
		}
		witness[index] = make([][]byte, len(steps))
		for stepIndex, step := range steps {
			witness[index][stepIndex] = append([]byte(nil), t.nodes[step.sibling]...)
		}
	}
	return ConsistencyProof{OldSize: oldSize, NewSize: newSize, OldPeaks: cloneProof(oldPeaks), Witness: cloneWitness(witness), NewPeaks: cloneProof(newPeaks)}, nil
}

// VerifyConsistency validates the accumulator consistency proof carried by the
// capsule-emit checkpoint COSE profile.
func VerifyConsistency(oldRoot, newRoot []byte, proof ConsistencyProof) bool {
	if len(oldRoot) != hashSize || len(newRoot) != hashSize || proof.OldSize == 0 || proof.OldSize > proof.NewSize || !validMMRSize(proof.OldSize) || !validMMRSize(proof.NewSize) {
		return false
	}
	oldPositions := dtmmr.Peaks(proof.OldSize - 1)
	newPositions := dtmmr.Peaks(proof.NewSize - 1)
	if len(proof.OldPeaks) != len(oldPositions) || len(proof.NewPeaks) != len(newPositions) || len(proof.Witness) != len(oldPositions) {
		return false
	}
	for _, collection := range [][][]byte{proof.OldPeaks, proof.NewPeaks} {
		for _, node := range collection {
			if len(node) != hashSize {
				return false
			}
		}
	}
	if !bytes.Equal(RootFromPeaks(proof.OldPeaks), oldRoot) || !bytes.Equal(RootFromPeaks(proof.NewPeaks), newRoot) {
		return false
	}
	for index, oldPeak := range oldPositions {
		containing := containingPeak(oldPeak, newPositions)
		if containing < 0 {
			return false
		}
		steps, ok := pathToPeak(newPositions[containing], dtmmr.IndexHeight(newPositions[containing]), oldPeak)
		if !ok || len(steps) != len(proof.Witness[index]) {
			return false
		}
		accumulator := append([]byte(nil), proof.OldPeaks[index]...)
		for stepIndex, step := range steps {
			sibling := proof.Witness[index][stepIndex]
			if len(sibling) != hashSize {
				return false
			}
			if step.targetIsRight {
				accumulator = interiorHash(sibling, accumulator, step.parent)
			} else {
				accumulator = interiorHash(accumulator, sibling, step.parent)
			}
		}
		if !bytes.Equal(accumulator, proof.NewPeaks[containing]) {
			return false
		}
	}
	return true
}

// RootFromPeaks bags an ordered tallest-to-smallest accumulator into the
// scalar root used by the local CLL proof API.
func RootFromPeaks(peaks [][]byte) []byte {
	if len(peaks) == 0 {
		return make([]byte, hashSize)
	}
	return dtmmr.HashPeaksRHS(sha256.New(), cloneProof(peaks))
}

type pathStep struct {
	sibling       uint64
	targetIsRight bool
	parent        uint64
}

func containingPeak(position uint64, peaks []uint64) int {
	for index, peak := range peaks {
		height := dtmmr.IndexHeight(peak)
		mountainSize := (uint64(1) << (height + 1)) - 1
		start := peak - mountainSize + 1
		if position >= start && position <= peak {
			return index
		}
	}
	return -1
}

func pathToPeak(root, height, target uint64) ([]pathStep, bool) {
	steps := make([]pathStep, 0, height)
	currentRoot := root
	currentHeight := height
	for currentHeight > 0 && currentRoot != target {
		leftSize := (uint64(1) << currentHeight) - 1
		leftRoot := currentRoot - leftSize - 1
		rightRoot := currentRoot - 1
		if target <= leftRoot {
			steps = append(steps, pathStep{sibling: rightRoot, parent: currentRoot})
			currentRoot = leftRoot
		} else {
			steps = append(steps, pathStep{sibling: leftRoot, targetIsRight: true, parent: currentRoot})
			currentRoot = rightRoot
		}
		currentHeight--
	}
	if currentRoot != target {
		return nil, false
	}
	for left, right := 0, len(steps)-1; left < right; left, right = left+1, right-1 {
		steps[left], steps[right] = steps[right], steps[left]
	}
	return steps, true
}

func interiorHash(left, right []byte, position uint64) []byte {
	hasher := sha256.New()
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], position+1)
	hasher.Write(encoded[:])
	hasher.Write(left)
	hasher.Write(right)
	return hasher.Sum(nil)
}

func cloneWitness(input [][][]byte) [][][]byte {
	output := make([][][]byte, len(input))
	for index := range input {
		output[index] = cloneProof(input[index])
	}
	return output
}

// VerifyInclusion validates an AAC Capsule ID leaf against a bagged MMR root.
func VerifyInclusion(root []byte, mmrSize, leafIndex uint64, capsuleID string, proof [][]byte) bool {
	id, err := hex.DecodeString(capsuleID)
	if err != nil || len(id) != hashSize || hex.EncodeToString(id) != capsuleID {
		return false
	}
	return VerifyInclusionValue(root, mmrSize, leafIndex, id, proof)
}

// VerifyInclusionValue validates a generic 32-byte CLL record identity against
// a bagged MMR root.
func VerifyInclusionValue(root []byte, mmrSize, leafIndex uint64, value []byte, proof [][]byte) bool {
	if len(root) != hashSize || !validMMRSize(mmrSize) || leafIndex >= dtmmr.LeafCount(mmrSize) || len(value) != hashSize {
		return false
	}
	for _, node := range proof {
		if len(node) != hashSize {
			return false
		}
	}
	leaf := sha256.Sum256(append([]byte{0}, value...))
	return dtmmr.VerifyInclusionBagged(
		mmrSize,
		sha256.New(),
		leaf[:],
		dtmmr.MMRIndex(leafIndex),
		proof,
		root,
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
