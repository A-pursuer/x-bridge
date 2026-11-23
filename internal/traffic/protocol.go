package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

type Entry struct {
	SubscriptionID int64 `json:"subscription_id"`
	Up             int64 `json:"u"`
	Down           int64 `json:"d"`
}
type Batch struct {
	SourceID string  `json:"source_id"`
	Seq      int64   `json:"seq"`
	BatchID  string  `json:"batch_id"`
	Entries  []Entry `json:"entries"`
}
type Receipt struct {
	State       string `json:"state"`
	SourceID    string `json:"source_id"`
	Seq         int64  `json:"seq"`
	BatchID     string `json:"batch_id"`
	PayloadHash string `json:"payload_hash"`
	ProcessedAt int64  `json:"processed_at"`
	RawUp       int64  `json:"raw_u"`
	RawDown     int64  `json:"raw_d"`
}

func (b Batch) Hash(node int) string {
	entries := append([]Entry(nil), b.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].SubscriptionID < entries[j].SubscriptionID })
	s := fmt.Sprintf("%s\n%d\n%d\n%s\n", b.SourceID, node, b.Seq, b.BatchID)
	for _, e := range entries {
		s += fmt.Sprintf("%d:%d:%d\n", e.SubscriptionID, e.Up, e.Down)
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func (r Receipt) Validate(b Batch, node int) error {
	var u, d int64
	for _, e := range b.Entries {
		u += e.Up
		d += e.Down
	}
	if r.State != "processed" || r.SourceID != b.SourceID || r.Seq != b.Seq || r.BatchID != b.BatchID || r.PayloadHash != b.Hash(node) || r.ProcessedAt <= 0 || r.RawUp != u || r.RawDown != d {
		return fmt.Errorf("invalid database receipt for batch %s", b.BatchID)
	}
	return nil
}
