package tonwallet

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"
)

// testMnemonic is a throwaway phrase generated for these tests. It has never
// held funds and must never be funded.
const testMnemonic = "sense soldier wrong wool trap more goose raw unfold rack magic increase " +
	"flower false echo girl error rare chief tray grant extra bridge lift"

// The vectors below were produced by the fork's own wallet stack — Python
// tonutils 2.3.1 / ton-core 0.6.0, the library its vendored Fragment code pays
// through — from the same phrase. Matching them is what shows this package
// derives the wallet Fragment sees and builds the message Fragment expects.
var vectors = map[string]struct {
	friendly, raw, stateInitHash string
}{
	VersionV4R2: {
		friendly:      "UQAgXpqvSWJ7CF6ckQiDh7puLvZJyctaSyiL5pWu02YTH5fn",
		raw:           "0:205e9aaf49627b085e9c91088387ba6e2ef649c9cb5a4b288be695aed366131f",
		stateInitHash: "205e9aaf49627b085e9c91088387ba6e2ef649c9cb5a4b288be695aed366131f",
	},
	VersionV5R1: {
		friendly:      "UQCAjt9m9mVLiEQj6nlxLs4CG_Kp5czy81DUiKvhS6p3v1Vn",
		raw:           "0:808edf66f6654b884423ea79712ece021bf2a9e5ccf2f350d488abe14baa77bf",
		stateInitHash: "808edf66f6654b884423ea79712ece021bf2a9e5ccf2f350d488abe14baa77bf",
	},
}

const (
	vectorPublicKey = "a8683f337de55020dd199c6b50f7d4e8f8a4cbf4397ada6c76d812b694f3ef7d"
	// A Fragment-shaped payment comment and the internal message tonutils
	// builds for 1.23456789 TON to vectorDestination with it.
	vectorPayload      = "te6ccgEBAQEAIwAAQgAAAABUZWxlZ3JhbSBTdGFycyBSZWYjVEVTVFJFRjEyMw=="
	vectorDestination  = "EQBAjaOyi2wGWlk-EDkSabqqnF-MrrwMadnwqrurKpkla9nE"
	vectorAmount       = int64(1_234_567_890)
	vectorInternalHash = "0d62da390c9c2234d1712ad5acf68f418dd5b4187184f68d2757f10d9250016c"
	// tonutils' DEFAULT_SENDMODE: pay fees separately, ignore errors.
	vectorSendMode = 3
)

func TestFromMnemonicMatchesForkVectors(t *testing.T) {
	for version, want := range vectors {
		t.Run(version, func(t *testing.T) {
			w, err := FromMnemonic(testMnemonic, version)
			if err != nil {
				t.Fatal(err)
			}
			if w.Address() != want.friendly {
				t.Fatalf("address = %s, want %s", w.Address(), want.friendly)
			}
			account, err := w.Account()
			if err != nil {
				t.Fatal(err)
			}
			if account.Address != want.raw || account.PublicKey != vectorPublicKey || account.Chain != "-239" {
				t.Fatalf("account = %+v", account)
			}
			stateInit, err := DecodeCell(account.WalletStateInit)
			if err != nil {
				t.Fatal(err)
			}
			// BOC byte order differs between libraries; the cell hash does
			// not, and it is what the address commits to.
			if got := hex.EncodeToString(stateInit.Hash()); got != want.stateInitHash {
				t.Fatalf("state init hash = %s, want %s", got, want.stateInitHash)
			}
			if strings.Contains(w.String(), strings.Fields(testMnemonic)[0]) {
				t.Fatal("String must not print the mnemonic")
			}
		})
	}
}

