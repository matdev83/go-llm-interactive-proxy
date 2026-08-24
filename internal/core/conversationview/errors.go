package conversationview

import "errors"

var (
	// ErrNonMessageItem is returned when an item is not a complete message item.
	ErrNonMessageItem = errors.New("conversationview: item is not a complete message item")

	// ErrEmptyMessage is returned when a message has no content parts.
	ErrEmptyMessage = errors.New("conversationview: message has no content parts")

	// ErrInvalidRole is returned when a message role is missing or invalid.
	ErrInvalidRole = errors.New("conversationview: invalid message role")

	// ErrInvalidMessageIdentity is returned when a message identity string is malformed or invalid.
	ErrInvalidMessageIdentity = errors.New("conversationview: invalid message identity")

	// ErrInvalidMessageAnchor is returned when a message anchor is invalid.
	ErrInvalidMessageAnchor = errors.New("conversationview: invalid message anchor")

	// ErrAnchorNotFound is returned when an anchor cannot be resolved in a trajectory.
	ErrAnchorNotFound = errors.New("conversationview: anchor not found")

	// ErrPartialContentNotSupported is returned when an identity or anchor operation targets a partial content part rather than a complete message.
	ErrPartialContentNotSupported = errors.New("conversationview: partial content part identity is not supported")

	// ErrALegNotFound is returned when conversation-view state is requested for an unknown A-leg.
	ErrALegNotFound = errors.New("conversationview: a-leg not found")

	// ErrInvalidALegID is returned when an A-leg identifier is missing or malformed.
	ErrInvalidALegID = errors.New("conversationview: invalid a-leg id")

	// ErrInvalidTagRequest is returned when a TagRequest fails validation.
	ErrInvalidTagRequest = errors.New("conversationview: invalid tag request")

	// ErrTagLimitExceeded is returned when a tag mutation would exceed the 4096 unique-identity cap.
	ErrTagLimitExceeded = errors.New("conversationview: never_backend tag limit exceeded")

	// ErrInvalidReasonCode is returned when a ReasonCode is missing or exceeds bounds.
	ErrInvalidReasonCode = errors.New("conversationview: invalid reason code")

	// ErrInvalidOverlayID is returned when an overlay identifier is missing or exceeds bounds.
	ErrInvalidOverlayID = errors.New("conversationview: invalid overlay id")

	// ErrInvalidSteeringRequest is returned when a PutSteeringRequest fails validation.
	ErrInvalidSteeringRequest = errors.New("conversationview: invalid steering request")

	// ErrInvalidSteeringMessage is returned when a StoredMessageV1 fails validation.
	ErrInvalidSteeringMessage = errors.New("conversationview: invalid steering message")

	// ErrInvalidPlacement is returned when a StoredPlacement fails validation.
	ErrInvalidPlacement = errors.New("conversationview: invalid placement")

	// ErrInvalidAnchorMissingPolicy is returned when an AnchorMissingPolicy value is invalid.
	ErrInvalidAnchorMissingPolicy = errors.New("conversationview: invalid anchor missing policy")

	// ErrSteeringLimitExceeded is returned when a steering mutation would exceed a count or byte cap.
	ErrSteeringLimitExceeded = errors.New("conversationview: steering limit exceeded")

	// ErrOverlayNotFound is returned when an overlay cannot be found for deactivation.
	ErrOverlayNotFound = errors.New("conversationview: overlay not found")

	// ErrRevisionExhausted is returned when a revision counter would overflow.
	ErrRevisionExhausted = errors.New("conversationview: revision exhausted")

	// ErrAnchorMissing is returned when a required fixed anchor cannot be resolved.
	ErrAnchorMissing = errors.New("conversationview: anchor missing")

	// ErrProjectionFailed is returned when projection cannot produce a valid backend call.
	ErrProjectionFailed = errors.New("conversationview: projection failed")

	// ErrTerminalUserNotFound is returned when no terminal forwardable user message exists.
	ErrTerminalUserNotFound = errors.New("conversationview: terminal forwardable user message not found")

	// ErrTerminalNotUser is returned when the terminal forwardable message is not a user message.
	ErrTerminalNotUser = errors.New("conversationview: terminal forwardable message is not user")

	// ErrSteeringAnchorExcluded is returned when a steering registration would newly bind an
	// after_message anchor whose identity is already never_backend at the atomic persistence
	// point. Exclusion of a previously registered anchor is legitimate later anchor loss and is
	// handled by AnchorMissingPolicy at projection time.
	ErrSteeringAnchorExcluded = errors.New("conversationview: steering anchor identity is never_backend")
)
