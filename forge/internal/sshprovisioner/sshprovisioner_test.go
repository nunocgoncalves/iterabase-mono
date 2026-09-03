package sshprovisioner

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/nunocgoncalves/iterabase-mono/forge/internal/config"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/deployer"
	"github.com/nunocgoncalves/iterabase-mono/forge/internal/provisioner"
)

// handler returns canned stdout + exit code for a given remote command.
type handler func(cmd string) (string, int)

type sshCommandResult struct {
	stdout string
	stderr string
	code   int
}

type resultHandler func(cmd string) sshCommandResult

func TestHelmCmdUsesRootOwnedRegistryConfig(t *testing.T) {
	command := helmCmd("show", "chart", "oci://ghcr.io/example/private/chart", "--version", "1.2.3")
	assert.Contains(t, command, "'sudo' 'helm'")
	assert.Contains(t, command, "'--registry-config' '/etc/forge/helm-registry.json'")
	assert.Contains(t, command, "'--kubeconfig' '/etc/rancher/k3s/k3s.yaml'")
}

func TestConfiguredHostKeyCallbackPinsExactKey(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	callback, algorithms, err := configuredHostKey(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	require.NoError(t, err)
	require.Equal(t, []string{ssh.KeyAlgoED25519}, algorithms)
	require.NoError(t, callback("fixture", &net.TCPAddr{}, key))

	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, err := ssh.NewPublicKey(otherPublic)
	require.NoError(t, err)
	require.Error(t, callback("fixture", &net.TCPAddr{}, other))
}

func TestConfiguredHostKeyCallbackRejectsMalformedPin(t *testing.T) {
	_, _, err := configuredHostKey("not-an-openssh-key")
	require.ErrorContains(t, err, "parse pinned SSH host key")
}

// startFakeSSH starts an in-process SSH server accepting a single test key.
// It returns the server address, a client config usable to connect, and a
// cleanup func. The handler is invoked for each "exec" request.
func startFakeSSH(t *testing.T, h handler) (string, *ssh.ClientConfig, func()) {
	t.Helper()
	return startFakeSSHWithResult(t, func(cmd string) sshCommandResult {
		stdout, code := h(cmd)
		return sshCommandResult{stdout: stdout, code: code}
	})
}

func startFakeSSHWithResult(t *testing.T, h resultHandler) (string, *ssh.ClientConfig, func()) {
	t.Helper()
	hostPub, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	require.NoError(t, err)
	hostSSHPub, err := ssh.NewPublicKey(hostPub)
	require.NoError(t, err)

	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	clientSigner, err := ssh.NewSignerFromKey(clientPriv)
	require.NoError(t, err)
	authorized, err := ssh.NewPublicKey(clientPub)
	require.NoError(t, err)
	authorizedBytes := authorized.Marshal()

	srvCfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, pk ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(pk.Marshal(), authorizedBytes) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	srvCfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				serveConn(c, srvCfg, h)
			}(conn)
		}
	}()

	clientCfg := &ssh.ClientConfig{
		User:            "forge",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientSigner)},
		HostKeyCallback: ssh.FixedHostKey(hostSSHPub),
	}
	cleanup := func() {
		_ = ln.Close()
		wg.Wait()
	}
	return ln.Addr().String(), clientCfg, cleanup
}

func serveConn(conn net.Conn, srvCfg *ssh.ServerConfig, h resultHandler) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, srvCfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		go serveSession(ch, h)
	}
}

func serveSession(ch ssh.NewChannel, h resultHandler) {
	sch, reqs, err := ch.Accept()
	if err != nil {
		return
	}
	defer sch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		cmd := parseExecPayload(req.Payload)
		result := h(cmd)
		_ = req.Reply(true, nil)
		// Commands that read stdin (runStdin) must drain stdin before exit-status
		// so the client's write completes cleanly: "cat > file" (overlay git cred)
		// and "kubectl apply -f -" (secret-sync; the '-' arg is the stdin marker).
		if strings.Contains(cmd, "cat >") || strings.Contains(cmd, "'-'") {
			_, _ = io.Copy(io.Discard, sch)
		}
		if result.stdout != "" {
			_, _ = sch.Write([]byte(result.stdout))
		}
		if result.stderr != "" {
			_, _ = sch.Stderr().Write([]byte(result.stderr))
		}
		exit := make([]byte, 4)
		binary.BigEndian.PutUint32(exit, uint32(result.code))
		_, _ = sch.SendRequest("exit-status", false, exit)
		_ = sch.Close()
		return
	}
}

func parseExecPayload(p []byte) string {
	if len(p) < 4 {
		return ""
	}
	n := binary.BigEndian.Uint32(p[:4])
	if int(n) > len(p)-4 {
		return ""
	}
	return string(p[4 : 4+n])
}

// newProvisioner builds an SSHProvisioner wired to the fake server.
func newProvisioner(t *testing.T, addr string, clientCfg *ssh.ClientConfig) *SSHProvisioner {
	t.Helper()
	p, err := New(config.Host{Address: addr, SSHUser: "forge", SSHKeyPath: "/dev/null"},
		WithSSHConfig(clientCfg),
		WithDial(func(_ context.Context, _, _ string, _ *ssh.ClientConfig) (*ssh.Client, error) {
			return ssh.Dial("tcp", addr, clientCfg)
		}),
	)
	require.NoError(t, err)
	return p
}

func TestPreflight(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch cmd {
		case "cat /etc/os-release":
			return "PRETTY_NAME=\"Ubuntu 24.04 LTS\"\nNAME=\"Ubuntu\"\n", 0
		case "sudo -n true":
			return "", 0
		case "command -v curl":
			return "/usr/bin/curl\n", 0
		case "pidof systemd":
			return "1\n", 0
		case "command -v k3s":
			return "/usr/local/bin/k3s\n", 0
		case "ip -6 addr show scope global":
			return "1: lo ...\n", 0
		case "grep -qi 0x10de /sys/bus/pci/devices/*/vendor":
			return "", 0
		case "test -f /lib/modules/$(uname -r)/build/Makefile":
			return "", 0
		case "command -v dkms":
			return "/usr/sbin/dkms\n", 0
		case "command -v gcc":
			return "/usr/bin/gcc\n", 0
		case "command -v make":
			return "/usr/bin/make\n", 0
		case "command -v iscsiadm >/dev/null && systemctl is-active --quiet iscsid":
			return "", 0
		case "command -v mount.nfs >/dev/null":
			return "", 0
		case "findmnt -n -o PROPAGATION / | grep -Eq '(^|,)r?shared(,|$)'":
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	r, err := p.Preflight(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Ubuntu 24.04 LTS", r.OS)
	assert.True(t, r.HasSudo)
	assert.True(t, r.HasCurl)
	assert.True(t, r.HasSystemd)
	assert.True(t, r.Installed)
	assert.True(t, r.HasIPv6)
	assert.True(t, r.HasNVIDIAGPU)
	assert.True(t, r.KernelHeadersInstalled)
	assert.True(t, r.HasDKMS)
	assert.True(t, r.HasGCC)
	assert.True(t, r.HasMake)
}

func TestPreflightRequiresReadableOperatingSystemIdentity(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		exitStatus int
		want       string
	}{
		{name: "probe failure", exitStatus: 1, want: "inspect operating system: ssh run"},
		{name: "missing pretty name", output: "NAME=Ubuntu\n", want: "has no PRETTY_NAME"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
				if cmd == "cat /etc/os-release" {
					return tt.output, tt.exitStatus
				}
				return "", 1
			})
			defer cleanup()

			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			result, err := p.Preflight(context.Background())
			require.Nil(t, result)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestInstall_CommandShape(t *testing.T) {
	const installerPath = "/tmp/forge-k3s-installer.test"
	var got string
	sawChecksum := false
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "mktemp /tmp/forge-k3s-installer.XXXXXX":
			return installerPath + "\n", 0
		case strings.HasPrefix(cmd, "curl -fsSL"):
			assert.Contains(t, cmd, shellQuote(k3sInstallScriptURL))
			return "", 0
		case strings.Contains(cmd, "sha256sum --check --status"):
			sawChecksum = true
			assert.Contains(t, cmd, k3sInstallScriptSHA256)
			return "", 0
		case strings.HasPrefix(cmd, "sudo env INSTALL_K3S_VERSION="):
			got = cmd
			return "", 0
		case cmd == "rm -f "+shellQuote(installerPath):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	args := []string{"server", "--cluster-cidr", "10.42.0.0/16,fd42::/48", "--disable", "traefik"}
	err := p.Install(context.Background(), "v1.31.5", args)
	require.NoError(t, err)
	assert.True(t, sawChecksum)
	assert.Contains(t, got, "INSTALL_K3S_VERSION='v1.31.5+k3s1'")
	assert.Contains(t, got, "sh '"+installerPath+"'")
	assert.Contains(t, got, "'server'")
	assert.Contains(t, got, "'--cluster-cidr'")
	assert.Contains(t, got, "'10.42.0.0/16,fd42::/48'")
}

func TestFetchKubeconfig(t *testing.T) {
	want := "apiVersion: v1\nclusters:\n- name: opo1\n"
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if cmd == "sudo cat /etc/rancher/k3s/k3s.yaml" {
			return want, 0
		}
		return "", 1
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	got, err := p.FetchKubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, string(got))
}

func TestReadState_Installed(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch cmd {
		case "command -v k3s":
			return "/usr/local/bin/k3s\n", 0
		case "sudo k3s --version":
			return "k3s version v1.31.5+k3s1 (abcdef)\n", 0
		case "sudo cat /etc/systemd/system/k3s.service":
			// Mirrors the exact unit the k3s install script (get.k3s.io) writes:
			// each ExecStart arg on its own backslash-continued line, single-quoted
			// by the script's quote() helper.
			return "[Unit]\nDescription=Lightweight Kubernetes\n[Service]\nExecStart=/usr/local/bin/k3s \\\n    server \\\n\t'--cluster-cidr' \\\n\t'10.42.0.0/16,fd42::/48' \\\n\t'--service-cidr' \\\n\t'10.43.0.0/16,fd43::/112' \\\n\t'--flannel-backend=vxlan' \\\n\t'--tls-san' \\\n\t'10.20.0.10' \\\n\t'--disable' \\\n\t'traefik' \\\n\t'--disable' \\\n\t'servicelb' \\\n\t'--write-kubeconfig-mode' \\\n\t'0600' \\\n[Install]\n", 0
		default:
			return "", 1
		}
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	st, err := p.ReadState(context.Background())
	require.NoError(t, err)
	assert.True(t, st.Installed)
	assert.Equal(t, "v1.31.5+k3s1", st.Version)
	assert.Equal(t, "10.42.0.0/16,fd42::/48", st.ClusterCIDR)
	assert.Equal(t, "10.43.0.0/16,fd43::/112", st.ServiceCIDR)
	assert.True(t, st.DualStack)
}

func TestReadState_NotInstalled(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		return "", 1 // every command fails
	})
	defer cleanup()

	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	st, err := p.ReadState(context.Background())
	require.NoError(t, err)
	assert.False(t, st.Installed)
}

