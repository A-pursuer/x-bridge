package sync

import (
	"context"
	"errors"
	"fmt"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/protocol"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/store"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xboard"
)

func (w *bridgeWorker) syncTraffic(ctx context.Context) error {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	// Keep observing while an older batch waits for its ACK, including counter resets.
	stats, err := w.xuiC.GetClientTrafficsByInboundID(ctx, w.cfg.XuiInboundID)
	if err != nil {
		return err
	}
	for _, t := range stats {
		_, node, parseErr := protocol.ParseEmail(t.Email)
		if parseErr != nil || node != w.cfg.XboardNodeID {
			continue
		}
		if _, e := w.store.GetBaseline(ctx, w.cfg.Name, t.Email); errors.Is(e, store.ErrNotFound) {
			continue
		} else if e != nil {
			return e
		}
		if _, e := w.store.ObserveTraffic(ctx, w.cfg.Name, t.Email, t.Up, t.Down); e != nil {
			return e
		}
	}
	return w.flushTraffic(ctx, "")
}

// flushTraffic is called under lifecycleMu by both periodic and retirement workers.
// Every retry queries/replays the same immutable batch. Legacy /push is never a fallback.
func (w *bridgeWorker) flushTraffic(ctx context.Context, email string) error {
	if err := w.xboardC.TrafficCapabilities(ctx, w.cfg.XboardNodeID, w.cfg.XboardNodeType); err != nil {
		return err
	}
	// Bound per-cycle work while allowing more than 1000 users to drain in chunks.
	for n := 0; n < 100; n++ {
		b, err := w.store.PendingTrafficBatch(ctx, w.cfg.Name)
		if errors.Is(err, store.ErrNotFound) {
			b, err = w.store.PrepareTrafficBatch(ctx, w.cfg.Name, w.cfg.XboardNodeID, email)
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if b.Node != w.cfg.XboardNodeID {
			return fmt.Errorf("pending batch node mismatch; reconciliation required")
		}
		if b.State == "conflict" {
			return fmt.Errorf("batch %s conflict: %s", b.Batch.BatchID, b.Detail)
		}
		receipt, err := w.xboardC.TrafficBatch(ctx, b.Node, w.cfg.XboardNodeType, b.Batch, true)
		if err == nil && receipt.State == "missing" {
			if err = w.store.SetTrafficBatchState(ctx, b.Batch.BatchID, "sending", ""); err != nil {
				return err
			}
			receipt, err = w.xboardC.TrafficBatch(ctx, b.Node, w.cfg.XboardNodeType, b.Batch, false)
		}
		if err != nil {
			state := "retry"
			var apiErr *xboard.Error
			if errors.As(err, &apiErr) && (apiErr.HTTPStatus == 409 || apiErr.HTTPStatus == 422) {
				state = "conflict"
			}
			// Do not persist error bodies or URLs: they may contain credentials.
			detail := "receipt unavailable; retry same batch"
			if state == "conflict" {
				detail = "server rejected sequence/content/identity; manual reconciliation required"
			}
			if e := w.store.SetTrafficBatchState(ctx, b.Batch.BatchID, state, detail); e != nil {
				return e
			}
			return fmt.Errorf("traffic batch %s (%s): %w", b.Batch.BatchID, state, err)
		}
		if err = w.store.AckTrafficBatch(ctx, b, receipt); err != nil {
			return err
		}
		w.log.Info("traffic batch database committed", "batch_id", b.Batch.BatchID, "seq", b.Batch.Seq, "entries", len(b.Batch.Entries))
	}
	return fmt.Errorf("traffic backlog exceeds per-cycle bound; next cycle will continue")
}
