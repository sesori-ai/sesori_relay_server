package protocol

const (
	CloseAuthFailure   = 4001
	CloseAuthRequired  = 4002
	CloseRoomFull      = 4003
	CloseRoomNotFound  = 4004
	CloseAccountFull   = 4005
	CloseBridgeRevoked = 4006
	// CloseBridgeReplaced is sent to a bridge connection that is displaced
	// when a newer connection with the same bridgeId connects for the account;
	// bridges with different ids stay connected side by side.
	// A dedicated code (rather than a plain 1000 normal close with a "replaced"
	// reason) lets the displaced bridge recognise the takeover reliably: close
	// reason strings are fragile (intermediaries may strip/rewrite them) while
	// codes survive. The displaced bridge reconnects only on a long backoff so
	// two always-on processes sharing one bridgeId don't tight-loop kicking
	// each other.
	CloseBridgeReplaced = 4007
)
