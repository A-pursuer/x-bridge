package xboard

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/xboard-bridge/xboard-xui-bridge/internal/traffic"
	"net/http"
	"strconv"
)

type TrafficEntry = traffic.Entry
type TrafficBatch = traffic.Batch
type TrafficReceipt = traffic.Receipt

func (c *Client) TrafficCapabilities(ctx context.Context, node int, typ string) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/server/traffic/capabilities", c.baseQuery(node, typ), nil)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if err = assertHTTPOK(resp, "traffic/capabilities"); err != nil {
		return err
	}
	var v struct {
		Data struct {
			Protocol string `json:"protocol"`
			Ack      string `json:"ack"`
			Max      int    `json:"max_entries"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return err
	}
	if v.Data.Protocol != "traffic-batch-v1" || v.Data.Ack != "database-committed" || v.Data.Max < 1000 {
		return fmt.Errorf("XBoard does not support committed traffic-batch-v1; legacy fallback forbidden")
	}
	return nil
}
func (c *Client) TrafficBatch(ctx context.Context, node int, typ string, b TrafficBatch, lookup bool) (TrafficReceipt, error) {
	q := c.baseQuery(node, typ)
	method := http.MethodPost
	var body any = b
	if lookup {
		method = http.MethodGet
		body = nil
		q.Set("source_id", b.SourceID)
		q.Set("seq", strconv.FormatInt(b.Seq, 10))
		q.Set("batch_id", b.BatchID)
	}
	resp, err := c.do(ctx, method, "/api/v2/server/traffic/batch", q, body)
	if err != nil {
		return TrafficReceipt{}, err
	}
	defer drainAndClose(resp.Body)
	if err = assertHTTPOK(resp, "traffic/batch"); err != nil {
		return TrafficReceipt{}, err
	}
	var v struct {
		Data TrafficReceipt `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return TrafficReceipt{}, err
	}
	if lookup && v.Data.State == "missing" && v.Data.SourceID == b.SourceID && v.Data.Seq == b.Seq && v.Data.BatchID == b.BatchID {
		return v.Data, nil
	}
	if err = v.Data.Validate(b, node); err != nil {
		return TrafficReceipt{}, err
	}
	return v.Data, nil
}
