package persistence

import (
	"errors"
	"fmt"
)

type wireField struct {
	number uint64
	wire   uint64
	value  uint64
	bytes  []byte
}

func consumeVarint(input []byte, offset *int) (uint64, error) {
	var value uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if *offset >= len(input) {
			return 0, errors.New("truncated protobuf varint")
		}
		current := input[*offset]
		*offset = *offset + 1
		if shift == 63 && current > 1 {
			return 0, errors.New("overflowing protobuf varint")
		}
		value |= uint64(current&0x7f) << shift
		if current < 0x80 {
			return value, nil
		}
	}
	return 0, errors.New("overflowing protobuf varint")
}

func consumeWireFields(input []byte) ([]wireField, error) {
	fields := make([]wireField, 0, 8)
	for offset := 0; offset < len(input); {
		key, err := consumeVarint(input, &offset)
		if err != nil {
			return nil, err
		}
		number := key >> 3
		wire := key & 7
		if number == 0 {
			return nil, errors.New("protobuf field zero is invalid")
		}
		field := wireField{number: number, wire: wire}
		switch wire {
		case 0:
			field.value, err = consumeVarint(input, &offset)
		case 1:
			if len(input)-offset < 8 {
				err = errors.New("truncated fixed64 protobuf field")
			} else {
				offset += 8
			}
		case 2:
			var length uint64
			length, err = consumeVarint(input, &offset)
			if err == nil {
				if length > uint64(len(input)-offset) {
					err = errors.New("truncated protobuf bytes field")
				} else {
					field.bytes = input[offset : offset+int(length)]
					offset += int(length)
				}
			}
		case 5:
			if len(input)-offset < 4 {
				err = errors.New("truncated fixed32 protobuf field")
			} else {
				offset += 4
			}
		default:
			err = errors.New("unsupported protobuf wire type")
		}
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

type currentPlanSegmentWire struct {
	activityID string
	state      uint64
	start      *int64
	end        *int64
}

type currentPlanWire struct {
	planID           string
	planRevision     uint64
	origin           uint64
	segments         []currentPlanSegmentWire
	createdAtUnixMS  int64
	sourceProposalID string
}

func parseCurrentPlanSegment(input []byte) (currentPlanSegmentWire, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return currentPlanSegmentWire{}, err
	}
	var result currentPlanSegmentWire
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return result, errors.New("activity id has wrong wire type")
			}
			result.activityID = string(field.bytes)
		case 2:
			if field.wire != 0 {
				return result, errors.New("plan-entry state has wrong wire type")
			}
			result.state = field.value
		case 3:
			if field.wire != 0 {
				return result, errors.New("segment start has wrong wire type")
			}
			value := int64(field.value)
			result.start = &value
		case 4:
			if field.wire != 0 {
				return result, errors.New("segment end has wrong wire type")
			}
			value := int64(field.value)
			result.end = &value
		}
	}
	if !validCanonicalUUID(result.activityID) {
		return result, errors.New("current-plan activity id is invalid")
	}
	switch result.state {
	case 1:
		if result.start == nil || result.end == nil ||
			*result.start >= *result.end {
			return result, errors.New("scheduled current-plan segment is invalid")
		}
	case 2:
		if result.start != nil || result.end != nil {
			return result, errors.New("omitted current-plan segment has times")
		}
	default:
		return result, errors.New("current-plan segment state is invalid")
	}
	return result, nil
}

