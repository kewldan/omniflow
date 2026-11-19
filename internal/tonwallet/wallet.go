// Package tonwallet derives the operator's own TON wallet from a mnemonic,
// signs transfers offline, and talks to a TON HTTP API to read the wallet and
// broadcast what it signed.
//
// It exists for one caller — the direct Fragment goods provider — and it keeps
// the parts of that job that touch keys and the network apart from the parts
// that touch Fragment. Cells, BOCs, wallet contracts, and signatures come from
// github.com/xssnick/tonutils-go (MIT); this package only decides which of
// them to use and how a failure is reported.
//
// The one property every exported function preserves: a failure reported as
// happening *before* broadcast really did happen before anything was sent, so
// a caller may retry it. Anything that could have reached the network is
// reported as a broadcast failure, which a caller must never retry blindly.
package tonwallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// Wallet versions the operator may hold. They match the fork's
// FRAGMENT_WALLET_VERSION values so a migrating operator changes nothing.
const (
	VersionV5R1 = "V5R1"
	VersionV4R2 = "V4R2"
)

// MainnetGlobalID is the network identifier Fragment's TON Connect payload
// names as the chain ("-239").
const MainnetGlobalID = -239

// maxMessages is how many outgoing messages one external message may carry.
var maxMessages = map[string]int{VersionV4R2: 4, VersionV5R1: 255}

var (
	// ErrInvalidMnemonic reports a seed phrase that is not 12, 18, or 24 words
	// or that does not derive a key.
	ErrInvalidMnemonic = errors.New("TON wallet mnemonic is not usable")
	// ErrUnsupportedVersion reports a wallet contract this package does not sign
	// for.
	ErrUnsupportedVersion = errors.New("TON wallet version is not supported")
	// ErrTooManyMessages reports a transfer the wallet contract cannot carry in
	// one external message.
	ErrTooManyMessages = errors.New("too many messages for this wallet version")
)

// Wallet is the operator's signing wallet. It holds the private key and must
// never be logged or serialised; String deliberately prints only the address.
type Wallet struct {
	version string
	inner   *wallet.Wallet
	public  ed25519.PublicKey
	signing sync.Mutex
}

// seqnoSpec is what both supported contract specs expose for offline signing.
type seqnoSpec interface {
	SetSeqnoFetcher(func(ctx context.Context, subWallet uint32) (uint32, error))
}

// FromMnemonic derives the wallet from the operator's seed phrase.
//
// Only the TON-native mnemonic scheme is accepted, which is what Tonkeeper and
// the fork's tonutils produce. The phrase is normalised for whitespace so a
// value pasted with line breaks into an environment file still works.
func FromMnemonic(mnemonic, version string) (*Wallet, error) {
	words := strings.Fields(mnemonic)
	if len(words) != 12 && len(words) != 18 && len(words) != 24 {
		return nil, ErrInvalidMnemonic
	}
	config, err := versionConfig(version)
	if err != nil {
		return nil, err
	}
	inner, err := wallet.FromSeed(nil, words, config)
	if err != nil {
		// The library's message can quote the failing check, never the
		// words, but the cause is not worth exposing: the operator needs to
		// know the phrase is wrong, not why.
		return nil, ErrInvalidMnemonic
	}
	public, ok := inner.PrivateKey().Public().(ed25519.PublicKey)
	if !ok {
		return nil, ErrInvalidMnemonic
	}
	return &Wallet{version: normalizeVersion(version), inner: inner, public: public}, nil
}

func normalizeVersion(version string) string {
	switch strings.ToUpper(strings.TrimSpace(version)) {
	case "", VersionV5R1, "W5", "V5":
		return VersionV5R1
	case VersionV4R2, "V4":
		return VersionV4R2
	default:
		return strings.ToUpper(strings.TrimSpace(version))
	}
}

func versionConfig(version string) (wallet.VersionConfig, error) {
	switch normalizeVersion(version) {
	case VersionV5R1:
		return wallet.ConfigV5R1Final{NetworkGlobalID: MainnetGlobalID, Workchain: 0}, nil
	case VersionV4R2:
		return wallet.V4R2, nil
	default:
		return nil, ErrUnsupportedVersion
	}
}

// Version is the wallet contract in use.
func (w *Wallet) Version() string { return w.version }

// Address is the user-friendly, non-bounceable form (UQ…), which is how a
// wallet app shows it and what an operator funds.
func (w *Wallet) Address() string { return w.inner.WalletAddress().String() }

// RawAddress is the workchain:hex form Fragment's TON Connect payload carries.
func (w *Wallet) RawAddress() string { return w.inner.WalletAddress().StringRaw() }

// String prints the address and nothing else, so a wallet that ends up in a
// log line by accident leaks nothing that matters.
func (w *Wallet) String() string { return "tonwallet(" + w.Address() + ")" }

// PublicKeyHex is the ed25519 public key, as TON Connect transmits it.
func (w *Wallet) PublicKeyHex() string { return hex.EncodeToString(w.public) }

// StateInitBOC is the wallet's state init as a base64 BOC, which Fragment needs
// to prepare a payment from a wallet that may not be deployed yet.
func (w *Wallet) StateInitBOC() (string, error) {
	config, err := versionConfig(w.version)
	if err != nil {
		return "", err
	}
	stateInit, err := wallet.GetStateInit(w.public, config, w.inner.GetSubwalletID())
	if err != nil {
		return "", err
	}
	encoded, err := tlb.ToCell(stateInit)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(encoded.ToBOCWithFlags(false)), nil
}

