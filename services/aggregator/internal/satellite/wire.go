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

type detectionChanges struct {
	FRP        *float64
	Confidence *string
}

// The course feed puts new detections under "detection" and patches under "changes".
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
		Detection    *wireDetection  `json:"detection"`
		DetectionKey string          `json:"detection_key"`
		Key          string          `json:"key"`
		PreviousKey  string          `json:"previous_key"`
		Offset       *int64          `json:"offset"`
		Changes      json.RawMessage `json:"changes"`
	}
	if err := json.Unmarshal(frame.Data, &wire); err != nil {
		return fail("invalid detection JSON")
	}
	if wire.Offset != nil && *wire.Offset != frame.Offset {
		return fail("payload offset does not match SSE id")
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
	if frame.Kind == "update" && len(wire.Changes) != 0 {
		if !validDetectionKey(key) {
			return fail("invalid update key")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(wire.Changes, &fields) != nil || fields == nil {
			return fail("invalid update changes")
		}
		changes := &detectionChanges{}
		for _, field := range []string{"satellite", "acq_date", "acq_time", "latitude", "longitude"} {
			if _, found := fields[field]; found {
				return fail("patch changes detection identity")
			}
		}
		if raw, found := fields["confidence"]; found {
			var value string
			if json.Unmarshal(raw, &value) != nil || value == "" {
				return fail("invalid update confidence")
			}
			changes.Confidence = &value
		}
		if raw, found := fields["frp"]; found {
			value, err := strconv.ParseFloat(scalarText(raw), 64)
			if err != nil {
				return fail("invalid update FRP")
			}
			changes.FRP = &value
		}
		mutation.Key, mutation.Changes = key, changes
		return mutation, nil
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
