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
)
