package remotenode

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	"github.com/bobmcallan/satelle/internal/ledger"
)

const (
	sealPayloadKey = "satelle.seal"
	sealDomain     = "satelle-node-outcome-v1"
)

func newRunSeal() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

func sealMessage(out LocalOutcome) ([]byte, error) {
	out.Seal = ""
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(sealDomain), 0), b...), nil
}

func sealOutcome(priv ed25519.PrivateKey, out LocalOutcome) (string, error) {
	if priv == nil {
		return "", nil
	}
	msg, err := sealMessage(out)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ed25519.Sign(priv, msg)), nil
}

func checkSeal(pubHex string, out LocalOutcome) error {
	if pubHex == "" {
		return errors.New("no seal key recorded")
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("the recorded seal key is not an Ed25519 public key")
	}
	if out.Seal == "" {
		return errors.New("the outcome carries no seal")
	}
	sig, err := hex.DecodeString(out.Seal)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the outcome's seal is not an Ed25519 signature")
	}
	msg, err := sealMessage(out)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return errors.New("the outcome's seal does not verify")
	}
	return nil
}

func ledgerHeads(l *ledger.Store) (map[string]string, error) {
	ids, err := l.Items()
	if err != nil {
		return nil, err
	}
	heads := map[string]string{}
	for _, id := range ids {
		h, err := l.Head(id)
		if err != nil {
			return nil, err
		}
		heads[id] = h
	}
	return heads, nil
}

func checkHeads(dir string, want map[string]string) error {
	l, err := ledger.Open(dir)
	if err != nil {
		return err
	}
	got, err := ledgerHeads(l)
	if err != nil {
		return err
	}
	if !maps.Equal(got, want) {
		return fmt.Errorf("the ledger brought back does not end at the heads the node sealed")
	}
	return nil
}
