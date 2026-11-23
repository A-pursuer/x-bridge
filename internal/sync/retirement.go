package sync

import (
	"context"
	"fmt"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/store"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/xui"
	"time"
)

// retireClients serializes disable -> stable observation -> accepted report -> deletion.
// A persisted sending state is never blindly retransmitted after a crash or timeout.
func (w *bridgeWorker) retireClients(ctx context.Context, remove map[string]struct{}, existing map[string]xui.ClientSettings) error {
	active, err := w.store.ListRetirements(ctx, w.cfg.Name)
	if err != nil {
		return err
	}
	byEmail := map[string]store.Retirement{}
	for _, r := range active {
		byEmail[r.Email] = r
	}
	var first error
	for email := range remove {
		r, ok := byEmail[email]
		if !ok {
			bl, e := w.store.GetBaseline(ctx, w.cfg.Name, email)
			if e != nil {
				return fmt.Errorf("retirement baseline unavailable: %w", e)
			}
			r = store.Retirement{Bridge: w.cfg.Name, Email: email, UID: bl.XboardUserID, State: "disabling", UpdatedAt: time.Now().Unix()}
			if err = w.store.SaveRetirement(ctx, r); err != nil {
				return err
			}
		}
		client, exists := existing[email]
		switch r.State {
		case "disabling":
			if !exists {
				r.State = "unknown"
				r.Detail = "client disappeared before final observation"
				break
			}
			client.Enable = false
			if err = w.xuiC.UpdateClient(ctx, w.cfg.XuiInboundID, client); err != nil {
				return err
			}
			r.State = "disabled"
			r.UpdatedAt = time.Now().Unix()
		case "disabled":
			if !exists {
				r.State = "unknown"
				r.Detail = "client disappeared before final observation"
				break
			}
			if client.Enable {
				client.Enable = false
				if err = w.xuiC.UpdateClient(ctx, w.cfg.XuiInboundID, client); err != nil {
					return err
				}
				r.UpdatedAt = time.Now().Unix()
				break
			}
			if time.Now().Unix()-r.UpdatedAt < 15 {
				delete(remove, email)
				continue
			}
			stats, e := w.xuiC.GetClientTrafficsByInboundID(ctx, w.cfg.XuiInboundID)
			if e != nil {
				return e
			}
			found := false
			for _, t := range stats {
				if t.Email != email {
					continue
				}
				found = true
				bl, e := w.store.ObserveTraffic(ctx, w.cfg.Name, email, t.Up, t.Down)
				if e != nil {
					return e
				}
				r.CurUp = t.Up
				r.CurDown = t.Down
				r.Up = bl.ObservedUp - bl.AckedUp
				r.Down = bl.ObservedDown - bl.AckedDown
				if r.Up < 0 || r.Down < 0 || bl.UID != r.UID {
					return fmt.Errorf("invalid retirement baseline")
				}
			}
			if !found {
				r.State = "unknown"
				r.Detail = "final counters missing"
				break
			}
			r.State = "captured"
		case "captured", "batch_pending":
			// Persist the new-protocol state before any network call. Legacy sending/unknown
			// states below are deliberately excluded from automatic migration/replay.
			r.State = "batch_pending"
			if err = w.store.SaveRetirement(ctx, r); err != nil {
				return err
			}
			if err = w.flushTraffic(ctx, email); err != nil {
				return err
			}
			r.State = "processed"
			r.Detail = "XBoard transaction committed; final bytes settled"
		case "processed", "accepted":
			// accepted is an old, already-acknowledged queue batch: never send it again.
			if exists {
				if err = w.xuiC.DelClientByEmail(ctx, email); err != nil {
					return err
				}
			}
			r.State = "completed"
			r.UpdatedAt = time.Now().Unix()
		case "sending":
			r.State = "unknown"
			r.Detail = "legacy report interrupted; manual reconciliation required"
		case "unknown":
			if first == nil {
				first = fmt.Errorf("retirement requires manual review (UID %d)", r.UID)
			}
		default:
			return fmt.Errorf("invalid retirement state %q", r.State)
		}
		if err = w.store.SaveRetirement(ctx, r); err != nil {
			return err
		}
		if r.State != "completed" {
			delete(remove, email)
		}
	}
	return first
}