func TestParseSystemdUnit(t *testing.T) {
	// realQuotedDualStack mirrors the exact unit the k3s install script
	// (get.k3s.io) writes for a dual-stack install: every ExecStart arg on its
	// own backslash-continued line, single-quoted by the script's quote().
	const realQuotedDualStack = `[Unit]
Description=Lightweight Kubernetes
[Service]
ExecStart=/usr/local/bin/k3s \
    server \
	'--cluster-cidr' \
	'10.42.0.0/16,fd42::/48' \
	'--service-cidr' \
	'10.43.0.0/16,fd43::/112' \
	'--flannel-backend=vxlan' \
	'--disable' \
	'traefik' \
	'--write-kubeconfig-mode' \
	'0600' \
[Install]
`
	const realQuotedSingleStack = `[Unit]
Description=Lightweight Kubernetes
[Service]
ExecStart=/usr/local/bin/k3s \
    server \
	'--cluster-cidr' \
	'10.42.0.0/16' \
	'--service-cidr' \
	'10.43.0.0/16' \
	'--flannel-backend=vxlan' \
[Install]
`

	tests := []struct {
		name            string
		unit            string
		wantClusterCIDR string
		wantServiceCIDR string
		wantDualStack   bool
	}{
		{
			name:            "install-script quoted dual-stack",
			unit:            realQuotedDualStack,
			wantClusterCIDR: "10.42.0.0/16,fd42::/48",
			wantServiceCIDR: "10.43.0.0/16,fd43::/112",
			wantDualStack:   true,
		},
		{
			name:            "install-script quoted single-stack",
			unit:            realQuotedSingleStack,
			wantClusterCIDR: "10.42.0.0/16",
			wantServiceCIDR: "10.43.0.0/16",
			wantDualStack:   false,
		},
		{
			name:            "unquoted space-separated (legacy)",
			unit:            "ExecStart=/usr/local/bin/k3s server --cluster-cidr 10.42.0.0/16,fd42::/48 --service-cidr 10.43.0.0/16,fd43::/112\n",
			wantClusterCIDR: "10.42.0.0/16,fd42::/48",
			wantServiceCIDR: "10.43.0.0/16,fd43::/112",
			wantDualStack:   true,
		},
		{
			name:            "equals form unquoted",
			unit:            "ExecStart=/usr/local/bin/k3s server --cluster-cidr=10.42.0.0/16,fd42::/48 --service-cidr=10.43.0.0/16,fd43::/112\n",
			wantClusterCIDR: "10.42.0.0/16,fd42::/48",
			wantServiceCIDR: "10.43.0.0/16,fd43::/112",
			wantDualStack:   true,
		},
		{
			name:            "equals form quoted",
			unit:            "ExecStart=/usr/local/bin/k3s \\\n    server \\\n\t'--cluster-cidr=10.42.0.0/16,fd42::/48' \\\n\t'--service-cidr=10.43.0.0/16,fd43::/112' \\\n",
			wantClusterCIDR: "10.42.0.0/16,fd42::/48",
			wantServiceCIDR: "10.43.0.0/16,fd43::/112",
			wantDualStack:   true,
		},
		{
			name: "embedded single quote in value is unescaped",
			// k3s quote() escapes an embedded single quote with the POSIX
			// backslash-quote sequence. The parser must restore the original. Use a
			// raw string so the backslash is preserved literally (in a double-quoted
			// literal a backslash before a quote would collapse to a bare quote).
			unit: `ExecStart=/usr/local/bin/k3s server '--cluster-cidr' 'a'\''b,c::/48'
`,
			wantClusterCIDR: "a'b,c::/48",
			wantServiceCIDR: "",
			wantDualStack:   true,
		},
		{
			name:            "no execstart args",
			unit:            "[Service]\nExecStart=/usr/local/bin/k3s server\n",
			wantClusterCIDR: "",
			wantServiceCIDR: "",
			wantDualStack:   false,
		},
		{
			name:            "empty unit",
			unit:            "",
			wantClusterCIDR: "",
			wantServiceCIDR: "",
			wantDualStack:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc, sc, ds := parseSystemdUnit(tt.unit)
			assert.Equal(t, tt.wantClusterCIDR, cc, "clusterCIDR")
			assert.Equal(t, tt.wantServiceCIDR, sc, "serviceCIDR")
			assert.Equal(t, tt.wantDualStack, ds, "dualStack")
		})
	}
}

func TestNodeReady(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
			if cmd == "sudo k3s kubectl get --raw=/readyz" {
				return "ok", 0
			}
			return "", 1
		})
		defer cleanup()
		p := newProvisioner(t, addr, cfg)
		defer p.Close()
		ok, err := p.NodeReady(context.Background())
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("not ready", func(t *testing.T) {
		addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
			if cmd == "sudo k3s kubectl get --raw=/readyz" {
				return "etcd: false\n", 0
			}
			return "", 1
		})
		defer cleanup()
		p := newProvisioner(t, addr, cfg)
		defer p.Close()
		ok, err := p.NodeReady(context.Background())
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

func TestUninstall(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		got = cmd
		if cmd == "if test -x /usr/local/bin/k3s-uninstall.sh; then sudo /usr/local/bin/k3s-uninstall.sh; fi" {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Uninstall(context.Background()))
	assert.Equal(t, "if test -x /usr/local/bin/k3s-uninstall.sh; then sudo /usr/local/bin/k3s-uninstall.sh; fi", got)
}

func TestRebootUsesAcknowledgedNonBlockingSystemdRequest(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		got = cmd
		if cmd == "sudo systemctl reboot --no-block" {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Reboot(context.Background()))
	assert.Equal(t, "sudo systemctl reboot --no-block", got)
}

func TestSelectMetalLBCRDs(t *testing.T) {
	in := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\nspec:\n  group: metallb.io\n---\napiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: agentpools.platform.iterabase.com\nspec:\n  group: platform.iterabase.com\n"
	out, err := selectMetalLBCRDs(in)
	require.NoError(t, err)
	if !strings.Contains(out, "ipaddresspools.metallb.io") {
		t.Fatalf("selectMetalLBCRDs dropped the MetalLB CRD:\n%s", out)
	}
	if strings.Contains(out, "agentpools.platform.iterabase.com") {
		t.Fatalf("selectMetalLBCRDs kept a non-MetalLB CRD:\n%s", out)
	}
	// Empty input is the empty string, not a stray newline, so the pre-apply
	// emptiness guard triggers when MetalLB is disabled.
	empty, err := selectMetalLBCRDs("")
	require.NoError(t, err)
	require.Equal(t, "", empty)
}

func TestExtractChartCRDs(t *testing.T) {
	const preamble = `Pulled: ghcr.io/example/chart:1.0.0
Digest: sha256:abc123
---
# source comment
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: not-a-crd
`
	const authoritative = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.21.0
    operator.prometheus.io/version: 0.93.0
    schema-marker: authoritative
  name: podmonitors.monitoring.coreos.com
`
	const stale = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.9.2
    schema-marker: stale
  name: podmonitors.monitoring.coreos.com
`
	const widget = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
`

	got, err := extractChartCRDs(strings.Join([]string{preamble, authoritative, widget, stale}, "\n---\n"))
	require.NoError(t, err)
	reversed, err := extractChartCRDs(strings.Join([]string{preamble, stale, widget, authoritative}, "\n---\n"))
	require.NoError(t, err)

	assert.Equal(t, got, reversed, "duplicate resolution and output order must not depend on Helm traversal order")
	assert.NotContains(t, got, "Pulled:")
	assert.NotContains(t, got, "ConfigMap")
	assert.Contains(t, got, "schema-marker: authoritative")
	assert.NotContains(t, got, "schema-marker: stale")
	assert.Less(t, strings.Index(got, "podmonitors.monitoring.coreos.com"), strings.Index(got, "widgets.example.com"))
	assert.Equal(t, 2, strings.Count(got, "kind: CustomResourceDefinition"))
}

func TestCompareNumericVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{left: "0.93.0", right: "0.9.2", want: 1},
		{left: "v0.93", right: "0.93.0", want: 0},
		{left: "0.93.0", right: "0.100.0", want: -1},
	}
	for _, tt := range tests {
		got, err := compareNumericVersions(tt.left, tt.right)
		require.NoError(t, err)
		assert.Equal(t, tt.want, got, "%s compared with %s", tt.left, tt.right)
	}
}

func TestExtractChartCRDs_EmptyMalformedAndAmbiguous(t *testing.T) {
	got, err := extractChartCRDs("Pulled: example/chart:1.0.0\nDigest: sha256:abc\n")
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = extractChartCRDs("apiVersion: [unterminated\n")
	require.ErrorContains(t, err, "decode chart CRDs")

	const ambiguous = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: examples.example.com
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    schema-marker: conflicting
  name: examples.example.com
`
	_, err = extractChartCRDs(ambiguous)
	require.ErrorContains(t, err, `conflicting duplicate chart CRD "examples.example.com" has no authoritative version annotation`)
}

func TestDeployer_Apply(t *testing.T) {
	var got string
	var kubectlCalled bool
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0 // charts without CRDs are a no-op
		case strings.Contains(cmd, "'template'"):
			return "", 0 // no rendered CRDs
		case strings.Contains(cmd, "kubectl"):
			kubectlCalled = true
			return "", 0
		case strings.Contains(cmd, "upgrade"):
			got = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.1.0", Namespace: "iterabase-system",
		Values: []string{"driver.enabled=true", "toolkit.enabled=true"},
	}))
	assert.False(t, kubectlCalled, "an empty helm show crds result must skip kubectl")
	assert.Contains(t, got, "--set")
	assert.Contains(t, got, "driver.enabled=true")
	assert.Contains(t, got, "toolkit.enabled=true")
	assert.Contains(t, got, "'--wait'")
}

func TestDeployer_Apply_NoWait(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return "", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			got = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.3.0", Namespace: "iterabase-system", NoWait: true,
	}))
	assert.NotContains(t, got, "'--wait'")
	assert.Contains(t, got, "'--timeout' '10m'")
}

