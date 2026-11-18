package satellite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type wireDetection struct {
	Satellite  string          `json:"satellite"`
	AcqDate    string          `json:"acq_date"`
	AcqTime    json.RawMessage `json:"acq_time"`
	Latitude   json.RawMessage `json:"latitude"`
	Longitude  json.RawMessage `json:"longitude"`
	Confidence string          `json:"confidence"`
	FRP        json.RawMessage `json:"frp"`
}

// decodeFrame uses the assignment's VIIRS fields. The update/retract envelope
// conventions need checking against an authenticated sample from the assistants.
func decodeFrame(frame Frame) (Mutation, error) {
	mutation := Mutation{Offset: frame.Offset, Kind: frame.Kind}
	if frame.Kind == "heartbeat" {
		return mutation, nil
	}
	fail := func(reason string) (Mutation, error) {
		return Mutation{}, fmt.Errorf("%w: %s at offset %d", ErrProtocol, reason, frame.Offset)
	}
	var wire struct {
		wireDetection
		Detection    *wireDetection `json:"detection"`
		DetectionKey string         `json:"detection_key"`
		Key          string         `json:"key"`
		PreviousKey  string         `json:"previous_key"`
	}
	if err := json.Unmarshal(frame.Data, &wire); err != nil {
		return fail("invalid detection JSON")
	}
	detection := wire.wireDetection
	if wire.Detection != nil {
		detection = *wire.Detection
	}
	identity := Detection{
		Satellite: detection.Satellite, AcqDate: detection.AcqDate,
		AcqTime: scalarText(detection.AcqTime), Latitude: scalarText(detection.Latitude),
		Longitude: scalarText(detection.Longitude),
	}
	key := wire.DetectionKey
	if key == "" {
		key = wire.Key
	}
	switch frame.Kind {
	case "retract":
		if key == "" {
			identity.Confidence = "n"
			if _, err := parse(identity); err != nil {
				return fail("invalid retraction identity")
			}
			key = identity.Key()
		}
		mutation.Key = key
	case "detection", "update":
		identity.Confidence = detection.Confidence
		frp, err := strconv.ParseFloat(scalarText(detection.FRP), 64)
		if err != nil {
			return fail("invalid FRP")
		}
		identity.FRP = frp
		if _, err := parse(identity); err != nil {
			return fail(err.Error())
		}
		mutation.Detection = &identity
		if frame.Kind == "update" {
			mutation.Key = wire.PreviousKey
			if mutation.Key == "" {
				mutation.Key = key
			}
		}
	default:
		return fail("unsupported event kind")
	}
	if mutation.Key != "" && !validDetectionKey(mutation.Key) {
		return fail("invalid detection key")
	}
	return mutation, nil
}

func scalarText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
		return ""
	}
	return string(raw)
}

func validDetectionKey(key string) bool {
	parts := strings.Split(key, "|")
	if len(parts) != 5 {
		return false
	}
	_, err := parse(Detection{Satellite: parts[0], AcqDate: parts[1], AcqTime: parts[2], Latitude: parts[3], Longitude: parts[4], Confidence: "n"})
	return err == nil
}
