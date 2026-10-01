package dws

import (
	"context"
	"encoding/json"
	"errors"
)

// CardService posts and updates streaming AI cards.
type CardService struct{ c *Client }

// FlowStatus is a streaming card state.
type FlowStatus string

const (
	FlowProcessing FlowStatus = "1"
	FlowInputting  FlowStatus = "2"
	FlowFinished   FlowStatus = "3"
	FlowExecuting  FlowStatus = "4"
	FlowFailed     FlowStatus = "5"
)

// Card is a streaming AI card; BizID addresses later updates.
type Card struct {
	BizID  string `json:"bizId"`
	TaskID string `json:"taskId,omitempty"`
}

// Create posts an empty streaming card to target.
func (s *CardService) Create(ctx context.Context, target Target) (Card, error) {
	args := map[string]any{}
	if err := target.apply(args); err != nil {
		return Card{}, err
	}
	raw, err := s.c.Call(ctx, ServerIM, "create_and_send_card", args)
	if err != nil {
		return Card{}, err
	}
	var w struct {
		BizID      string `json:"bizId"`
		OpenTaskID string `json:"openTaskId"`
	}
	_ = json.Unmarshal(raw, &w)
	if w.BizID == "" {
		return Card{}, errors.New("dws: create_and_send_card returned no bizId")
	}
	return Card{BizID: w.BizID, TaskID: w.OpenTaskID}, nil
}

// Update replaces the card text. It does not append: send the whole text
// every time. FlowFinished or FlowFailed closes the card.
func (s *CardService) Update(ctx context.Context, bizID, text string, status FlowStatus) error {
	if bizID == "" {
		return invalid("card update needs bizId")
	}
	_, err := s.c.Call(ctx, ServerIM, "update_streaming_card", map[string]any{
		"bizId": bizID, "msgContent": text, "flowStatus": string(status),
	})
	return err
}
