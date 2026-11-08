// Package station drives a rooted Yandex Station 2 over SSH.
package station

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Runner executes one shell command on the speaker and returns its stdout.
// Everything the service does to the station goes through it, so tests swap in a fake.
type Runner interface {
	Run(ctx context.Context, cmd string) (string, error)
	// RunStdin is Run with data on the command's stdin (the SSH command line is capped at 9000 bytes).
	RunStdin(ctx context.Context, cmd string, stdin []byte) (string, error)
}

// SSHConfig describes the SSH route to the speaker.
type SSHConfig struct {
	Addr        string // host:port, e.g. 192.168.1.100:2222
	User        string // root
	KeyFile     string // private key, ed25519
	HostKeySHA  string // "SHA256:..." fingerprint of the speaker's dropbear host key
	DialTimeout time.Duration
}

// SSHRunner opens one short-lived SSH connection per command: calls are rare and a
// connection costs a few hundred ms, so there is no pool to keep healthy.
type SSHRunner struct {
	cfg    SSHConfig
	signer ssh.Signer
}

// NewSSHRunner loads the key and validates the config. The host key fingerprint is mandatory:
// the speaker root login has an empty password, so we must not talk to an impostor.
func NewSSHRunner(cfg SSHConfig) (*SSHRunner, error) {
	if cfg.Addr == "" || cfg.User == "" || cfg.KeyFile == "" {
		return nil, errors.New("station ssh: addr, user and key file are required")
	}
	if !strings.HasPrefix(cfg.HostKeySHA, "SHA256:") {
		return nil, errors.New("station ssh: host key fingerprint (SHA256:...) is required")
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	pem, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("station ssh: read key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("station ssh: parse key: %w", err)
	}
	return &SSHRunner{cfg: cfg, signer: signer}, nil
}

func fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Run implements Runner. The context bounds the whole call, connect included.
func (r *SSHRunner) Run(ctx context.Context, cmd string) (string, error) {
	return r.RunStdin(ctx, cmd, nil)
}

// RunStdin implements Runner.
func (r *SSHRunner) RunStdin(ctx context.Context, cmd string, stdin []byte) (string, error) {
	d := net.Dialer{Timeout: r.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", r.cfg.Addr)
	if err != nil {
		return "", fmt.Errorf("station unreachable: %w", err)
	}
	cc := &ssh.ClientConfig{
		User: r.cfg.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(r.signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if got := fingerprint(key); got != r.cfg.HostKeySHA {
				return fmt.Errorf("host key mismatch: got %s", got)
			}
			return nil
		},
		Timeout: r.cfg.DialTimeout,
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, r.cfg.Addr, cc)
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("station ssh handshake: %w", err)
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("station ssh session: %w", err)
	}
	defer sess.Close()
	var stdout, stderr bytes.Buffer
	sess.Stdout, sess.Stderr = &stdout, &stderr
	if stdin != nil {
		sess.Stdin = bytes.NewReader(stdin)
	}

	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()
	select {
	case <-ctx.Done():
		_ = client.Close()
		return stdout.String(), ctx.Err()
	case err := <-done:
		if err != nil {
			return stdout.String(), fmt.Errorf("station command failed: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
}
