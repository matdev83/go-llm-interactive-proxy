package openairesponsestream

import "encoding/json"

func CallIDFromRawJSON(rawJSON string) string {
	if rawJSON == "" {
		return ""
	}
	var probe struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &probe); err != nil {
		return ""
	}
	return probe.CallID
}

func ToolCallIDFromRaw(itemID, rawJSON string) string {
	if itemID != "" {
		return itemID
	}
	return CallIDFromRawJSON(rawJSON)
}
