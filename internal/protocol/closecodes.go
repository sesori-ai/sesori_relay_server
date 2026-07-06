package protocol

const (
	CloseAuthFailure   = 4001
	CloseAuthRequired  = 4002
	CloseRoomFull      = 4003
	CloseRoomNotFound  = 4004
	CloseAccountFull   = 4005
	CloseBridgeRevoked = 4006
	// CloseBridgeReplaced is sent to a bridge that is displaced when another
	// bridge for the same account connects and takes the single bridge slot.
	// A dedicated code (rather than a plain 1000 normal close with a "replaced"
	// reason) lets the displaced bridge recognise the takeover reliably: close
	// reason strings are fragile (intermediaries may strip/rewrite them) while
	// codes survive. The displaced bridge reconnects only on a long backoff so
	// two always-on bridges don't tight-loop kicking each other.
	CloseBridgeReplaced = 4007
)
