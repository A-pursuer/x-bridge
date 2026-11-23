package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/traffic"
	"time"
)

// Observation positions never go backwards, even across panel counter resets.
type TrafficPosition struct {
	Email                                             string
	UID, ObservedUp, ObservedDown, AckedUp, AckedDown int64
}
type BatchSnapshot struct {
	Email string `json:"email"`
	UID   int64  `json:"uid"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
}
type OutboxBatch struct {
	Bridge    string          `json:"-"`
	Node      int             `json:"node_id"`
	Batch     traffic.Batch   `json:"batch"`
	Snapshots []BatchSnapshot `json:"-"`
	State     string          `json:"state"`
	Detail    string          `json:"detail"`
	UpdatedAt int64           `json:"updated_at"`
}

func (s *sqliteStore) ObserveTraffic(ctx context.Context, bridge, email string, up, down int64) (TrafficPosition, error) {
	if up < 0 || down < 0 {
		return TrafficPosition{}, fmt.Errorf("negative traffic observation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TrafficPosition{}, err
	}
	defer tx.Rollback()
	var uid, lastU, lastD, repU, repD, pendU, pendD int64
	err = tx.QueryRowContext(ctx, `SELECT xboard_user_id,last_seen_up,last_seen_down,reported_up,reported_down,pending_up,pending_down FROM traffic_baseline WHERE bridge_name=? AND email=?`, bridge, email).Scan(&uid, &lastU, &lastD, &repU, &repD, &pendU, &pendD)
	if err != nil {
		return TrafficPosition{}, err
	}
	if uid <= 0 || lastU-repU+pendU < 0 || lastD-repD+pendD < 0 {
		return TrafficPosition{}, fmt.Errorf("invalid legacy baseline")
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO traffic_positions(bridge_name,email,uid,observed_up,observed_down,acked_up,acked_down) VALUES(?,?,?,?,?,0,0)`, bridge, email, uid, lastU-repU+pendU, lastD-repD+pendD)
	if err != nil {
		return TrafficPosition{}, err
	}
	p := TrafficPosition{Email: email, UID: uid}
	var storedUID int64
	err = tx.QueryRowContext(ctx, `SELECT uid,observed_up,observed_down,acked_up,acked_down FROM traffic_positions WHERE bridge_name=? AND email=?`, bridge, email).Scan(&storedUID, &p.ObservedUp, &p.ObservedDown, &p.AckedUp, &p.AckedDown)
	if err != nil {
		return p, err
	}
	if storedUID != uid {
		return p, fmt.Errorf("traffic identity conflict; reconcile source")
	}
	du, dd := up-lastU, down-lastD
	if du < 0 {
		du = up
	}
	if dd < 0 {
		dd = down
	}
	const max = int64(9007199254740991)
	if p.ObservedUp > max-du || p.ObservedDown > max-dd {
		return p, fmt.Errorf("traffic observation overflow")
	}
	p.ObservedUp += du
	p.ObservedDown += dd
	_, err = tx.ExecContext(ctx, `UPDATE traffic_positions SET observed_up=?,observed_down=? WHERE bridge_name=? AND email=?`, p.ObservedUp, p.ObservedDown, bridge, email)
	if err != nil {
		return p, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE traffic_baseline SET reported_up=?,reported_down=?,last_seen_up=?,last_seen_down=?,pending_up=?,pending_down=?,updated_at=? WHERE bridge_name=? AND email=?`, up, down, up, down, p.ObservedUp-p.AckedUp, p.ObservedDown-p.AckedDown, time.Now().Unix(), bridge, email)
	if err != nil {
		return p, err
	}
	return p, tx.Commit()
}
func (s *sqliteStore) PendingTrafficBatch(ctx context.Context, bridge string) (OutboxBatch, error) {
	return scanOutbox(s.db.QueryRowContext(ctx, `SELECT bridge_name,node_id,payload,snapshots,state,detail,updated_at FROM traffic_outbox WHERE bridge_name=? AND state!='processed' ORDER BY seq LIMIT 1`, bridge))
}
func scanOutbox(row *sql.Row) (OutboxBatch, error) {
	var b OutboxBatch
	var payload, snap string
	err := row.Scan(&b.Bridge, &b.Node, &payload, &snap, &b.State, &b.Detail, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal([]byte(payload), &b.Batch); err != nil {
		return b, err
	}
	err = json.Unmarshal([]byte(snap), &b.Snapshots)
	return b, err
}

// PrepareTrafficBatch freezes a maximum of 1000 positions and reserves a sequence atomically.
// onlyEmail is used by final retirement and never includes another client's counters.
func (s *sqliteStore) PrepareTrafficBatch(ctx context.Context, bridge string, node int, onlyEmail string) (OutboxBatch, error) {
	if b, e := s.PendingTrafficBatch(ctx, bridge); !errors.Is(e, ErrNotFound) {
		return b, e
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboxBatch{}, err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_outbox WHERE bridge_name=? AND state!='processed'`, bridge).Scan(&n); err != nil {
		return OutboxBatch{}, err
	}
	if n != 0 {
		return OutboxBatch{}, fmt.Errorf("outbox changed; retry")
	}
	// Bind source to physical mapping; changing a live mapping requires explicit reconciliation.
	var panel string
	var inbound int
	err = tx.QueryRowContext(ctx, `SELECT xui_panel,xui_inbound_id FROM bridges WHERE name=?`, bridge).Scan(&panel, &inbound)
	if errors.Is(err, sql.ErrNoRows) {
		panel = ""
		inbound = 0
	} else if err != nil {
		return OutboxBatch{}, err
	}
	fingerprint := fmt.Sprintf("%d:%s:%d", node, panel, inbound)
	buf := make([]byte, 16)
	if _, err = rand.Read(buf); err != nil {
		return OutboxBatch{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO traffic_sources(bridge_name,source_id,last_seq,mapping) VALUES(?,?,0,?)`, bridge, hex.EncodeToString(buf), fingerprint)
	if err != nil {
		return OutboxBatch{}, err
	}
	var source, mapping string
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT source_id,last_seq,mapping FROM traffic_sources WHERE bridge_name=?`, bridge).Scan(&source, &seq, &mapping)
	if err != nil {
		return OutboxBatch{}, err
	}
	if mapping != fingerprint {
		return OutboxBatch{}, fmt.Errorf("source mapping conflict; reconcile before editing node/panel/inbound")
	}
	q := `SELECT p.email,p.uid,p.observed_up,p.observed_down,p.acked_up,p.acked_down FROM traffic_positions p JOIN traffic_baseline b ON b.bridge_name=p.bridge_name AND b.email=p.email WHERE p.bridge_name=? AND (p.observed_up>p.acked_up OR p.observed_down>p.acked_down)`
	args := []any{bridge}
	if onlyEmail != "" {
		q += ` AND p.email=?`
		args = append(args, onlyEmail)
	} else {
		q += ` AND NOT EXISTS(SELECT 1 FROM traffic_retirements r WHERE r.bridge_name=p.bridge_name AND r.email=p.email AND r.state!='completed')`
	}
	q += ` ORDER BY p.email LIMIT 1000`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return OutboxBatch{}, err
	}
	b := OutboxBatch{Bridge: bridge, Node: node, State: "pending", UpdatedAt: time.Now().Unix(), Batch: traffic.Batch{SourceID: source, Seq: seq + 1, BatchID: fmt.Sprintf("%s-%d", source, seq+1)}}
	seen := map[int64]bool{}
	var total int64
	for rows.Next() {
		var p TrafficPosition
		if err = rows.Scan(&p.Email, &p.UID, &p.ObservedUp, &p.ObservedDown, &p.AckedUp, &p.AckedDown); err != nil {
			rows.Close()
			return b, err
		}
		if seen[p.UID] {
			rows.Close()
			return b, fmt.Errorf("duplicate billing UID in source")
		}
		seen[p.UID] = true
		u, d := p.ObservedUp-p.AckedUp, p.ObservedDown-p.AckedDown
		if u < 0 || d < 0 || u > 9007199254740991-total-d {
			rows.Close()
			return b, fmt.Errorf("invalid batch total")
		}
		total += u + d
		b.Batch.Entries = append(b.Batch.Entries, traffic.Entry{SubscriptionID: p.UID, Up: u, Down: d})
		b.Snapshots = append(b.Snapshots, BatchSnapshot{Email: p.Email, UID: p.UID, Up: p.ObservedUp, Down: p.ObservedDown})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return b, err
	}
	if len(b.Batch.Entries) == 0 {
		return b, ErrNotFound
	}
	payload, _ := json.Marshal(b.Batch)
	snap, _ := json.Marshal(b.Snapshots)
	_, err = tx.ExecContext(ctx, `INSERT INTO traffic_outbox(bridge_name,node_id,source_id,seq,batch_id,payload,snapshots,state,detail,updated_at) VALUES(?,?,?,?,?,?,?,'pending','',?)`, bridge, node, source, seq+1, b.Batch.BatchID, string(payload), string(snap), b.UpdatedAt)
	if err != nil {
		return b, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE traffic_sources SET last_seq=? WHERE bridge_name=?`, seq+1, bridge)
	if err != nil {
		return b, err
	}
	return b, tx.Commit()
}
func (s *sqliteStore) SetTrafficBatchState(ctx context.Context, id, state, detail string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE traffic_outbox SET state=?,detail=?,updated_at=? WHERE batch_id=? AND state!='processed'`, state, detail, time.Now().Unix(), id)
	return err
}
func (s *sqliteStore) AckTrafficBatch(ctx context.Context, b OutboxBatch, receipt traffic.Receipt) error {
	if err := receipt.Validate(b.Batch, b.Node); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored, err := scanOutbox(tx.QueryRowContext(ctx, `SELECT bridge_name,node_id,payload,snapshots,state,detail,updated_at FROM traffic_outbox WHERE batch_id=?`, b.Batch.BatchID))
	if err != nil {
		return err
	}
	if stored.State == "processed" {
		return nil
	}
	if stored.Node != b.Node || stored.Batch.Hash(b.Node) != receipt.PayloadHash {
		return fmt.Errorf("stored outbox conflict")
	}
	for _, p := range stored.Snapshots {
		res, e := tx.ExecContext(ctx, `UPDATE traffic_positions SET acked_up=?,acked_down=? WHERE bridge_name=? AND email=? AND uid=? AND acked_up<=? AND acked_down<=? AND observed_up>=? AND observed_down>=?`, p.Up, p.Down, b.Bridge, p.Email, p.UID, p.Up, p.Down, p.Up, p.Down)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return fmt.Errorf("outbox position conflict")
		}
		_, e = tx.ExecContext(ctx, `UPDATE traffic_baseline SET pending_up=(SELECT observed_up-acked_up FROM traffic_positions WHERE bridge_name=? AND email=?),pending_down=(SELECT observed_down-acked_down FROM traffic_positions WHERE bridge_name=? AND email=?),last_reported_at=? WHERE bridge_name=? AND email=?`, b.Bridge, p.Email, b.Bridge, p.Email, time.Now().Unix(), b.Bridge, p.Email)
		if e != nil {
			return e
		}
	}
	data, _ := json.Marshal(receipt)
	_, err = tx.ExecContext(ctx, `UPDATE traffic_outbox SET state='processed',detail='',receipt=?,updated_at=? WHERE batch_id=?`, string(data), time.Now().Unix(), b.Batch.BatchID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *sqliteStore) TrafficBatchStatus(ctx context.Context, bridge string) ([]OutboxBatch, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT batch_id FROM traffic_outbox WHERE bridge_name=? ORDER BY seq DESC LIMIT 10`, bridge)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []OutboxBatch{}
	for _, id := range ids {
		b, e := scanOutbox(s.db.QueryRowContext(ctx, `SELECT bridge_name,node_id,payload,snapshots,state,detail,updated_at FROM traffic_outbox WHERE batch_id=?`, id))
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, nil
}