func TestDeployer_Apply_ReconcilesCRDsBeforeUpgrade(t *testing.T) {
	const crds = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: examples.example.com\n"
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return crds, 0
		case strings.Contains(cmd, "'template'"):
			return "", 0
		case strings.Contains(cmd, "'kubectl' 'apply'"):
			return "customresourcedefinition.apiextensions.k8s.io/examples.example.com serverside-applied\n", 0
		case strings.Contains(cmd, "'kubectl' 'wait'"):
			return "customresourcedefinition.apiextensions.k8s.io/examples.example.com condition met\n", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.1.27", Namespace: "iterabase-system",
	}))
	// Every crds/-directory CRD (surfaced by `helm show crds`) is preserved in
	// the pre-apply set so an operator-feature-enable upgrade can introduce new
	// dependency CRDs (DES-HOR-511-03); only rendered MetalLB CRDs receive Helm
	// ownership. A non-MetalLB crds/-dir CRD therefore still triggers apply/wait.
	require.Len(t, commands, 6)
	assert.Equal(t, helmVerifyCommand, commands[0])
	assert.Contains(t, commands[1], "'show' 'crds'")
	assert.Contains(t, commands[2], "'template'")
	assert.Contains(t, commands[3], "'kubectl' 'apply'")
	assert.Contains(t, commands[4], "'kubectl' 'wait'")
	assert.Contains(t, commands[5], "'upgrade' '--install'")
}

func TestDeployer_Apply_ReconcilesRenderedTemplateCRDs(t *testing.T) {
	const renderedCRD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n"
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return renderedCRD, 0
		case strings.Contains(cmd, "'kubectl' 'apply'"):
			return "customresourcedefinition.apiextensions.k8s.io/ipaddresspools.metallb.io serverside-applied\n", 0
		case strings.Contains(cmd, "'kubectl' 'wait'"):
			return "customresourcedefinition.apiextensions.k8s.io/ipaddresspools.metallb.io condition met\n", 0
		case strings.Contains(cmd, "'status'"):
			// MetalLB already installed at the steady-state Fail policy => no bootstrap.
			return `{"info":{"status":"deployed"},"chart":{"metadata":{"version":"0.3.19"}}}`, 0
		case strings.Contains(cmd, "validatingwebhookconfiguration"):
			return "Fail", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	// Metallb CRDs render as ordinary template resources and are absent from
	// `helm show crds`; the render-extract step must discover and establish them.
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.3.19", Namespace: "iterabase-system",
		ValueFiles: []string{"/tmp/values.yaml"},
		Values:     []string{"metallb.enabled=true"},
	}))
	require.Len(t, commands, 10)
	assert.Contains(t, commands[2], "'template' 'oci://ghcr.io/nunocgoncalves/iterabase-platform' '--version' '0.3.19' '-n' 'iterabase-system' '-f' '/tmp/values.yaml' '--set' 'metallb.enabled=true'")
	assert.Contains(t, commands[3], "'kubectl' 'apply' '--server-side' '--force-conflicts' '-f' '-'")
	assert.Contains(t, commands[4], "'kubectl' 'wait' '--for=condition=Established'")
	assert.Contains(t, commands[6], "'status'")
	assert.Contains(t, commands[7], "validatingwebhookconfiguration")
	assert.Contains(t, commands[8], "'upgrade' '--install'")
	assert.Contains(t, commands[9], "validatingwebhookconfiguration")
}

func TestDeployer_Apply_MetalLBBootstrapConverges(t *testing.T) {
	const renderedCRD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n"
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return renderedCRD, 0
		case strings.Contains(cmd, "'kubectl' 'apply'"):
			return "customresourcedefinition.apiextensions.k8s.io/ipaddresspools.metallb.io serverside-applied\n", 0
		case strings.Contains(cmd, "'kubectl' 'wait'"):
			return "customresourcedefinition.apiextensions.k8s.io/ipaddresspools.metallb.io condition met\n", 0
		case strings.Contains(cmd, "'status'"):
			return "", 1 // release not found => fresh install => bootstrap
		case strings.Contains(cmd, "validatingwebhookconfiguration"):
			// pre-bootstrap probe: no webhook yet => ""; final probe: converged Fail.
			return "Fail", 0
		case strings.Contains(cmd, "'get' 'deployment'"):
			return "1", 0
		case strings.Contains(cmd, "'endpoints'"):
			return "10.0.0.5", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.3.19", Namespace: "iterabase-system",
		Values: []string{"metallb.enabled=true"},
	}))
	require.Len(t, commands, 13)
	assert.Contains(t, commands[6], "'status'")
	assert.Contains(t, commands[7], "validatingwebhookconfiguration")
	assert.Contains(t, commands[8], "'upgrade' '--install'")
	assert.Contains(t, commands[8], "metallb.crds.validationFailurePolicy=Ignore")
	assert.Contains(t, commands[9], "'get' 'deployment'")
	assert.Contains(t, commands[10], "'endpoints'")
	assert.Contains(t, commands[11], "'upgrade' '--install'")
	assert.NotContains(t, commands[11], "validationFailurePolicy=Ignore")
	assert.Contains(t, commands[12], "validatingwebhookconfiguration")
}

func TestDeployer_Apply_MetalLBBootstrapTimeout(t *testing.T) {
	const renderedCRD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n"
	orig := metalLBBackendWaitTimeout
	metalLBBackendWaitTimeout = 150 * time.Millisecond
	defer func() { metalLBBackendWaitTimeout = orig }()
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return renderedCRD, 0
		case strings.Contains(cmd, "'kubectl' 'apply'"), strings.Contains(cmd, "'kubectl' 'wait'"):
			return "", 0
		case strings.Contains(cmd, "'status'"):
			return "", 1
		case strings.Contains(cmd, "validatingwebhookconfiguration"):
			// Fresh install: the webhook configuration is absent (NotFound), not a
			// read failure, so the bootstrap proceeds and then times out.
			return "Error from server (NotFound): validatingwebhookconfigurations.admissionregistration.k8s.io \"metallb-webhook-configuration\" not found\n", 1
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1 // deployment/endpoints never ready => backend probe never succeeds
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.3.19", Namespace: "iterabase-system",
		Values: []string{"metallb.enabled=true"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metallb admission backend not ready")
}

func TestDeployer_Apply_MetalLBInterruptedBootstrapConverges(t *testing.T) {
	// A reapply of an interrupted bootstrap: the release exists but is still at
	// the bootstrap Ignore policy, so Apply must re-open admission, re-wait the
	// backend, then converge to the steady-state Fail and assert it.
	const renderedCRD = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n"
	var commands []string
	var vwcReads int
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return renderedCRD, 0
		case strings.Contains(cmd, "'kubectl' 'apply'"), strings.Contains(cmd, "'kubectl' 'wait'"):
			return "", 0
		case strings.Contains(cmd, "'status'"):
			return `{"info":{"status":"deployed"},"chart":{"metadata":{"version":"0.3.19"}}}`, 0
		case strings.Contains(cmd, "validatingwebhookconfiguration"):
			vwcReads++
			if vwcReads == 1 {
				return "Ignore", 0 // interrupted bootstrap not yet converged
			}
			return "Fail", 0 // converged after the steady apply
		case strings.Contains(cmd, "'get' 'deployment'"):
			return "1", 0
		case strings.Contains(cmd, "'endpoints'"):
			return "10.0.0.5", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.3.19", Namespace: "iterabase-system",
		Values: []string{"metallb.enabled=true"},
	}))
	// The Ignore bootstrap apply runs, then the backend wait, then the steady
	// apply, then the final assertion probe.
	require.Len(t, commands, 13)
	assert.Contains(t, commands[8], "validationFailurePolicy=Ignore")
	assert.Contains(t, commands[11], "'upgrade' '--install'")
	assert.NotContains(t, commands[11], "validationFailurePolicy=Ignore")
}

func TestMarkHelmAdoptableCRDs(t *testing.T) {
	in := "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n  annotations:\n    controller-gen.kubebuilder.io/version: v0.19.0\nspec:\n  group: metallb.io\n"
	out, err := markHelmAdoptableCRDs(in, "opo1", "iterabase-system")
	require.NoError(t, err)
	assert.Contains(t, out, "meta.helm.sh/release-name: opo1")
	assert.Contains(t, out, "meta.helm.sh/release-namespace: iterabase-system")
	assert.Contains(t, out, "app.kubernetes.io/managed-by: Helm")
	assert.Contains(t, out, "controller-gen.kubebuilder.io/version: v0.19.0") // existing metadata preserved
	// Idempotent.
	out2, err := markHelmAdoptableCRDs(out, "opo1", "iterabase-system")
	require.NoError(t, err)
	assert.Equal(t, out, out2)
}

func TestDeployer_Apply_CRDFailuresStopBeforeUpgrade(t *testing.T) {
	const crds = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: ipaddresspools.metallb.io\n"
	tests := []struct {
		name       string
		failAt     string
		wantErr    string
		showOutput string
	}{
		{name: "discover", failAt: "show", wantErr: "discover chart CRDs"},
		{name: "apply", failAt: "apply", wantErr: "apply chart CRDs", showOutput: crds},
		{name: "wait", failAt: "wait", wantErr: "wait for chart CRDs", showOutput: crds},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upgradeCalled := false
			addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
				switch {
				case cmd == helmVerifyCommand:
					return "/usr/local/bin/helm\n", 0
				case strings.Contains(cmd, "'show' 'crds'"):
					if tt.failAt == "show" {
						return "", 1
					}
					return tt.showOutput, 0
				case strings.Contains(cmd, "'template'"):
					return "", 0
				case strings.Contains(cmd, "'kubectl' 'apply'"):
					if tt.failAt == "apply" {
						return "", 1
					}
					return "", 0
				case strings.Contains(cmd, "'kubectl' 'wait'"):
					if tt.failAt == "wait" {
						return "", 1
					}
					return "", 0
				case strings.Contains(cmd, "'upgrade' '--install'"):
					upgradeCalled = true
					return "", 0
				default:
					return "", 1
				}
			})
			defer cleanup()
			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			err := p.Apply(context.Background(), deployer.ApplyOpts{
				Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
				Version: "0.1.27", Namespace: "iterabase-system",
			})
			require.ErrorContains(t, err, tt.wantErr)
			assert.False(t, upgradeCalled)
		})
	}
}