// AccountInfo is the TON Connect account description Fragment's
// getBuyStarsLink expects, in the exact shape the fork sends.
type AccountInfo struct {
	Address         string `json:"address"`
	PublicKey       string `json:"publicKey"`
	Chain           string `json:"chain"`
	WalletStateInit string `json:"walletStateInit"`
}

// Account builds the TON Connect description. It needs no network: everything
// in it follows from the key and the contract version.
func (w *Wallet) Account() (AccountInfo, error) {
	stateInit, err := w.StateInitBOC()
	if err != nil {
		return AccountInfo{}, err
	}
	return AccountInfo{
		Address: w.RawAddress(), PublicKey: w.PublicKeyHex(),
		Chain: fmt.Sprint(MainnetGlobalID), WalletStateInit: stateInit,
	}, nil
}

// Transfer is one outgoing message a payment asks for.
type Transfer struct {
	// Destination in any form TON accepts: user-friendly or workchain:hex.
	Destination string
	AmountNano  int64
	// PayloadBOC is the base64 (standard or URL-safe) cell to attach as the
	// message body. Fragment's payment comment travels here; it is attached
	// as the cell Fragment produced rather than re-encoded, so the reference
	// Fragment matches the payment by is byte-for-byte what it asked for.
	PayloadBOC string
}

// Signed is an external message ready to broadcast.
type Signed struct {
	// BOC is the base64 external message. Fragment's confirmReq takes it too.
	BOC string
	// Hash is the normalized external-message hash in hex — the identifier
	// tonviewer and tonscan resolve to the resulting transaction.
	Hash string
	// Seqno is the wallet sequence number the message was signed for. The
	// contract accepts at most one message per seqno, which is what makes
	// re-sending these exact bytes safe and signing a new message unsafe.
	Seqno uint32
}

// Sign builds and signs one external message offline.
//
// `seqno` and `deployed` come from the network, read immediately before; this
// call itself makes no request. An undeployed wallet carries its state init,
// so the first payment from a fresh wallet deploys it.
func (w *Wallet) Sign(transfers []Transfer, seqno uint32, deployed bool) (Signed, error) {
	if len(transfers) == 0 {
		return Signed{}, errors.New("a transfer needs at least one message")
	}
	if len(transfers) > maxMessages[w.version] {
		return Signed{}, ErrTooManyMessages
	}
	messages := make([]*wallet.Message, 0, len(transfers))
	for index, transfer := range transfers {
		message, err := buildMessage(transfer)
		if err != nil {
			return Signed{}, fmt.Errorf("message %d: %w", index, err)
		}
		messages = append(messages, message)
	}

	spec, ok := w.inner.GetSpec().(seqnoSpec)
	if !ok {
		return Signed{}, ErrUnsupportedVersion
	}
	// The library asks a fetcher for the seqno while it builds. The fetcher is
	// replaced with the value read over HTTP, which is what lets this sign
	// without a lite client — and the lock keeps two concurrent signings from
	// seeing each other's value.
	w.signing.Lock()
	defer w.signing.Unlock()
	spec.SetSeqnoFetcher(func(context.Context, uint32) (uint32, error) { return seqno, nil })

	external, err := w.inner.PrepareExternalMessageForMany(context.Background(), !deployed, messages)
	if err != nil {
		return Signed{}, fmt.Errorf("sign transfer: %w", err)
	}
	root, err := tlb.ToCell(external)
	if err != nil {
		return Signed{}, fmt.Errorf("serialise transfer: %w", err)
	}
	return Signed{
		BOC:   base64.StdEncoding.EncodeToString(root.ToBOCWithFlags(false)),
		Hash:  hex.EncodeToString(external.NormalizedHash()),
		Seqno: seqno,
	}, nil
}

func buildMessage(transfer Transfer) (*wallet.Message, error) {
	if transfer.AmountNano <= 0 {
		return nil, errors.New("amount must be positive")
	}
	destination, err := ParseAddress(transfer.Destination)
	if err != nil {
		return nil, err
	}
	var body *cell.Cell
	if strings.TrimSpace(transfer.PayloadBOC) != "" {
		if body, err = DecodeCell(transfer.PayloadBOC); err != nil {
			return nil, fmt.Errorf("payload: %w", err)
		}
	}
	// Bounce follows the destination's own flag, which is what the fork's
	// tonutils does with bounce=None.
	return wallet.SimpleMessageAutoBounce(
		destination, tlb.FromNanoTONU(uint64(transfer.AmountNano)), body,
	), nil
}

// ParseAddress accepts both the user-friendly and the workchain:hex forms.
func ParseAddress(value string) (*address.Address, error) {
	value = strings.TrimSpace(value)
	if parsed, err := address.ParseAddr(value); err == nil {
		return parsed, nil
	}
	parsed, err := address.ParseRawAddr(value)
	if err != nil {
		return nil, fmt.Errorf("address is not a TON address")
	}
	return parsed, nil
}

// DecodeCell parses a single-root BOC in standard or URL-safe base64, padded or
// not — Fragment has been seen to send both.
func DecodeCell(encoded string) (*cell.Cell, error) {
	value := strings.TrimSpace(encoded)
	value = strings.NewReplacer("-", "+", "_", "/").Replace(value)
	if remainder := len(value) % 4; remainder != 0 {
		value += strings.Repeat("=", 4-remainder)
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return cell.FromBOC(raw)
}
