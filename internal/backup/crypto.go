package backup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
)

// Backups are encrypted with AES-256-GCM in fixed-size frames rather than as one
// sealed blob, so a multi-gigabyte dump never has to fit in memory.
//
// File layout:
//
//	magic       5 bytes  "OFBK1"
//	base nonce 12 bytes  random per file
//	frames               repeated: uint32 big-endian ciphertext length, ciphertext
//
// Every frame is sealed with a nonce derived from the base nonce and the frame
// index, and authenticates its own index plus a final-frame flag. A reordered,
// duplicated, dropped, or truncated frame therefore fails to open instead of
// producing a plausible-looking partial dump.
const (
	magic      = "OFBK1"
	frameSize  = 1 << 20
	nonceSize  = 12
	lengthSize = 4
	// maxFrame bounds an attacker-supplied frame length so a corrupted header
	// cannot make the reader allocate an arbitrary buffer.
	maxFrame = frameSize + 64*1024
)

var (
	ErrBadFormat = errors.New("backup file is not an Omniflow encrypted backup")
	ErrCorrupt   = errors.New("backup file failed authentication")
)

// Encrypt streams plaintext from source into destination.
func Encrypt(destination io.Writer, source io.Reader, key []byte) error {
	sealer, err := newAEAD(key)
	if err != nil {
		return err
	}
	base := make([]byte, nonceSize)
	if _, err = rand.Read(base); err != nil {
		return err
	}
	if _, err = destination.Write([]byte(magic)); err != nil {
		return err
	}
	if _, err = destination.Write(base); err != nil {
		return err
	}
	plaintext := make([]byte, frameSize)
	header := make([]byte, lengthSize)
	var index uint64
	for {
		read, readErr := io.ReadFull(source, plaintext)
		final := errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)
		if readErr != nil && !final {
			return readErr
		}
		sealed := sealer.Seal(nil, frameNonce(base, index), plaintext[:read], frameAAD(index, final))
		binary.BigEndian.PutUint32(header, uint32(len(sealed)))
		if _, err = destination.Write(header); err != nil {
			return err
		}
		if _, err = destination.Write(sealed); err != nil {
			return err
		}
		if final {
			return nil
		}
		index++
	}
}

// Decrypt streams plaintext from an encrypted backup into destination.
func Decrypt(destination io.Writer, source io.Reader, key []byte) error {
	opener, err := newAEAD(key)
	if err != nil {
		return err
	}
	prologue := make([]byte, len(magic)+nonceSize)
	if _, err = io.ReadFull(source, prologue); err != nil {
		return ErrBadFormat
	}
	if string(prologue[:len(magic)]) != magic {
		return ErrBadFormat
	}
	base := prologue[len(magic):]
	header := make([]byte, lengthSize)
	var index uint64
	for {
		if _, err = io.ReadFull(source, header); err != nil {
			// A well-formed file always ends immediately after its final frame,
			// which the loop below returns on. Reaching here means truncation.
			return ErrCorrupt
		}
		length := binary.BigEndian.Uint32(header)
		if length == 0 || length > maxFrame {
			return ErrCorrupt
		}
		sealed := make([]byte, length)
		if _, err = io.ReadFull(source, sealed); err != nil {
			return ErrCorrupt
		}
		nonce := frameNonce(base, index)
		plaintext, openErr := opener.Open(nil, nonce, sealed, frameAAD(index, false))
		if openErr != nil {
			final, finalErr := opener.Open(nil, nonce, sealed, frameAAD(index, true))
			if finalErr != nil {
				return ErrCorrupt
			}
			if _, err = destination.Write(final); err != nil {
				return err
			}
			return nil
		}
		if _, err = destination.Write(plaintext); err != nil {
			return err
		}
		index++
	}
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("backup encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// frameNonce mixes the frame index into the file's base nonce. Frames within one
// file therefore never reuse a nonce, and two files never share one because the
// base is random per file.
func frameNonce(base []byte, index uint64) []byte {
	nonce := make([]byte, nonceSize)
	copy(nonce, base)
	counter := make([]byte, 8)
	binary.BigEndian.PutUint64(counter, index)
	for offset := range counter {
		nonce[nonceSize-8+offset] ^= counter[offset]
	}
	return nonce
}

// frameAAD binds each frame to its position and to whether it ends the file.
func frameAAD(index uint64, final bool) []byte {
	aad := make([]byte, 9)
	binary.BigEndian.PutUint64(aad, index)
	if final {
		aad[8] = 1
	}
	return aad
}
