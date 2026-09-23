package library

// CompleteAcceptance transfers the result to a buffered, request-owned receipt.
// Only the goroutine owning msg may call this method. The HTTP handler keeps its
// own channel reference and never touches msg after queue publication.
func (msg *FluentMsg) CompleteAcceptance(err error) {
	if msg.DurableAck == nil {
		return
	}
	receipt := msg.DurableAck
	msg.DurableAck = nil
	receipt <- err
}
