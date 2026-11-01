// Package heartbeat is a tiny UDP echo between every client and the server.
//
// It exists to answer one question the SSH tunnel cannot answer about itself:
// when the TCP tunnel goes down, is the path to the server dead (UDP dies too)
// or is only TCP broken (UDP keeps flowing)? The server records the last
// heartbeat per client and shows it next to the TCP state; the client measures
// loss and round-trip time from the replies.
//
// Packets are fixed-size and the reply is exactly as long as the request, so
// the server cannot be used as an amplifier. They are authenticated with an
// HMAC over a shared key (TK_UDP_KEY); a packet that does not verify is dropped
// without a reply.
package heartbeat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
)

const (
	magic    = "TKHB"
	version  = 1
	typeReq  = 1
	typeRep  = 2
	nameLen  = 32
	macLen   = 16
	headLen  = 4 + 1 + 1 + 4 + 8 + nameLen // magic ver type seq ts name
	PacketSz = headLen + macLen
)

// packet is the decoded heartbeat.
type packet struct {
	Reply bool
	Seq   uint32
	TS    int64 // sender's unix nanoseconds, echoed back untouched
	Name  string
}

func sign(key []byte, head []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(head)
	return m.Sum(nil)[:macLen]
}

// validName keeps names to what a client name looks like (tk-site-role).
func validName(n string) bool {
	if n == "" || len(n) > nameLen {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.@", r)) {
			return false
		}
	}
	return true
}

func (p packet) encode(key []byte) ([]byte, error) {
	if !validName(p.Name) {
		return nil, errors.New("heartbeat: invalid client name")
	}
	b := make([]byte, PacketSz)
	copy(b, magic)
	b[4] = version
	b[5] = typeReq
	if p.Reply {
		b[5] = typeRep
	}
	binary.BigEndian.PutUint32(b[6:], p.Seq)
	binary.BigEndian.PutUint64(b[10:], uint64(p.TS))
	copy(b[18:18+nameLen], p.Name)
	copy(b[headLen:], sign(key, b[:headLen]))
	return b, nil
}

func decode(key, b []byte) (packet, error) {
	if len(b) != PacketSz || string(b[:4]) != magic || b[4] != version {
		return packet{}, errors.New("heartbeat: not a heartbeat packet")
	}
	if !hmac.Equal(b[headLen:], sign(key, b[:headLen])) {
		return packet{}, errors.New("heartbeat: bad signature")
	}
	name := strings.TrimRight(string(b[18:18+nameLen]), "\x00")
	if !validName(name) || (b[5] != typeReq && b[5] != typeRep) {
		return packet{}, errors.New("heartbeat: malformed packet")
	}
	return packet{
		Reply: b[5] == typeRep,
		Seq:   binary.BigEndian.Uint32(b[6:]),
		TS:    int64(binary.BigEndian.Uint64(b[10:])),
		Name:  name,
	}, nil
}