func parseCurrentPlan(input []byte) (currentPlanWire, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return currentPlanWire{}, err
	}
	var result currentPlanWire
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return result, errors.New("plan id has wrong wire type")
			}
			result.planID = string(field.bytes)
		case 2:
			if field.wire != 0 {
				return result, errors.New("plan revision has wrong wire type")
			}
			result.planRevision = field.value
		case 3:
			if field.wire != 0 {
				return result, errors.New("plan origin has wrong wire type")
			}
			result.origin = field.value
		case 4:
			if field.wire != 2 {
				return result, errors.New("plan segment has wrong wire type")
			}
			segment, err := parseCurrentPlanSegment(field.bytes)
			if err != nil {
				return result, err
			}
			result.segments = append(result.segments, segment)
		case 5:
			if field.wire != 0 {
				return result, errors.New("plan creation time has wrong wire type")
			}
			result.createdAtUnixMS = int64(field.value)
		case 6:
			if field.wire != 2 {
				return result, errors.New("source proposal id has wrong wire type")
			}
			result.sourceProposalID = string(field.bytes)
		}
	}
	if !validCanonicalUUID(result.planID) ||
		result.planRevision == 0 ||
		result.origin != 2 ||
		!validCanonicalUUID(result.sourceProposalID) {
		return result, errors.New("accepted current-plan metadata is invalid")
	}
	seen := make(map[string]struct{}, len(result.segments))
	var priorScheduledEnd *int64
	for _, segment := range result.segments {
		if _, duplicate := seen[segment.activityID]; duplicate {
			return result, errors.New("current plan repeats an activity")
		}
		seen[segment.activityID] = struct{}{}
		if segment.state != 1 {
			continue
		}
		if priorScheduledEnd != nil && *segment.start < *priorScheduledEnd {
			return result, errors.New("current-plan segments overlap")
		}
		value := *segment.end
		priorScheduledEnd = &value
	}
	return result, nil
}

func parseProposalSegment(input []byte) (currentPlanSegmentWire, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return currentPlanSegmentWire{}, err
	}
	var result currentPlanSegmentWire
	var disposition uint64
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return result, errors.New("proposal activity id has wrong wire type")
			}
			result.activityID = string(field.bytes)
		case 4:
			if field.wire != 0 {
				return result, errors.New("proposal start has wrong wire type")
			}
			value := int64(field.value)
			result.start = &value
		case 5:
			if field.wire != 0 {
				return result, errors.New("proposal end has wrong wire type")
			}
			value := int64(field.value)
			result.end = &value
		case 7:
			if field.wire != 0 {
				return result, errors.New("proposal disposition has wrong wire type")
			}
			disposition = field.value
		}
	}
	if !validCanonicalUUID(result.activityID) {
		return result, errors.New("proposal activity id is invalid")
	}
	if disposition == 4 {
		result.state = 2
		if result.start != nil || result.end != nil {
			return result, errors.New("skipped proposal segment has times")
		}
	} else if disposition == 1 || disposition == 2 || disposition == 5 {
		result.state = 1
		if result.start == nil || result.end == nil ||
			*result.start >= *result.end {
			return result, errors.New("scheduled proposal segment is invalid")
		}
	} else {
		return result, errors.New("proposal segment disposition is invalid")
	}
	return result, nil
}

func proposalCurrentPlanSegments(input []byte) ([]currentPlanSegmentWire, error) {
	storedFields, err := consumeWireFields(input)
	if err != nil {
		return nil, err
	}
	var proposalPayload []byte
	for _, field := range storedFields {
		if field.number == 1 && field.wire == 2 {
			proposalPayload = field.bytes
		}
	}
	if proposalPayload == nil {
		return nil, errors.New("stored proposal has no proposal payload")
	}
	proposalFields, err := consumeWireFields(proposalPayload)
	if err != nil {
		return nil, err
	}
	var result []currentPlanSegmentWire
	for _, field := range proposalFields {
		if (field.number == 7 || field.number == 8) && field.wire == 2 {
			segment, err := parseProposalSegment(field.bytes)
			if err != nil {
				return nil, err
			}
			result = append(result, segment)
		}
	}
	return result, nil
}

func validateProposalPlanMapping(
	plan currentPlanWire,
	storedProposal []byte,
) error {
	expected, err := proposalCurrentPlanSegments(storedProposal)
	if err != nil {
		return err
	}
	if len(expected) != len(plan.segments) {
		return errors.New("proposal-to-plan segment count differs")
	}
	for index := range expected {
		left := expected[index]
		right := plan.segments[index]
		if left.activityID != right.activityID ||
			left.state != right.state ||
			!optionalInt64Equal(left.start, right.start) ||
			!optionalInt64Equal(left.end, right.end) {
			return fmt.Errorf("proposal-to-plan segment %d differs", index)
		}
	}
	return nil
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