func TestDeployer_Apply_HelmBootstrapFailuresStopBeforeChart(t *testing.T) {
	const installerPath = "/tmp/forge-helm-installer.test"
	tests := []struct {
		name    string
		failAt  string
		wantErr string
	}{
		{name: "download failure", failAt: "download", wantErr: "download helm-installer: ssh run"},
		{name: "checksum mismatch", failAt: "checksum", wantErr: "verify helm-installer content checksum"},
		{name: "installer failure", failAt: "installer", wantErr: "execute helm installer: ssh run"},
		{name: "privileged PATH mismatch", failAt: "verify", wantErr: "verify helm installation through privileged PATH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chartCalled, installerCalled bool
			addr, cfg, cleanup := startFakeSSHWithResult(t, func(cmd string) sshCommandResult {
				switch {
				case cmd == helmVerifyCommand:
					return sshCommandResult{stderr: "sudo: helm: command not found\n", code: 1}
				case cmd == helmInstallerTempCmd:
					return sshCommandResult{stdout: installerPath + "\n"}
				case strings.HasPrefix(cmd, "curl -fsSL"):
					if tt.failAt == "download" {
						return sshCommandResult{stderr: "curl: (22) The requested URL returned error: 503\n", code: 22}
					}
					return sshCommandResult{}
				case strings.Contains(cmd, "sha256sum --check --status"):
					if tt.failAt == "checksum" {
						return sshCommandResult{code: 1}
					}
					return sshCommandResult{}
				case cmd == "sudo env DESIRED_VERSION="+shellQuote(helmInstallVersion)+" bash "+shellQuote(installerPath):
					installerCalled = true
					if tt.failAt == "installer" {
						return sshCommandResult{stdout: "get_helm.sh: unsupported architecture\n", code: 1}
					}
					return sshCommandResult{}
				case cmd == "rm -f "+shellQuote(installerPath):
					return sshCommandResult{}
				case strings.Contains(cmd, "'show' 'crds'"), strings.Contains(cmd, "'template'"), strings.Contains(cmd, "'upgrade' '--install'"):
					chartCalled = true
					return sshCommandResult{}
				default:
					return sshCommandResult{code: 1}
				}
			})
			defer cleanup()
			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			err := p.Apply(context.Background(), deployer.ApplyOpts{
				Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
				Version: "0.1.0", Namespace: "iterabase-system",
			})
			require.ErrorContains(t, err, tt.wantErr)
			switch tt.failAt {
			case "download":
				require.ErrorContains(t, err, "curl: (22) The requested URL returned error: 503")
			case "installer":
				require.ErrorContains(t, err, "get_helm.sh: unsupported architecture")
			case "verify":
				require.ErrorContains(t, err, "sudo: helm: command not found")
			}
			if tt.failAt == "download" || tt.failAt == "checksum" {
				assert.False(t, installerCalled)
			}
			assert.False(t, chartCalled, "chart discovery/apply must not run after Helm bootstrap fails")
		})
	}
}

func TestEnsureHelmRetriesRecognizedInstallerTransportFailure(t *testing.T) {
	const installerPath = "/tmp/forge-helm-installer.test"
	originalInterval := helmInstallerRetryInterval
	helmInstallerRetryInterval = time.Millisecond
	defer func() { helmInstallerRetryInterval = originalInterval }()

	installed := false
	installerCalls := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			if installed {
				return "v4.0.0\n", 0
			}
			return "", 1
		case cmd == helmInstallerTempCmd:
			return installerPath + "\n", 0
		case strings.HasPrefix(cmd, "curl -fsSL"), strings.Contains(cmd, "sha256sum --check --status"), cmd == "rm -f "+shellQuote(installerPath):
			return "", 0
		case cmd == "sudo env DESIRED_VERSION="+shellQuote(helmInstallVersion)+" bash "+shellQuote(installerPath):
			installerCalls++
			if installerCalls == 1 {
				return "Failed to install helm\n", 1
			}
			installed = true
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.ensureHelm(context.Background()))
	assert.Equal(t, 2, installerCalls)
}

func TestDeployer_Apply_EnsuresHelm(t *testing.T) {
	const installerPath = "/tmp/forge-helm-installer.test"
	var commands []string
	helmChecks := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		switch {
		case cmd == helmVerifyCommand:
			helmChecks++
			if helmChecks == 1 {
				return "", 1
			}
			return "v4.0.0+gabcdef\n", 0
		case cmd == helmInstallerTempCmd:
			return installerPath + "\n", 0
		case strings.HasPrefix(cmd, "curl -fsSL"),
			strings.Contains(cmd, "sha256sum --check --status"),
			cmd == "sudo env DESIRED_VERSION="+shellQuote(helmInstallVersion)+" bash "+shellQuote(installerPath),
			cmd == "rm -f "+shellQuote(installerPath):
			return "", 0
		case strings.Contains(cmd, "'show' 'crds'"):
			return "", 0
		case strings.Contains(cmd, "'template'"):
			return "", 0
		case strings.Contains(cmd, "'upgrade' '--install'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.Apply(context.Background(), deployer.ApplyOpts{
		Release: "opo1", Repository: "oci://ghcr.io/nunocgoncalves/iterabase-platform",
		Version: "0.1.0", Namespace: "iterabase-system",
	}))

	require.Len(t, commands, 10)
	assert.Equal(t, helmVerifyCommand, commands[0])
	assert.Equal(t, helmInstallerTempCmd, commands[1])
	assert.Contains(t, commands[2], "curl -fsSL --retry 4 --retry-delay 2 --retry-all-errors --connect-timeout 10 -o '"+installerPath+"'")
	assert.Contains(t, commands[2], shellQuote(helmInstallScript))
	assert.NotContains(t, commands[2], "|")
	assert.Contains(t, commands[3], "sha256sum --check --status")
	assert.Contains(t, commands[3], helmInstallScriptSHA256)
	assert.Equal(t, "sudo env DESIRED_VERSION="+shellQuote(helmInstallVersion)+" bash "+shellQuote(installerPath), commands[4])
	assert.Equal(t, helmVerifyCommand, commands[5])
	assert.Equal(t, "rm -f "+shellQuote(installerPath), commands[6])
	assert.Contains(t, commands[7], "'show' 'crds'")
	assert.Contains(t, commands[8], "'template'")
	assert.Contains(t, commands[9], "'upgrade' '--install'")
}

func TestDeployer_Status(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "status"):
			return `{"info":{"status":"deployed"},"chart":{"metadata":{"version":"0.1.0"}}}`, 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	st, err := p.Status(context.Background(), "opo1", "iterabase-system")
	require.NoError(t, err)
	assert.True(t, st.Installed)
	assert.Equal(t, "deployed", st.Status)
	assert.Equal(t, "0.1.0", st.Version)
}

func TestDeployer_Status_Helm4UsesMetadataVersion(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'status'"):
			return `{"name":"opo1","version":19,"info":{"status":"deployed"}}`, 0
		case strings.Contains(cmd, "'get' 'metadata'"):
			return `{"name":"opo1","chart":"iterabase-platform","version":"0.2.2","status":"deployed"}`, 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	st, err := p.Status(context.Background(), "opo1", "iterabase-system")
	require.NoError(t, err)
	assert.True(t, st.Installed)
	assert.Equal(t, "deployed", st.Status)
	assert.Equal(t, "0.2.2", st.Version)
}

func TestDeployer_Status_Helm4RejectsMissingMetadataVersion(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'status'"):
			return `{"name":"opo1","version":19,"info":{"status":"deployed"}}`, 0
		case strings.Contains(cmd, "'get' 'metadata'"):
			return `{"name":"opo1","chart":"iterabase-platform","version":""}`, 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	_, err := p.Status(context.Background(), "opo1", "iterabase-system")
	require.ErrorContains(t, err, "chart version is empty")
}

func TestDeployer_Status_NotInstalled(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "status"):
			return "", 1 // release not found
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	st, err := p.Status(context.Background(), "opo1", "iterabase-system")
	require.NoError(t, err)
	assert.False(t, st.Installed)
}

func TestDeployer_CRDOwnedBy(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want bool
	}{
		{
			name: "all selected CRDs owned by companion",
			out: "certificates.cert-manager.io\topo1-cert-manager\titerabase-system\n" +
				"issuers.cert-manager.io\topo1-cert-manager\titerabase-system\n",
			want: true,
		},
		{
			name: "partial transfer remains incomplete",
			out: "certificates.cert-manager.io\topo1-cert-manager\titerabase-system\n" +
				"issuers.cert-manager.io\topo1\titerabase-system\n",
			want: false,
		},
		{name: "empty selection", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
				if strings.Contains(cmd, "'get' 'crd'") && strings.Contains(cmd, "'jsonpath=") {
					return tt.out, 0
				}
				return "", 1
			})
			defer cleanup()
			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			owned, err := p.CRDOwnedBy(context.Background(),
				"app.kubernetes.io/name=cert-manager", "opo1-cert-manager", "iterabase-system")
			require.NoError(t, err)
			assert.Equal(t, tt.want, owned)
		})
	}
}

func TestDeployer_CRDsAnnotated(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "'get' 'crd'") && strings.Contains(cmd, "'-o' 'json'") {
			return `{"items":[{"metadata":{"annotations":{"forge.horizonshift.io/certificate-substrate-migration":"0.3.0"}}},{"metadata":{"annotations":{"forge.horizonshift.io/certificate-substrate-migration":"0.3.0"}}}]}`, 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	complete, err := p.CRDsAnnotated(context.Background(), "app.kubernetes.io/name=cert-manager",
		"forge.horizonshift.io/certificate-substrate-migration", "0.3.0")
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestDeployer_AnnotateCRDs(t *testing.T) {
	var annotate string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "'get' 'crd'"):
			return "customresourcedefinition.apiextensions.k8s.io/certificates.cert-manager.io\n", 0
		case strings.Contains(cmd, "'annotate' '--overwrite'"):
			annotate = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.AnnotateCRDs(context.Background(), "app.kubernetes.io/name=cert-manager",
		"forge.horizonshift.io/certificate-substrate-migration", "0.3.0"))
	assert.Contains(t, annotate, "'forge.horizonshift.io/certificate-substrate-migration=0.3.0'")
}

func TestDeployer_TransferCertificateHookOwnership(t *testing.T) {
	var annotations []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "'get' 'clusterissuer'"):
			return "clusterissuer.cert-manager.io/selfsigned\nclusterissuer.cert-manager.io/internal-ca\n", 0
		case strings.Contains(cmd, "'get' 'certificate'"):
			return "certificate.cert-manager.io/opo1-internal-ca-root\n", 0
		case strings.Contains(cmd, "'annotate' '--overwrite'"):
			annotations = append(annotations, cmd)
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.TransferCertificateHookOwnership(context.Background(),
		"app.kubernetes.io/instance=opo1,app.kubernetes.io/managed-by=Helm", "opo1", "iterabase-system"))
	require.Len(t, annotations, 2)
	assert.Contains(t, annotations[0], "'clusterissuer.cert-manager.io/selfsigned'")
	assert.NotContains(t, annotations[0], "'-n'")
	assert.Contains(t, annotations[1], "'-n' 'iterabase-system'")
	assert.Contains(t, annotations[1], "'certificate.cert-manager.io/opo1-internal-ca-root'")
	for _, annotate := range annotations {
		assert.Contains(t, annotate, "'meta.helm.sh/release-name=opo1'")
		assert.Contains(t, annotate, "'meta.helm.sh/release-namespace=iterabase-system'")
		assert.Contains(t, annotate, "'helm.sh/hook-'")
		assert.Contains(t, annotate, "'helm.sh/hook-weight-'")
	}
}

