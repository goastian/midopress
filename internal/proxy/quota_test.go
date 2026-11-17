package proxy

import (
	"bytes"
	"io"
	"net"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goastian/midopress/internal/quota"
)

func quotaLease(t *testing.T, bytes uint64) (*quota.Ledger, *quota.Lease) {
	t.Helper()
	ledger, err := quota.Open(filepath.Join(t.TempDir(), "usage.json"), time.Hour, quota.Limits{Bytes: bytes}, nil)
	require.NoError(t, err)
	t.Cleanup(ledger.Close)
	lease, err := ledger.Begin("fixture")
	require.NoError(t, err)
	t.Cleanup(lease.Close)
	return ledger, lease
}

type readSignalConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *readSignalConn) Read(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(b)
}

func TestQuotaPendingReadDoesNotConsumeDuplexBudget(t *testing.T) {
	ledger, lease := quotaLease(t, 64)
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	signal := &readSignalConn{Conn: server, started: make(chan struct{})}
	conn := &quotaConn{Conn: signal, lease: lease, allowed: func() bool { return true }}
	defer func() { _ = conn.Close() }()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = conn.Read(make([]byte, 32<<10)) }()
	<-signal.started
	assert.Zero(t, ledger.View("fixture").Bytes, "an idle read cannot reserve the peer's write allowance")
	received := make(chan []byte, 1)
	go func() {
		b := make([]byte, 5)
		_, err := io.ReadFull(client, b)
		if err != nil {
			received <- nil
			return
		}
		received <- b
	}()
	n, err := conn.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	select {
	case b := <-received:
		assert.Equal(t, []byte("hello"), b)
	case <-time.After(time.Second):
		t.Fatal("server greeting blocked behind idle client read")
	}
	assert.Equal(t, uint64(5), ledger.View("fixture").Bytes)
	require.NoError(t, conn.Close())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending quota read did not stop")
	}
}

func TestQuotaReaderAndWriterAcceptOnlyRemainingBytes(t *testing.T) {
	ledger, lease := quotaLease(t, 10)
	reader := &quotaReader{ReadCloser: io.NopCloser(bytes.NewReader([]byte("123456789012345"))), lease: lease}
	b := make([]byte, 32)
	n, err := reader.Read(b)
	assert.ErrorIs(t, err, quota.ErrBytes)
	assert.Equal(t, 10, n)
	assert.Equal(t, "1234567890", string(b[:n]))
	assert.Equal(t, uint64(10), ledger.View("fixture").Bytes)
	_, err = reader.Read(b)
	assert.Error(t, err)

	ledger, lease = quotaLease(t, 10)
	out := httptest.NewRecorder()
	writer := &quotaWriter{ResponseWriter: out, lease: lease}
	n, err = writer.Write([]byte("123456789012345"))
	assert.ErrorIs(t, err, quota.ErrBytes)
	assert.Equal(t, 10, n)
	assert.Equal(t, "1234567890", out.Body.String())
	assert.Equal(t, uint64(10), ledger.View("fixture").Bytes)
}

func TestQuotaClosedOrRevokedTunnelCannotDeliverBytes(t *testing.T) {
	ledger, lease := quotaLease(t, 64)
	server, client := net.Pipe()
	defer func() { _ = client.Close() }()
	conn := &quotaConn{Conn: server, lease: lease, allowed: func() bool { return false }}
	_, err := conn.Write([]byte("fixture"))
	assert.ErrorIs(t, err, quota.ErrUnavailable)
	assert.Zero(t, ledger.View("fixture").Bytes)
	assert.Zero(t, ledger.View("fixture").Active)
	_, err = client.Read(make([]byte, 1))
	assert.ErrorIs(t, err, io.EOF)
}
