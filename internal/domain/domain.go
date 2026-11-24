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
