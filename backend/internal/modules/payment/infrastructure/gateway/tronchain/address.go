package tronchain

import (
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"strings"
)

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func hexString(b []byte) string { return hex.EncodeToString(b) }
func decodeAddress(s string) ([]byte, bool) {
	if len(s) != 34 || s[0] != 'T' {
		return nil, false
	}
	n := new(big.Int)
	for _, char := range s {
		v := strings.IndexRune(alphabet, char)
		if v < 0 {
			return nil, false
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(v)))
	}
	raw := n.Bytes()
	if len(raw) != 25 || raw[0] != 0x41 {
		return nil, false
	}
	one := sha256.Sum256(raw[:21])
	two := sha256.Sum256(one[:])
	if string(raw[21:]) != string(two[:4]) || encodeAddress(raw[:21]) != s {
		return nil, false
	}
	return raw[:21], true
}
func encodeAddress(raw []byte) string {
	one := sha256.Sum256(raw)
	two := sha256.Sum256(one[:])
	data := append(append([]byte{}, raw...), two[:4]...)
	n := new(big.Int).SetBytes(data)
	base := big.NewInt(58)
	rem := new(big.Int)
	out := []byte{}
	for n.Sign() > 0 {
		n.QuoRem(n, base, rem)
		out = append(out, alphabet[rem.Int64()])
	}
	for _, b := range data {
		if b != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
func NormalizeAddress(s string) (string, bool) {
	raw, ok := decodeAddress(s)
	if !ok || hexString(raw[1:]) == strings.Repeat("0", 40) {
		return "", false
	}
	return s, true
}
func NormalizeTxID(s string) (string, bool) {
	if len(s) != 64 {
		return "", false
	}
	_, e := hex.DecodeString(s)
	return strings.ToLower(s), e == nil
}
