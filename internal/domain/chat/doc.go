// SPDX-License-Identifier: Apache-2.0

// Package chat defines the provider-neutral chat Request, Message, Response,
// Chunk and Usage types shared by the router, the providers and the API.
//
// Adapters translate their wire formats into these types at the boundary;
// nothing here knows about HTTP or a specific provider.
//
//	req := chat.Request{Model: "triage-auto", Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "hi")}}
//	text := req.LastUserText()
package chat
