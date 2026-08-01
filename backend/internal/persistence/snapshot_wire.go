package persistence

import "errors"

type snapshotPayloadMetadata struct {
	tripID                    string
	currentPlanID             string
	tripRevision              uint64
	acceptedMutationSequence  uint64
	finalizedMutationSequence uint64
	schemaVersion             uint64
}

func validateNestedMessage(input []byte, messageFields map[uint64]struct{}) error {
	fields, err := consumeWireFields(input)
	if err != nil {
		return err
	}
	for _, field := range fields {
		if _, nested := messageFields[field.number]; !nested {
			continue
		}
		if field.wire != 2 {
			return errors.New("nested protobuf field has wrong wire type")
		}
		if _, err := consumeWireFields(field.bytes); err != nil {
			return err
		}
	}
	return nil
}

func validateActivityWire(input []byte) error {
	fields, err := consumeWireFields(input)
	if err != nil {
		return err
	}
	var activityID string
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return errors.New("snapshot activity id has wrong wire type")
			}
			activityID = string(field.bytes)
		case 4:
			if field.wire != 2 {
				return errors.New("snapshot location has wrong wire type")
			}
			if _, err := consumeWireFields(field.bytes); err != nil {
				return err
			}
		case 11:
			if field.wire != 2 {
				return errors.New("snapshot timing has wrong wire type")
			}
			timingFields, err := consumeWireFields(field.bytes)
			if err != nil {
				return err
			}
			for _, timingField := range timingFields {
				if timingField.number == 1 {
					if timingField.wire != 2 {
						return errors.New(
							"snapshot time window has wrong wire type",
						)
					}
					if _, err := consumeWireFields(
						timingField.bytes,
					); err != nil {
						return err
					}
				}
			}
		}
	}
	if !validCanonicalUUID(activityID) {
		return errors.New("snapshot activity id is invalid")
	}
	return nil
}

func parseSnapshotTrip(input []byte) (string, string, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return "", "", err
	}
	var tripID string
	var ownerUserID string
	var timeZoneName string
	var currentPlanID string
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot trip id has wrong wire type",
				)
			}
			tripID = string(field.bytes)
		case 2:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot owner id has wrong wire type",
				)
			}
			ownerUserID = string(field.bytes)
		case 3:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot time zone has wrong wire type",
				)
			}
			timeZoneName = string(field.bytes)
		case 4:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot activity has wrong wire type",
				)
			}
			if err := validateActivityWire(field.bytes); err != nil {
				return "", "", err
			}
		case 7:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot current-plan id has wrong wire type",
				)
			}
			currentPlanID = string(field.bytes)
		case 8:
			if field.wire != 2 {
				return "", "", errors.New(
					"snapshot travel delay has wrong wire type",
				)
			}
			if err := validateNestedMessage(
				field.bytes,
				map[uint64]struct{}{},
			); err != nil {
				return "", "", err
			}
		}
	}
	if !validCanonicalUUID(tripID) ||
		!validCanonicalUUID(ownerUserID) ||
		timeZoneName == "" ||
		!validCanonicalUUID(currentPlanID) {
		return "", "", errors.New("snapshot trip metadata is invalid")
	}
	return tripID, currentPlanID, nil
}

type storedCurrentPlanMetadata struct {
	planID           string
	revision         uint64
	origin           uint64
	sourceProposalID string
	createdAtUnixMS  int64
	segments         []currentPlanSegmentWire
}

func parseSnapshotCurrentPlan(
	input []byte,
) (storedCurrentPlanMetadata, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return storedCurrentPlanMetadata{}, err
	}
	var result storedCurrentPlanMetadata
	seenActivities := make(map[string]struct{})
	var priorEnd *int64
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return result, errors.New(
					"snapshot plan id has wrong wire type",
				)
			}
			result.planID = string(field.bytes)
		case 2:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot plan revision has wrong wire type",
				)
			}
			result.revision = field.value
		case 3:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot plan origin has wrong wire type",
				)
			}
			result.origin = field.value
		case 4:
			if field.wire != 2 {
				return result, errors.New(
					"snapshot plan segment has wrong wire type",
				)
			}
			segment, err := parseCurrentPlanSegment(field.bytes)
			if err != nil {
				return result, err
			}
			if _, exists := seenActivities[segment.activityID]; exists {
				return result, errors.New(
					"snapshot plan repeats an activity",
				)
			}
			seenActivities[segment.activityID] = struct{}{}
			if segment.state == 1 {
				if priorEnd != nil && *segment.start < *priorEnd {
					return result, errors.New(
						"snapshot plan segments overlap",
					)
				}
				value := *segment.end
				priorEnd = &value
			}
			result.segments = append(result.segments, segment)
		case 5:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot plan creation time has wrong wire type",
				)
			}
			result.createdAtUnixMS = int64(field.value)
		case 6:
			if field.wire != 2 {
				return result, errors.New(
					"snapshot source proposal has wrong wire type",
				)
			}
			result.sourceProposalID = string(field.bytes)
		}
	}
	if !validCanonicalUUID(result.planID) || result.revision == 0 {
		return result, errors.New("snapshot plan metadata is invalid")
	}
	if result.origin == 1 {
		if result.sourceProposalID != "" {
			return result, errors.New(
				"user snapshot plan has a source proposal",
			)
		}
	} else if result.origin == 2 {
		if !validCanonicalUUID(result.sourceProposalID) {
			return result, errors.New(
				"engine snapshot plan lacks its source proposal",
			)
		}
	} else {
		return result, errors.New("snapshot plan origin is invalid")
	}
	return result, nil
}

func parseSnapshotPayload(input []byte) (snapshotPayloadMetadata, error) {
	fields, err := consumeWireFields(input)
	if err != nil {
		return snapshotPayloadMetadata{}, err
	}
	var result snapshotPayloadMetadata
	var tripPresent bool
	var planPresent bool
	for _, field := range fields {
		switch field.number {
		case 1:
			if field.wire != 2 {
				return result, errors.New(
					"snapshot trip has wrong wire type",
				)
			}
			result.tripID, result.currentPlanID, err =
				parseSnapshotTrip(field.bytes)
			if err != nil {
				return result, err
			}
			tripPresent = true
		case 2:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot trip revision has wrong wire type",
				)
			}
			result.tripRevision = field.value
		case 3:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot accepted sequence has wrong wire type",
				)
			}
			result.acceptedMutationSequence = field.value
		case 4:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot finalized sequence has wrong wire type",
				)
			}
			result.finalizedMutationSequence = field.value
		case 5:
			if field.wire != 2 {
				return result, errors.New(
					"snapshot current plan has wrong wire type",
				)
			}
			plan, err := parseSnapshotCurrentPlan(field.bytes)
			if err != nil {
				return result, err
			}
			if result.currentPlanID != "" &&
				plan.planID != result.currentPlanID {
				return result, errors.New(
					"snapshot current-plan identifiers differ",
				)
			}
			planPresent = true
		case 6:
			if field.wire != 0 {
				return result, errors.New(
					"snapshot schema has wrong wire type",
				)
			}
			result.schemaVersion = field.value
		}
	}
	if !tripPresent || !planPresent ||
		result.tripRevision == 0 ||
		result.schemaVersion != 1 ||
		result.acceptedMutationSequence !=
			result.finalizedMutationSequence {
		return result, errors.New("snapshot payload metadata is invalid")
	}
	return result, nil
}
