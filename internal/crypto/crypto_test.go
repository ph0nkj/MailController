package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey() []byte {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	_, err := New(make([]byte, 16))
	require.Error(t, err)

	_, err = New(nil)
	require.Error(t, err)
}

func TestRoundTrip(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)

	plaintext := []byte("refresh-token-like-secret")
	sealed, err := c.Encrypt(plaintext)
	require.NoError(t, err)
	require.NotEqual(t, plaintext, sealed)
	require.Greater(t, len(sealed), len(plaintext))

	opened, err := c.Decrypt(sealed)
	require.NoError(t, err)
	require.Equal(t, plaintext, opened)
}

func TestRoundTripString(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)

	sealed, err := c.EncryptString("api-key")
	require.NoError(t, err)
	opened, err := c.DecryptString(sealed)
	require.NoError(t, err)
	require.Equal(t, "api-key", opened)
}

func TestEncryptUsesUniqueNonces(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)

	a, err := c.Encrypt([]byte("same"))
	require.NoError(t, err)
	b, err := c.Encrypt([]byte("same"))
	require.NoError(t, err)
	require.False(t, bytes.Equal(a, b))
}

func TestDecryptRejectsTampering(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)

	sealed, err := c.Encrypt([]byte("secret"))
	require.NoError(t, err)
	sealed[len(sealed)-1] ^= 0xff

	_, err = c.Decrypt(sealed)
	require.Error(t, err)
}

func TestDecryptRejectsShortValue(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)

	_, err = c.Decrypt([]byte("short"))
	require.Error(t, err)
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	c, err := New(testKey())
	require.NoError(t, err)
	sealed, err := c.Encrypt([]byte("secret"))
	require.NoError(t, err)

	otherKey := testKey()
	otherKey[0] ^= 0xff
	other, err := New(otherKey)
	require.NoError(t, err)

	_, err = other.Decrypt(sealed)
	require.Error(t, err)
}

func TestNilCipher(t *testing.T) {
	var c *Cipher
	_, err := c.Encrypt([]byte("x"))
	require.Error(t, err)
	_, err = c.Decrypt([]byte("x"))
	require.Error(t, err)
}
