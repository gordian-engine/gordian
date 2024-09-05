package ggrpc

import (
	coreapp "cosmossdk.io/core/app"
	"cosmossdk.io/core/event"
)

func Pointy[T any](x T) *T {
	return &x
}

// convertGordianResponseFromSDKResult converts an app manager TxResult to the gRPC proto result.
func convertGordianResponseFromSDKResult(res coreapp.TxResult) *TxResultResponse {
	resp := &TxResultResponse{
		Code:      res.Code,
		Events:    convertEvent(res.Events),
		Data:      res.Data,
		Log:       res.Log,
		Info:      res.Info,
		GasWanted: res.GasWanted,
		GasUsed:   res.GasUsed,
		Codespace: res.Codespace,
		TxHash:    "", // set with tx.Hash() after conversion
	}
	if res.Error != nil {
		resp.Error = res.Error.Error()
	}
	return resp
}

// convertEvent converts from the cosmos-sdk core event type to the gRPC proto event.
func convertEvent(e []event.Event) []*Event {
	events := make([]*Event, len(e))
	for i, ev := range e {
		attrs := make([]*EventAttribute, len(ev.Attributes))
		for j, a := range ev.Attributes {
			attrs[j] = &EventAttribute{
				Key:   a.Key,
				Value: a.Value,
			}
		}

		events[i] = &Event{
			Type:       ev.Type,
			Attributes: attrs,
		}
	}
	return events
}