func TestFromMnemonicRejectsBadInput(t *testing.T) {
	cases := map[string]struct{ mnemonic, version string }{
		"short":   {"one two three", VersionV5R1},
		"garbage": {strings.Repeat("abandon ", 24), VersionV5R1},
		"version": {testMnemonic, "V3R2"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := FromMnemonic(test.mnemonic, test.version); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

func TestSignBuildsVerifiableMessage(t *testing.T) {
	for _, version := range []string{VersionV4R2, VersionV5R1} {
		t.Run(version, func(t *testing.T) {
			w, err := FromMnemonic(testMnemonic, version)
			if err != nil {
				t.Fatal(err)
			}
			signed, err := w.Sign([]Transfer{{
				Destination: vectorDestination, AmountNano: vectorAmount, PayloadBOC: vectorPayload,
			}}, 42, true)
			if err != nil {
				t.Fatal(err)
			}
			if signed.Seqno != 42 || len(signed.Hash) != 64 {
				t.Fatalf("signed = %+v", signed)
			}
			root, err := DecodeCell(signed.BOC)
			if err != nil {
				t.Fatal(err)
			}
			var external tlb.ExternalMessage
			if err := tlb.LoadFromCell(&external, root.MustBeginParse()); err != nil {
				t.Fatal(err)
			}
			if external.StateInit != nil {
				t.Fatal("a deployed wallet must not resend its state init")
			}
			if external.DstAddr.StringRaw() != vectors[version].raw {
				t.Fatalf("external destination = %s", external.DstAddr.StringRaw())
			}
			if hex.EncodeToString(external.NormalizedHash()) != signed.Hash {
				t.Fatal("hash is not the normalized external message hash")
			}
			seqno, mode, internal := inspectBody(t, version, external.Body)
			if seqno != 42 {
				t.Fatalf("seqno = %d", seqno)
			}
			if mode != vectorSendMode {
				t.Fatalf("send mode = %d, want %d", mode, vectorSendMode)
			}
			if got := hex.EncodeToString(internal.Hash()); got != vectorInternalHash {
				t.Fatalf("internal message hash = %s, want the fork's %s", got, vectorInternalHash)
			}
		})
	}
}

func TestSignUndeployedWalletCarriesStateInit(t *testing.T) {
	w, err := FromMnemonic(testMnemonic, VersionV5R1)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := w.Sign([]Transfer{{Destination: vectorDestination, AmountNano: 1}}, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := DecodeCell(signed.BOC)
	var external tlb.ExternalMessage
	if err := tlb.LoadFromCell(&external, root.MustBeginParse()); err != nil {
		t.Fatal(err)
	}
	if external.StateInit == nil {
		t.Fatal("the first payment from a fresh wallet has to deploy it")
	}
}

func TestSignRefusesTooManyMessages(t *testing.T) {
	w, _ := FromMnemonic(testMnemonic, VersionV4R2)
	transfers := make([]Transfer, 5)
	for index := range transfers {
		transfers[index] = Transfer{Destination: vectorDestination, AmountNano: 1}
	}
	if _, err := w.Sign(transfers, 1, true); !errors.Is(err, ErrTooManyMessages) {
		t.Fatalf("err = %v", err)
	}
}

// inspectBody verifies the signature and returns the seqno, the send mode, and
// the single internal message a wallet body carries.
func inspectBody(t *testing.T, version string, body *cell.Cell) (uint64, uint64, *cell.Cell) {
	t.Helper()
	public, _ := hex.DecodeString(vectorPublicKey)
	slice := body.MustBeginParse()
	switch version {
	case VersionV4R2:
		signature := loadBits(t, slice, 512)
		signedPart := slice.Copy()
		if !ed25519.Verify(public, signedPart.MustToCell().Hash(), signature) {
			t.Fatal("V4R2 signature does not verify")
		}
		mustLoadUint(t, slice, 32) // subwallet
		mustLoadUint(t, slice, 32) // valid until
		seqno := mustLoadUint(t, slice, 32)
		if op := mustLoadUint(t, slice, 8); op != 0 {
			t.Fatalf("op = %d", op)
		}
		mode := mustLoadUint(t, slice, 8)
		internal, err := slice.LoadRefCell()
		if err != nil {
			t.Fatal(err)
		}
		return seqno, mode, internal
	default:
		// W5 puts the signature last and signs everything before it,
		// references included.
		bits := slice.BitsLeft()
		data := slice.Copy()
		signedBits := loadBits(t, data, bits-512)
		signature := loadBits(t, data, 512)
		builder := cell.BeginCell().MustStoreSlice(signedBits, bits-512)
		for index := 0; index < int(body.RefsNum()); index++ {
			builder.MustStoreRef(body.MustPeekRef(index))
		}
		if !ed25519.Verify(public, builder.EndCell().Hash(), signature) {
			t.Fatal("V5R1 signature does not verify")
		}
		if op := mustLoadUint(t, slice, 32); op != 0x7369676e {
			t.Fatalf("op = %x", op)
		}
		mustLoadUint(t, slice, 32) // wallet id
		mustLoadUint(t, slice, 32) // valid until
		seqno := mustLoadUint(t, slice, 32)
		actions, err := slice.LoadMaybeRef()
		if err != nil || actions == nil {
			t.Fatalf("actions: %v", err)
		}
		if tag := mustLoadUint(t, actions, 32); tag != 0x0ec3c86d {
			t.Fatalf("action tag = %x", tag)
		}
		mode := mustLoadUint(t, actions, 8)
		if _, err := actions.LoadRef(); err != nil { // previous action list
			t.Fatal(err)
		}
		internal, err := actions.LoadRefCell()
		if err != nil {
			t.Fatal(err)
		}
		return seqno, mode, internal
	}
}

func loadBits(t *testing.T, slice *cell.Slice, bits uint) []byte {
	t.Helper()
	value, err := slice.LoadSlice(bits)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustLoadUint(t *testing.T, slice *cell.Slice, bits uint) uint64 {
	t.Helper()
	value, err := slice.LoadUInt(bits)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDecodeCellAcceptsURLSafeUnpadded(t *testing.T) {
	urlSafe := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(vectorPayload), "=")
	first, err := DecodeCell(vectorPayload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeCell(urlSafe)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(first.Hash()) != hex.EncodeToString(second.Hash()) {
		t.Fatal("encodings decoded differently")
	}
	if _, err := base64.StdEncoding.DecodeString(vectorPayload); err != nil {
		t.Fatal(err)
	}
}