func TestDeployer_TransferMetalLBHookOwnership(t *testing.T) {
	var annotates []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "'get' 'ipaddresspool'"):
			return "ipaddresspool.metallb.io/opo1-edge\nipaddresspool.metallb.io/opo1-internal\n", 0
		case strings.Contains(cmd, "'get' 'l2advertisement'"):
			return "l2advertisement.metallb.io/opo1-edge\n", 0
		case strings.Contains(cmd, "'annotate' '--overwrite'"):
			annotates = append(annotates, cmd)
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.TransferMetalLBHookOwnership(context.Background(), "opo1", "iterabase-system"))
	require.Len(t, annotates, 2)
	for _, annotate := range annotates {
		assert.Contains(t, annotate, "'-n' 'iterabase-system'")
		assert.Contains(t, annotate, "'meta.helm.sh/release-name=opo1'")
		assert.Contains(t, annotate, "'meta.helm.sh/release-namespace=iterabase-system'")
		assert.Contains(t, annotate, "'helm.sh/hook-'")
		assert.Contains(t, annotate, "'helm.sh/hook-weight-'")
	}
	assert.Contains(t, annotates[0], "'ipaddresspool.metallb.io/opo1-edge'")
	assert.Contains(t, annotates[0], "'ipaddresspool.metallb.io/opo1-internal'")
	assert.Contains(t, annotates[1], "'l2advertisement.metallb.io/opo1-edge'")
}

func TestDeployer_TransferMetalLBHookOwnership_NoObjectsNoCRDs(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "'get' 'ipaddresspool'") || strings.Contains(cmd, "'get' 'l2advertisement'") {
			// no objects or no CRDs => empty output / unknown kind
			return "", 1
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	// Unknown-kind errors are tolerated as no-ops so cloud installs (where the
	// MetalLB CRDs never existed) do not fail the platform apply.
	require.NoError(t, p.TransferMetalLBHookOwnership(context.Background(), "opo1", "iterabase-system"))
}

func TestDeployer_TransferCRDOwnership(t *testing.T) {
	var annotate string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "'get' 'crd'"):
			return "customresourcedefinition.apiextensions.k8s.io/certificates.cert-manager.io\ncustomresourcedefinition.apiextensions.k8s.io/issuers.cert-manager.io\n", 0
		case strings.Contains(cmd, "'annotate' '--overwrite'"):
			annotate = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.TransferCRDOwnership(context.Background(),
		"app.kubernetes.io/name=cert-manager", "opo1-cert-manager", "iterabase-system"))
	assert.Contains(t, annotate, "'customresourcedefinition.apiextensions.k8s.io/certificates.cert-manager.io'")
	assert.Contains(t, annotate, "'meta.helm.sh/release-name=opo1-cert-manager'")
	assert.Contains(t, annotate, "'meta.helm.sh/release-namespace=iterabase-system'")
}

func TestDeployer_RestartDeployment(t *testing.T) {
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		return "", 0
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.RestartDeployment(context.Background(),
		"app.kubernetes.io/name=control-plane,app.kubernetes.io/instance=opo1,app.kubernetes.io/component=gateway",
		"iterabase-system"))
	require.Len(t, commands, 2)
	assert.Contains(t, commands[0], "'rollout' 'restart' 'deployment' '-n' 'iterabase-system'")
	assert.Contains(t, commands[0], "'-l=app.kubernetes.io/name=control-plane,app.kubernetes.io/instance=opo1,app.kubernetes.io/component=gateway'")
	assert.Contains(t, commands[1], "'rollout' 'status' 'deployment'")
	assert.Contains(t, commands[1], "'--timeout=5m'")
}

func TestDeployer_UninstallChart(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v helm":
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'status'"):
			return "STATUS: deployed\n", 0
		case strings.Contains(cmd, "'uninstall'"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.UninstallChart(context.Background(), "opo1", "iterabase-system"))
}

func TestDeployer_UninstallChart_PropagatesHookRefusal(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v helm":
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "'status'"):
			return "STATUS: deployed\n", 0
		case strings.Contains(cmd, "'uninstall'"):
			return "refusing platform uninstall: active consumer remains\n", 1
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.UninstallChart(context.Background(), "opo1", "iterabase-system")
	require.ErrorContains(t, err, "uninstall Helm release")
	require.ErrorContains(t, err, "refusing platform uninstall")
}

func TestDeployer_UninstallChart_MissingReleaseIsIdempotent(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if cmd == "command -v helm" {
			return "/usr/local/bin/helm\n", 0
		}
		if strings.Contains(cmd, "'status'") {
			return "Error: release: not found\n", 1
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.UninstallChart(context.Background(), "absent", "iterabase-system"))
}

