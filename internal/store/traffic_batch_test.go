package store

import (
	"context"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/traffic"
	"path/filepath"
	"testing"
)

func TestOutboxRecoveryResetAndAtomicAck(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bridge.db")
	s, e := OpenSQLite(path)
	if e != nil {
		t.Fatal(e)
	}
	s.EnsureBaseline(ctx, "b", "e", 17)
	p, e := s.ObserveTraffic(ctx, "b", "e", 100, 200)
	if e != nil || p.ObservedUp != 100 {
		t.Fatal(p, e)
	}
	b, e := s.PrepareTrafficBatch(ctx, "b", 1, "")
	if e != nil {
		t.Fatal(e)
	}
	// Server committed, but no local ACK; new bytes and a reset arrive in the meantime.
	s.ObserveTraffic(ctx, "b", "e", 150, 260)
	p, e = s.ObserveTraffic(ctx, "b", "e", 10, 20)
	if e != nil || p.ObservedUp != 160 || p.ObservedDown != 280 {
		t.Fatal(p, e)
	}
	s.Close()
	s, e = OpenSQLite(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	same, e := s.PendingTrafficBatch(ctx, "b")
	if e != nil || same.Batch.BatchID != b.Batch.BatchID || same.Batch.Entries[0].Up != 100 {
		t.Fatal(same, e)
	}
	r := traffic.Receipt{State: "processed", SourceID: b.Batch.SourceID, Seq: b.Batch.Seq, BatchID: b.Batch.BatchID, PayloadHash: b.Batch.Hash(1), ProcessedAt: 1, RawUp: 100, RawDown: 200}
	wrong := r
	wrong.PayloadHash = "wrong"
	if s.AckTrafficBatch(ctx, b, wrong) == nil {
		t.Fatal("accepted wrong receipt")
	}
	if e = s.AckTrafficBatch(ctx, b, r); e != nil {
		t.Fatal(e)
	}
	if e = s.AckTrafficBatch(ctx, b, r); e != nil {
		t.Fatal(e)
	}
	next, e := s.PrepareTrafficBatch(ctx, "b", 1, "")
	if e != nil || next.Batch.Seq != 2 || next.Batch.Entries[0].Up != 60 || next.Batch.Entries[0].Down != 80 {
		t.Fatal(next, e)
	}
}
func TestLegacyOutstandingMigratesWithoutHistoricalReplay(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenSQLite(filepath.Join(t.TempDir(), "b.db"))
	defer s.Close()
	s.EnsureBaseline(ctx, "b", "e", 1)
	s.RecordSeen(ctx, "b", "e", 100, 200)
	s.ApplyReportSuccess(ctx, "b", "e", 80, 180)
	p, e := s.ObserveTraffic(ctx, "b", "e", 110, 210)
	if e != nil || p.ObservedUp != 30 || p.ObservedDown != 30 {
		t.Fatal(p, e)
	}
	b, e := s.PrepareTrafficBatch(ctx, "b", 1, "")
	if e != nil || b.Batch.Entries[0].Up != 30 {
		t.Fatal(b, e)
	}
}
func TestAckFailureDoesNotPartiallyAdvancePositions(t *testing.T) {
	ctx := context.Background()
	s, _ := OpenSQLite(filepath.Join(t.TempDir(), "b.db"))
	defer s.Close()
	for i, email := range []string{"a", "b"} {
		s.EnsureBaseline(ctx, "b", email, int64(i+1))
		s.ObserveTraffic(ctx, "b", email, 10, 20)
	}
	b, e := s.PrepareTrafficBatch(ctx, "b", 1, "")
	if e != nil {
		t.Fatal(e)
	}
	raw := s.(*sqliteStore)
	raw.db.Exec(`UPDATE traffic_positions SET uid=999 WHERE email='b'`)
	r := traffic.Receipt{State: "processed", SourceID: b.Batch.SourceID, Seq: 1, BatchID: b.Batch.BatchID, PayloadHash: b.Batch.Hash(1), ProcessedAt: 1, RawUp: 20, RawDown: 40}
	if s.AckTrafficBatch(ctx, b, r) == nil {
		t.Fatal("expected conflict")
	}
	var ack int
	raw.db.QueryRow(`SELECT acked_up FROM traffic_positions WHERE email='a'`).Scan(&ack)
	if ack != 0 {
		t.Fatal("partial ACK", ack)
	}
	pending, e := s.PendingTrafficBatch(ctx, "b")
	if e != nil || pending.State == "processed" {
		t.Fatal(pending, e)
	}
}
