package npln

import (
	"encoding/json"
	"fmt"

	commonpb "github.com/n-popescu/switchnet/proto/common"
	mmpb "github.com/n-popescu/switchnet/proto/matchmaking/v1"
)

// sessionDefinitionClaims encodes the player's submitted matchmaking data for
// the gamesync claim. The reference token carries typed values, not protobuf
// JSON, and latency durations become integer nanoseconds by region.
func sessionDefinitionClaims(def *mmpb.UserDefinition) (string, string, error) {
	attributes := make(map[string]any)
	for name, value := range def.GetAttributes().GetFields() {
		typeName, raw := sessionAttribute(value)
		attributes[name] = map[string]any{"type": typeName, "value": raw}
	}
	attr, err := json.Marshal(attributes)
	if err != nil {
		return "", "", fmt.Errorf("encode session attributes: %w", err)
	}
	latencies := make(map[string]any)
	for region, duration := range def.GetLatencyData().GetLatencies() {
		latencies[region] = map[string]int64{
			"nanos": duration.GetSeconds()*1_000_000_000 + int64(duration.GetNanos()),
		}
	}
	ltcy, err := json.Marshal(map[string]any{"latencies": latencies})
	if err != nil {
		return "", "", fmt.Errorf("encode session latencies: %w", err)
	}
	return string(attr), string(ltcy), nil
}

func sessionAttribute(value *commonpb.Value) (string, any) {
	switch v := value.GetValueType().(type) {
	case *commonpb.Value_StringValue:
		return "string", v.StringValue
	case *commonpb.Value_IntegerValue:
		return "integer", v.IntegerValue
	case *commonpb.Value_DoubleValue:
		return "double", v.DoubleValue
	case *commonpb.Value_FloatValue:
		return "double", v.FloatValue
	case *commonpb.Value_BooleanValue:
		return "boolean", v.BooleanValue
	case *commonpb.Value_ArrayValue:
		// The reference's match attributes use an empty typed array for
		// block-list-like fields; the array contents are not routed here.
		return "array", []any{}
	default:
		return "string", ""
	}
}
