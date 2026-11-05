package remotechan

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// The frame types on the relay. A relay sees these and nothing else: the two
// key halves of the handshake, sealed data, and a bare refusal.
const (
	frameHelloRemote byte = 1 // remote → local: remote's ephemeral public key
	frameHelloLocal  byte = 2 // local → remote: local's ephemeral public key
	frameData        byte = 3 // either way: type | seq(8, big-endian) | AES-256-GCM ciphertext
	frameRefused     byte = 4 // local → remote, alone: the remote's enrollment did not open under the pinned key
)

const (
	pubLen    = 32 // X25519 public key
	headerLen = 1 + 8
)

var (
	// ErrKeyMismatch: the remote that answered cannot prove the public key local
	// satelle pinned at launch. A substituted node key lands here and nothing the
	// remote sent is delivered.
	ErrKeyMismatch = errors.New("remotechan: remote does not hold the key pinned at launch")
	// ErrFrame: a frame was malformed, of an unexpected type, or out of order
	// (a replayed, dropped or reordered frame breaks the sequence).
	ErrFrame = errors.New("remotechan: bad frame")
)

// Identity is a node's static X25519 key. The node generates it; local satelle
// reads Public at launch and pins it, and the node proves it holds the private
// half by deriving the session keys with it.
type Identity struct{ key *ecdh.PrivateKey }

// NewIdentity generates a fresh node identity.
func NewIdentity() (*Identity, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("remotechan: generate identity: %w", err)
	}
	return &Identity{key: k}, nil
}

// Public is the 32-byte public key to pin.
func (i *Identity) Public() []byte { return i.key.PublicKey().Bytes() }

func newEphemeral() (*ecdh.PrivateKey, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("remotechan: generate ephemeral key: %w", err)
	}
	return k, nil
}

func parsePublic(b []byte) (*ecdh.PublicKey, error) {
	if len(b) != pubLen {
		return nil, fmt.Errorf("%w: public key is %d bytes, want %d", ErrFrame, len(b), pubLen)
	}
	return ecdh.X25519().NewPublicKey(b)
}

// localKeys derives the local end's keys. The static-key term (eL · pinned) is
// why a substituted node key fails: only the holder of the pinned key's private
// half can derive the same secret.
func localKeys(session string, eL *ecdh.PrivateKey, eRPub, pinned []byte) (*sealer, *opener, error) {
	eR, err := parsePublic(eRPub)
	if err != nil {
		return nil, nil, err
	}
	rS, err := parsePublic(pinned)
	if err != nil {
		return nil, nil, fmt.Errorf("remotechan: pinned key: %w", err)
	}
	ee, err := eL.ECDH(eR)
	if err != nil {
		return nil, nil, err
	}
	es, err := eL.ECDH(rS)
	if err != nil {
		return nil, nil, err
	}
	return deriveKeys(RoleLocal, session, eL.PublicKey().Bytes(), eRPub, pinned, ee, es)
}

// remoteKeys derives the remote end's keys from its static identity.
func remoteKeys(session string, id *Identity, eR *ecdh.PrivateKey, eLPub []byte) (*sealer, *opener, error) {
	eL, err := parsePublic(eLPub)
	if err != nil {
		return nil, nil, err
	}
	ee, err := eR.ECDH(eL)
	if err != nil {
		return nil, nil, err
	}
	es, err := id.key.ECDH(eL)
	if err != nil {
		return nil, nil, err
	}
	return deriveKeys(RoleRemote, session, eLPub, eR.PublicKey().Bytes(), id.Public(), ee, es)
}

// deriveKeys runs HKDF-SHA256 over both DH results, salted with the whole
// transcript (both ephemerals, the static key, the session), into one AES-256
// key per direction.
func deriveKeys(role Role, session string, eL, eR, rS, ee, es []byte) (*sealer, *opener, error) {
	secret := append(append([]byte{}, ee...), es...)
	salt := append(append(append(append([]byte{}, eL...), eR...), rS...), session...)
	l2r, err := hkdf.Key(sha256.New, secret, salt, "satelle remotechan v1 local-to-remote", 32)
	if err != nil {
		return nil, nil, err
	}
	r2l, err := hkdf.Key(sha256.New, secret, salt, "satelle remotechan v1 remote-to-local", 32)
	if err != nil {
		return nil, nil, err
	}
	sendKey, recvKey := l2r, r2l
	if role == RoleRemote {
		sendKey, recvKey = r2l, l2r
	}
	send, err := newAEAD(sendKey)
	if err != nil {
		return nil, nil, err
	}
	recv, err := newAEAD(recvKey)
	if err != nil {
		return nil, nil, err
	}
	return &sealer{aead: send, session: session}, &opener{aead: recv, session: session}, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealer encrypts one direction. The sequence number is the nonce, so it never
// repeats under a key, and it is authenticated in the header.
type sealer struct {
	aead    cipher.AEAD
	session string
	seq     uint64
}

// seal returns the wire frame for plain. Callers serialise seal and the write
// that follows it, so frames reach the relay in sequence order.
func (s *sealer) seal(plain []byte) []byte {
	header := make([]byte, headerLen, headerLen+len(plain)+s.aead.Overhead())
	header[0] = frameData
	binary.BigEndian.PutUint64(header[1:], s.seq)
	s.seq++
	return s.aead.Seal(header, nonceFor(header[1:]), plain, aad(header, s.session))
}

// opener decrypts one direction and insists on the next sequence number.
type opener struct {
	aead    cipher.AEAD
	session string
	next    uint64
}

var errOpen = errors.New("remotechan: frame does not authenticate")

// open returns the plaintext of frame. A frame that fails to authenticate
// returns errOpen and does not advance the sequence.
func (o *opener) open(frame []byte) ([]byte, error) {
	if len(frame) < headerLen+o.aead.Overhead() || frame[0] != frameData {
		return nil, ErrFrame
	}
	if binary.BigEndian.Uint64(frame[1:headerLen]) != o.next {
		return nil, fmt.Errorf("%w: out of sequence", ErrFrame)
	}
	plain, err := o.aead.Open(nil, nonceFor(frame[1:headerLen]), frame[headerLen:], aad(frame[:headerLen], o.session))
	if err != nil {
		return nil, errOpen
	}
	o.next++
	return plain, nil
}

func nonceFor(seq []byte) []byte {
	nonce := make([]byte, 12)
	copy(nonce[4:], seq)
	return nonce
}

func aad(header []byte, session string) []byte {
	return append(append([]byte{}, header...), session...)
}