func TestDeployer_UninstallChart_HelmAbsent(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if cmd == "command -v helm" {
			return "", 1 // absent
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.ErrorContains(t, p.UninstallChart(context.Background(), "opo1", "iterabase-system"), "helm is unavailable")
}

func TestPreflight_NoGPU(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch cmd {
		case "cat /etc/os-release":
			return "PRETTY_NAME=\"Ubuntu 24.04 LTS\"\n", 0
		case "sudo -n true", "command -v curl", "pidof systemd", "command -v k3s",
			"ip -6 addr show scope global", "test -f /lib/modules/$(uname -r)/build/Makefile",
			"command -v dkms", "command -v gcc", "command -v make":
			return "", 0
		case "grep -qi 0x10de /sys/bus/pci/devices/*/vendor":
			return "", 1 // no NVIDIA device
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	r, err := p.Preflight(context.Background())
	require.NoError(t, err)
	assert.False(t, r.HasNVIDIAGPU)
	assert.True(t, r.KernelHeadersInstalled)
	assert.True(t, r.HasDKMS)
	assert.True(t, r.HasGCC)
	assert.True(t, r.HasMake)
}

func TestPreflight_MissingDriverBuildDeps(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch cmd {
		case "cat /etc/os-release":
			return "PRETTY_NAME=\"Ubuntu 24.04 LTS\"\n", 0
		case "sudo -n true", "command -v curl", "pidof systemd", "command -v k3s",
			"ip -6 addr show scope global", "grep -qi 0x10de /sys/bus/pci/devices/*/vendor":
			return "", 0
		case "test -f /lib/modules/$(uname -r)/build/Makefile", "command -v dkms",
			"command -v gcc", "command -v make":
			return "", 1
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	r, err := p.Preflight(context.Background())
	require.NoError(t, err)
	assert.True(t, r.HasNVIDIAGPU)
	assert.False(t, r.KernelHeadersInstalled)
	assert.False(t, r.HasDKMS)
	assert.False(t, r.HasGCC)
	assert.False(t, r.HasMake)
}

func TestEnsureDriverBuildDeps_CommandShape(t *testing.T) {
	var got []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		got = append(got, cmd)
		switch cmd {
		case "sudo apt-get update && sudo apt-get install -y linux-headers-$(uname -r) build-essential dkms":
			return "", 0
		case "apt-cache show linux-headers-$(uname -r) >/dev/null 2>&1":
			return "", 0
		case "test -f /lib/modules/$(uname -r)/build/Makefile && command -v dkms >/dev/null && command -v gcc >/dev/null && command -v make >/dev/null":
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureDriverBuildDeps(context.Background()))
	assert.Equal(t, []string{
		"sudo apt-get update && sudo apt-get install -y linux-headers-$(uname -r) build-essential dkms",
		"apt-cache show linux-headers-$(uname -r) >/dev/null 2>&1",
		"test -f /lib/modules/$(uname -r)/build/Makefile && command -v dkms >/dev/null && command -v gcc >/dev/null && command -v make >/dev/null",
	}, got)
}

func TestWorkspacePurgeScriptIsIdentityBoundedAndIdempotent(t *testing.T) {
	script := workspacePurgeScript(provisioner.AgentPoolWorkspaceSpec{
		InstallName: "opo1", Device: "/dev/disk/by-id/scsi-workspace", Filesystem: config.WorkspaceFilesystemAuto,
	})
	for _, expected := range []string{
		"workspace purge refusal",
		"selected disk backs system path",
		"workspace receipt install mismatch",
		"workspace disk serial identity mismatch",
		"workspace filesystem UUID drift",
		"workspace filesystem is in use",
		"workspace block device is in use",
		"wipefs --all --force",
		"FORGE_WORKSPACE_PURGE_RESULT",
		"already-clean",
	} {
		assert.Contains(t, script, expected)
	}
	assert.NotContains(t, script, "mkfs.", "purge must leave a blank disk for the next apply instead of creating a replacement filesystem")
}

func TestPurgeAgentPoolWorkspaceRunsOneQuotedRemoteScript(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		got = cmd
		return "FORGE_WORKSPACE_PURGE_RESULT\t/dev/disk/by-id/scsi-workspace\tpurged\n", 0
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.PurgeAgentPoolWorkspace(context.Background(), provisioner.AgentPoolWorkspaceSpec{
		InstallName: "opo1", Device: "/dev/disk/by-id/scsi-workspace", Filesystem: config.WorkspaceFilesystemAuto,
	}))
	assert.True(t, strings.HasPrefix(got, "sudo bash -ceu "))
	assert.Contains(t, got, "FORGE_WORKSPACE_PURGE_RESULT")
}

func TestAgentPoolWorkspaceCommandIsBoundedAndCrashResumable(t *testing.T) {
	for _, filesystem := range []string{config.WorkspaceFilesystemAuto, config.WorkspaceFilesystemExt4, config.WorkspaceFilesystemXFS} {
		t.Run(filesystem, func(t *testing.T) {
			script := workspaceReconcileScript(provisioner.AgentPoolWorkspaceSpec{
				InstallName: "opo1", Device: "/dev/disk/by-id/scsi-workspace", Filesystem: filesystem,
			}, "reconcile")
			for _, expected := range []string{
				"probe_identity_topology", "list_process_ids", "list_process_fds", "probe_active_raw_consumers", "process_ids=$(list_process_ids)", "LC_ALL=C ls -1U", "set -o pipefail", "head -n 65537", "65536-/proc-entry limit", "65536-descriptor limit", "could not enumerate /proc", "current_process_ids=$(list_process_ids)", "remaining_fds=$(list_process_fds", "for fd_round in 1 2 3", "for fd_attempt in 1 2 3", "stat -Lc '%t:%T'", "after 3 bounded rounds", "wipefs -n --noheadings --output TYPE", "blkid -p", "write_receipt planned",
				"mkfs.ext4 -F", "mkfs.xfs -f", "filesystem_selection", "transport_b64", "UUID=$planned_uuid",
				"nodev,nosuid", workspaceFilesystemLabel, workspaceMarkerName,
			} {
				assert.Contains(t, script, expected)
			}
			for _, forbidden := range []string{
				"if=/dev/", "FORGE_AGENTPOOL_WORKSPACE_FORCE", "wipefs -a", ">/tmp/forge-workspace",
				`/proc/[0-9]*`, `test -d "$process/fd" || continue`, `for fd in "$process"/fd/[0-9]*`, `test -L "$fd" || continue`,
			} {
				assert.NotContains(t, script, forbidden)
			}
			assert.LessOrEqual(t, strings.Count(script, "probe_blank_signatures"), 4, "bounded probes are repeated only at the authorization boundary")
		})
	}
}

func TestAgentPoolWorkspaceActiveOpenProbeBehavior(t *testing.T) {
	t.Run("unreadable fd directory for a live PID fails closed", func(t *testing.T) {
		output, err := runWorkspaceActiveOpenProbe(t, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_PROC_ROOT" ]]; then printf '100\n'; exit 0; fi
if [[ "$path" == "$TEST_PROC_ROOT/100/fd" ]]; then printf 'permission denied\n' >&2; exit 13; fi
printf 'unexpected ls path: %s\n' "$path" >&2
exit 2
`, `#!/bin/bash
set -eu
printf '8:1\n'
`)
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 42, exitErr.ExitCode())
		assert.Contains(t, output, "could not enumerate")
		assert.Contains(t, output, "/100/fd after 3 attempts: permission denied")
	})

	t.Run("PID disappearance proven by successful process re-enumeration is ignored", func(t *testing.T) {
		output, err := runWorkspaceActiveOpenProbe(t, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_PROC_ROOT" ]]; then
  count=0
  if [[ -f "$TEST_STATE" ]]; then read -r count < "$TEST_STATE"; fi
  if [[ "$count" == 0 ]]; then printf '100\nself\n'; else printf 'self\n'; fi
  printf '%s\n' "$((count + 1))" > "$TEST_STATE"
  exit 0
fi
if [[ "$path" == "$TEST_PROC_ROOT/100/fd" ]]; then printf 'process exited\n' >&2; exit 1; fi
exit 2
`, `#!/bin/bash
set -eu
printf '8:1\n'
`)
		require.NoError(t, err, output)
	})

	t.Run("persistently unreadable descriptor for a live PID fails closed", func(t *testing.T) {
		output, err := runWorkspaceActiveOpenProbe(t, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_PROC_ROOT" ]]; then printf '100\n'; exit 0; fi
if [[ "$path" == "$TEST_PROC_ROOT/100/fd" ]]; then printf '9\n'; exit 0; fi
exit 2
`, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_DEVICE" ]]; then printf '8:1\n'; exit 0; fi
printf 'descriptor unreadable\n' >&2
exit 13
`)
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, 42, exitErr.ExitCode())
		assert.Contains(t, output, "/100/fd/9 after 3 bounded rounds: descriptor unreadable")
	})

	t.Run("a reused descriptor number is inspected in the next bounded round", func(t *testing.T) {
		output, err := runWorkspaceActiveOpenProbe(t, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_PROC_ROOT" ]]; then printf '100\n'; exit 0; fi
if [[ "$path" == "$TEST_PROC_ROOT/100/fd" ]]; then printf '9\n'; exit 0; fi
exit 2
`, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_DEVICE" ]]; then printf '8:1\n'; exit 0; fi
count=0
if [[ -f "$TEST_STAT_STATE" ]]; then read -r count < "$TEST_STAT_STATE"; fi
printf '%s\n' "$((count + 1))" > "$TEST_STAT_STATE"
if [[ "$count" -lt 3 ]]; then printf 'descriptor changed\n' >&2; exit 1; fi
printf '8:2\n'
`)
		require.NoError(t, err, output)
	})

	t.Run("descriptor disappearance proven by successful re-enumeration is ignored", func(t *testing.T) {
		output, err := runWorkspaceActiveOpenProbe(t, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_PROC_ROOT" ]]; then printf '100\n'; exit 0; fi
if [[ "$path" == "$TEST_PROC_ROOT/100/fd" ]]; then
  count=0
  if [[ -f "$TEST_STATE" ]]; then read -r count < "$TEST_STATE"; fi
  if [[ "$count" == 0 ]]; then printf '9\n'; fi
  printf '%s\n' "$((count + 1))" > "$TEST_STATE"
  exit 0
fi
exit 2
`, `#!/bin/bash
set -eu
path="${!#}"
if [[ "$path" == "$TEST_DEVICE" ]]; then printf '8:1\n'; exit 0; fi
printf 'descriptor disappeared\n' >&2
exit 1
`)
		require.NoError(t, err, output)
	})
}

func runWorkspaceActiveOpenProbe(t *testing.T, lsScript, statScript string) (string, error) {
	t.Helper()
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	require.NoError(t, os.MkdirAll(filepath.Join(procRoot, "100", "fd"), 0o755))
	device := filepath.Join(root, "device")
	require.NoError(t, os.WriteFile(device, nil, 0o600))
	state := filepath.Join(root, "ls-state")
	statState := filepath.Join(root, "stat-state")
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ls"), []byte(lsScript), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "stat"), []byte(statScript), 0o755))

	generated := workspaceReconcileScript(provisioner.AgentPoolWorkspaceSpec{
		InstallName: "probe-test", Device: "/dev/disk/by-id/probe-test", Filesystem: config.WorkspaceFilesystemExt4,
	}, "reconcile")
	start := strings.Index(generated, "list_process_ids() {")
	end := strings.Index(generated, "\nprobe_blank_signatures() {")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	probe := strings.ReplaceAll(generated[start:end], "/proc", procRoot)
	script := fmt.Sprintf("set -eu\nfail() { printf 'workspace refusal: %%s\\n' \"$*\" >&2; exit 42; }\ndevice=%s\n%s\nprobe_active_raw_consumers\n", shellQuote(device), probe)

	cmd := exec.Command("/bin/bash", "-ceu", script)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
		"TEST_PROC_ROOT="+procRoot,
		"TEST_DEVICE="+device,
		"TEST_STATE="+state,
		"TEST_STAT_STATE="+statState,
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestAgentPoolLocalPathSetupPreservesDedicatedMountMode(t *testing.T) {
	script := agentPoolLocalPathSetupScript()
	assert.Contains(t, script, provisioner.AgentPoolWorkspaceMount+"/*)")
	assert.Contains(t, script, `parent=${VOL_DIR%/*}`)
	assert.Contains(t, script, `chmod 0711 "$parent"`)
	assert.Contains(t, script, `*) chmod 0701 "$VOL_DIR/.."`)
	assert.NotContains(t, script, "chown", "the pinned helper image intentionally provides only the default minimal toolset")
}

func TestParseAgentPoolWorkspaceResultIncludesTransportAndFilesystem(t *testing.T) {
	state, err := parseWorkspaceResult("FORGE_WORKSPACE_RESULT\t/dev/disk/by-id/nvme-ws\t/dev/nvme1n1\tModel\tSerial\tWWN\t107374182400\tnvme\txfs\t11111111-1111-1111-1111-111111111111\tcomplete\n")
	require.NoError(t, err)
	assert.Equal(t, "nvme", state.Transport)
	assert.Equal(t, config.WorkspaceFilesystemXFS, state.Filesystem)
	assert.Equal(t, uint64(107374182400), state.SizeBytes)
}

func TestEnsureAgentPoolWorkspaceToolsInstallsAndVerifiesXFS(t *testing.T) {
	verifyCalls := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "command -v mkfs.xfs"):
			verifyCalls++
			if verifyCalls == 1 {
				return "", 1
			}
			return "", 0
		case strings.Contains(cmd, "apt-get install -y xfsprogs"):
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureAgentPoolWorkspaceTools(context.Background(), config.WorkspaceFilesystemXFS))
	assert.Equal(t, 2, verifyCalls)
}

func TestEnsureAgentPoolWorkspaceToolsChecksExt4WithoutPackageMutation(t *testing.T) {
	var commands []string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		commands = append(commands, cmd)
		if strings.Contains(cmd, "command -v mkfs.ext4") {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureAgentPoolWorkspaceTools(context.Background(), config.WorkspaceFilesystemExt4))
	require.Len(t, commands, 1)
	assert.NotContains(t, commands[0], "apt-get")
}

func TestDriverVersionWithoutDigest(t *testing.T) {
	assert.Equal(t, "580.126.20", driverVersionWithoutDigest("580.126.20@sha256:"+strings.Repeat("a", 64)))
	assert.Equal(t, "580.126.20", driverVersionWithoutDigest("580.126.20"))
}

func TestReadGPUReadiness(t *testing.T) {
	const (
		query     = "sudo k3s kubectl get clusterpolicy,nodes -o json"
		candidate = "595.71.05"
		baseline  = "580.126.20"
	)
	tests := []struct {
		name         string
		output       string
		wantReady    bool
		wantTerminal bool
		wantState    string
		wantReason   string
	}{
		{
			name:       "coherent ready state",
			output:     gpuReadinessSnapshot("ready", "True", "False", candidate, candidate, "upgrade-done", true, false),
			wantReady:  true,
			wantState:  "ready",
			wantReason: "converged",
		},
		{
			name:       "documented legacy state conflict",
			output:     gpuReadinessSnapshot("notReady", "True", "False", candidate, candidate, "upgrade-done", true, false),
			wantReady:  true,
			wantState:  "notReady",
			wantReason: "remains contradictory",
		},
		{
			name:       "stale pre-transition policy driver",
			output:     gpuReadinessSnapshot("ready", "True", "False", baseline, candidate, "upgrade-done", true, false),
			wantState:  "ready",
			wantReason: "has not selected the requested driver",
		},
		{
			name:       "stale pre-transition node driver",
			output:     gpuReadinessSnapshot("ready", "True", "False", candidate, baseline, "upgrade-done", true, false),
			wantState:  "ready",
			wantReason: "has not loaded the requested driver",
		},
		{
			name:       "operator error condition",
			output:     gpuReadinessSnapshot("notReady", "True", "True", candidate, candidate, "upgrade-done", true, false),
			wantState:  "notReady",
			wantReason: "Error condition is not False",
		},
		{
			name:       "upgrade still progressing",
			output:     gpuReadinessSnapshot("notReady", "True", "False", candidate, candidate, "validation-required", true, false),
			wantState:  "notReady",
			wantReason: "still in progress",
		},
		{
			name:         "terminal upgrade failure",
			output:       gpuReadinessSnapshot("notReady", "False", "True", candidate, baseline, "upgrade-failed", true, true),
			wantTerminal: true,
			wantState:    "notReady",
			wantReason:   "upgrade-failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
				if cmd == query {
					return tt.output, 0
				}
				return "", 1
			})
			defer cleanup()
			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			readiness, err := p.ReadGPUReadiness(context.Background(), candidate)
			require.NoError(t, err)
			assert.Equal(t, tt.wantReady, readiness.Ready)
			assert.Equal(t, tt.wantTerminal, readiness.Terminal)
			assert.Equal(t, tt.wantState, readiness.PolicyState)
			assert.Contains(t, readiness.Reason, tt.wantReason)
			assert.Equal(t, candidate, readiness.RequestedDriverVersion)
		})
	}

	t.Run("chart-default driver still requires a loaded node driver", func(t *testing.T) {
		readiness, err := parseGPUReadiness(
			gpuReadinessSnapshot("ready", "True", "False", "", candidate, "", true, false),
			"",
		)
		require.NoError(t, err)
		assert.True(t, readiness.Ready, readiness.String())
		assert.Empty(t, readiness.RequestedDriverVersion)
		assert.Equal(t, candidate, readiness.NodeDriverVersion)
	})

	t.Run("clusterpolicy absent", func(t *testing.T) {
		// Before the operator is installed the CRD may not exist. The query error
		// is retained for lifecycle timeout diagnostics while polling continues.
		addr, cfg, cleanup := startFakeSSH(t, func(string) (string, int) {
			return "the server doesn't have a resource type clusterpolicy", 1
		})
		defer cleanup()
		p := newProvisioner(t, addr, cfg)
		defer p.Close()
		readiness, err := p.ReadGPUReadiness(context.Background(), candidate)
		require.Error(t, err)
		assert.Nil(t, readiness)
		assert.Contains(t, err.Error(), "read GPU readiness resources")
	})
}

func gpuReadinessSnapshot(policyState, readyCondition, errorCondition, policyDriver, nodeDriver, upgradeState string, nodeReady, unschedulable bool) string {
	return fmt.Sprintf(`{
		"items": [
			{
				"kind": "ClusterPolicy",
				"metadata": {"name": "cluster-policy"},
				"spec": {"driver": {"version": %q}},
				"status": {
					"state": %q,
					"conditions": [
						{"type": "Ready", "status": %q},
						{"type": "Error", "status": %q}
					]
				}
			},
			{
				"kind": "Node",
				"metadata": {
					"name": "gpu-1",
					"labels": {
						"nvidia.com/cuda.driver-version.full": %q,
						"nvidia.com/gpu-driver-upgrade-state": %q
					}
				},
				"spec": {"unschedulable": %t},
				"status": {"conditions": [{"type": "Ready", "status": %q}]}
			}
		]
	}`, policyDriver, policyState, readyCondition, errorCondition, nodeDriver, upgradeState, unschedulable, map[bool]string{true: "True", false: "False"}[nodeReady])
}

func TestDownloadVerifiedHelmChartRejectsSubstitutedBytesBeforeUse(t *testing.T) {
	const directory = "/tmp/forge-chart.test"
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "exact", false: "substituted"}[valid], func(t *testing.T) {
			used := false
			addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
				switch {
				case cmd == "mktemp -d /tmp/forge-chart.XXXXXX":
					return directory + "\n", 0
				case strings.Contains(cmd, "'pull' 'nvidia/gpu-operator'"):
					return "", 0
				case strings.Contains(cmd, "sha256sum --check --status"):
					if valid {
						return "", 0
					}
					return "", 1
				case cmd == "sudo rm -rf "+shellQuote(directory):
					return "", 0
				case strings.Contains(cmd, "'show' 'crds'"), strings.Contains(cmd, "'upgrade' '--install'"):
					used = true
					return "", 0
				default:
					return "", 1
				}
			})
			defer cleanup()
			p := newProvisioner(t, addr, cfg)
			defer p.Close()
			archive, remove, err := p.downloadVerifiedHelmChart(context.Background(), "nvidia/gpu-operator", "v26.3.3", defaultGPUOperatorChecksumForSSHTest)
			if valid {
				require.NoError(t, err)
				assert.Equal(t, directory+"/gpu-operator-v26.3.3.tgz", archive)
				remove()
			} else {
				require.ErrorContains(t, err, "verify Helm chart content checksum")
			}
			assert.False(t, used)
		})
	}
}

const defaultGPUOperatorChecksumForSSHTest = "59abb5852a24b3ae0ef757bfea3051f419acbf559ee5efd72f0672d28af56a68"

func TestEnsureRepo_CommandShape(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == helmVerifyCommand:
			return "/usr/local/bin/helm\n", 0
		case strings.Contains(cmd, "--force-update"):
			got = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureRepo(context.Background(), "nvidia", "https://helm.ngc.nvidia.com/nvidia"))
	assert.Contains(t, got, "repo")
	assert.Contains(t, got, "add")
	assert.Contains(t, got, "--force-update")
	assert.Contains(t, got, "nvidia")
	assert.Contains(t, got, "https://helm.ngc.nvidia.com/nvidia")
}

func TestEnsureDriverBuildDeps_RetriesOnAptLock(t *testing.T) {
	prev := aptLockRetryInterval
	aptLockRetryInterval = time.Millisecond
	defer func() { aptLockRetryInterval = prev }()

	calls := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apt-get install -y linux-headers-$(uname -r) build-essential dkms") {
			calls++
			if calls < 3 {
				// apt lock held by cloud-init/unattended-upgrades on first boot
				return "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1238 (apt-get)\n", 100
			}
			return "", 0
		}
		if strings.HasPrefix(cmd, "apt-cache show linux-headers-") || strings.HasPrefix(cmd, "test -f /lib/modules/") {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureDriverBuildDeps(context.Background()))
	assert.Equal(t, 3, calls)
}

func TestEnsureDriverBuildDeps_AptLockHeldTooLong(t *testing.T) {
	prev := aptLockRetryInterval
	aptLockRetryInterval = time.Millisecond
	defer func() { aptLockRetryInterval = prev }()

	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apt-get install -y linux-headers-$(uname -r)") {
			return "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 1238 (apt-get)\n", 100
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.EnsureDriverBuildDeps(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install GPU driver build dependencies")
}

func TestEnsureDriverBuildDeps_RejectsUnavailableRunningKernelHeaders(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apt-get install -y linux-headers-$(uname -r) build-essential dkms") {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.EnsureDriverBuildDeps(context.Background())
	require.ErrorContains(t, err, "running kernel headers are not available")
}

func TestEnsureDriverBuildDeps_VerifiesInstalledSurface(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apt-get install -y linux-headers-$(uname -r) build-essential dkms") ||
			strings.HasPrefix(cmd, "apt-cache show linux-headers-") {
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.EnsureDriverBuildDeps(context.Background())
	require.ErrorContains(t, err, "verify GPU driver build dependencies")
}

func TestIsAptLockHeld(t *testing.T) {
	assert.True(t, isAptLockHeld("ssh run ...; stderr: E: Could not get lock /var/lib/apt/lists/lock"))
	assert.True(t, isAptLockHeld("E: Unable to lock directory /var/lib/apt/lists/"))
	assert.True(t, isAptLockHeld("...is held by process 1238 (apt-get)"))
	assert.False(t, isAptLockHeld("E: Unable to locate package linux-headers-6.8.0"))
}

func TestOverlayer_Clone(t *testing.T) {
	var gotClone, gotCheck string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v git":
			return "/usr/bin/git\n", 0
		case strings.Contains(cmd, "clone"):
			gotClone = cmd
			return "", 0
		case strings.HasPrefix(cmd, "test -f "):
			gotCheck = cmd
			return "", 0
		case strings.Contains(cmd, "rev-parse HEAD"):
			return "deadbeef\n", 0
		default: // rm -rf, mkdir -p
			return "", 0
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	commit, err := p.Clone(context.Background(), "https://github.com/example/overlay.git", "master", "/var/lib/forge/overlay/opo1", nil)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", commit)
	assert.Contains(t, gotClone, "https://github.com/example/overlay.git")
	assert.Contains(t, gotClone, "http.version=HTTP/1.1")
	assert.Contains(t, gotClone, "/var/lib/forge/overlay/opo1")
	assert.Contains(t, gotCheck, "values.yaml")
	assert.Contains(t, gotCheck, "values.client.yaml")
	assert.Contains(t, gotCheck, "crds/client/kustomization.yaml")
}

func TestOverlayer_Clone_RetriesTransientTransportFailure(t *testing.T) {
	previousInterval := overlayCloneRetryInterval
	overlayCloneRetryInterval = time.Millisecond
	defer func() { overlayCloneRetryInterval = previousInterval }()

	cloneCalls := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "clone"):
			cloneCalls++
			if cloneCalls < overlayCloneMaxAttempts {
				return "fatal: expected flush after ref listing", 128
			}
			return "", 0
		case strings.HasPrefix(cmd, "test -f "):
			return "", 0
		case strings.Contains(cmd, "rev-parse HEAD"):
			return "deadbeef\n", 0
		default:
			return "", 0
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()

	commit, err := p.Clone(context.Background(), "https://github.com/example/overlay.git", "master", "/var/lib/forge/overlay/opo1", nil)
	require.NoError(t, err)
	assert.Equal(t, "deadbeef", commit)
	assert.Equal(t, overlayCloneMaxAttempts, cloneCalls)
}

func TestOverlayer_Clone_DoesNotRetryAuthenticationFailure(t *testing.T) {
	cloneCalls := 0
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "clone") {
			cloneCalls++
			return "fatal: Authentication failed", 128
		}
		return "", 0
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()

	_, err := p.Clone(context.Background(), "https://github.com/example/overlay.git", "master", "/var/lib/forge/overlay/opo1", nil)
	require.ErrorContains(t, err, "overlay clone")
	assert.Equal(t, 1, cloneCalls)
}

func TestIsTransientGitCloneFailure(t *testing.T) {
	assert.True(t, isTransientGitCloneFailure("fatal: expected flush after ref listing"))
	assert.True(t, isTransientGitCloneFailure("RPC failed; HTTP 503"))
	assert.False(t, isTransientGitCloneFailure("fatal: Authentication failed"))
	assert.False(t, isTransientGitCloneFailure("Repository not found"))
}

func TestOverlayer_Clone_StructureValidation(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v git":
			return "/usr/bin/git\n", 0
		case strings.Contains(cmd, "clone"):
			return "", 0
		case strings.HasPrefix(cmd, "test -f "):
			return "", 1 // structure missing
		default:
			return "", 0
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	_, err := p.Clone(context.Background(), "https://github.com/example/overlay.git", "master", "/var/lib/forge/overlay/opo1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlay structure")
}

func TestOverlayer_Clone_TokenCredFile(t *testing.T) {
	var gotClone string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v git":
			return "/usr/bin/git\n", 0
		case strings.Contains(cmd, "clone"):
			gotClone = cmd
			return "", 0
		case strings.HasPrefix(cmd, "test -f "):
			return "", 0
		case strings.Contains(cmd, "rev-parse HEAD"):
			return "abc\n", 0
		default: // cat > credFile (runStdin), rm -rf, mkdir -p
			return "", 0
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	_, err := p.Clone(context.Background(), "https://github.com/example/overlay.git", "master", "/var/lib/forge/overlay/opo1", []byte("ghp_secret"))
	require.NoError(t, err)
	assert.NotContains(t, gotClone, "ghp_secret", "token must not appear in the clone command/ps")
	assert.Contains(t, gotClone, "credential.helper=store --file=")
	assert.Contains(t, gotClone, "https://github.com/example/overlay.git", "clone URL has no embedded token")
}

func TestDeployer_ApplyKustomize_Empty(t *testing.T) {
	var applied bool
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "kustomize"):
			return "", 0 // empty render (no objects)
		case strings.Contains(cmd, "apply"):
			applied = true
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.ApplyKustomize(context.Background(), "/var/lib/forge/overlay/opo1/crds/client"))
	assert.False(t, applied, "empty kustomize => apply skipped (no objects)")
}

func TestDeployer_ApplyKustomize_Objects(t *testing.T) {
	var applied bool
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "kustomize"):
			return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n", 0
		case strings.Contains(cmd, "apply"):
			applied = true
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.ApplyKustomize(context.Background(), "/var/lib/forge/overlay/opo1/crds/client"))
	assert.True(t, applied, "non-empty kustomize => apply runs")
}

func TestDeployer_ApplyManifest(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apply") && strings.Contains(cmd, "-f") {
			got = cmd
			return "", 0
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	// A Secret manifest carrying the value in stringData; it is piped via stdin.
	manifest := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"tok","namespace":"ns"},"stringData":{"api-token":"supersecret"}}`
	require.NoError(t, p.ApplyManifest(context.Background(), manifest))
	assert.Contains(t, got, "kubectl")
	assert.Contains(t, got, "apply")
	assert.Contains(t, got, "-f")
	assert.NotContains(t, got, "supersecret", "value must be piped via stdin, not in the command/ps")
	assert.NotContains(t, got, "stringData", "manifest must be piped via stdin, not in the command")
}

func TestDeployer_ApplyManifest_Error(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "apply") && strings.Contains(cmd, "-f") {
			return "error: no kind \"Secret\" is registered", 1
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.ApplyManifest(context.Background(), `{"apiVersion":"v1","kind":"Secret"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kubectl apply -f -")
}

func TestOverlayer_ReadFile(t *testing.T) {
	t.Run("exists", func(t *testing.T) {
		addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
			if strings.HasPrefix(cmd, "cat ") {
				return "secrets: []\n", 0
			}
			return "", 1
		})
		defer cleanup()
		p := newProvisioner(t, addr, cfg)
		defer p.Close()
		out, err := p.ReadFile(context.Background(), "/var/lib/forge/overlay/opo1", "secrets.yaml")
		require.NoError(t, err)
		assert.Equal(t, "secrets: []\n", out)
	})
	t.Run("missing", func(t *testing.T) {
		// A missing file makes cat exit non-zero; the real host's stderr carries
		// "No such file" (asserted via the lifecycle fake). This legacy handler
		// injects only stdout, so this case asserts only that the error surfaces.
		addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
			if strings.HasPrefix(cmd, "cat ") {
				return "", 1
			}
			return "", 1
		})
		defer cleanup()
		p := newProvisioner(t, addr, cfg)
		defer p.Close()
		_, err := p.ReadFile(context.Background(), "/var/lib/forge/overlay/opo1", "secrets.yaml")
		require.Error(t, err)
	})
}

func TestFluxer_EnsureFlux_InstallsCLI(t *testing.T) {
	const installerPath = "/tmp/forge-flux-installer.test"
	var gotDownload, gotInstall, gotFluxInstall string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v flux":
			return "", 1 // absent
		case cmd == "mktemp /tmp/forge-flux-installer.XXXXXX":
			return installerPath + "\n", 0
		case strings.HasPrefix(cmd, "curl -fsSL"):
			gotDownload = cmd
			return "", 0
		case strings.Contains(cmd, "sha256sum --check --status"):
			assert.Contains(t, cmd, fluxInstallScriptSHA256)
			return "", 0
		case strings.HasPrefix(cmd, "sudo env FLUX_VERSION="):
			gotInstall = cmd
			return "", 0
		case cmd == "rm -f "+shellQuote(installerPath):
			return "", 0
		case strings.Contains(cmd, "flux") && strings.Contains(cmd, "install") && strings.Contains(cmd, "--version="):
			gotFluxInstall = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureFlux(context.Background(), "v2.4.0"))
	// CLI install script is version-pinned via FLUX_VERSION; the version never
	// appears bare in a way that could mismatch a tag filter.
	assert.Contains(t, gotDownload, shellQuote(fluxInstallScriptURL))
	assert.Contains(t, gotInstall, "FLUX_VERSION='2.4.0'", "install script takes the version without the leading v (it prepends v internally)")
	assert.Contains(t, gotInstall, shellQuote(installerPath))
	// flux install runs against the k3s kubeconfig via KUBECONFIG env (sudo root
	// reads the root-owned 0600 kubeconfig); version pinned.
	assert.Contains(t, gotFluxInstall, "KUBECONFIG=/etc/rancher/k3s/k3s.yaml")
	assert.Contains(t, gotFluxInstall, "--version=v2.4.0")
}

func TestFluxer_EnsureFlux_CLIPresent(t *testing.T) {
	var gotFluxInstall string
	sawInstallScript := false
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v flux":
			return "/usr/local/bin/flux\n", 0 // present
		case strings.Contains(cmd, "fluxcd.io/install.sh"):
			sawInstallScript = true
			return "", 0
		case strings.Contains(cmd, "flux") && strings.Contains(cmd, "install") && strings.Contains(cmd, "--version="):
			gotFluxInstall = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.EnsureFlux(context.Background(), "v2.4.0"))
	assert.False(t, sawInstallScript, "CLI already present => install script skipped")
	assert.Contains(t, gotFluxInstall, "--version=v2.4.0")
}

func TestFluxer_EnsureFlux_InstallFails(t *testing.T) {
	const installerPath = "/tmp/forge-flux-installer.test"
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v flux":
			return "", 1
		case cmd == "mktemp /tmp/forge-flux-installer.XXXXXX":
			return installerPath + "\n", 0
		case strings.HasPrefix(cmd, "curl -fsSL"), strings.Contains(cmd, "sha256sum --check --status"), cmd == "rm -f "+shellQuote(installerPath):
			return "", 0
		case strings.HasPrefix(cmd, "sudo env FLUX_VERSION="):
			return "", 1 // CLI installer fails
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	err := p.EnsureFlux(context.Background(), "v2.4.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "install flux cli")
}

func TestFluxer_UninstallFlux(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case cmd == "command -v flux":
			return "/usr/local/bin/flux\n", 0
		case strings.Contains(cmd, "flux") && strings.Contains(cmd, "uninstall"):
			got = cmd
			return "", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	require.NoError(t, p.UninstallFlux(context.Background()))
	assert.Contains(t, got, "uninstall")
	assert.Contains(t, got, "--silent") // non-interactive
	assert.Contains(t, got, "KUBECONFIG=/etc/rancher/k3s/k3s.yaml")
}

func TestFluxer_UninstallFlux_FluxAbsent(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if cmd == "command -v flux" {
			return "", 1 // CLI absent
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	// No flux CLI => nothing to remove, not an error (destroy proceeds to k3s).
	require.NoError(t, p.UninstallFlux(context.Background()))
}

func TestFluxer_GitRepositoryArtifact(t *testing.T) {
	var got string
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		switch {
		case strings.Contains(cmd, "gitrepository"):
			got = cmd
			return "True\tmain@sha1:abc\tsha256:0123456789abcdef\n", 0
		default:
			return "", 1
		}
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	artifact, err := p.GitRepositoryArtifact(context.Background(), "overlay")
	require.NoError(t, err)
	assert.True(t, artifact.Ready)
	assert.Equal(t, "main@sha1:abc", artifact.Revision)
	assert.Equal(t, "sha256:0123456789abcdef", artifact.Digest)
	assert.Contains(t, got, "gitrepository")
	assert.Contains(t, got, "flux-system")
	assert.Contains(t, got, "overlay")
	assert.Contains(t, got, "--ignore-not-found=true")
}

func TestFluxer_GitRepositoryArtifact_NotPresent(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(cmd string) (string, int) {
		if strings.Contains(cmd, "--ignore-not-found=true") {
			return "", 0 // absent CR is a successful empty read
		}
		return "", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()
	artifact, err := p.GitRepositoryArtifact(context.Background(), "overlay")
	require.NoError(t, err, "a missing/not-ready GitRepository is tolerated, not an error")
	assert.False(t, artifact.Ready)
	assert.Empty(t, artifact.Revision)
	assert.Empty(t, artifact.Digest)
}

func TestFluxer_GitRepositoryArtifact_CommandFailure(t *testing.T) {
	addr, cfg, cleanup := startFakeSSH(t, func(string) (string, int) {
		return "forbidden", 1
	})
	defer cleanup()
	p := newProvisioner(t, addr, cfg)
	defer p.Close()

	_, err := p.GitRepositoryArtifact(context.Background(), "overlay")
	require.ErrorContains(t, err, `get Flux GitRepository "overlay"`)
	assert.Contains(t, err.Error(), "ssh run", "the underlying kubectl/SSH failure remains visible")
}
