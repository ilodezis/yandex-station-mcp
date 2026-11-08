package station_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ilodezis/yandex-station-mcp/internal/station"
)

// fakeDropbear is an in-process SSH server that records every exec command byte for byte.
type fakeDropbear struct {
	addr     string
	hostFP   string
	clientFP string
	mu       sync.Mutex
	cmds     []string
	stdins   [][]byte
}

// maxCommandLen mirrors dropbear (MAX_CMD_LEN): a longer command line closes the channel with a bare EOF.
const maxCommandLen = 9000

func (d *fakeDropbear) inputs() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([][]byte(nil), d.stdins...)
}

func (d *fakeDropbear) commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.cmds...)
}

func startFakeDropbear(t *testing.T, authorized ssh.PublicKey) *fakeDropbear {
	t.Helper()
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDropbear{}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(authorized.Marshal()) {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d.addr = ln.Addr().String()
	d.hostFP = ssh.FingerprintSHA256(hostSigner.PublicKey())
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(c, cfg)
		}
	}()
	return d
}

func (d *fakeDropbear) serve(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					_ = r.Reply(false, nil)
					continue
				}
				n := binary.BigEndian.Uint32(r.Payload[:4])
				if n > maxCommandLen {
					_ = r.Reply(true, nil)
					return
				}
				cmd := string(r.Payload[4 : 4+n])
				_ = r.Reply(true, nil)
				in, _ := io.ReadAll(ch)
				d.mu.Lock()
				d.cmds = append(d.cmds, cmd)
				d.stdins = append(d.stdins, in)
				d.mu.Unlock()
				// The "speaker" answers with the bytes it received, like `printf %s '<text>'` would.
				_, _ = ch.Write([]byte(cmd))
				if strings.Contains(cmd, "FAILME") {
					_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 3})
				} else {
					_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
				}
				return
			}
		}()
	}
}

func writeKey(t *testing.T) (path string, pub ssh.PublicKey) {
	t.Helper()
	pubK, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pubK)
	if err != nil {
		t.Fatal(err)
	}
	return path, pk
}

func TestSSHRunnerCarriesCyrillicUnchanged(t *testing.T) {
	keyPath, pub := writeKey(t)
	srv := startFakeDropbear(t, pub)
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: srv.addr, User: "root", KeyFile: keyPath, HostKeySHA: srv.hostFP})
	if err != nil {
		t.Fatal(err)
	}
	st := station.New(r, station.DefaultConfig())
	const phrase = "Сессия ждёт ответа: проверь, пожалуйста — ёжик, щука, Ъ. It's fine."
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.Say(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	got := srv.commands()
	if len(got) != 1 {
		t.Fatalf("commands = %q", got)
	}
	want := "say " + station.Quote(phrase)
	if got[0] != want {
		t.Errorf("bytes changed on the way:\n got %q\nwant %q", got[0], want)
	}
	// The same bytes must also survive the way back (stdout of the "speaker", which echoes the command).
	out, err := r.Run(ctx, "printf %s "+station.Quote(phrase))
	if err != nil || out != "printf %s "+station.Quote(phrase) {
		t.Errorf("echo %q, %v", out, err)
	}
}

func TestSSHRunnerRefusesWrongHostKey(t *testing.T) {
	keyPath, pub := writeKey(t)
	srv := startFakeDropbear(t, pub)
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: srv.addr, User: "root", KeyFile: keyPath,
		HostKeySHA: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "say 'x'"); err == nil || !strings.Contains(err.Error(), "host key") {
		t.Fatalf("want host key refusal, got %v", err)
	}
	if len(srv.commands()) != 0 {
		t.Error("no command may run on an unverified host")
	}
}

func TestSSHRunnerRefusesUnknownKey(t *testing.T) {
	keyPath, _ := writeKey(t)
	_, otherPub := writeKey(t)
	srv := startFakeDropbear(t, otherPub)
	r, _ := station.NewSSHRunner(station.SSHConfig{Addr: srv.addr, User: "root", KeyFile: keyPath, HostKeySHA: srv.hostFP})
	if _, err := r.Run(context.Background(), "echo hi"); err == nil {
		t.Fatal("an unauthorized key must fail")
	}
}

func TestSSHRunnerReportsFailureAndUnreachable(t *testing.T) {
	keyPath, pub := writeKey(t)
	srv := startFakeDropbear(t, pub)
	r, _ := station.NewSSHRunner(station.SSHConfig{Addr: srv.addr, User: "root", KeyFile: keyPath, HostKeySHA: srv.hostFP})
	if _, err := r.Run(context.Background(), "FAILME"); err == nil {
		t.Error("a non-zero exit must be an error")
	}
	dead, _ := station.NewSSHRunner(station.SSHConfig{Addr: "127.0.0.1:1", User: "root", KeyFile: keyPath, HostKeySHA: srv.hostFP, DialTimeout: time.Second})
	if _, err := dead.Run(context.Background(), "echo hi"); err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("want unreachable, got %v", err)
	}
}

func TestNewSSHRunnerValidatesConfig(t *testing.T) {
	keyPath, _ := writeKey(t)
	for name, cfg := range map[string]station.SSHConfig{
		"no host key":  {Addr: "a:1", User: "root", KeyFile: keyPath},
		"bad host key": {Addr: "a:1", User: "root", KeyFile: keyPath, HostKeySHA: "MD5:xx"},
		"no key file":  {Addr: "a:1", User: "root", HostKeySHA: "SHA256:x"},
		"missing key":  {Addr: "a:1", User: "root", KeyFile: filepath.Join(t.TempDir(), "none"), HostKeySHA: "SHA256:x"},
	} {
		if _, err := station.NewSSHRunner(cfg); err == nil {
			t.Errorf("%s must fail", name)
		}
	}
}

// The sound is a wav of tens of kilobytes. It must travel on stdin: as part of the command line it
// is over dropbear's 9000-byte limit and the speaker answers with a bare EOF.
func TestSSHRunnerSendsSoundOnStdin(t *testing.T) {
	keyPath, pub := writeKey(t)
	srv := startFakeDropbear(t, pub)
	r, err := station.NewSSHRunner(station.SSHConfig{Addr: srv.addr, User: "root", KeyFile: keyPath, HostKeySHA: srv.hostFP})
	if err != nil {
		t.Fatal(err)
	}
	st := station.New(r, station.DefaultConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, name := range station.SoundNames() {
		if err := st.PlaySound(ctx, name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := st.Timer(ctx, time.Minute, "чай готов"); err != nil {
		t.Fatalf("timer: %v", err)
	}
	cmds, ins := srv.commands(), srv.inputs()
	if len(cmds) != len(station.SoundNames())+1 || len(ins) != len(cmds) {
		t.Fatalf("commands %d, inputs %d", len(cmds), len(ins))
	}
	for i, in := range ins {
		if len(in) < 5000 || string(in[:4]) != "RIFF" {
			t.Errorf("command %d (%.20q): stdin is %d bytes, starts %.4q; want a wav", i, cmds[i], len(in), in)
		}
	}
	// plain commands still go with an empty stdin
	if _, err := r.Run(ctx, "echo hi"); err != nil {
		t.Fatal(err)
	}
	if last := srv.inputs(); len(last[len(last)-1]) != 0 {
		t.Errorf("Run must send no stdin")
	}
}
