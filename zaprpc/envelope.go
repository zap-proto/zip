package zaprpc

import (
	"fmt"

	zap "github.com/zap-proto/go"
)

// The ZAP RPC envelope is the canonical on-the-wire framing for one RPC
// call carried over HTTP (or any byte transport). It is a ZAP Message
// whose root Object has a 24-byte fixed section with three fields:
//
//	offset  0: service  (text  — 8 bytes: u32 rel-offset + u32 len)
//	offset  8: method   (text  — 8 bytes)
//	offset 16: payload  (bytes — 8 bytes)
//
// Binary is canonical: the X-ZAP-Service / X-ZAP-Method HTTP headers are
// only a fast-path hint and the envelope wins on any disagreement.
const (
	envServiceOff = 0
	envMethodOff  = 8
	envPayloadOff = 16
	envFixedSize  = 24
)

// Envelope is the decoded form of a ZAP RPC frame.
type Envelope struct {
	Service string
	Method  string
	Payload []byte
}

// EncodeEnvelope builds the canonical ZAP RPC frame for e.
func EncodeEnvelope(e Envelope) []byte {
	b := zap.NewBuilder(256 + len(e.Payload))
	ob := b.StartObject(envFixedSize)
	ob.SetText(envServiceOff, e.Service)
	ob.SetText(envMethodOff, e.Method)
	ob.SetBytes(envPayloadOff, e.Payload)
	ob.FinishAsRoot()
	return b.Finish()
}

// DecodeEnvelope parses a ZAP RPC frame. Returns an error if the bytes
// are not a valid ZAP message.
func DecodeEnvelope(data []byte) (Envelope, error) {
	msg, err := zap.Parse(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("zaprpc: decode envelope: %w", err)
	}
	root := msg.Root()
	if root.IsNull() {
		return Envelope{}, fmt.Errorf("zaprpc: decode envelope: null root")
	}
	return Envelope{
		Service: root.Text(envServiceOff),
		Method:  root.Text(envMethodOff),
		Payload: root.Bytes(envPayloadOff),
	}, nil
}

// EncodeResponse wraps a method's response payload in a ZAP frame. The
// response uses the same envelope shape with empty service/method so a
// single decoder handles both directions; callers that only need the
// payload can use DecodeResponse.
func EncodeResponse(payload []byte) []byte {
	return EncodeEnvelope(Envelope{Payload: payload})
}

// DecodeResponse extracts the payload from a response frame.
func DecodeResponse(data []byte) ([]byte, error) {
	e, err := DecodeEnvelope(data)
	if err != nil {
		return nil, err
	}
	return e.Payload, nil
}
