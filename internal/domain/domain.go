// Package domain contains pure application types shared across layers. It
// imports no other internal package.
package domain

import "time"

// GoogleAccount is a connected Gmail account with encrypted OAuth token data.
type GoogleAccount struct {
	Email     string
	TokenEnc  []byte
	CreatedAt time.Time
}

// AuditEntry is one security-relevant action recorded in the audit log.
type AuditEntry struct {
	// ID is assigned by the store when the entry is read back.
	ID int64
	// TS is the time of the action.
	TS time.Time
	// Actor identifies who performed the action.
	Actor string
	// Action is a short stable verb such as "mail.clean.execute".
	Action string
	// Details is a JSON object with counts and parameters, never content.
	Details string
}

// Message is a normalized Gmail message.
type Message struct {
	ID       string
	ThreadID string
	From     string
	To       string
	Cc       string
	Subject  string
	Snippet  string
	BodyText string
	BodyHTML string
	Date     time.Time
	Unread   bool
	Labels   []string
}
